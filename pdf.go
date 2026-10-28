package main

import (
	"bytes"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unidoc/unipdf/v3/common"
	"github.com/unidoc/unipdf/v3/model"
	"github.com/unidoc/unipdf/v3/render"
)

const maxPDFPages = 400

func init() {
	common.SetLogger(common.NewConsoleLogger(common.LogLevelError))
}

type pdfShot struct {
	png []byte
	at  time.Time
}

var (
	pdfMu    sync.Mutex
	pdfCache = map[string]pdfShot{}
	renderMu sync.Mutex
)

func (s *server) pdfPage(w http.ResponseWriter, r *http.Request) {
	full, info, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil || strings.ToLower(filepath.Ext(name)) != ".pdf" {
		http.NotFound(w, r)
		return
	}
	pageRaw := r.URL.Query().Get("page")
	if pageRaw == "" {
		n, err := pdfPageCount(full)
		if err != nil || n < 1 {
			http.Error(w, "This PDF could not be rendered.", http.StatusUnsupportedMediaType)
			return
		}
		if n > maxPDFPages {
			n = maxPDFPages
		}
		writeJSON(w, http.StatusOK, map[string]any{"pages": n})
		return
	}
	page, err := strconv.Atoi(pageRaw)
	if err != nil || page < 1 || page > maxPDFPages {
		http.Error(w, "This PDF could not be rendered.", http.StatusBadRequest)
		return
	}
	width := 1200
	if raw := r.URL.Query().Get("w"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			width = n
		}
	}
	if width < 64 {
		width = 64
	}
	if width > 1400 {
		width = 1400
	}
	key := full + "|" + info.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.Itoa(page) + "|" + strconv.Itoa(width)
	data, err := pdfPNG(key, full, page, width)
	if err != nil || len(data) == 0 {
		http.Error(w, "This PDF could not be rendered.", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(data)
}

func pdfPNG(key, path string, page, width int) ([]byte, error) {
	pdfMu.Lock()
	if shot, ok := pdfCache[key]; ok {
		shot.at = time.Now()
		pdfCache[key] = shot
		pdfMu.Unlock()
		return shot.png, nil
	}
	pdfMu.Unlock()

	renderMu.Lock()
	defer renderMu.Unlock()
	pdfMu.Lock()
	if shot, ok := pdfCache[key]; ok {
		pdfMu.Unlock()
		return shot.png, nil
	}
	pdfMu.Unlock()

	data, err := renderPDFPage(path, page, width)
	if err != nil {
		return nil, err
	}
	pdfMu.Lock()
	pdfCache[key] = pdfShot{png: data, at: time.Now()}
	for len(pdfCache) > 16 {
		var oldest string
		var when time.Time
		for k, shot := range pdfCache {
			if oldest == "" || shot.at.Before(when) {
				oldest = k
				when = shot.at
			}
		}
		delete(pdfCache, oldest)
	}
	pdfMu.Unlock()
	return data, nil
}

func pdfPageCount(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	reader, err := model.NewPdfReader(f)
	if err != nil {
		return 0, err
	}
	if enc, err := reader.IsEncrypted(); err != nil || enc {
		return 0, os.ErrInvalid
	}
	return reader.GetNumPages()
}

func renderPDFPage(path string, page, width int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader, err := model.NewPdfReader(f)
	if err != nil {
		return nil, err
	}
	if enc, err := reader.IsEncrypted(); err != nil || enc {
		return nil, os.ErrInvalid
	}
	n, err := reader.GetNumPages()
	if err != nil || page < 1 || page > n || page > maxPDFPages {
		return nil, os.ErrInvalid
	}
	pg, err := reader.GetPage(page)
	if err != nil {
		return nil, err
	}
	device := render.NewImageDevice()
	device.OutputWidth = width
	img, err := device.Render(pg)
	if err != nil || img == nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
