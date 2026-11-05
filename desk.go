package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

const maxTextBytes = 2 << 20

// Media the page may show. Everything else is text, a zip name list, or info only.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".heic": "image/heic",
	".heif": "image/heif",
	".svg":  "image/svg+xml",
}

// Chromium plays these from the file itself. Everything else in videoExts is
// remuxed or transcoded on the fly.
var directVideo = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
}

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".webm": true,
	".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".mpeg": true, ".mpg": true, ".ts": true,
	".m2ts": true, ".mts": true, ".3gp": true, ".3g2": true,
	".ogv": true, ".vob": true, ".asf": true, ".divx": true,
	".f4v": true,
}

var audioTypes = map[string]string{
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".m4a":  "audio/mp4",
	".ogg":  "audio/ogg",
	".flac": "audio/flac",
	".aac":  "audio/aac",
	".opus": "audio/ogg",
}

var textExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".rst": true, ".log": true,
	".json": true, ".csv": true, ".tsv": true, ".xml": true, ".css": true,
	".ini": true, ".cfg": true, ".conf": true, ".yml": true, ".yaml": true, ".toml": true,
	".go": true, ".py": true, ".rs": true, ".java": true, ".c": true, ".h": true,
	".cpp": true, ".hpp": true, ".cs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".html": true, ".htm": true, ".js": true, ".mjs": true, ".cjs": true,
	".ps1": true, ".bat": true, ".cmd": true, ".vbs": true, ".sh": true,
	".text": true, ".env": true, ".properties": true, ".sql": true, ".svg": true,
	".srt": true, ".lrc": true, ".nfo": true, ".cue": true,
}

// These are never sent as a document the webview could run.
var blockedExts = map[string]bool{
	".exe": true, ".dll": true, ".scr": true, ".com": true, ".msi": true,
	".jar": true, ".apk": true, ".lnk": true, ".iso": true, ".img": true, ".jse": true,
}

var (
	stopOnce sync.Once
	stopCh   = make(chan struct{})
)

func requestStop() {
	stopOnce.Do(func() { close(stopCh) })
}

func allowLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	reqHost := r.Host
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		reqHost = h
	}
	switch strings.ToLower(reqHost) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		switch origin {
		case "http://127.0.0.1:" + port, "http://localhost:" + port, "http://[::1]:" + port:
		default:
			return false
		}
	}
	return true
}

func guardLocal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !allowLocal(r) {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"app": "fasttransfer"})
}

func phonePage(w http.ResponseWriter, r *http.Request) {
	b, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-CH", "Sec-CH-UA-Platform, Sec-CH-UA-Platform-Version, Sec-CH-UA-Model")
	w.Header().Set("Permissions-Policy", "ch-ua-platform=(self), ch-ua-platform-version=(self), ch-ua-model=(self)")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self' data:; worker-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeShipped(w, r, "web/index.html", b)
}

// asset serves a file shipped inside the app. It holds no secrets, so phones
// load it before they have shown the key. Browsers check back every time
// with the fingerprint, so a new build is picked up at once and an unchanged
// file costs one tiny request.
func asset(name, ctype string) http.HandlerFunc {
	b, err := webFiles.ReadFile(name)
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", ctype)
		writeShipped(w, r, name, b)
	}
}

// awakeClip is a two second black video the remote loops out of sight. A
// playing video keeps a phone from dimming and locking, which the screen
// wake lock cannot do on a plain http address. Phones fetch video in ranges,
// so it goes through ServeContent.
var awakeBytes, _ = webFiles.ReadFile("web/awake.mp4")

func awakeClip(w http.ResponseWriter, r *http.Request) {
	if len(awakeBytes) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "awake.mp4", time.Time{}, bytes.NewReader(awakeBytes))
}

func deskPage(w http.ResponseWriter, r *http.Request) {
	if !allowLocal(r) {
		http.NotFound(w, r)
		return
	}
	b, err := webFiles.ReadFile("web/desk.html")
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self'; font-src 'self' data:; worker-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	writeShipped(w, r, "web/desk.html", b)
}

// playerPage is the media window the desk opens. Like the desk it only
// answers this computer.
func playerPage(w http.ResponseWriter, r *http.Request) {
	if !allowLocal(r) {
		http.NotFound(w, r)
		return
	}
	b, err := webFiles.ReadFile("web/player.html")
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeShipped(w, r, "web/player.html", b)
}

type phoneLink struct {
	IP  string `json:"ip"`
	URL string `json:"url"`
}

func phoneLinks(key string) []phoneLink {
	ips := lanIPs()
	out := make([]phoneLink, 0, len(ips))
	for _, ip := range ips {
		out = append(out, phoneLink{
			IP:  ip,
			URL: "http://" + ip + ":" + port + "/?k=" + key,
		})
	}
	return out
}

func chosenLink(key, ip string) (phoneLink, bool) {
	links := phoneLinks(key)
	if len(links) == 0 {
		return phoneLink{}, false
	}
	if ip == "" {
		return links[0], true
	}
	for _, link := range links {
		if link.IP == ip {
			return link, true
		}
	}
	return phoneLink{}, false
}

func (s *server) deskInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"links": phoneLinks(s.key),
	})
}

func (s *server) qr(w http.ResponseWriter, r *http.Request) {
	link, ok := chosenLink(s.key, r.URL.Query().Get("ip"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(link.URL, qrcode.Medium, 360)
	if err != nil {
		http.Error(w, "could not make the code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}

type dirEntry struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Rel      string    `json:"rel"`
	Path     string    `json:"path"`
	Folder   string    `json:"folder"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Type     string    `json:"type"`
	Image    bool      `json:"image"`
	Kind     string    `json:"kind"`
	Width    int       `json:"w,omitempty"`
	Height   int       `json:"h,omitempty"`
}

type crumb struct {
	Rel   string `json:"rel"`
	Label string `json:"label"`
}

func (s *server) dirList(w http.ResponseWriter, r *http.Request) {
	rel, err := parseRel(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dir, err := absInside(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}
	rows := []dirEntry{}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !plainFileName(name) {
				continue
			}
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			childAbs, err := absInside(childRel)
			if err != nil {
				continue
			}
			fi, err := entry.Info()
			if err != nil {
				continue
			}
			isDir := entry.IsDir()
			if st, err := os.Stat(childAbs); err == nil {
				isDir = st.IsDir()
				fi = st
			}
			kind := fileKind(name, isDir)
			var iw, ih int
			if kind == "image" {
				iw, ih = imageDims(childAbs, fi)
			}
			rows = append(rows, dirEntry{
				Name:     name,
				Label:    displayName(name, isDir),
				Rel:      childRel,
				Path:     childAbs,
				Folder:   dir,
				Dir:      isDir,
				Size:     fi.Size(),
				Modified: fi.ModTime(),
				Type:     typeLabel(name, isDir),
				Image:    kind == "image",
				Kind:     kind,
				Width:    iw,
				Height:   ih,
			})
		}
	}
	root := saveRoot()
	writeJSON(w, http.StatusOK, map[string]any{
		"rel":      rel,
		"path":     dir,
		"rootPath": root,
		"rootName": rootLabel(),
		"crumbs":   crumbsFor(rel),
		"entries":  rows,
	})
}

func (s *server) file(w http.ResponseWriter, r *http.Request) {
	full, info, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if videoExts[ext] && (r.URL.Query().Get("transcode") == "1" || directVideo[ext] == "") {
		streamTranscoded(w, r, full)
		return
	}
	if audioTypes[ext] != "" && r.URL.Query().Get("transcode") == "1" {
		streamAudio(w, r, full)
		return
	}
	ctype, ok := mediaType(ext)
	if !ok {
		refuseFile(w)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `inline; filename="`+quoteName(name)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if ext == ".svg" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; sandbox")
	} else {
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *server) readText(w http.ResponseWriter, r *http.Request) {
	full, info, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !textExts[strings.ToLower(filepath.Ext(name))] || blockedExts[strings.ToLower(filepath.Ext(name))] {
		refuseFile(w)
		return
	}
	if info.Size() > maxTextBytes {
		http.Error(w, "file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if err != nil {
		http.Error(w, "could not read the file", http.StatusInternalServerError)
		return
	}
	if int64(len(buf)) > maxTextBytes {
		http.Error(w, "file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf)
}

func (s *server) zipNames(w http.ResponseWriter, r *http.Request) {
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil || strings.ToLower(filepath.Ext(name)) != ".zip" {
		http.NotFound(w, r)
		return
	}
	zr, err := zip.OpenReader(full)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"names": []string{}})
		return
	}
	defer zr.Close()
	names := make([]string, 0, len(zr.File))
	for _, entry := range zr.File {
		if len(names) >= 500 {
			break
		}
		names = append(names, entry.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
}

func (s *server) saveText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if len(req.Text) > maxTextBytes {
		http.Error(w, "file is too large", http.StatusRequestEntityTooLarge)
		return
	}
	full, info, name, err := regularInside(req.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !textExts[ext] || blockedExts[ext] || info.Size() > maxTextBytes {
		refuseFile(w)
		return
	}
	if err := writeAtom(full, []byte(req.Text)); err != nil {
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) renameEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !readBody(w, r, &req) {
		return
	}
	rel, full, info, err := entryInside(req.Path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	next, err := safeUploadName(req.Name)
	if err != nil {
		http.Error(w, "that name is not allowed", http.StatusBadRequest)
		return
	}
	if next == info.Name() {
		writeJSON(w, http.StatusOK, map[string]string{"name": next, "rel": rel})
		return
	}
	dest := filepath.Join(filepath.Dir(full), next)
	if !insideUpload(dest) {
		http.Error(w, "that name is not allowed", http.StatusBadRequest)
		return
	}
	if _, err := os.Lstat(dest); err == nil {
		http.Error(w, "a file with that name already exists", http.StatusConflict)
		return
	}
	if err := os.Rename(full, dest); err != nil {
		http.Error(w, "could not rename", http.StatusInternalServerError)
		return
	}
	parent := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		parent = rel[:i]
	} else {
		parent = ""
	}
	newRel := next
	if parent != "" {
		newRel = parent + "/" + next
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": next, "rel": newRel})
}

func (s *server) deleteFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readBody(w, r, &req) {
		return
	}
	_, full, info, err := entryInside(req.Path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if err := os.Remove(full); err != nil {
		http.Error(w, "could not delete", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func refuseFile(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "attachment")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("this file is not previewed"))
}

func readBody(w http.ResponseWriter, r *http.Request, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxTextBytes+8192)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeAtom(full string, data []byte) error {
	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, ".ft-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		if err2 := os.Remove(full); err2 != nil {
			return err
		}
		if err2 := os.Rename(tmpName, full); err2 != nil {
			return err2
		}
	}
	ok = true
	return nil
}

func (s *server) shutdown(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		requestStop()
		stopWindow()
	}()
}

func fileKind(name string, dir bool) string {
	if dir {
		return "folder"
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case blockedExts[ext]:
		return "blocked"
	case ext == ".pdf":
		return "pdf"
	case ext == ".docx" || ext == ".xlsx" || ext == ".pptx":
		return "office"
	case ext == ".zip":
		return "zip"
	case imageTypes[ext] != "":
		return "image"
	case videoExts[ext]:
		return "video"
	case audioTypes[ext] != "":
		return "audio"
	case textExts[ext]:
		return "text"
	default:
		return "file"
	}
}

func mediaType(ext string) (string, bool) {
	if ext == ".pdf" {
		return "application/pdf", true
	}
	if ctype, ok := imageTypes[ext]; ok {
		return ctype, true
	}
	if ctype, ok := directVideo[ext]; ok {
		return ctype, true
	}
	if ctype, ok := audioTypes[ext]; ok {
		return ctype, true
	}
	return "", false
}

func regularInside(raw string) (full string, info os.FileInfo, name string, err error) {
	rel, link, lst, err := entryInside(raw)
	if err != nil || lst.IsDir() {
		return "", nil, "", os.ErrInvalid
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil || !insideUpload(resolved) {
		return "", nil, "", os.ErrInvalid
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() {
		return "", nil, "", os.ErrInvalid
	}
	return resolved, st, filepath.Base(rel), nil
}

func entryInside(raw string) (rel, full string, info os.FileInfo, err error) {
	rel, err = parseRel(raw)
	if err != nil || rel == "" {
		return "", "", nil, os.ErrInvalid
	}
	full, err = absInside(rel)
	if err != nil {
		return "", "", nil, err
	}
	info, err = os.Lstat(full)
	if err != nil {
		return "", "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil || !insideUpload(resolved) {
			return "", "", nil, os.ErrInvalid
		}
	}
	return rel, full, info, nil
}

func insideUpload(full string) bool {
	root, err := filepath.Abs(saveRoot())
	if err != nil {
		return false
	}
	full, err = filepath.Abs(full)
	if err != nil || !sameOrInside(root, full) {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		return sameOrInside(resolvedRoot, resolved)
	}
	return true
}

func safeUploadName(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || strings.Contains(raw, "/") || strings.Contains(raw, ":") {
		return "", os.ErrInvalid
	}
	base := filepath.Base(raw)
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
	stem = strings.Trim(stem, " .")
	if stem == "" || stem == "." || stem == ".." {
		return "", os.ErrInvalid
	}
	return stem + ext, nil
}

// parseRel keeps a slash-separated path under uploads. ".." and absolute paths are rejected.
func parseRel(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." {
		return "", nil
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(raw, "/") || strings.Contains(raw, ":") {
		return "", os.ErrInvalid
	}
	parts := strings.Split(raw, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", os.ErrInvalid
		}
		clean = append(clean, part)
	}
	return strings.Join(clean, "/"), nil
}

func absInside(rel string) (string, error) {
	rel, err := parseRel(rel)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(saveRoot())
	if err != nil {
		return "", err
	}
	full := root
	if rel != "" {
		full = filepath.Join(root, filepath.FromSlash(rel))
	}
	full, err = filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if !sameOrInside(root, full) {
		return "", os.ErrInvalid
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil && !sameOrInside(resolvedRoot, resolved) {
		return "", os.ErrInvalid
	}
	return full, nil
}

func sameOrInside(dir, target string) bool {
	if dir == target {
		return true
	}
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func displayName(name string, dir bool) string {
	if !dir {
		return name
	}
	switch strings.ToLower(name) {
	case "iphone":
		return "iPhone"
	case "ipad":
		return "iPad"
	case "android":
		return "Android"
	case "other":
		return "Other"
	case "files":
		return "Files"
	default:
		return name
	}
}

func typeLabel(name string, dir bool) string {
	if dir {
		return "File folder"
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "PNG image"
	case ".jpg", ".jpeg":
		return "JPEG image"
	case ".gif":
		return "GIF image"
	case ".webp":
		return "WEBP image"
	case ".heic", ".heif":
		return "HEIC image"
	case "":
		return "File"
	default:
		return strings.ToUpper(strings.TrimPrefix(ext, ".")) + " file"
	}
}

func crumbsFor(rel string) []crumb {
	out := []crumb{{Rel: "", Label: rootLabel()}}
	if rel == "" {
		return out
	}
	parts := strings.Split(rel, "/")
	acc := ""
	for _, part := range parts {
		if acc == "" {
			acc = part
		} else {
			acc = acc + "/" + part
		}
		out = append(out, crumb{Rel: acc, Label: displayName(part, true)})
	}
	return out
}

func plainFileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) || name != filepath.Base(name) {
		return false
	}
	return true
}

func quoteName(name string) string {
	return strings.NewReplacer(`\`, `_`, `"`, `_`, "\r", "", "\n", "").Replace(name)
}
