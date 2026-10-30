package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A browser download cannot attach X-Transfer-Key itself. After a request
// presents that header, the phone may follow a normal link for a short while.
const (
	phoneCookie     = "ft-phone"
	phoneCookieLife = 12 * time.Hour
	phoneLiveFor    = 6 * time.Second
)

var errNoPhone = errors.New("No phone connected")

type livePhone struct {
	ID       string
	Name     string
	LastSeen time.Time
}

type sendOffer struct {
	ID      string
	Session string
	Name    string
	Size    int64
	Rel     string
	Abs     string
	Outside bool
	Created time.Time
}

var (
	liveMu sync.Mutex
	live   = map[string]*livePhone{}
	offers = map[string]*sendOffer{}
)

func (s *server) protect(next http.HandlerFunc, cookieOK bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "requests from other sites are not allowed"})
			return
		}
		ip := clientIP(r)
		if s.tooManyMisses(ip) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many wrong keys, wait a few minutes"})
			return
		}
		got := r.Header.Get("X-Transfer-Key")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.key)) == 1 {
			s.issuePhone(w, r)
			next(w, r)
			return
		}
		if cookieOK && s.touchPhone(w, r) {
			next(w, r)
			return
		}
		s.miss(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "open the link printed on the PC, it has the key"})
	}
}

func (s *server) issuePhone(w http.ResponseWriter, r *http.Request) {
	if s.touchPhone(w, r) {
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	if s.phones == nil {
		s.phones = map[string]time.Time{}
	}
	s.prunePhonesLocked()
	s.phones[token] = time.Now().Add(phoneCookieLife)
	s.mu.Unlock()
	http.SetCookie(w, phoneCookieHeader(token))
}

func (s *server) touchPhone(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie(phoneCookie)
	if err != nil || len(c.Value) != 64 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phones == nil {
		return false
	}
	s.prunePhonesLocked()
	if _, ok := s.phones[c.Value]; !ok {
		return false
	}
	s.phones[c.Value] = time.Now().Add(phoneCookieLife)
	http.SetCookie(w, phoneCookieHeader(c.Value))
	return true
}

func (s *server) prunePhonesLocked() {
	now := time.Now()
	for token, exp := range s.phones {
		if now.After(exp) {
			delete(s.phones, token)
		}
	}
}

func phoneCookieHeader(token string) *http.Cookie {
	return &http.Cookie{
		Name:     phoneCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(phoneCookieLife / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func randHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func cleanPhoneName(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
		n++
		if n >= 24 {
			break
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		return "Phone"
	}
	return name
}

func pruneLiveLocked() {
	now := time.Now()
	for id, phone := range live {
		if now.Sub(phone.LastSeen) > phoneLiveFor {
			delete(live, id)
			for oid, off := range offers {
				if off.Session == id {
					delete(offers, oid)
				}
			}
		}
	}
}

func sessionAlive(id string) bool {
	if len(id) != 64 {
		return false
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	pruneLiveLocked()
	phone, ok := live[id]
	if !ok {
		return false
	}
	phone.LastSeen = time.Now()
	return true
}

func (s *server) phoneHello(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	_ = json.NewDecoder(r.Body).Decode(&req)
	id, err := randHex(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not connect"})
		return
	}
	name := cleanPhoneName(req.Name)
	liveMu.Lock()
	pruneLiveLocked()
	live[id] = &livePhone{ID: id, Name: name, LastSeen: time.Now()}
	liveMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"session": id, "name": name})
}

func (s *server) phonePulse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if json.NewDecoder(r.Body).Decode(&req) != nil || !sessionAlive(req.Session) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not connected"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) phoneInbox(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if !sessionAlive(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not connected"})
		return
	}
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	rows := []item{}
	liveMu.Lock()
	for _, off := range offers {
		if off.Session == id {
			rows = append(rows, item{ID: off.ID, Name: off.Name, Size: off.Size})
		}
	}
	liveMu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"files": rows})
}

func (s *server) phoneFile(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	offerID := r.URL.Query().Get("id")
	if !sessionAlive(session) || len(offerID) != 32 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
		return
	}
	liveMu.Lock()
	off := offers[offerID]
	if off == nil || off.Session != session {
		liveMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
		return
	}
	rel, absPath, outside, name := off.Rel, off.Abs, off.Outside, off.Name
	liveMu.Unlock()

	var full string
	var info os.FileInfo
	if outside {
		st, err := os.Stat(absPath)
		if err != nil || !st.Mode().IsRegular() {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
			return
		}
		full, info = absPath, st
	} else {
		var err error
		full, info, name, err = regularInside(rel)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
			return
		}
	}
	f, err := os.Open(full)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *server) phoneDirectory(w http.ResponseWriter, _ *http.Request) {
	liveMu.Lock()
	pruneLiveLocked()
	rows := make([]map[string]string, 0, len(live))
	for _, phone := range live {
		rows = append(rows, map[string]string{"id": phone.ID, "name": phone.Name})
	}
	liveMu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i]["name"] == rows[j]["name"] {
			return rows[i]["id"] < rows[j]["id"]
		}
		return rows[i]["name"] < rows[j]["name"]
	})
	writeJSON(w, http.StatusOK, map[string]any{"phones": rows})
}

func (s *server) sendSaved(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string   `json:"session"`
		Paths   []string `json:"paths"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !sessionAlive(req.Session) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "No phone connected"})
		return
	}
	batch := make([]*sendOffer, 0, len(req.Paths))
	for _, raw := range req.Paths {
		if len(batch) >= 100 {
			break
		}
		rel, err := parseRel(raw)
		if err != nil || rel == "" {
			continue
		}
		_, info, name, err := regularInside(rel)
		if err != nil || info.IsDir() {
			continue
		}
		id, err := randHex(16)
		if err != nil {
			continue
		}
		batch = append(batch, &sendOffer{
			ID:      id,
			Session: req.Session,
			Name:    name,
			Size:    info.Size(),
			Rel:     rel,
			Created: time.Now(),
		})
	}
	liveMu.Lock()
	if _, ok := live[req.Session]; !ok {
		liveMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "No phone connected"})
		return
	}
	for _, off := range batch {
		offers[off.ID] = off
	}
	liveMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"count": len(batch)})
}

func (s *server) sendOutside(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string   `json:"session"`
		Paths   []string `json:"paths"`
	}
	if !readBody(w, r, &req) {
		return
	}
	n, err := queueOutside(req.Session, req.Paths)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

func queueOutside(session string, paths []string) (int, error) {
	if !sessionAlive(session) {
		return 0, errNoPhone
	}
	batch := make([]*sendOffer, 0, len(paths))
	for _, raw := range paths {
		if len(batch) >= 100 {
			break
		}
		full, err := filepath.Abs(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			continue
		}
		id, err := randHex(16)
		if err != nil {
			continue
		}
		batch = append(batch, &sendOffer{
			ID:      id,
			Session: session,
			Name:    filepath.Base(full),
			Size:    info.Size(),
			Abs:     full,
			Outside: true,
			Created: time.Now(),
		})
	}
	if len(batch) == 0 && len(paths) > 0 {
		return 0, errors.New("could not send those files")
	}
	liveMu.Lock()
	defer liveMu.Unlock()
	if _, ok := live[session]; !ok {
		return 0, errNoPhone
	}
	for _, off := range batch {
		offers[off.ID] = off
	}
	return len(batch), nil
}

func attachmentDisposition(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	ascii = strings.Trim(ascii, " .")
	if ascii == "" {
		ascii = "file"
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}
