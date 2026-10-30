package main

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Each phone has its own live pad with the PC, so a room full of phones never
// types over each other. Clips go to one phone, or to every phone at once.
// None of it is written to disk, so quitting clears it.
const (
	maxLiveText = 64 << 10
	maxClipText = 16 << 10
	maxClips    = 40
	clipPreview = 200
	// Every pad and clip on the PC shares this, so a flood of phones cannot
	// grow memory without end. Past it, new text is refused until some is cleared.
	maxTextBudget = 64 << 20
)

var (
	tabToken = regexp.MustCompile(`^[a-z0-9]{8,16}$`)
	clipID   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

type clip struct {
	ID   string
	Text string
	By   string
	From string
	At   time.Time
	n    int64
}

// The clock on Windows can hand two clips the same time, so order comes from here.
var clipCount atomic.Int64

type clipView struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	By   string `json:"by"`
	From string `json:"from"`
	Age  int64  `json:"age"`
}

// A rough per address token bucket. Typing sends often, so the live pad gets a
// generous rate and clips get a tighter one.
type limiter struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	seen  map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(perSecond, burst float64) *limiter {
	return &limiter{rate: perSecond, burst: burst, seen: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.seen) > 8192 {
		for k, b := range l.seen {
			if now.Sub(b.last) > time.Minute {
				delete(l.seen, k)
			}
		}
	}
	b := l.seen[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.seen[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

var (
	typeLimit = newLimiter(20, 40)
	clipLimit = newLimiter(2, 12)
)

func cleanLiveText(raw string, limit int) (string, bool) {
	if len(raw) > limit || !utf8.ValidString(raw) {
		return "", false
	}
	return strings.ReplaceAll(raw, "\x00", ""), true
}

func preview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= clipPreview {
		return text
	}
	r := []rune(text)
	return string(r[:clipPreview]) + "…"
}

func slowDown(w http.ResponseWriter) {
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down a little"})
}

func notConnected(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not connected"})
}

func badText(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that text could not be sent"})
}

func newClip(text, by, from string) (clip, bool) {
	id, err := randHex(8)
	if err != nil {
		return clip{}, false
	}
	return clip{ID: id, Text: text, By: by, From: from, At: time.Now(), n: clipCount.Add(1)}, true
}

func (h *deviceHub) fitsLocked(extra int) bool {
	return extra <= 0 || h.textBytes+int64(extra) <= maxTextBudget
}

func (h *deviceHub) setPadLocked(d *device, p padState) {
	h.textBytes += int64(len(p.text) - len(d.pad.text))
	d.pad = p
}

func (h *deviceHub) pushClipLocked(list []clip, c clip) []clip {
	list = append([]clip{c}, list...)
	h.textBytes += int64(len(c.Text))
	if len(list) > maxClips {
		for _, old := range list[maxClips:] {
			h.textBytes -= int64(len(old.Text))
		}
		list = list[:maxClips]
	}
	return list
}

func (h *deviceHub) dropClipLocked(list []clip, id string, all bool) ([]clip, bool) {
	kept := list[:0]
	for _, c := range list {
		if all || c.ID == id {
			h.textBytes -= int64(len(c.Text))
			continue
		}
		kept = append(kept, c)
	}
	return kept, len(kept) != len(list)
}

func tooMuchText(w http.ResponseWriter) {
	writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": "the PC is holding too much text, clear some clips"})
}

func (s *server) phoneLive(w http.ResponseWriter, r *http.Request) {
	if !typeLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Session string `json:"session"`
		Text    string `json:"text"`
		Tab     string `json:"tab"`
	}
	if !readBody(w, r, &req) {
		return
	}
	text, ok := cleanLiveText(req.Text, maxLiveText)
	if !ok || !tabToken.MatchString(req.Tab) {
		badText(w)
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(req.Session)
	if d == nil {
		notConnected(w)
		return
	}
	hub.touchLocked(d)
	if d.pad.text != text {
		if !hub.fitsLocked(len(text) - len(d.pad.text)) {
			tooMuchText(w)
			return
		}
		now := time.Now()
		hub.setPadLocked(d, padState{text: text, by: d.name, tab: req.Tab, at: now, told: d.pad.told})
		d.bumpLocked()
		if now.Sub(d.pad.told) > 2*time.Second {
			d.pad.told = now
			hub.emitLocked(feedEvent{Kind: "typing", Handle: d.handle, Name: d.name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]int64{"ver": d.ver})
}

func (s *server) phoneClip(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Session string `json:"session"`
		Text    string `json:"text"`
	}
	if !readBody(w, r, &req) {
		return
	}
	text, ok := cleanLiveText(req.Text, maxClipText)
	if !ok || strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that clip is empty or too long"})
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(req.Session)
	if d == nil {
		notConnected(w)
		return
	}
	if !hub.fitsLocked(len(text)) {
		tooMuchText(w)
		return
	}
	c, ok := newClip(text, d.name, "phone")
	if !ok {
		badText(w)
		return
	}
	hub.touchLocked(d)
	d.clips = hub.pushClipLocked(d.clips, c)
	d.bumpLocked()
	hub.emitLocked(feedEvent{Kind: "clip", Handle: d.handle, Name: d.name, Text: preview(text)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// A phone can only remove clips in its own list. Clips sent to everyone stay put.
func (s *server) phoneClipRemove(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Session string `json:"session"`
		ID      string `json:"id"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !clipID.MatchString(req.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that clip is not here"})
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(req.Session)
	if d == nil {
		notConnected(w)
		return
	}
	var changed bool
	if d.clips, changed = hub.dropClipLocked(d.clips, req.ID, false); changed {
		d.bumpLocked()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type deskLiveView struct {
	Ver    int64      `json:"ver"`
	Name   string     `json:"name"`
	Online bool       `json:"online"`
	Text   string     `json:"text"`
	By     string     `json:"by"`
	Tab    string     `json:"tab"`
	Age    int64      `json:"age"`
	Clips  []clipView `json:"clips"`
}

func (h *deviceHub) deskViewLocked(d *device) deskLiveView {
	now := time.Now()
	if d == nil {
		return deskLiveView{Ver: h.sver, Name: "Everyone", Clips: clipViews(now, h.shared)}
	}
	return deskLiveView{
		Ver:    d.ver,
		Name:   d.name,
		Online: d.online,
		Text:   d.pad.text,
		By:     d.pad.by,
		Tab:    d.pad.tab,
		Age:    padAge(now, d.pad),
		Clips:  clipViews(now, d.clips),
	}
}

// deskTarget resolves "all" to the shared list and anything else to one device.
func (h *deviceHub) deskTargetLocked(raw string) (d *device, everyone, ok bool) {
	if raw == "all" {
		return nil, true, true
	}
	d = h.byHandleLocked(raw)
	return d, false, d != nil
}

func (s *server) deskLive(w http.ResponseWriter, r *http.Request) {
	since := pollSince(r)
	hub.mu.Lock()
	d, everyone, ok := hub.deskTargetLocked(r.URL.Query().Get("device"))
	if !ok {
		hub.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that phone is gone"})
		return
	}
	v := hub.deskViewLocked(d)
	if v.Ver != since {
		hub.mu.Unlock()
		writeJSON(w, http.StatusOK, v)
		return
	}
	wake := hub.swake
	if !everyone {
		wake = d.wake
	}
	hub.mu.Unlock()

	if !hub.park(r, wake) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many open connections"})
		return
	}
	hub.mu.Lock()
	v = hub.deskViewLocked(d)
	hub.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deskType(w http.ResponseWriter, r *http.Request) {
	if !typeLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Device string `json:"device"`
		Text   string `json:"text"`
		Tab    string `json:"tab"`
	}
	if !readBody(w, r, &req) {
		return
	}
	text, ok := cleanLiveText(req.Text, maxLiveText)
	if !ok || !tabToken.MatchString(req.Tab) {
		badText(w)
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.byHandleLocked(req.Device)
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that phone is gone"})
		return
	}
	if d.pad.text != text {
		if !hub.fitsLocked(len(text) - len(d.pad.text)) {
			tooMuchText(w)
			return
		}
		hub.setPadLocked(d, padState{text: text, by: "PC", tab: req.Tab, at: time.Now(), told: d.pad.told})
		d.bumpLocked()
	}
	writeJSON(w, http.StatusOK, map[string]int64{"ver": d.ver})
}

func (s *server) deskClip(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Device string `json:"device"`
		Text   string `json:"text"`
	}
	if !readBody(w, r, &req) {
		return
	}
	text, ok := cleanLiveText(req.Text, maxClipText)
	if !ok || strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that clip is empty or too long"})
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d, everyone, found := hub.deskTargetLocked(req.Device)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that phone is gone"})
		return
	}
	if !hub.fitsLocked(len(text)) {
		tooMuchText(w)
		return
	}
	from := "pc"
	if everyone {
		from = "all"
	}
	c, ok := newClip(text, "PC", from)
	if !ok {
		badText(w)
		return
	}
	if everyone {
		hub.shared = hub.pushClipLocked(hub.shared, c)
		hub.sharedChangedLocked()
	} else {
		d.clips = hub.pushClipLocked(d.clips, c)
		d.bumpLocked()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) deskClipRemove(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Device string `json:"device"`
		ID     string `json:"id"`
		All    bool   `json:"all"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !req.All && !clipID.MatchString(req.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that clip is not here"})
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d, everyone, found := hub.deskTargetLocked(req.Device)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that phone is gone"})
		return
	}
	var changed bool
	if everyone {
		if hub.shared, changed = hub.dropClipLocked(hub.shared, req.ID, req.All); changed {
			hub.sharedChangedLocked()
		}
	} else if d.clips, changed = hub.dropClipLocked(d.clips, req.ID, req.All); changed {
		d.bumpLocked()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Shared clips sit in every phone's mailbox, so each one has to be woken.
func (h *deviceHub) sharedChangedLocked() {
	h.sver++
	close(h.swake)
	h.swake = make(chan struct{})
	for _, d := range h.byKey {
		d.bumpLocked()
	}
}
