package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Every phone that opens the link becomes a device. It keeps its entry across
// reloads and screen locks because the phone remembers its own key, so files
// and text sent while it was away are still waiting when it comes back.
//
// A phone holds one long poll on its own mailbox. Nothing is broadcast to a
// device that has nothing new, so a thousand idle phones cost a thousand
// parked goroutines and almost no CPU.
const (
	maxDevices    = 5000
	awayAfter     = 30 * time.Second
	forgetAfter   = 24 * time.Hour
	maxOffersEach = 500
	offerLife     = 24 * time.Hour
	maxFeed       = 512
	pollWait      = 20 * time.Second
	maxPolls      = 8192
	maxSendPaths  = 100
	maxTabsEach   = 4
)

var (
	deviceKeyRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	handleRe    = regexp.MustCompile(`^[0-9a-f]{16}$`)
	sessionRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	offerRe     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	kinds       = map[string]bool{"iphone": true, "ipad": true, "android": true, "other": true}

	errNoPhone   = errors.New("No phone connected")
	errHubFull   = errors.New("too many phones are connected")
	errNoTargets = errors.New("pick at least one phone")
)

type sendFile struct {
	Name    string
	Size    int64
	Rel     string
	Abs     string
	Outside bool
}

type sendOffer struct {
	ID      string
	File    *sendFile
	Created time.Time
}

type padState struct {
	text string
	by   string
	tab  string
	at   time.Time
	told time.Time
}

// tab is one open copy of the phone page. A phone is connected while any of
// its tabs holds a poll or has been heard from lately and has not said goodbye.
type tab struct {
	session string
	dev     *device
	polls   int
	seen    time.Time
	away    bool
}

func (t *tab) liveAt(now time.Time) bool {
	return !t.away && (t.polls > 0 || now.Sub(t.seen) < awayAfter)
}

type device struct {
	key    string
	tabs   []*tab
	handle string
	name   string
	given  string
	model  string
	os     string
	kind   string
	seen   time.Time
	joined time.Time
	ip     string
	online bool
	ver    int64
	wake   chan struct{}
	offers []*sendOffer
	pad    padState
	clips  []clip
}

func (d *device) liveAt(now time.Time) bool {
	for _, t := range d.tabs {
		if t.liveAt(now) {
			return true
		}
	}
	return false
}

func (d *device) bumpLocked() {
	d.ver++
	close(d.wake)
	d.wake = make(chan struct{})
}

type feedEvent struct {
	Seq    int64  `json:"seq"`
	Kind   string `json:"kind"`
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name,omitempty"`
	Device string `json:"device,omitempty"`
	Text   string `json:"text,omitempty"`
	Count  int    `json:"count,omitempty"`
	Dest   string `json:"dest,omitempty"`
	Path   string `json:"path,omitempty"`
}

type deviceHub struct {
	mu        sync.Mutex
	bySession map[string]*tab
	byHandle  map[string]*device
	byKey     map[string]*device
	shared    []clip
	sver      int64
	swake     chan struct{}
	feed      []feedEvent
	seq       int64
	fwake     chan struct{}
	polls     atomic.Int32
	textBytes int64
}

func newHub() *deviceHub {
	return &deviceHub{
		bySession: map[string]*tab{},
		byHandle:  map[string]*device{},
		byKey:     map[string]*device{},
		swake:     make(chan struct{}),
		fwake:     make(chan struct{}),
	}
}

var hub = newHub()

func (h *deviceHub) emitLocked(ev feedEvent) {
	h.seq++
	ev.Seq = h.seq
	h.feed = append(h.feed, ev)
	if len(h.feed) > 2*maxFeed {
		h.feed = append([]feedEvent(nil), h.feed[len(h.feed)-maxFeed:]...)
	}
	close(h.fwake)
	h.fwake = make(chan struct{})
}

func (h *deviceHub) emit(ev feedEvent) {
	h.mu.Lock()
	h.emitLocked(ev)
	h.mu.Unlock()
}

func (h *deviceHub) touchLocked(d *device) {
	now := time.Now()
	d.seen = now
	if !d.online {
		d.online = true
		d.joined = now
		h.emitLocked(feedEvent{Kind: "join", Handle: d.handle, Name: d.name, Device: d.kind})
	}
}

func (h *deviceHub) tabLocked(session string) *tab {
	if !sessionRe.MatchString(session) {
		return nil
	}
	return h.bySession[session]
}

// bySessionLocked finds the phone behind a session. Any request from a tab
// counts as hearing from it, so that tab is live again.
func (h *deviceHub) bySessionLocked(session string) *device {
	t := h.tabLocked(session)
	if t == nil {
		return nil
	}
	t.seen = time.Now()
	t.away = false
	return t.dev
}

// refreshLocked marks a phone away the moment its last live tab is gone,
// instead of waiting for the sweep. If another row for the same handset
// connected in the last minute, this row is folded into that one.
func (h *deviceHub) refreshLocked(d *device) (dropped []string, keep string) {
	now := time.Now()
	if d == nil || !d.online || d.liveAt(now) {
		return nil, ""
	}
	d.online = false
	d.seen = now
	h.emitLocked(feedEvent{Kind: "leave", Handle: d.handle})
	return h.foldIntoRecentLocked(d)
}

func (h *deviceHub) dropTabLocked(t *tab) {
	delete(h.bySession, t.session)
	d := t.dev
	kept := d.tabs[:0]
	for _, other := range d.tabs {
		if other != t {
			kept = append(kept, other)
		}
	}
	d.tabs = kept
}

func (h *deviceHub) byHandleLocked(handle string) *device {
	if !handleRe.MatchString(handle) {
		return nil
	}
	return h.byHandle[handle]
}

func (h *deviceHub) dropLocked(d *device) {
	h.textBytes -= int64(len(d.pad.text))
	for _, c := range d.clips {
		h.textBytes -= int64(len(c.Text))
	}
	for _, t := range d.tabs {
		delete(h.bySession, t.session)
	}
	d.tabs = nil
	delete(h.byHandle, d.handle)
	delete(h.byKey, d.key)
	d.bumpLocked()
}

// hello registers a phone or reattaches it to its old entry. The list is keyed
// by the phone's device id. A new id that shows up within a minute of the same
// handset going away reuses that row, so a reconnect does not add a second one.
// A device keeps a few sessions so two open tabs do not knock each other off.
func (h *deviceHub) hello(key string, facts phoneFacts, handleHint, ip string) (*device, string, []string, error) {
	session, err := randHex(32)
	if err != nil {
		return nil, "", nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var d *device
	if deviceKeyRe.MatchString(key) {
		d = h.byKey[key]
	}
	if d == nil {
		d = h.awayTwinLocked(facts, ip)
	}
	if d == nil {
		if len(h.byKey) >= maxDevices && !h.evictLocked() {
			return nil, "", nil, errHubFull
		}
		if !deviceKeyRe.MatchString(key) {
			key, err = randHex(16)
			if err != nil {
				return nil, "", nil, err
			}
		}
		handle := handleHint
		if !handleRe.MatchString(handle) || h.byHandle[handle] != nil {
			handle, err = randHex(8)
			if err != nil {
				return nil, "", nil, err
			}
		}
		d = &device{key: key, handle: handle, wake: make(chan struct{})}
		h.byKey[key] = d
		h.byHandle[handle] = d
	}
	if ip != "" {
		d.ip = ip
	}
	if facts.Given != "" {
		d.given = facts.Given
	}
	if facts.Model != "" {
		d.model = facts.Model
	}
	if facts.OS != "" {
		d.os = facts.OS
	}
	if facts.Kind != "" && facts.Kind != "other" {
		d.kind = facts.Kind
	} else if d.kind == "" {
		d.kind = "other"
	}
	name := composeLabel(d.given, d.model, d.os)
	if name == "" {
		name = facts.Label
	}
	if name == "" {
		name = "This device"
	}
	if d.name != name && d.name != "" && d.online {
		h.emitLocked(feedEvent{Kind: "rename", Handle: d.handle, Name: name})
	}
	d.name = name
	if len(d.tabs) >= maxTabsEach {
		h.dropTabLocked(d.tabs[0])
	}
	t := &tab{session: session, dev: d, seen: time.Now()}
	d.tabs = append(d.tabs, t)
	h.bySession[session] = t
	h.touchLocked(d)
	dropped := h.absorbRecentAwayLocked(d)
	d.bumpLocked()
	return d, session, dropped, nil
}

// awayTwinLocked is a phone that is not connected and is almost surely this
// one: the same model and system that went away in the last minute, or the
// same model on the same Wi-Fi address this week. Phones keep their id per
// web address and per browser, so a home screen shortcut, a private tab or a
// new PC address would otherwise look like a new phone. A phone that is
// still connected is left alone.
func (h *deviceHub) awayTwinLocked(facts phoneFacts, ip string) *device {
	if facts.Model == "" || facts.OS == "" {
		return nil
	}
	now := time.Now()
	probe := &device{given: facts.Given, model: facts.Model, os: facts.OS, ip: ip}
	var best *device
	for _, d := range h.byKey {
		if d.online || !likelySame(probe, d, now) {
			continue
		}
		if best == nil || d.seen.After(best.seen) {
			best = d
		}
	}
	return best
}

func factsMatch(d *device, facts phoneFacts) bool {
	if d == nil || d.model == "" || d.os == "" || genericDevice(d.model) || genericDevice(facts.Model) {
		return false
	}
	if !strings.EqualFold(d.model, facts.Model) || !strings.EqualFold(d.os, facts.OS) {
		return false
	}
	if d.given != "" && facts.Given != "" && !strings.EqualFold(d.given, facts.Given) {
		return false
	}
	return true
}

func sameHandset(a, b *device) bool {
	if a == nil || b == nil || a == b {
		return false
	}
	return factsMatch(a, phoneFacts{Given: b.given, Model: b.model, OS: b.os})
}

// sameAddressWindow is how long a Wi-Fi address still points at one phone.
// Routers hand the same address back to a phone for days, but not forever.
const sameAddressWindow = 7 * 24 * time.Hour

// likelySame says whether the away row other is the phone probe describes.
// The address rule lets the system version differ, since phones update.
func likelySame(probe, other *device, now time.Time) bool {
	if probe == nil || other == nil || probe == other {
		return false
	}
	if now.Sub(other.seen) <= time.Minute && sameHandset(probe, other) {
		return true
	}
	if probe.ip == "" || probe.ip != other.ip || now.Sub(other.seen) > sameAddressWindow {
		return false
	}
	if other.model == "" || genericDevice(other.model) || genericDevice(probe.model) || !strings.EqualFold(other.model, probe.model) {
		return false
	}
	if osFamily(other.os) != osFamily(probe.os) {
		return false
	}
	return other.given == "" || probe.given == "" || strings.EqualFold(other.given, probe.given)
}

func osFamily(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, ' '); i > 0 {
		name = name[:i]
	}
	return strings.ToLower(name)
}

// absorbRecentAwayLocked pulls other rows for this handset that just went away
// into the one that just connected. Two connected phones are not merged.
func (h *deviceHub) absorbRecentAwayLocked(keep *device) []string {
	now := time.Now()
	var twins []*device
	for _, other := range h.byKey {
		if other == keep || other.online || !likelySame(keep, other, now) {
			continue
		}
		twins = append(twins, other)
	}
	dropped := make([]string, 0, len(twins))
	for _, twin := range twins {
		dropped = append(dropped, twin.key)
		h.absorbLocked(keep, twin)
	}
	return dropped
}

// foldIntoRecentLocked runs when a row goes away. If the same handset just
// connected as a second row, keep that row and drop this one.
func (h *deviceHub) foldIntoRecentLocked(away *device) (dropped []string, keep string) {
	if away == nil {
		return nil, ""
	}
	now := time.Now()
	var into *device
	for _, other := range h.byKey {
		if other == away || !other.online || other.joined.IsZero() || now.Sub(other.joined) > time.Minute || !sameHandset(away, other) {
			continue
		}
		if into == nil || other.joined.After(into.joined) {
			into = other
		}
	}
	if into == nil {
		return nil, ""
	}
	key := away.key
	h.absorbLocked(into, away)
	return []string{key}, into.key
}

func (h *deviceHub) absorbLocked(keep, twin *device) {
	if keep == nil || twin == nil || keep == twin {
		return
	}
	if keep.pad.text == "" && twin.pad.text != "" {
		keep.pad = twin.pad
		twin.pad = padState{}
	}
	if len(keep.clips) == 0 && len(twin.clips) > 0 {
		keep.clips = twin.clips
		twin.clips = nil
	}
	if len(twin.offers) > 0 {
		keep.offers = append(keep.offers, twin.offers...)
		twin.offers = nil
		if len(keep.offers) > maxOffersEach {
			keep.offers = keep.offers[len(keep.offers)-maxOffersEach:]
		}
	}
	handle := twin.handle
	h.dropLocked(twin)
	h.emitLocked(feedEvent{Kind: "forget", Handle: handle})
}

func (h *deviceHub) evictLocked() bool {
	var oldest *device
	for _, d := range h.byKey {
		if d.online {
			continue
		}
		if oldest == nil || d.seen.Before(oldest.seen) {
			oldest = d
		}
	}
	if oldest == nil {
		return false
	}
	h.dropLocked(oldest)
	h.emitLocked(feedEvent{Kind: "forget", Handle: oldest.handle})
	return true
}

// sweep marks phones away when they stop polling and forgets old ones. It runs
// on a timer so no request has to walk the whole device list.
func (h *deviceHub) sweep(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, d := range h.byKey {
		live := d.liveAt(now)
		if d.online && !live {
			d.online = false
			h.emitLocked(feedEvent{Kind: "leave", Handle: d.handle})
		}
		if !live && now.Sub(d.seen) > forgetAfter {
			h.dropLocked(d)
			h.emitLocked(feedEvent{Kind: "forget", Handle: d.handle})
			continue
		}
		if len(d.offers) > 0 && now.Sub(d.offers[0].Created) > offerLife {
			kept := d.offers[:0]
			for _, off := range d.offers {
				if now.Sub(off.Created) <= offerLife {
					kept = append(kept, off)
				}
			}
			d.offers = kept
			d.bumpLocked()
		}
	}
}

func (h *deviceHub) run(stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			h.sweep(now)
		}
	}
}

func (h *deviceHub) park(r *http.Request, wake <-chan struct{}) bool {
	if h.polls.Add(1) > maxPolls {
		h.polls.Add(-1)
		return false
	}
	defer h.polls.Add(-1)
	t := time.NewTimer(pollWait)
	defer t.Stop()
	select {
	case <-wake:
	case <-t.C:
	case <-r.Context().Done():
	}
	return true
}

// nameFor tags an upload with the phone that sent it. Unknown sessions are fine.
func (h *deviceHub) nameFor(session string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d := h.bySessionLocked(session); d != nil {
		h.touchLocked(d)
		return d.name
	}
	return ""
}

type offerView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type mailView struct {
	Ver   int64       `json:"ver"`
	Name  string      `json:"name"`
	Files []offerView `json:"files"`
	Text  string      `json:"text"`
	By    string      `json:"by"`
	Tab   string      `json:"tab"`
	Age   int64       `json:"age"`
	Clips []clipView  `json:"clips"`
}

func clipViews(now time.Time, lists ...[]clip) []clipView {
	n := 0
	for _, l := range lists {
		n += len(l)
	}
	all := make([]clip, 0, n)
	for _, l := range lists {
		all = append(all, l...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].n > all[j].n })
	out := make([]clipView, 0, n)
	for _, c := range all {
		out = append(out, clipView{ID: c.ID, Text: c.Text, By: c.By, From: c.From, Age: now.Sub(c.At).Milliseconds()})
	}
	return out
}

func padAge(now time.Time, p padState) int64 {
	if p.at.IsZero() {
		return 0
	}
	return now.Sub(p.at).Milliseconds()
}

func (h *deviceHub) mailLocked(d *device) mailView {
	now := time.Now()
	files := make([]offerView, 0, len(d.offers))
	for i := len(d.offers) - 1; i >= 0; i-- {
		off := d.offers[i]
		files = append(files, offerView{ID: off.ID, Name: off.File.Name, Size: off.File.Size})
	}
	return mailView{
		Ver:   d.ver,
		Name:  d.name,
		Files: files,
		Text:  d.pad.text,
		By:    d.pad.by,
		Tab:   d.pad.tab,
		Age:   padAge(now, d.pad),
		Clips: clipViews(now, d.clips, h.shared),
	}
}

func pollSince(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

var helloLimit = newLimiter(1, 6)

func (s *server) phoneHello(w http.ResponseWriter, r *http.Request) {
	if !helloLimit.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down a little"})
		return
	}
	var req struct {
		Device          string `json:"device"`
		Name            string `json:"name"`
		Kind            string `json:"kind"`
		Platform        string `json:"platform"`
		PlatformVersion string `json:"platformVersion"`
		Model           string `json:"model"`
	}
	if !readBody(w, r, &req) {
		return
	}
	req.Device = strings.ToLower(strings.TrimSpace(req.Device))
	if req.Device != "" && !deviceKeyRe.MatchString(req.Device) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this page is out of date, reload it"})
		return
	}
	req.Device = canonicalDeviceKey(req.Device)
	facts := describePhone(
		r.UserAgent(),
		headerHint(r.Header.Get("Sec-CH-UA-Platform")),
		headerHint(r.Header.Get("Sec-CH-UA-Platform-Version")),
		headerHint(r.Header.Get("Sec-CH-UA-Model")),
		req.Platform,
		req.PlatformVersion,
		req.Model,
		req.Name,
	)
	if !kinds[facts.Kind] {
		kind := strings.ToLower(req.Kind)
		if kinds[kind] {
			facts.Kind = kind
		} else {
			facts.Kind = "other"
		}
	}
	hint := ""
	if prev, ok := savedWiFi(req.Device); ok {
		hint = prev.Handle
		if facts.Model == "" {
			facts.Model = prev.Model
		}
		if facts.OS == "" {
			facts.OS = prev.OS
		}
		if (facts.Kind == "" || facts.Kind == "other") && kinds[prev.Kind] && prev.Kind != "other" {
			facts.Kind = prev.Kind
		}
		if facts.Given == "" {
			facts.Given = prev.Name
		}
	}
	facts.Label = composeLabel(facts.Given, facts.Model, facts.OS)
	if facts.Label == "" {
		facts.Label = "This device"
	}
	d, session, dropped, err := hub.hello(req.Device, facts, hint, clientIP(r))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the PC has too many devices connected"})
		return
	}
	if req.Device != "" && req.Device != d.key {
		dropped = append(dropped, req.Device)
	}
	retargetWiFi(d, dropped)
	deliverPending(d.key)
	http.SetCookie(w, &http.Cookie{
		Name:     "ft-device-key",
		Value:    d.key,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"session": session, "name": d.name, "device": d.key})
}

func (s *server) phoneEvents(w http.ResponseWriter, r *http.Request) {
	since := pollSince(r)
	session := r.URL.Query().Get("session")
	hub.mu.Lock()
	d := hub.bySessionLocked(session)
	if d == nil {
		hub.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not connected"})
		return
	}
	t := hub.tabLocked(session)
	hub.touchLocked(d)
	if d.ver != since {
		v := hub.mailLocked(d)
		hub.mu.Unlock()
		writeJSON(w, http.StatusOK, v)
		return
	}
	t.polls++
	wake := d.wake
	hub.mu.Unlock()

	ok := hub.park(r, wake)

	// The end of a poll is not news from the phone. Only a new request is, so
	// a phone that walked away stops looking connected right after this.
	hub.mu.Lock()
	t.polls--
	v := hub.mailLocked(d)
	var dropped []string
	var keep string
	if r.Context().Err() != nil {
		t.seen = time.Time{}
		dropped, keep = hub.refreshLocked(d)
	}
	hub.mu.Unlock()
	persistPhoneMerge(keep, dropped)
	if !ok {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many open connections"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// phoneBye is sent as the page closes or goes to the background. It may come
// from sendBeacon, which can only carry the cookie, so it is behind guardPhone.
func (s *server) phoneBye(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Gone    bool   `json:"gone"`
	}
	if !readBody(w, r, &req) {
		return
	}
	hub.mu.Lock()
	t := hub.tabLocked(req.Session)
	if t == nil {
		hub.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d := t.dev
	if req.Gone {
		hub.dropTabLocked(t)
	} else {
		t.away = true
	}
	dropped, keep := hub.refreshLocked(d)
	hub.mu.Unlock()
	persistPhoneMerge(keep, dropped)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) phoneRename(w http.ResponseWriter, r *http.Request) {
	if !clipLimit.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow down a little"})
		return
	}
	var req struct {
		Session string `json:"session"`
		Name    string `json:"name"`
	}
	if !readBody(w, r, &req) {
		return
	}
	given := chosenName(req.Name)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(req.Session)
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not connected"})
		return
	}
	hub.touchLocked(d)
	name := applyPhoneLabel(d, given)
	d.bumpLocked()
	hub.emitLocked(feedEvent{Kind: "rename", Handle: d.handle, Name: name})
	rememberWiFi(d.key, d)
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *server) phoneDismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		ID      string `json:"id"`
		All     bool   `json:"all"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if !req.All && !offerRe.MatchString(req.ID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that file is not here"})
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.bySessionLocked(req.Session)
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not connected"})
		return
	}
	hub.touchLocked(d)
	before := len(d.offers)
	if req.All {
		d.offers = nil
	} else {
		kept := d.offers[:0]
		for _, off := range d.offers {
			if off.ID != req.ID {
				kept = append(kept, off)
			}
		}
		d.offers = kept
	}
	if len(d.offers) != before {
		d.bumpLocked()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Many phones pulling the same big file at once mostly fights over Wi-Fi.
// Capping it keeps the PC's disk and memory steady and the rest just wait.
var downloadSlots = make(chan struct{}, 48)

func takeSlot(r *http.Request, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	case <-r.Context().Done():
		return false
	}
}

func (s *server) phoneFile(w http.ResponseWriter, r *http.Request) {
	offerID := r.URL.Query().Get("id")
	missing := func() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that file is not available"})
	}
	if !offerRe.MatchString(offerID) {
		missing()
		return
	}
	hub.mu.Lock()
	d := hub.bySessionLocked(r.URL.Query().Get("session"))
	var file *sendFile
	if d != nil {
		hub.touchLocked(d)
		for _, off := range d.offers {
			if off.ID == offerID {
				file = off.File
				break
			}
		}
	}
	hub.mu.Unlock()
	if file == nil {
		missing()
		return
	}

	var full string
	var info os.FileInfo
	name := file.Name
	if file.Outside {
		st, err := os.Stat(file.Abs)
		if err != nil || !st.Mode().IsRegular() {
			missing()
			return
		}
		full, info = file.Abs, st
	} else {
		var err error
		full, info, name, err = regularInside(file.Rel)
		if err != nil {
			missing()
			return
		}
	}
	viewing := r.URL.Query().Get("view") == "1"
	viewType, viewKind := "", ""
	if viewing {
		viewType, viewKind = previewType(name)
		if viewKind == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no preview"})
			return
		}
		if viewKind == "text" && info.Size() > 256<<10 {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "too large to view"})
			return
		}
	}
	if !takeSlot(r, downloadSlots) {
		return
	}
	defer func() { <-downloadSlots }()
	f, err := os.Open(full)
	if err != nil {
		missing()
		return
	}
	defer f.Close()
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if viewing {
		w.Header().Set("Content-Type", viewType)
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		http.ServeContent(w, r, "preview", info.ModTime(), f)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func previewType(name string) (string, string) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", "image"
	case ".png":
		return "image/png", "image"
	case ".gif":
		return "image/gif", "image"
	case ".webp":
		return "image/webp", "image"
	case ".bmp":
		return "image/bmp", "image"
	case ".mp4", ".m4v":
		return "video/mp4", "video"
	case ".webm":
		return "video/webm", "video"
	case ".ogv":
		return "video/ogg", "video"
	case ".mov":
		return "video/quicktime", "video"
	case ".mp3":
		return "audio/mpeg", "audio"
	case ".m4a", ".aac":
		return "audio/mp4", "audio"
	case ".wav":
		return "audio/wav", "audio"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg", "audio"
	case ".flac":
		return "audio/flac", "audio"
	case ".txt", ".text", ".md", ".csv", ".log", ".json", ".xml", ".html", ".htm", ".css", ".js", ".svg":
		return "text/plain; charset=utf-8", "text"
	default:
		return "", ""
	}
}

type deviceView struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
	Age    int64  `json:"age"`
	Files  int    `json:"files"`
}

func (h *deviceHub) directory() (int64, []deviceView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	rows := make([]deviceView, 0, len(h.byKey))
	seenHandle := map[string]bool{}
	for _, d := range h.byKey {
		if d == nil || seenHandle[d.handle] {
			continue
		}
		seenHandle[d.handle] = true
		rows = append(rows, deviceView{Handle: d.handle, Name: d.name, Kind: d.kind, Online: d.online, Age: now.Sub(d.seen).Milliseconds(), Files: len(d.offers)})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Online != rows[j].Online {
			return rows[i].Online
		}
		a, b := strings.ToLower(rows[i].Name), strings.ToLower(rows[j].Name)
		if a != b {
			return a < b
		}
		return rows[i].Handle < rows[j].Handle
	})
	return h.seq, rows
}

func (s *server) deskDevices(w http.ResponseWriter, _ *http.Request) {
	seq, rows := hub.directory()
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "devices": rows})
}

// deskFeed is how the window hears about joins, leaves, clips and uploads.
// If it falls too far behind it is told to reset and reload the directory.
func (s *server) deskFeed(w http.ResponseWriter, r *http.Request) {
	since := pollSince(r)
	hub.mu.Lock()
	events, reset := hub.feedSinceLocked(since)
	if reset || len(events) > 0 {
		seq := hub.seq
		hub.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "reset": reset, "events": events})
		return
	}
	wake := hub.fwake
	hub.mu.Unlock()

	if !hub.park(r, wake) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many open connections"})
		return
	}
	hub.mu.Lock()
	events, reset = hub.feedSinceLocked(since)
	seq := hub.seq
	hub.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "reset": reset, "events": events})
}

func (h *deviceHub) feedSinceLocked(since int64) ([]feedEvent, bool) {
	if since < 0 || since > h.seq {
		return []feedEvent{}, true
	}
	if since == h.seq {
		return []feedEvent{}, false
	}
	if len(h.feed) == 0 || since < h.feed[0].Seq-1 {
		return []feedEvent{}, true
	}
	start := int(since - h.feed[0].Seq + 1)
	return append([]feedEvent(nil), h.feed[start:]...), false
}

func (s *server) deskDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
		Forget bool   `json:"forget"`
	}
	if !readBody(w, r, &req) {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	d := hub.byHandleLocked(req.Handle)
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that phone is gone"})
		return
	}
	if req.Forget {
		if d.online {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "that phone is still connected"})
			return
		}
		hub.dropLocked(d)
		hub.emitLocked(feedEvent{Kind: "forget", Handle: d.handle})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	given := chosenName(req.Name)
	if given == "" {
		writeJSON(w, http.StatusOK, map[string]string{"name": d.name})
		return
	}
	name := applyPhoneLabel(d, given)
	d.bumpLocked()
	hub.emitLocked(feedEvent{Kind: "rename", Handle: d.handle, Name: name})
	rememberWiFi(d.key, d)
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

type sendRequest struct {
	Devices []string `json:"devices"`
	All     bool     `json:"all"`
	Paths   []string `json:"paths"`
	When    string   `json:"when"`
}

// deliver hands the same files to every target. Each offer only points at a
// shared file record, so sending to a thousand phones stays cheap.
func (h *deviceHub) deliver(files []*sendFile, handles []string, all bool) (int, error) {
	if len(files) == 0 {
		return 0, errors.New("could not send those files")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var targets []*device
	if all {
		for _, d := range h.byKey {
			targets = append(targets, d)
		}
	} else {
		seen := map[*device]bool{}
		for _, handle := range handles {
			if d := h.byHandleLocked(handle); d != nil && !seen[d] {
				seen[d] = true
				targets = append(targets, d)
			}
			if len(targets) >= maxDevices {
				break
			}
		}
	}
	if len(targets) == 0 {
		return 0, errNoTargets
	}
	now := time.Now()
	for _, d := range targets {
		for _, f := range files {
			id, err := randHex(16)
			if err != nil {
				return 0, err
			}
			d.offers = append(d.offers, &sendOffer{ID: id, File: f, Created: now})
		}
		if extra := len(d.offers) - maxOffersEach; extra > 0 {
			d.offers = append([]*sendOffer(nil), d.offers[extra:]...)
		}
		d.bumpLocked()
	}
	return len(targets), nil
}

func savedFiles(paths []string) []*sendFile {
	out := make([]*sendFile, 0, len(paths))
	for _, raw := range paths {
		if len(out) >= maxSendPaths {
			break
		}
		rel, err := parseRel(raw)
		if err != nil || rel == "" {
			continue
		}
		_, info, name, err := regularInside(rel)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, &sendFile{Name: name, Size: info.Size(), Rel: rel})
	}
	return out
}

func outsideFiles(paths []string) []*sendFile {
	out := make([]*sendFile, 0, len(paths))
	for _, raw := range paths {
		if len(out) >= maxSendPaths {
			break
		}
		full, err := filepath.Abs(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, &sendFile{Name: filepath.Base(full), Size: info.Size(), Abs: full, Outside: true})
	}
	return out
}

func (s *server) sendSaved(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if !readBody(w, r, &req) {
		return
	}
	when, timed, err := parseWhen(req.When)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	files := savedFiles(req.Paths)
	sent, scheduled, err := dispatchSend(files, req.Devices, req.All, when, timed)
	writeSendResult(w, len(files), sent, scheduled, err)
}

func (s *server) sendOutside(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if !readBody(w, r, &req) {
		return
	}
	when, timed, err := parseWhen(req.When)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	files := outsideFiles(req.Paths)
	sent, scheduled, err := dispatchSend(files, req.Devices, req.All, when, timed)
	writeSendResult(w, len(files), sent, scheduled, err)
}

func queueOutside(handles []string, all bool, paths []string, whenRaw string) (map[string]any, error) {
	when, timed, err := parseWhen(whenRaw)
	if err != nil {
		return map[string]any{"count": 0, "devices": 0, "scheduled": 0, "error": err.Error()}, nil
	}
	files := outsideFiles(paths)
	sent, scheduled, err := dispatchSend(files, handles, all, when, timed)
	if err != nil {
		return map[string]any{"count": 0, "devices": sent, "scheduled": scheduled, "error": err.Error()}, nil
	}
	return map[string]any{"count": len(files), "devices": sent, "scheduled": scheduled}, nil
}
