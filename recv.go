package main

import (
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// Uploads from many phones run side by side, so each one has its own entry.
// Byte counts are atomics because the reader bumps them on every chunk.
type upload struct {
	id    int64
	from  string
	dest  string
	total int64
	got   atomic.Int64
	mu    sync.Mutex
	name  string
	index int
}

type recvItem struct {
	Name  string `json:"name"`
	From  string `json:"from"`
	Dest  string `json:"dest"`
	Got   int64  `json:"got"`
	Total int64  `json:"total"`
	Index int    `json:"index"`
}

type recvSnap struct {
	Active int        `json:"active"`
	Got    int64      `json:"got"`
	Total  int64      `json:"total"`
	Items  []recvItem `json:"items"`
}

type recvTracker struct {
	mu     sync.Mutex
	next   int64
	active map[int64]*upload
}

var uploads = &recvTracker{active: map[int64]*upload{}}

func (t *recvTracker) begin(dest, from string, total int64) *upload {
	if total < 0 {
		total = 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	u := &upload{id: t.next, from: from, dest: dest, total: total}
	t.active[u.id] = u
	return u
}

func (u *upload) file(name string, index int) {
	u.mu.Lock()
	u.name = name
	u.index = index
	u.mu.Unlock()
}

func (t *recvTracker) finish(u *upload, count int) {
	t.mu.Lock()
	delete(t.active, u.id)
	t.mu.Unlock()
	u.mu.Lock()
	name := u.name
	u.mu.Unlock()
	hub.emit(feedEvent{Kind: "recv", Name: u.from, Text: name, Count: count, Dest: u.dest})
}

func (t *recvTracker) abort(u *upload) {
	t.mu.Lock()
	delete(t.active, u.id)
	t.mu.Unlock()
}

func (t *recvTracker) snapshot() recvSnap {
	t.mu.Lock()
	list := make([]*upload, 0, len(t.active))
	for _, u := range t.active {
		list = append(list, u)
	}
	t.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	snap := recvSnap{Active: len(list), Items: []recvItem{}}
	for _, u := range list {
		got := u.got.Load()
		snap.Got += got
		snap.Total += u.total
		if len(snap.Items) < 4 {
			u.mu.Lock()
			snap.Items = append(snap.Items, recvItem{Name: u.name, From: u.from, Dest: u.dest, Got: got, Total: u.total, Index: u.index})
			u.mu.Unlock()
		}
	}
	return snap
}

func recvStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, uploads.snapshot())
}

// progReader counts bytes as the multipart body is written to disk.
type progReader struct {
	r io.Reader
	u *upload
}

func (p *progReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.u.got.Add(int64(n))
	}
	return n, err
}

func (p *progReader) Close() error {
	if c, ok := p.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
