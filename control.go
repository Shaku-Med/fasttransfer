package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Remote control lets a phone drive this PC's mouse and keyboard. A phone gets
// there only by asking and someone at the PC saying yes. The yes hands that
// phone a random token every input must carry, and it ends when the phone
// leaves, sits idle, or the PC stops it. "Always allow" keeps just a hash of a
// second token the phone holds, so the file beside the exe is no key by itself.

const (
	askLife        = 60 * time.Second
	grantIdle      = 20 * time.Minute
	grantAwayGrace = 45 * time.Second
	heldButtonLife = 8 * time.Second
	maxPendingAsks = 4
	maxGrants      = 4
	maxTrusted     = 16
	maxInputEvents = 128
	maxInputText   = 512
	maxControlBody = 32 << 10
	maxMove        = 4000
	driverHold     = 1500 * time.Millisecond
	maxWheel       = 2400
)

var (
	errNoControl  = errors.New("remote control works on Windows PCs for now")
	controlHex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	askIDRe       = regexp.MustCompile(`^[0-9a-f]{16}$`)
	frameTagRe    = regexp.MustCompile(`^[0-9a-f]{8}$`)
	askLimit      = newLimiter(0.2, 4)
	inputLimit    = newLimiter(240, 480)
	screenLimit   = newLimiter(30, 30)
	screenSlots   = make(chan struct{}, 3)
	controlEnders = map[string]bool{"lock": true, "sleep": true}
)

// Virtual key codes by the names the phone may send. Only these get through.
var keyCodes = func() map[string]uint16 {
	m := map[string]uint16{
		"enter": 0x0D, "esc": 0x1B, "tab": 0x09, "backspace": 0x08, "space": 0x20,
		"delete": 0x2E, "insert": 0x2D, "home": 0x24, "end": 0x23, "pageup": 0x21, "pagedown": 0x22,
		"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,
		"printscreen": 0x2C, "menu": 0x5D, "win": 0x5B, "ctrl": 0x11, "alt": 0x12, "shift": 0x10,
		"playpause": 0xB3, "next": 0xB0, "prev": 0xB1, "stop": 0xB2,
		"mute": 0xAD, "voldown": 0xAE, "volup": 0xAF,
		"plus": 0xBB, "minus": 0xBD, "comma": 0xBC, "period": 0xBE,
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = uint16(c - 'a' + 'A')
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = uint16(c)
	}
	for i := 1; i <= 12; i++ {
		m["f"+strconv.Itoa(i)] = uint16(0x6F + i)
	}
	return m
}()

// Kept as variables so tests can watch input without moving the real mouse.
var (
	inject  = injectEvents
	capture = captureScreen
	caret   = caretBox
	pointer = pointerAt
	power   = powerAction
)

var modKeys = map[string]bool{"ctrl": true, "alt": true, "shift": true, "win": true}

// ctlEvent is one thing the phone did. T says which: m move by, a move to (0
// to 1 across all screens), b button, w wheel, k key, s text, p power.
type ctlEvent struct {
	T string   `json:"t"`
	X float64  `json:"x,omitempty"`
	Y float64  `json:"y,omitempty"`
	B string   `json:"b,omitempty"`
	D string   `json:"d,omitempty"`
	K string   `json:"k,omitempty"`
	M []string `json:"m,omitempty"`
	S string   `json:"s,omitempty"`
}

type controlAsk struct {
	id      string
	session string
	handle  string
	name    string
	at      time.Time
	state   string
	token   string
	trust   string
	done    chan struct{}
}

type controlGrant struct {
	token  string
	handle string
	name   string
	since  time.Time
	used   time.Time
	away   time.Time
}

type trustedPhone struct {
	ID    string    `json:"id"`
	Hash  string    `json:"hash"`
	Name  string    `json:"name"`
	Added time.Time `json:"added"`
}

type controlConf struct {
	Off     bool           `json:"off"`
	Trusted []trustedPhone `json:"trusted"`
}

type controlHub struct {
	mu     sync.Mutex
	path   string
	conf   controlConf
	asks   map[string]*controlAsk
	grants map[string]*controlGrant
	held   map[string]bool
	input  time.Time
	// Only one phone drives at a time. The others can watch and listen, and
	// take over once the driver has been still for a moment.
	driver   string
	driverAt time.Time
}

var control = &controlHub{asks: map[string]*controlAsk{}, grants: map[string]*controlGrant{}, held: map[string]bool{}}

func useControlFile(path string) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.path = path
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var conf controlConf
	if json.Unmarshal(raw, &conf) != nil {
		return
	}
	kept := conf.Trusted[:0]
	for _, t := range conf.Trusted {
		if controlHex.MatchString(t.Hash) && askIDRe.MatchString(t.ID) {
			t.Name = cleanPhoneName(t.Name)
			kept = append(kept, t)
		}
	}
	if len(kept) > maxTrusted {
		kept = kept[len(kept)-maxTrusted:]
	}
	conf.Trusted = kept
	control.conf = conf
}

func (c *controlHub) saveLocked() {
	if c.path == "" {
		return
	}
	raw, err := json.MarshalIndent(c.conf, "", "  ")
	if err != nil {
		return
	}
	_ = writeAtom(c.path, raw)
}

func hashTrust(token string) string {
	sum := sha256.Sum256([]byte("fasttransfer-control:" + token))
	return hex.EncodeToString(sum[:])
}

func (c *controlHub) trustedLocked(token string) bool {
	if !controlHex.MatchString(token) {
		return false
	}
	want := hashTrust(token)
	found := 0
	for _, t := range c.conf.Trusted {
		found |= subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want))
	}
	return found == 1
}

// grantLocked lets a phone in. Several phones may be allowed at once so they
// can all watch and listen, but only one drives the mouse and keyboard at a
// time (see phoneControlInput), so letting another in never fights the cursor.
func (c *controlHub) grantLocked(handle, name string) (string, error) {
	if _, had := c.grants[handle]; !had && len(c.grants) >= maxGrants {
		return "", errors.New("too many phones are connected to this PC")
	}
	token, err := randHex(32)
	if err != nil {
		return "", err
	}
	now := time.Now()
	c.grants[handle] = &controlGrant{token: token, handle: handle, name: name, since: now, used: now}
	return token, nil
}

// endLocked drops grants and reports whether any went. Buttons held down by a
// phone are let go once nobody is left driving.
func (c *controlHub) endLocked(handle string) bool {
	ended := false
	for h := range c.grants {
		if handle == "" || h == handle {
			delete(c.grants, h)
			ended = true
		}
	}
	if len(c.grants) == 0 {
		c.releaseLocked()
	}
	return ended
}

func (c *controlHub) releaseLocked() {
	for b := range c.held {
		_ = inject([]ctlEvent{{T: "b", B: b, D: "up"}})
		delete(c.held, b)
	}
}

func (c *controlHub) pruneLocked(now time.Time) bool {
	changed := false
	for id, a := range c.asks {
		if a.state == "waiting" && now.Sub(a.at) > askLife {
			a.state = "expired"
			close(a.done)
			changed = true
		}
		if now.Sub(a.at) > 3*askLife {
			delete(c.asks, id)
		}
	}
	for h, g := range c.grants {
		if now.Sub(g.used) > grantIdle || (!g.away.IsZero() && now.Sub(g.away) > grantAwayGrace) {
			delete(c.grants, h)
			changed = true
		}
	}
	if len(c.held) > 0 && (len(c.grants) == 0 || now.Sub(c.input) > heldButtonLife) {
		c.releaseLocked()
	}
	return changed
}

// run keeps grants honest: a phone that has gone away for a while loses
// control even if it never says goodbye.
func (c *controlHub) run(stop <-chan struct{}) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			c.mu.Lock()
			c.endLocked("")
			c.mu.Unlock()
			return
		case <-tick.C:
		}
		c.mu.Lock()
		handles := make([]string, 0, len(c.grants))
		for h := range c.grants {
			handles = append(handles, h)
		}
		c.mu.Unlock()
		online := map[string]bool{}
		if len(handles) > 0 {
			hub.mu.Lock()
			for _, h := range handles {
				if d := hub.byHandleLocked(h); d != nil && d.online {
					online[h] = true
				}
			}
			hub.mu.Unlock()
		}
		now := time.Now()
		c.mu.Lock()
		for h, g := range c.grants {
			switch {
			case online[h]:
				g.away = time.Time{}
			case g.away.IsZero():
				g.away = now
			}
		}
		changed := c.pruneLocked(now)
		c.mu.Unlock()
		if changed {
			hub.emit(feedEvent{Kind: "control"})
		}
	}
}

func controlErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readControl(w http.ResponseWriter, r *http.Request, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		controlErr(w, http.StatusBadRequest, "bad request")
		return false
	}
	return true
}

func phoneBySession(session string) (handle, name string, ok bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(session)
	if d == nil {
		return "", "", false
	}
	hub.touchLocked(d)
	return d.handle, d.name, true
}

// grantFor finds the grant behind a request, checking both the phone's
// session and the control token it was handed.
func grantFor(r *http.Request, session string) *controlGrant {
	handle, _, ok := phoneBySession(session)
	if !ok {
		return nil
	}
	token := r.Header.Get("X-Control-Token")
	if !controlHex.MatchString(token) {
		return nil
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	g := control.grants[handle]
	if g == nil || subtle.ConstantTimeCompare([]byte(g.token), []byte(token)) != 1 {
		return nil
	}
	return g
}

func controlEnded(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "control has ended", "ended": true})
}

func (s *server) phoneControlAsk(w http.ResponseWriter, r *http.Request) {
	if !askLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Session string `json:"session"`
		Trust   string `json:"trust"`
	}
	if !readControl(w, r, &req) {
		return
	}
	if !controlSupported {
		controlErr(w, http.StatusNotImplemented, errNoControl.Error())
		return
	}
	handle, name, ok := phoneBySession(req.Session)
	if !ok {
		notConnected(w)
		return
	}
	control.mu.Lock()
	now := time.Now()
	control.pruneLocked(now)
	if control.conf.Off {
		control.mu.Unlock()
		controlErr(w, http.StatusForbidden, "Remote control is turned off on the PC")
		return
	}
	if req.Trust != "" && control.trustedLocked(req.Trust) {
		token, err := control.grantLocked(handle, name)
		control.mu.Unlock()
		if err != nil {
			controlErr(w, http.StatusConflict, err.Error())
			return
		}
		hub.emit(feedEvent{Kind: "control", Handle: handle, Name: name, Text: "start"})
		writeJSON(w, http.StatusOK, map[string]string{"state": "granted", "token": token})
		return
	}
	// One request per phone. Only the tab that made it may wait on it, so a
	// second tab claiming the same phone cannot collect the yes meant for the first.
	waiting := 0
	for _, a := range control.asks {
		if a.state != "waiting" {
			continue
		}
		if a.handle == handle {
			if a.session == req.Session {
				control.mu.Unlock()
				writeJSON(w, http.StatusOK, map[string]string{"state": "waiting", "ask": a.id})
				return
			}
			a.state = "expired"
			close(a.done)
			continue
		}
		waiting++
	}
	if waiting >= maxPendingAsks {
		control.mu.Unlock()
		controlErr(w, http.StatusTooManyRequests, "the PC already has requests waiting")
		return
	}
	id, err := randHex(8)
	if err != nil {
		control.mu.Unlock()
		controlErr(w, http.StatusInternalServerError, "could not ask the PC")
		return
	}
	control.asks[id] = &controlAsk{id: id, session: req.Session, handle: handle, name: name, at: now, state: "waiting", done: make(chan struct{})}
	control.mu.Unlock()
	hub.emit(feedEvent{Kind: "control-ask", Handle: handle, Name: name, Text: id})
	raiseDesk()
	writeJSON(w, http.StatusOK, map[string]string{"state": "waiting", "ask": id})
}

// phoneControlWait holds until the PC answers, or a little while passes.
func (s *server) phoneControlWait(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Ask     string `json:"ask"`
	}
	if !readControl(w, r, &req) {
		return
	}
	if !askIDRe.MatchString(req.Ask) {
		controlErr(w, http.StatusBadRequest, "bad request")
		return
	}
	handle, _, ok := phoneBySession(req.Session)
	if !ok {
		notConnected(w)
		return
	}
	control.mu.Lock()
	a := control.asks[req.Ask]
	if a == nil || a.handle != handle || subtle.ConstantTimeCompare([]byte(a.session), []byte(req.Session)) != 1 {
		control.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"state": "expired"})
		return
	}
	done := a.done
	control.mu.Unlock()
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-r.Context().Done():
		return
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	out := map[string]string{"state": a.state}
	if a.state == "granted" {
		out["token"] = a.token
		if a.trust != "" {
			out["trust"] = a.trust
			a.trust = ""
		}
		a.token = ""
		delete(control.asks, a.id)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) phoneControlInput(w http.ResponseWriter, r *http.Request) {
	if !inputLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Session string     `json:"session"`
		Events  []ctlEvent `json:"events"`
	}
	if !readControl(w, r, &req) {
		return
	}
	events, err := cleanEvents(req.Events)
	if err != nil {
		controlErr(w, http.StatusBadRequest, err.Error())
		return
	}
	g := grantFor(r, req.Session)
	if g == nil {
		controlEnded(w)
		return
	}
	if len(events) == 0 {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	control.mu.Lock()
	now := time.Now()
	if d := control.grants[control.driver]; d != nil && d != g && now.Sub(control.driverAt) < driverHold {
		name := d.name
		control.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": name + " is using the PC right now", "busy": true})
		return
	}
	if control.driver != g.handle {
		control.releaseLocked()
		control.driver = g.handle
	}
	control.driverAt = now
	g.used = now
	control.input = now
	for _, ev := range events {
		if ev.T != "b" {
			continue
		}
		switch ev.D {
		case "down":
			control.held[ev.B] = true
		case "up", "click", "double":
			delete(control.held, ev.B)
		}
	}
	err = inject(events)
	control.mu.Unlock()
	if err != nil {
		controlErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, ev := range events {
		if ev.T == "p" && controlEnders[ev.K] {
			control.mu.Lock()
			control.endLocked(g.handle)
			control.mu.Unlock()
			hub.emit(feedEvent{Kind: "control", Handle: g.handle, Text: "end"})
			go func(k string) {
				time.Sleep(400 * time.Millisecond)
				power(k)
			}(ev.K)
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// phoneControlScreen sends one frame of the PC's screens. When nothing has
// changed since the frame the phone already has, it waits a moment for a
// change rather than sending the same picture again.
func (s *server) phoneControlScreen(w http.ResponseWriter, r *http.Request) {
	if !screenLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	q := r.URL.Query()
	session := q.Get("session")
	width, err := strconv.Atoi(q.Get("w"))
	if err != nil || width < 240 || width > 2560 {
		controlErr(w, http.StatusBadRequest, "bad request")
		return
	}
	quality := 62
	if raw := q.Get("q"); raw != "" {
		quality, err = strconv.Atoi(raw)
		if err != nil || quality < 30 || quality > 90 {
			controlErr(w, http.StatusBadRequest, "bad request")
			return
		}
	}
	last := q.Get("last")
	if last != "" && !frameTagRe.MatchString(last) {
		controlErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if grantFor(r, session) == nil {
		controlEnded(w)
		return
	}
	select {
	case screenSlots <- struct{}{}:
		defer func() { <-screenSlots }()
	default:
		slowDown(w)
		return
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		img, err := capture(width)
		if err != nil {
			controlErr(w, http.StatusServiceUnavailable, "could not see the screen right now")
			return
		}
		tag := strconv.FormatUint(uint64(crc32.Checksum(img.Pix, crcTable)), 16)
		tag = strings.Repeat("0", 8-len(tag)) + tag
		if tag != last {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
				controlErr(w, http.StatusInternalServerError, "could not send the screen")
				return
			}
			h := w.Header()
			h.Set("Content-Type", "image/jpeg")
			h.Set("Cache-Control", "no-store")
			h.Set("X-Frame", tag)
			h.Set("X-Caret", caretHeader())
			h.Set("X-Pointer", pointerHeader())
			h.Set("Content-Length", strconv.Itoa(buf.Len()))
			_, _ = w.Write(buf.Bytes())
			return
		}
		if time.Now().After(deadline) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Caret", caretHeader())
			w.Header().Set("X-Pointer", pointerHeader())
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(90 * time.Millisecond):
		}
	}
}

var crcTable = crc32.MakeTable(crc32.Castagnoli)

func (s *server) phoneControlEnd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	if !readControl(w, r, &req) {
		return
	}
	g := grantFor(r, req.Session)
	if g == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	control.mu.Lock()
	control.endLocked(g.handle)
	control.mu.Unlock()
	hub.emit(feedEvent{Kind: "control", Handle: g.handle, Text: "end"})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type controlView struct {
	Supported bool          `json:"supported"`
	Off       bool          `json:"off"`
	Active    []activeView  `json:"active"`
	Asks      []askView     `json:"asks"`
	Trusted   []trustedView `json:"trusted"`
}

type activeView struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Since  int64  `json:"since"`
}

type askView struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

type trustedView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Added string `json:"added"`
}

func (s *server) deskControl(w http.ResponseWriter, _ *http.Request) {
	control.mu.Lock()
	defer control.mu.Unlock()
	now := time.Now()
	control.pruneLocked(now)
	v := controlView{Supported: controlSupported, Off: control.conf.Off, Active: []activeView{}, Asks: []askView{}, Trusted: []trustedView{}}
	for _, g := range control.grants {
		v.Active = append(v.Active, activeView{Handle: g.handle, Name: g.name, Since: int64(now.Sub(g.since).Seconds())})
	}
	for _, a := range control.asks {
		if a.state == "waiting" {
			v.Asks = append(v.Asks, askView{ID: a.id, Handle: a.handle, Name: a.name})
		}
	}
	for _, t := range control.conf.Trusted {
		v.Trusted = append(v.Trusted, trustedView{ID: t.ID, Name: t.Name, Added: t.Added.Format("2006-01-02")})
	}
	sort.Slice(v.Active, func(i, j int) bool { return v.Active[i].Since > v.Active[j].Since })
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deskControlAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ask    string `json:"ask"`
		Allow  bool   `json:"allow"`
		Always bool   `json:"always"`
	}
	if !readControl(w, r, &req) {
		return
	}
	if !askIDRe.MatchString(req.Ask) {
		controlErr(w, http.StatusBadRequest, "bad request")
		return
	}
	control.mu.Lock()
	control.pruneLocked(time.Now())
	a := control.asks[req.Ask]
	if a == nil || a.state != "waiting" {
		control.mu.Unlock()
		controlErr(w, http.StatusNotFound, "that request has expired")
		return
	}
	if !req.Allow {
		a.state = "denied"
		close(a.done)
		control.mu.Unlock()
		hub.emit(feedEvent{Kind: "control", Handle: a.handle, Text: "deny"})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	token, err := control.grantLocked(a.handle, a.name)
	if err != nil {
		a.state = "denied"
		close(a.done)
		control.mu.Unlock()
		controlErr(w, http.StatusConflict, err.Error())
		return
	}
	a.token = token
	if req.Always {
		if trust, id := mustHex(32), mustHex(8); trust != "" && id != "" {
			a.trust = trust
			// A phone that lost its saved pass asks again and gets a new one.
			// Its old entry can never be used again, so it is replaced rather
			// than listed twice. At worst a second phone with the same name
			// has to ask once more.
			kept := control.conf.Trusted[:0]
			for _, t := range control.conf.Trusted {
				if t.Name != a.name {
					kept = append(kept, t)
				}
			}
			control.conf.Trusted = append(kept, trustedPhone{ID: id, Hash: hashTrust(trust), Name: a.name, Added: time.Now()})
			if n := len(control.conf.Trusted); n > maxTrusted {
				control.conf.Trusted = control.conf.Trusted[n-maxTrusted:]
			}
			control.saveLocked()
		}
	}
	a.state = "granted"
	close(a.done)
	control.mu.Unlock()
	hub.emit(feedEvent{Kind: "control", Handle: a.handle, Name: a.name, Text: "start"})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func mustHex(n int) string {
	s, err := randHex(n)
	if err != nil {
		return ""
	}
	return s
}

// deskControlSet turns requests on or off, stops a phone, or forgets one
// that was always allowed.
func (s *server) deskControlSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Off    *bool  `json:"off"`
		Stop   string `json:"stop"`
		Forget string `json:"forget"`
	}
	if !readControl(w, r, &req) {
		return
	}
	if (req.Stop != "" && req.Stop != "all" && !handleRe.MatchString(req.Stop)) || (req.Forget != "" && !askIDRe.MatchString(req.Forget)) {
		controlErr(w, http.StatusBadRequest, "bad request")
		return
	}
	control.mu.Lock()
	if req.Off != nil {
		control.conf.Off = *req.Off
		if *req.Off {
			control.endLocked("")
			for _, a := range control.asks {
				if a.state == "waiting" {
					a.state = "denied"
					close(a.done)
				}
			}
		}
		control.saveLocked()
	}
	switch req.Stop {
	case "":
	case "all":
		control.endLocked("")
	default:
		control.endLocked(req.Stop)
	}
	if req.Forget != "" {
		kept := control.conf.Trusted[:0]
		for _, t := range control.conf.Trusted {
			if t.ID != req.Forget {
				kept = append(kept, t)
			}
		}
		control.conf.Trusted = kept
		control.saveLocked()
	}
	control.mu.Unlock()
	hub.emit(feedEvent{Kind: "control"})
	s.deskControl(w, r)
}

// cleanEvents checks every event against what a phone may send and drops
// nothing silently: one bad event refuses the whole batch.
func cleanEvents(in []ctlEvent) ([]ctlEvent, error) {
	bad := errors.New("that input was not understood")
	if len(in) > maxInputEvents {
		return nil, errors.New("too much input at once")
	}
	text := 0
	out := in[:0]
	for _, ev := range in {
		if math.IsNaN(ev.X) || math.IsNaN(ev.Y) || math.IsInf(ev.X, 0) || math.IsInf(ev.Y, 0) || len(ev.M) > 4 {
			return nil, bad
		}
		switch ev.T {
		case "m":
			if math.Abs(ev.X) > maxMove || math.Abs(ev.Y) > maxMove {
				return nil, bad
			}
		case "a":
			if ev.X < 0 || ev.X > 1 || ev.Y < 0 || ev.Y > 1 {
				return nil, bad
			}
		case "w":
			if math.Abs(ev.X) > maxWheel || math.Abs(ev.Y) > maxWheel {
				return nil, bad
			}
		case "b":
			if (ev.B != "l" && ev.B != "r" && ev.B != "m") || (ev.D != "down" && ev.D != "up" && ev.D != "click" && ev.D != "double") {
				return nil, bad
			}
		case "k":
			if _, ok := keyCodes[ev.K]; !ok {
				return nil, bad
			}
			seen := map[string]bool{}
			for _, m := range ev.M {
				if !modKeys[m] || seen[m] {
					return nil, bad
				}
				seen[m] = true
			}
		case "s":
			clean, ok := cleanTyped(ev.S)
			if !ok {
				return nil, bad
			}
			text += utf8.RuneCountInString(clean)
			if text > maxInputText {
				return nil, errors.New("too much text at once")
			}
			ev.S = clean
		case "p":
			if !controlEnders[ev.K] {
				return nil, bad
			}
		default:
			return nil, bad
		}
		out = append(out, ev)
	}
	return out, nil
}

// cleanTyped keeps printable text plus new lines and tabs.
func cleanTyped(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
		case unicode.IsControl(r):
			return "", false
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), true
}

// pointerHeader lets the phone follow the mouse when it zooms in on the screen.
func pointerHeader() string {
	x, y, ok := pointer()
	if !ok || x < 0 || x > 1 || y < 0 || y > 1 {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	return f(x) + "," + f(y)
}

// caretHeader tells the phone where the text cursor is, as fractions of all
// screens, so it can offer the keyboard and keep the typing in view. Empty
// when nothing on the PC is waiting for text.
func caretHeader() string {
	x, y, w, h, ok := caret()
	if !ok {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	return f(x) + "," + f(y) + "," + f(w) + "," + f(h)
}
