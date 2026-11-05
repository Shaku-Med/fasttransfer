package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const socialRootName = "Social media"

const socialMissing = "Put yt-dlp.exe in the same folder as FastTransfer. Add ffmpeg.exe there too when a video needs to be merged."

var (
	lookupYT     = func() string { return findBesideThenPath("yt-dlp") }
	lookupFF     = func() string { return findBin("ffmpeg") }
	launchSocial = runSocial
	pctRE        = regexp.MustCompile(`\[download\]\s+([0-9]+(?:\.[0-9]+)?)%`)
	destRE       = regexp.MustCompile(`(?i)Destination:\s*(.+)`)
	hostRE       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62})(?:\.[a-z0-9](?:[a-z0-9-]{0,62}))+$`)
)

type socialState struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	cmd     *exec.Cmd
	phase   string
	percent float64
	line    string
	errLine string
	note    string
	file    string
	folder  string
	sent    int
}

var social socialState

type socialView struct {
	YT      bool    `json:"yt"`
	FFmpeg  bool    `json:"ffmpeg"`
	Running bool    `json:"running"`
	Phase   string  `json:"phase"`
	Percent float64 `json:"percent"`
	Line    string  `json:"line"`
	Error   string  `json:"error"`
	Note    string  `json:"note"`
	File    string  `json:"file"`
	Folder  string  `json:"folder"`
	Sent    int     `json:"sent"`
	Hint    string  `json:"hint"`
}

func findBesideThenPath(name string) string {
	if p := bundledTool(name); p != "" {
		return p
	}
	file := name
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(file), ".exe") {
		file += ".exe"
	}
	var dirs []string
	if appDir != "" {
		dirs = append(dirs, appDir)
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		candidate := filepath.Join(dir, file)
		st, err := os.Stat(candidate)
		if err == nil && st.Mode().IsRegular() {
			return candidate
		}
	}
	if p, err := exec.LookPath(file); err == nil {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func mediaURL(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2000 || strings.ContainsAny(raw, "\r\n\t ") {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return nil, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Contains(host, "..") || strings.HasPrefix(host, ".") {
		return nil, false
	}
	return u, true
}

func socialHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	if strings.HasPrefix(host, "www.") {
		host = strings.TrimPrefix(host, "www.")
	}
	switch {
	case host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		host = "youtube.com"
	case host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com"):
		host = "tiktok.com"
	case host == "fb.watch" || host == "fb.com" || host == "facebook.com" || strings.HasSuffix(host, ".facebook.com"):
		host = "facebook.com"
	case host == "x.com" || host == "twitter.com" || strings.HasSuffix(host, ".twitter.com"):
		host = "x.com"
	case host == "instagram.com" || strings.HasSuffix(host, ".instagram.com"):
		host = "instagram.com"
	}
	if !hostRE.MatchString(host) || strings.Contains(host, "..") {
		return "", false
	}
	switch host {
	case "con", "prn", "aux", "nul":
		return "", false
	}
	if len(host) == 4 {
		prefix := host[:3]
		if (prefix == "com" || prefix == "lpt") && host[3] >= '1' && host[3] <= '9' {
			return "", false
		}
	}
	return host, true
}

func socialDir(host string) (dir, rel string, err error) {
	folder, ok := socialHost(host)
	if !ok {
		return "", "", os.ErrInvalid
	}
	root, err := filepath.Abs(saveRoot())
	if err != nil {
		return "", "", err
	}
	dir = filepath.Join(root, socialRootName, folder)
	abs, err := filepath.Abs(dir)
	if err != nil || !sameOrInside(root, abs) {
		return "", "", os.ErrInvalid
	}
	relPath, err := filepath.Rel(root, abs)
	if err != nil || !sameOrInside(root, abs) {
		return "", "", os.ErrInvalid
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", "", err
	}
	return abs, filepath.ToSlash(relPath), nil
}

func socialArgs(pageURL, dir, ffmpeg string) []string {
	out := filepath.ToSlash(dir) + "/%(title).180B-%(id).32B.%(ext)s"
	args := []string{
		"--ignore-config",
		"--no-mtime",
		"--newline",
		"--restrict-filenames",
		"--no-playlist",
		"--paths", "temp:" + filepath.ToSlash(dir),
		"--paths", "home:" + filepath.ToSlash(dir),
		"-o", out,
	}
	if ffmpeg != "" {
		args = append(args, "--ffmpeg-location", ffmpeg)
	}
	args = append(args, "--", pageURL)
	return args
}

func (s *server) socialStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, currentSocial())
}

func (s *server) socialStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		All    bool   `json:"all"`
		Device string `json:"device"`
	}
	if !readBody(w, r, &req) {
		return
	}
	code, body := beginSocial(req.URL, req.All, req.Device)
	writeJSON(w, code, body)
}

// beginSocial is the one downloader. Desktop and a connected device both use it.
func beginSocial(raw string, all bool, device string) (int, any) {
	page, ok := mediaURL(raw)
	if !ok {
		return http.StatusBadRequest, map[string]string{"error": "That link has to start with http:// or https://."}
	}
	bin := lookupYT()
	if bin == "" {
		return http.StatusBadRequest, map[string]string{"error": socialMissing}
	}
	dir, rel, err := socialDir(page.Hostname())
	if err != nil {
		return http.StatusBadRequest, map[string]string{"error": "That link could not be saved."}
	}
	if device != "" && !handleRe.MatchString(device) {
		device = ""
	}
	social.mu.Lock()
	if social.running {
		social.mu.Unlock()
		return http.StatusConflict, map[string]string{"error": "A download is already running."}
	}
	ctx, cancel := context.WithCancel(context.Background())
	social.running = true
	social.cancel = cancel
	social.phase = "running"
	social.percent = 0
	social.line = "Starting"
	social.errLine = ""
	social.note = ""
	social.file = ""
	social.folder = rel
	social.sent = 0
	social.cmd = nil
	social.mu.Unlock()

	before := dirNames(dir)
	go launchSocial(ctx, cancel, bin, socialArgs(page.String(), dir, lookupFF()), dir, rel, before, all, device)
	return http.StatusAccepted, currentSocial()
}

func (s *server) phoneSocial(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Session string `json:"session"`
		URL     string `json:"url"`
	}
	if !readBody(w, r, &req) {
		return
	}
	handle, ok := sessionHandle(req.Session)
	if !ok {
		notConnected(w)
		return
	}
	code, body := beginSocial(req.URL, false, handle)
	writeJSON(w, code, body)
}

func (s *server) phoneSocialStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := sessionHandle(r.URL.Query().Get("session")); !ok {
		notConnected(w)
		return
	}
	writeJSON(w, http.StatusOK, currentSocial())
}

func sessionHandle(session string) (string, bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(session)
	if d == nil {
		return "", false
	}
	hub.touchLocked(d)
	return d.handle, true
}

func (s *server) socialCancel(w http.ResponseWriter, _ *http.Request) {
	stopSocial()
	writeJSON(w, http.StatusOK, currentSocial())
}

func stopSocial() {
	social.mu.Lock()
	cancel := social.cancel
	cmd := social.cmd
	if social.running {
		social.phase = "cancelled"
		social.line = "Cancelled."
		social.note = "Cancelled."
	}
	social.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	killTree(cmd)
}

func runSocial(ctx context.Context, cancel context.CancelFunc, bin string, args []string, dir, rel string, before map[string]bool, all bool, device string) {
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideConsole(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	social.mu.Lock()
	social.cmd = cmd
	social.mu.Unlock()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-stopCh:
			stopSocial()
		}
		close(done)
	}()
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			noteSocialLine(sc.Text())
		}
	}()
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	_ = pw.Close()
	<-scanDone
	social.mu.Lock()
	cancelled := social.phase == "cancelled"
	social.mu.Unlock()
	cancel()
	<-done

	social.mu.Lock()
	defer social.mu.Unlock()
	social.running = false
	social.cmd = nil
	social.cancel = nil
	if cancelled {
		social.phase = "cancelled"
		social.line = "Cancelled."
		social.note = "Cancelled."
		return
	}
	if err != nil {
		social.phase = "error"
		social.errLine = plainSocialError(social.errLine)
		if social.errLine == "" {
			social.errLine = plainSocialError(social.line)
		}
		social.note = social.errLine
		return
	}
	name := newestSocialFile(dir, before)
	social.file = name
	social.phase = "done"
	social.percent = 100
	if name == "" {
		social.note = "Saved."
		social.line = "Saved."
		return
	}
	relFile := rel + "/" + name
	receiveSocial(relFile, device)
	sent, note := offerSocial(relFile, all, device)
	social.sent = sent
	social.note = note
	social.line = note
}

func noteSocialLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 500 {
		if len(line) > 500 {
			line = line[:500]
		} else {
			return
		}
	}
	social.mu.Lock()
	defer social.mu.Unlock()
	if social.phase == "cancelled" {
		return
	}
	social.line = line
	if m := pctRE.FindStringSubmatch(line); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil && n >= 0 && n <= 100 {
			social.percent = n
		}
	}
	upper := strings.ToUpper(line)
	if strings.Contains(upper, "ERROR:") || strings.HasPrefix(upper, "ERROR") {
		social.errLine = line
	}
	if m := destRE.FindStringSubmatch(line); m != nil {
		base := filepath.Base(strings.TrimSpace(m[1]))
		if base != "" && base != "." && base != string(filepath.Separator) {
			social.file = base
		}
	}
}

func plainSocialError(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return "yt-dlp could not save that."
	}
	upper := strings.ToUpper(line)
	if i := strings.LastIndex(upper, "ERROR:"); i >= 0 {
		line = strings.TrimSpace(line[i+len("ERROR:"):])
	}
	if line == "" {
		return "yt-dlp could not save that."
	}
	if len(line) > 240 {
		line = line[:240]
	}
	return line
}

func dirNames(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		out[entry.Name()] = true
	}
	return out
}

func newestSocialFile(dir string, before map[string]bool) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, entry := range entries {
		name := entry.Name()
		if before[name] || entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		low := strings.ToLower(name)
		if strings.HasSuffix(low, ".part") || strings.HasSuffix(low, ".ytdl") || strings.HasSuffix(low, ".temp") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = name
			bestTime = info.ModTime()
		}
	}
	return best
}

func offerSocial(rel string, all bool, device string) (int, string) {
	files := savedFiles([]string{rel})
	if len(files) == 0 {
		return 0, "Saved."
	}
	handles, specificOffline := socialHandles(all, device)
	if specificOffline {
		return 0, "Saved. That device is not connected."
	}
	if len(handles) == 0 {
		return 0, "Saved. No device is connected."
	}
	n, err := hub.deliver(files, handles, false)
	if err != nil || n == 0 {
		return 0, "Saved. No device is connected."
	}
	if n == 1 {
		name := deviceName(handles[0])
		if name == "" {
			name = "1 device"
		}
		return n, "Saved and sent to " + name + "."
	}
	return n, "Saved and sent to " + strconv.Itoa(n) + " devices."
}

func socialHandles(all bool, device string) (handles []string, specificOffline bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if !all && device != "" {
		d := hub.byHandleLocked(device)
		if d == nil || !d.online {
			return nil, true
		}
		return []string{d.handle}, false
	}
	for _, d := range hub.byKey {
		if d != nil && d.online {
			handles = append(handles, d.handle)
		}
	}
	sort.Strings(handles)
	return handles, false
}

func deviceName(handle string) string {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if d := hub.byHandleLocked(handle); d != nil {
		return d.name
	}
	return ""
}

func currentSocial() socialView {
	yt := lookupYT() != ""
	ff := lookupFF() != ""
	social.mu.Lock()
	defer social.mu.Unlock()
	phase := social.phase
	if phase == "" {
		phase = "idle"
	}
	view := socialView{
		YT: yt, FFmpeg: ff, Running: social.running, Phase: phase,
		Percent: social.percent, Line: social.line, Error: social.errLine,
		Note: social.note, File: social.file, Folder: social.folder, Sent: social.sent,
	}
	if !yt || !ff {
		if len(embeddedYT) > 0 || len(embeddedFF) > 0 {
			view.Hint = "Setting up tools…"
		} else if !yt {
			view.Hint = socialMissing
		} else {
			view.Hint = "ffmpeg.exe is not next to FastTransfer. A single file can still download. Merging needs ffmpeg.exe in that same folder."
		}
	}
	return view
}
