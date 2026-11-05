package main

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// One capture of the PC's sound feeds every phone listening. It starts with
// the first listener and stops with the last, so nothing listens when nobody
// is. A phone that falls behind loses a little sound instead of building lag.

type soundStart struct {
	rate int
	err  error
}

type soundHub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
	stop chan struct{}
	rate int
}

var (
	sound      = &soundHub{subs: map[chan []byte]bool{}}
	startSound = captureSound
	soundLimit = newLimiter(1, 5)
	soundSlots = make(chan struct{}, 3)
)

func (h *soundHub) join() (chan []byte, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop == nil {
		stop := make(chan struct{})
		ready := make(chan soundStart, 1)
		go func() {
			startSound(stop, ready, h.push)
			h.ended(stop)
		}()
		select {
		case s := <-ready:
			if s.err != nil {
				return nil, 0, s.err
			}
			h.rate = s.rate
		case <-time.After(3 * time.Second):
			close(stop)
			return nil, 0, errors.New("the PC sound did not start")
		}
		h.stop = stop
	}
	ch := make(chan []byte, 50)
	h.subs[ch] = true
	return ch, h.rate, nil
}

func (h *soundHub) leave(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.subs[ch] {
		return
	}
	delete(h.subs, ch)
	close(ch)
	if len(h.subs) == 0 && h.stop != nil {
		close(h.stop)
		h.stop = nil
	}
}

func (h *soundHub) push(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

// ended runs when a capture stops on its own, for example when headphones
// are plugged in. Listeners are let go so their phones reconnect to the new
// speakers.
func (h *soundHub) ended(stop chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != stop {
		return
	}
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
	h.stop = nil
}

func grantAlive(g *controlGrant) bool {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.grants[g.handle] == g
}

// phoneControlSound streams the PC's sound as 16 bit stereo at the rate in
// X-Audio-Rate, for as long as the phone stays in control.
func (s *server) phoneControlSound(w http.ResponseWriter, r *http.Request) {
	if !soundLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	g := grantFor(r, r.URL.Query().Get("session"))
	if g == nil {
		controlEnded(w)
		return
	}
	if !loopbackSupported {
		controlErr(w, http.StatusNotImplemented, "sound from the PC works on Windows for now")
		return
	}
	select {
	case soundSlots <- struct{}{}:
		defer func() { <-soundSlots }()
	default:
		slowDown(w)
		return
	}
	ch, rate, err := sound.join()
	if err != nil {
		controlErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer sound.leave(ch)

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Audio-Rate", strconv.Itoa(rate))
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := w.Write(b); err != nil || rc.Flush() != nil {
				return
			}
		case <-tick.C:
			if !grantAlive(g) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
