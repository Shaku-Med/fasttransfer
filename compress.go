package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

var (
	gzipMu    sync.Mutex
	gzipCache = map[string][]byte{}
)

// writeShipped sends a file built into the app, gzipped when the browser
// takes it. Pages and scripts shrink to about a quarter, which is most of the
// wait on a phone over Wi-Fi. Each file is compressed once and kept.
func writeShipped(w http.ResponseWriter, r *http.Request, name string, b []byte) {
	w.Header().Add("Vary", "Accept-Encoding")
	if len(b) < 1024 || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
		return
	}
	gzipMu.Lock()
	gz, ok := gzipCache[name]
	gzipMu.Unlock()
	if !ok {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(b)
		_ = zw.Close()
		gz = buf.Bytes()
		gzipMu.Lock()
		gzipCache[name] = gz
		gzipMu.Unlock()
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
	_, _ = w.Write(gz)
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		q := strings.ReplaceAll(strings.ToLower(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}
