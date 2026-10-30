package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// A browser download cannot attach X-Transfer-Key itself. After a request
// presents that header, the phone may follow a normal link for a short while.
const (
	phoneCookie     = "ft-phone"
	phoneCookieLife = 12 * time.Hour
	maxPhoneCookies = 4 * maxDevices
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
	// A client that never sends the cookie back would get a fresh one on every
	// request, so stop handing them out once there are far more than phones.
	if len(s.phones) >= maxPhoneCookies {
		s.mu.Unlock()
		return
	}
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
	exp, ok := s.phones[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.phones, c.Value)
		return false
	}
	s.phones[c.Value] = time.Now().Add(phoneCookieLife)
	http.SetCookie(w, phoneCookieHeader(c.Value))
	return true
}

// Pruning walks every cookie, so it runs at most twice a minute rather than on
// every request a busy room of phones makes.
func (s *server) prunePhonesLocked() {
	now := time.Now()
	if now.Sub(s.pruned) < 30*time.Second {
		return
	}
	s.pruned = now
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
