package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"

	"image"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Pictures in documents. Each one is uploaded once, kept in the docs folder
// under its document, and the document only stores its path. Anyone who can
// open the document can see its pictures; nobody else can.

func (h *docHub) mediaDir(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == "" || !docIDRe.MatchString(id) {
		return ""
	}
	return filepath.Join(h.dir, "media", id)
}

// Every document's pictures together stay under this, so nobody with the
// link can fill the PC's disk one document at a time.
const maxAllMedia = 2 << 30

func mediaTotal(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

var pictureExts = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

var mediaTypes = map[string]string{
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
}

func docsImage(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !clipLimit.allow(clientIP(r)) {
			slowDown(w)
			return
		}
		q := r.URL.Query()
		if _, ok := who(r, q.Get("session")); !ok {
			notConnected(w)
			return
		}
		id := q.Get("id")
		docs.mu.Lock()
		_, err := docs.loadLocked(id)
		docs.mu.Unlock()
		if err != nil {
			docError(w, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxDocImage)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "pictures can be up to 10 MB"})
			return
		}
		// The type comes from the bytes, not from what the browser claims.
		ext := pictureExts[http.DetectContentType(data)]
		if ext == "" {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "use a PNG, JPG, GIF or WebP picture"})
			return
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > maxImagePixels {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "that picture could not be read"})
			return
		}
		dir := docs.mediaDir(id)
		if dir == "" || os.MkdirAll(dir, 0o700) != nil {
			docError(w, errDocsOff)
			return
		}
		entries, _ := os.ReadDir(dir)
		var used int64
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				used += info.Size()
			}
		}
		if len(entries) >= maxDocImages || used+int64(len(data)) > maxDocMedia {
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": "this document has too many pictures"})
			return
		}
		if mediaTotal(filepath.Dir(dir))+int64(len(data)) > maxAllMedia {
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": "documents are holding too many pictures on this PC"})
			return
		}
		name, err := randHex(8)
		if err != nil {
			docError(w, err)
			return
		}
		name += "." + ext
		if err := writeAtom(filepath.Join(dir, name), data); err != nil {
			docError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": "/docmedia/" + id + "/" + name})
	}
}

// phoneAllowed accepts the transfer key or the short-lived phone cookie. A
// picture is loaded by an <img>, which cannot send the key header.
func (s *server) phoneAllowed(w http.ResponseWriter, r *http.Request) bool {
	if got := r.Header.Get("X-Transfer-Key"); got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.key)) == 1 {
		return true
	}
	return s.touchPhone(w, r)
}

func (s *server) docMedia(w http.ResponseWriter, r *http.Request) {
	if !allowLocal(r) && !s.phoneAllowed(w, r) {
		http.NotFound(w, r)
		return
	}
	m := mediaRe.FindStringSubmatch("/docmedia/" + r.PathValue("doc") + "/" + r.PathValue("file"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	docs.mu.Lock()
	known := docs.index[m[1]] != nil
	docs.mu.Unlock()
	dir := docs.mediaDir(m[1])
	if !known || dir == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(dir, m[2]))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mediaTypes[strings.TrimPrefix(filepath.Ext(m[2]), ".")])
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, m[2], info.ModTime(), f)
}

// inlineImage turns a stored picture into a data URL, so an exported page
// carries its pictures with it.
func inlineImage(mediaDir, path string) string {
	m := mediaRe.FindStringSubmatch(path)
	if m == nil || mediaDir == "" || filepath.Base(mediaDir) != m[1] {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(mediaDir, m[2]))
	if err != nil || len(data) > maxDocImage {
		return ""
	}
	return "data:" + mediaTypes[strings.TrimPrefix(filepath.Ext(m[2]), ".")] + ";base64," + base64.StdEncoding.EncodeToString(data)
}
