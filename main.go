package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

const (
	port = "8787"
	// Screenshots go up in batches; any other file goes up one per request, so a long video gets its own allowance.
	maxShotBatch = 200 << 20
	maxOneFile   = 4 << 30
	// Wrong keys from one address before it has to wait, so the key cannot be guessed over the network.
	maxKeyMisses = 20
	missWindow   = 10 * time.Minute
)

// Uploads and the key sit next to the executable so a double-clicked app keeps its files.
// go run builds a throwaway binary under a go-build temp dir, so that case stays in the working directory.
var (
	appDir  string
	keyPath string
)

// Screenshot folders by device, plus one folder that takes any kind of file.
var destinations = map[string]bool{
	"iphone":  true,
	"ipad":    true,
	"android": true,
	"other":   true,
	"files":   true,
}

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".heic": true, ".heif": true, ".webp": true, ".gif": true}

var safeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type server struct {
	key    string
	mu     sync.Mutex
	misses map[string][]time.Time
	phones map[string]time.Time
}

func main() {
	defer func() {
		if rec := recover(); rec != nil {
			report(fmt.Sprint(rec))
		}
	}()

	dir, err := dataDir()
	if err != nil {
		report(err.Error())
		return
	}
	appDir = dir
	keyPath = filepath.Join(appDir, "transfer-key")

	if !claimApp() {
		if !focusExisting() {
			report("FastTransfer is already open.")
		}
		return
	}

	if err := initSaveRoot(); err != nil {
		report(err.Error())
		return
	}
	key, err := loadKey()
	if err != nil {
		report(err.Error())
		return
	}
	s := &server{key: key, misses: map[string][]time.Time{}, phones: map[string]time.Time{}}

	mux := s.routes()

	fmt.Println()
	fmt.Println("  FastTransfer: send files between this PC and your phone")
	fmt.Println("  ----------------------------------------------------")
	fmt.Println("  On this PC:   http://127.0.0.1:" + port + "/desk")
	for _, ip := range lanIPs() {
		fmt.Println("  On phone:     http://" + ip + ":" + port + "/?k=" + key)
	}
	fmt.Println("  Saves into:   " + abs(saveRoot()) + string(os.PathSeparator) + "{iphone,ipad,android,other,files}")
	fmt.Println("  Only links with this key can send or take files. Delete the transfer-key file to make a new one.")
	fmt.Println()

	ln, err := net.Listen("tcp", "0.0.0.0:"+port)
	if err != nil {
		if alreadyServing() {
			if !focusExisting() {
				report("FastTransfer is already open.")
			}
			return
		}
		report("FastTransfer could not start. Port " + port + " is already in use.")
		return
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	if !waitReady() {
		report("FastTransfer could not open its page.")
		return
	}
	runApp("http://127.0.0.1:" + port + "/desk?v=15")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", phonePage)
	mux.HandleFunc("GET /index.html", phonePage)
	mux.HandleFunc("GET /desk", deskPage)
	mux.HandleFunc("GET /api/health", guardLocal(health))
	mux.HandleFunc("GET /api/desk", guardLocal(s.deskInfo))
	mux.HandleFunc("GET /api/dir", guardLocal(s.dirList))
	mux.HandleFunc("GET /api/file", guardLocal(s.file))
	mux.HandleFunc("GET /api/video", guardLocal(s.videoInfo))
	mux.HandleFunc("GET /api/pdf", guardLocal(s.pdfPage))
	mux.HandleFunc("GET /desk/pdf", guardLocal(s.pdfPage))
	mux.HandleFunc("GET /api/audio", guardLocal(s.audioInfo))
	mux.HandleFunc("GET /api/cover", guardLocal(s.audioCover))
	mux.HandleFunc("GET /api/office", guardLocal(s.officeText))
	mux.HandleFunc("GET /api/text", guardLocal(s.readText))
	mux.HandleFunc("GET /api/zip", guardLocal(s.zipNames))
	mux.HandleFunc("POST /api/save", guardLocal(s.saveText))
	mux.HandleFunc("POST /api/rename", guardLocal(s.renameEntry))
	mux.HandleFunc("POST /api/delete", guardLocal(s.deleteFile))
	mux.HandleFunc("GET /qr.png", guardLocal(s.qr))
	mux.HandleFunc("POST /api/shutdown", guardLocal(s.shutdown))
	mux.HandleFunc("GET /api/recv", guardLocal(recvStatus))
	mux.HandleFunc("POST /api/root", guardLocal(s.setRoot))
	mux.HandleFunc("GET /api/info", s.guard(s.info))
	mux.HandleFunc("POST /api/upload", s.guard(s.upload))
	mux.HandleFunc("GET /api/list", s.guard(s.list))
	mux.HandleFunc("GET /api/browse", s.guard(s.phoneBrowse))
	mux.HandleFunc("GET /api/get", s.guardPhone(s.phoneSavedGet))
	mux.HandleFunc("GET /api/pack", s.guardPhone(s.phonePack))
	mux.HandleFunc("POST /api/phone/hello", s.guard(s.phoneHello))
	mux.HandleFunc("POST /api/phone/pulse", s.guard(s.phonePulse))
	mux.HandleFunc("GET /api/phone/inbox", s.guard(s.phoneInbox))
	mux.HandleFunc("GET /api/phone/file", s.guardPhone(s.phoneFile))
	mux.HandleFunc("GET /api/phones", guardLocal(s.phoneDirectory))
	mux.HandleFunc("POST /api/send", guardLocal(s.sendSaved))
	mux.HandleFunc("POST /api/send-outside", guardLocal(s.sendOutside))
	mux.HandleFunc("GET /api/live", s.guard(liveGet))
	mux.HandleFunc("POST /api/live", s.guard(liveTyping(true)))
	mux.HandleFunc("POST /api/clip", s.guard(clipAdd(true)))
	mux.HandleFunc("POST /api/clip/remove", s.guard(clipRemove))
	mux.HandleFunc("GET /api/desk/live", guardLocal(liveGet))
	mux.HandleFunc("POST /api/desk/live", guardLocal(liveTyping(false)))
	mux.HandleFunc("POST /api/desk/clip", guardLocal(clipAdd(false)))
	mux.HandleFunc("POST /api/desk/clip/remove", guardLocal(clipRemove))
	return mux
}

// The key lives in a file so the link on the phone keeps working after a restart.
func loadKey() (string, error) {
	if raw, err := os.ReadFile(keyPath); err == nil {
		if key := strings.TrimSpace(string(raw)); len(key) >= 10 {
			return key, nil
		}
	}
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("could not save the transfer key: %w", err)
	}
	return key, nil
}

// guard lets only this page, opened with the key, read or write.
func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return s.protect(next, false)
}

// guardPhone is the same check. A normal browser download may also use the
// short-lived cookie that a successful key request just set.
func (s *server) guardPhone(next http.HandlerFunc) http.HandlerFunc {
	return s.protect(next, true)
}

func (s *server) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-missWindow)
	kept := s.misses[ip][:0]
	for _, t := range s.misses[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(s.misses, ip)
		return nil
	}
	s.misses[ip] = kept
	return kept
}

func (s *server) tooManyMisses(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recent(ip)) >= maxKeyMisses
}

func (s *server) miss(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.misses[ip] = append(s.recent(ip), time.Now())
}

func (s *server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"destinations": []string{"iphone", "ipad", "android", "other", "files"},
		"urls":         lanIPs(),
	})
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	to := normalizeDestination(r.URL.Query().Get("to"))
	if to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick a folder"})
		return
	}
	entries, err := os.ReadDir(filepath.Join(saveRoot(), to))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"files": []string{}})
		return
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"to": to, "files": files})
}

// upload streams each file straight to disk, so a big video never has to fit in memory.
func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	to := normalizeDestination(r.URL.Query().Get("to"))
	if to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick iphone, ipad, android, other, or files"})
		return
	}
	anyFile := to == "files"
	limit := int64(maxShotBatch)
	if anyFile {
		limit = maxOneFile
	}
	id := uploads.begin(to, r.ContentLength)
	body := &progReader{r: r.Body, id: id}
	r.Body = http.MaxBytesReader(w, body, limit+(1<<20))
	finished := false
	defer func() {
		if !finished {
			uploads.abort(id)
		}
	}()

	parts, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send the files as a form upload"})
		return
	}
	dir := filepath.Join(saveRoot(), to)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create folder"})
		return
	}

	saved := []string{}
	stamp := time.Now().Format("20060102-150405")
	for n := 1; ; {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeJSON(w, uploadErrorStatus(err), map[string]string{"error": uploadErrorText(err, anyFile)})
			return
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue
		}
		shown := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
		uploads.file(id, shown, n)
		name, err := saveOne(dir, stamp, n, part, anyFile)
		_ = part.Close()
		if err != nil {
			writeJSON(w, uploadErrorStatus(err), map[string]string{"error": uploadErrorText(err, anyFile)})
			return
		}
		saved = append(saved, name)
		n++
	}
	if len(saved) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no files selected"})
		return
	}
	uploads.finish(id, len(saved), to)
	finished = true
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"to":     to,
		"count":  len(saved),
		"files":  saved,
		"folder": filepath.Join(saveRoot(), to),
	})
}

type rejectedError struct{ reason string }

func (e rejectedError) Error() string { return e.reason }

func uploadErrorStatus(err error) int {
	var tooBig *http.MaxBytesError
	var rejected rejectedError
	switch {
	case errors.As(err, &tooBig):
		return http.StatusRequestEntityTooLarge
	case errors.As(err, &rejected):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func uploadErrorText(err error, anyFile bool) string {
	var tooBig *http.MaxBytesError
	var rejected rejectedError
	switch {
	case errors.As(err, &tooBig) && anyFile:
		return "that file is over 4 GB"
	case errors.As(err, &tooBig):
		return "batch too large (max 200 MB), send fewer at once"
	case errors.As(err, &rejected):
		return rejected.reason
	}
	return "the upload stopped partway, try again"
}

func saveOne(dir, stamp string, n int, part *multipart.Part, anyFile bool) (string, error) {
	base := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	ext := strings.ToLower(filepath.Ext(base))
	stem := safeName.ReplaceAllString(strings.TrimSuffix(base, filepath.Ext(base)), "_")
	ext = safeName.ReplaceAllString(ext, "")
	if len(ext) > 12 {
		ext = ext[:12]
	}
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(stem) > 80 {
		stem = stem[:80]
	}
	if stem == "" || stem == "." || stem == ".." {
		stem = "file"
	}
	if !anyFile {
		switch {
		case ext == "":
			ext = ".png"
		case !imageExts[ext]:
			return "", rejectedError{base + " is not an image. Use Any file for videos and documents."}
		}
	}

	name := fmt.Sprintf("%s-%02d-%s%s", stamp, n, stem, ext)
	dstPath := filepath.Join(dir, name)
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		name = fmt.Sprintf("%s-%02d-%d-%s%s", stamp, n, time.Now().UnixNano()%100000, stem, ext)
		dstPath = filepath.Join(dir, name)
		dst, err = os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("could not save %s: %w", base, err)
		}
	}
	if _, err := io.Copy(dst, part); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return "", err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return "", err
	}
	return name, nil
}

func normalizeDestination(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	if destinations[d] {
		return d
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func lanIPs() []string {
	filtered := collectLAN(true)
	if len(filtered) > 0 {
		return filtered
	}
	return collectLAN(false)
}

func collectLAN(skipVirtual bool) []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if skipVirtual && virtualNIC(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip = ip.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := ipRank(out[i]), ipRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

func virtualNIC(name string) bool {
	n := strings.ToLower(name)
	for _, part := range []string{
		"virtual", "hyper-v", "vethernet", "wsl", "vmware", "virtualbox", "vbox",
		"bluetooth", "loopback", "teredo", "isatap", "tap-windows", "vpn",
		"zerotier", "tailscale", "wireguard", "nordlynx", "docker", "vswitch",
	} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

func ipRank(ip string) int {
	switch {
	case strings.HasPrefix(ip, "192.168."):
		return 0
	case strings.HasPrefix(ip, "10."):
		return 1
	}
	p := net.ParseIP(ip).To4()
	if p != nil && p[0] == 172 && p[1] >= 16 && p[1] <= 31 {
		return 2
	}
	return 3
}

func dataDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return os.Getwd()
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if strings.Contains(dir, "go-build") {
		return os.Getwd()
	}
	return dir, nil
}

func waitReady() bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func alreadyServing() bool {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	res, err := client.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		App string `json:"app"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1024)).Decode(&body) != nil {
		return false
	}
	return body.App == "fasttransfer"
}

func abs(path string) string {
	a, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return a
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
