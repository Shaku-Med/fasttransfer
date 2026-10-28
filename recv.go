package main

import (
	"io"
	"net/http"
	"sync"
)

// recvSnap is what the desktop window polls while a phone is sending.
type recvSnap struct {
	Active bool   `json:"active"`
	Name   string `json:"name"`
	Dest   string `json:"dest"`
	Got    int64  `json:"got"`
	Total  int64  `json:"total"`
	Index  int    `json:"index"`
	Count  int    `json:"count"`
	Done   bool   `json:"done"`
	Gen    int64  `json:"gen"`
}

type recvTracker struct {
	mu sync.Mutex
	recvSnap
}

var uploads recvTracker

func (t *recvTracker) begin(dest string, total int64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Gen++
	t.Active = true
	t.Done = false
	t.Name = ""
	t.Dest = dest
	t.Got = 0
	if total < 0 {
		total = 0
	}
	t.Total = total
	t.Index = 0
	t.Count = 0
	return t.Gen
}

func (t *recvTracker) file(id int64, name string, index int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Gen != id {
		return
	}
	t.Name = name
	t.Index = index
	t.Count = index
}

func (t *recvTracker) progress(id, got int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Gen != id || !t.Active {
		return
	}
	t.Got = got
}

func (t *recvTracker) finish(id int64, count int, dest string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Gen != id {
		return
	}
	t.Active = false
	t.Done = true
	t.Count = count
	t.Dest = dest
	if t.Total > 0 {
		t.Got = t.Total
	}
}

func (t *recvTracker) abort(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Gen != id {
		return
	}
	t.Active = false
	t.Done = false
}

func (t *recvTracker) snapshot() recvSnap {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.recvSnap
}

func recvStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, uploads.snapshot())
}

// progReader counts bytes as the multipart body is written to disk.
type progReader struct {
	r  io.Reader
	id int64
	n  int64
}

func (p *progReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		uploads.progress(p.id, p.n)
	}
	return n, err
}

func (p *progReader) Close() error {
	if c, ok := p.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
