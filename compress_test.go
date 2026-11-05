package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"gzip":                    true,
		"gzip, deflate, br, zstd": true,
		"br;q=1.0, gzip;q=0.8":    true,
		"gzip;q=0":                false,
		"gzip; q=0.000, identity": false,
		"x-gzip, deflate":         false,
		"identity":                false,
		"GZIP":                    true,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestShippedFilesAreGzipped(t *testing.T) {
	handler := asset("web/docs.js", "text/javascript; charset=utf-8")
	plain, _ := webFiles.ReadFile("web/docs.js")

	req := httptest.NewRequest(http.MethodGet, "/docs.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip, got %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Body.Len() >= len(plain)/2 {
		t.Fatalf("gzip barely helped: %d of %d bytes", rec.Body.Len(), len(plain))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	back, _ := io.ReadAll(zr)
	if !bytes.Equal(back, plain) {
		t.Fatal("gzip body does not match the file")
	}

	req = httptest.NewRequest(http.MethodGet, "/docs.js", nil)
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), plain) {
		t.Fatal("a browser without gzip should get the plain file")
	}
}
