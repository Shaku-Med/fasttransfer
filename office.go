package main

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

var errNotOffice = errors.New("not an office file")

func (s *server) officeText(w http.ResponseWriter, r *http.Request) {
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	text, err := extractOffice(full, strings.ToLower(filepath.Ext(name)))
	if err != nil {
		http.Error(w, "This document could not be read.", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(text))
}

func extractOffice(path, ext string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var names []string
	switch ext {
	case ".docx":
		names = []string{"word/document.xml"}
	case ".xlsx":
		names = []string{"xl/sharedStrings.xml"}
	case ".pptx":
		for _, f := range zr.File {
			n := strings.ToLower(f.Name)
			if strings.HasPrefix(n, "ppt/slides/slide") && strings.HasSuffix(n, ".xml") {
				names = append(names, f.Name)
			}
		}
		sort.Strings(names)
		if len(names) > 40 {
			names = names[:40]
		}
	default:
		return "", errNotOffice
	}
	var b strings.Builder
	for _, name := range names {
		entry := zipFind(zr, name)
		if entry == nil {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			continue
		}
		chunk := xmlText(rc)
		rc.Close()
		if chunk == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(chunk)
		if b.Len() > 200000 {
			break
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return "This document has no extracted text.", nil
	}
	if len(text) > 200000 {
		text = text[:200000]
	}
	return text, nil
}

func zipFind(zr *zip.ReadCloser, name string) *zip.File {
	want := strings.ToLower(name)
	for _, f := range zr.File {
		if strings.ToLower(f.Name) == want {
			return f
		}
	}
	return nil
}

func xmlText(r io.Reader) string {
	dec := xml.NewDecoder(io.LimitReader(r, 4<<20))
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "t" {
			continue
		}
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" || !utf8.ValidString(s) {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
		if b.Len() > 200000 {
			break
		}
	}
	return b.String()
}
