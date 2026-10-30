package main

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Live text is one shared pad that the PC and every phone type into, plus a
// short list of saved clips. It only lives in memory, so quitting clears it.
const (
	maxLiveText  = 64 << 10
	maxClipText  = 16 << 10
	maxClips     = 40
	liveWait     = 25 * time.Second
	maxLiveWaits = 64
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
}

type liveHub struct {
	mu    sync.Mutex
	ver   int64
	text  string
	by    string
	tab   string
	at    time.Time
	clips []clip
	wake  chan struct{}
	waits atomic.Int32
}

var pad = &liveHub{wake: make(chan struct{})}

type clipView struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	By   string `json:"by"`
	From string `json:"from"`
	Age  int64  `json:"age"`
}

type liveView struct {
	Ver   int64      `json:"ver"`
	Text  string     `json:"text"`
	By    string     `json:"by"`
	Tab   string     `json:"tab"`
	Age   int64      `json:"age"`
	Clips []clipView `json:"clips"`
}

func (h *liveHub) viewLocked() liveView {
	now := time.Now()
	out := liveView{Ver: h.ver, Text: h.text, By: h.by, Tab: h.tab, Clips: make([]clipView, 0, len(h.clips))}
	if !h.at.IsZero() {
		out.Age = now.Sub(h.at).Milliseconds()
	}
	for _, c := range h.clips {
		out.Clips = append(out.Clips, clipView{ID: c.ID, Text: c.Text, By: c.By, From: c.From, Age: now.Sub(c.At).Milliseconds()})
	}
	return out
}

// bumpLocked wakes every waiting poll by closing the current channel.
func (h *liveHub) bumpLocked() {
	h.ver++
	close(h.wake)
	h.wake = make(chan struct{})
}

// wait reports false when too many polls are already parked, so the caller can
// tell that client to back off instead of answering at once and letting it spin.
func (h *liveHub) wait(r *http.Request, since int64) (liveView, bool) {
	h.mu.Lock()
	if h.ver != since {
		v := h.viewLocked()
		h.mu.Unlock()
		return v, true
	}
	wake := h.wake
	h.mu.Unlock()

	if h.waits.Add(1) > maxLiveWaits {
		h.waits.Add(-1)
		return liveView{}, false
	}
	defer h.waits.Add(-1)
	timer := time.NewTimer(liveWait)
	defer timer.Stop()
	select {
	case <-wake:
	case <-timer.C:
	case <-r.Context().Done():
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.viewLocked(), true
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
	if len(l.seen) > 512 {
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

func liveSince(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

func liveGet(w http.ResponseWriter, r *http.Request) {
	v, ok := pad.wait(r, liveSince(r))
	if !ok {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many open connections"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// liveTyping returns the handler for one side. The PC is always "PC". A
// phone names itself, cleaned the same way as its connect name.
func liveTyping(fromPhone bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !typeLimit.allow(clientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down a little"})
			return
		}
		var req struct {
			Text string `json:"text"`
			Tab  string `json:"tab"`
			Name string `json:"name"`
		}
		if !readBody(w, r, &req) {
			return
		}
		text, ok := cleanLiveText(req.Text, maxLiveText)
		if !ok || !tabToken.MatchString(req.Tab) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that text could not be sent"})
			return
		}
		by := "PC"
		if fromPhone {
			by = cleanPhoneName(req.Name)
		}
		pad.mu.Lock()
		if pad.text != text {
			pad.text = text
			pad.by = by
			pad.tab = req.Tab
			pad.at = time.Now()
			pad.bumpLocked()
		}
		ver := pad.ver
		pad.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int64{"ver": ver})
	}
}

func clipAdd(fromPhone bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !clipLimit.allow(clientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down a little"})
			return
		}
		var req struct {
			Text string `json:"text"`
			Name string `json:"name"`
		}
		if !readBody(w, r, &req) {
			return
		}
		text, ok := cleanLiveText(req.Text, maxClipText)
		if !ok || strings.TrimSpace(text) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that clip is empty or too long"})
			return
		}
		id, err := randHex(8)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the clip"})
			return
		}
		c := clip{ID: id, Text: text, By: "PC", From: "pc", At: time.Now()}
		if fromPhone {
			c.By = cleanPhoneName(req.Name)
			c.From = "phone"
		}
		pad.mu.Lock()
		pad.clips = append([]clip{c}, pad.clips...)
		if len(pad.clips) > maxClips {
			pad.clips = pad.clips[:maxClips]
		}
		pad.bumpLocked()
		pad.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func clipRemove(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down a little"})
		return
	}
	var req struct {
		ID  string `json:"id"`
		All bool   `json:"all"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !req.All && !clipID.MatchString(req.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that clip is not here"})
		return
	}
	pad.mu.Lock()
	defer pad.mu.Unlock()
	before := len(pad.clips)
	if req.All {
		pad.clips = nil
	} else {
		kept := pad.clips[:0]
		for _, c := range pad.clips {
			if c.ID != req.ID {
				kept = append(kept, c)
			}
		}
		pad.clips = kept
	}
	if len(pad.clips) != before {
		pad.bumpLocked()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
