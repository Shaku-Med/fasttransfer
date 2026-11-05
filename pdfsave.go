package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// PDFs are drawn and edited in the window with pdf.js and pdf-lib. The server
// only hands the file over and takes the edited copy back.
const maxPDFSave = 300 << 20

var vendorTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".gif":   "image/gif",
	".png":   "image/png",
	".bcmap": "application/octet-stream",
	".pfb":   "application/octet-stream",
	".ttf":   "font/ttf",
}

var (
	vendorMu   sync.Mutex
	vendorTags = map[string]string{}
)

// vendorFile serves the bundled libraries under web/vendor. Only known file
// types go out, and browsers revalidate by fingerprint so an update lands at once.
func vendorFile(w http.ResponseWriter, r *http.Request) {
	rel := r.PathValue("path")
	ctype := vendorTypes[strings.ToLower(path.Ext(rel))]
	if ctype == "" || !fs.ValidPath(rel) || strings.Contains(rel, "\\") {
		http.NotFound(w, r)
		return
	}
	name := "web/vendor/" + rel
	b, err := webFiles.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	vendorMu.Lock()
	etag := vendorTags[name]
	if etag == "" {
		sum := sha256.Sum256(b)
		etag = `"` + hex.EncodeToString(sum[:8]) + `"`
		vendorTags[name] = etag
	}
	vendorMu.Unlock()
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

var errNotPDF = errors.New("not a pdf")

var pdfSaveLimit = newLimiter(0.5, 6)

// pdfSave writes an edited PDF over the original, or next to it as a copy.
func (s *server) pdfSave(w http.ResponseWriter, r *http.Request) {
	savePDF(w, r, r.URL.Query().Get("mode") == "copy")
}

// phonePDFSave never overwrites. A phone holding the key can already add files,
// but replacing one on the PC stays a choice made at the PC.
func (s *server) phonePDFSave(w http.ResponseWriter, r *http.Request) {
	savePDF(w, r, true)
}

// The body is streamed to a temp file in the same folder and only moved into
// place once it is complete and starts like a PDF.
func savePDF(w http.ResponseWriter, r *http.Request, copyMode bool) {
	if !pdfSaveLimit.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many saves, wait a moment"})
		return
	}
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil || strings.ToLower(filepath.Ext(name)) != ".pdf" {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPDFSave)
	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, ".ft-pdf-*.tmp")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
		return
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	body := bufio.NewReader(r.Body)
	head, _ := body.Peek(5)
	if string(head) != "%PDF-" {
		_ = tmp.Close()
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "that is not a PDF"})
		return
	}
	_, err = io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "that PDF is over 300 MB"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "the save stopped partway"})
		return
	}

	target := full
	if copyMode {
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		target = ""
		for i := 1; i < 100; i++ {
			candidate := stem + " (edited).pdf"
			if i > 1 {
				candidate = stem + " (edited " + strconv.Itoa(i) + ").pdf"
			}
			p := filepath.Join(dir, candidate)
			if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
				target = p
				break
			}
		}
		if target == "" || !insideUpload(target) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "could not pick a name for the copy"})
			return
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		if copyMode || os.Remove(target) != nil || os.Rename(tmpName, target) != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save"})
			return
		}
	}
	keep = true
	writeJSON(w, http.StatusOK, map[string]string{"name": filepath.Base(target)})
}
