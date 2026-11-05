package main

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// Docs are shared rich text documents edited live from the PC and phones.
//
// It works the way Google Docs does: operational transformation with one
// server in charge. Every change is sent along with the revision it was
// written against. If other changes landed in the meantime, the server
// rewrites the incoming one around them (delta.transform), applies it, and
// gives it the next revision number. Every editor receives the same changes in
// the same order, so two people typing on one line, or one typing while the
// other deletes, always end up with the same text and nobody's words vanish.
const (
	maxDocs        = 500
	maxDocTitle    = 120
	docHistory     = 2000
	maxOpUnits     = 256 << 10
	maxDocPeers    = 64
	peerLife       = 30 * time.Second
	docIdleUnload  = 5 * time.Minute
	docSaveEvery   = 700 * time.Millisecond
	maxLinkLength  = 2048
	maxSeqsPerDoc  = 1024
	docPollWait    = 20 * time.Second
	maxDocWaiters  = 4096
	maxDocImage    = 10 << 20
	maxDocImages   = 300
	maxDocMedia    = 300 << 20
	maxImagePixels = 60_000_000
)

var (
	docIDRe    = regexp.MustCompile(`^[0-9a-f]{16}$`)
	docClient  = regexp.MustCompile(`^[0-9a-f]{16}$`)
	codeLangRe = regexp.MustCompile(`^[a-z0-9+#-]{1,20}$`)
	colorRe    = regexp.MustCompile(`^(?:#[0-9a-fA-F]{3}|#[0-9a-fA-F]{6}|rgb\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*\))$`)
	mediaRe    = regexp.MustCompile(`^/docmedia/([0-9a-f]{16})/([0-9a-f]{16}\.(?:png|jpg|gif|webp))$`)

	errNoDoc     = errors.New("that document is gone")
	errResync    = errors.New("resync")
	errBadChange = errors.New("that change does not fit the document")
	errDocsFull  = errors.New("there are too many documents")
	errDocsOff   = errors.New("documents are not available")
)

type docRecord struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Rev     int64     `json:"rev"`
	Content delta     `json:"content"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	By      string    `json:"by"`
	// Source is the Word file a doc was imported from, kept on the PC only.
	Source string `json:"source,omitempty"`
}

type docMeta struct {
	ID      string
	Title   string
	Updated time.Time
	By      string
	Units   int
}

type docChange struct {
	Rev    int64  `json:"rev"`
	Delta  delta  `json:"delta"`
	Client string `json:"client"`
	Seq    int64  `json:"seq"`
	By     string `json:"by"`
}

type docPeer struct {
	client string
	name   string
	color  int
	index  int
	length int
	seen   time.Time
}

type liveDoc struct {
	rec     docRecord
	history []docChange
	seqs    map[string]int64
	peers   map[string]*docPeer
	pver    int64
	wake    chan struct{}
	dirty   bool
	used    time.Time
}

func (d *liveDoc) bumpLocked() {
	close(d.wake)
	d.wake = make(chan struct{})
}

func (d *liveDoc) base() int64 { return d.rec.Rev - int64(len(d.history)) }

type docHub struct {
	mu      sync.Mutex
	dir     string
	index   map[string]*docMeta
	live    map[string]*liveDoc
	waiters int
	lver    int64
}

var docs = &docHub{index: map[string]*docMeta{}, live: map[string]*liveDoc{}}

// open reads the list of documents. The documents themselves load when
// someone opens them and unload again after a few quiet minutes.
func (h *docHub) open(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dir = dir
	for _, e := range entries {
		name := e.Name()
		id := strings.TrimSuffix(name, ".json")
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") || !docIDRe.MatchString(id) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var rec docRecord
		if json.Unmarshal(raw, &rec) != nil || !validDoc(rec.Content) {
			continue
		}
		h.index[id] = &docMeta{ID: id, Title: cleanTitle(rec.Title), Updated: rec.Updated, By: rec.By, Units: rec.Content.docLength()}
	}
	return nil
}

func cleanTitle(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(raw) {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
		n++
		if n >= maxDocTitle {
			break
		}
	}
	title := strings.TrimSpace(b.String())
	if title == "" {
		return "Untitled document"
	}
	return title
}

func blankDoc() delta {
	return delta{{kind: opInsert, text: []uint16{'\n'}}}
}

func (h *docHub) loadLocked(id string) (*liveDoc, error) {
	if h.dir == "" {
		return nil, errDocsOff
	}
	if !docIDRe.MatchString(id) {
		return nil, errNoDoc
	}
	if d := h.live[id]; d != nil {
		d.used = time.Now()
		return d, nil
	}
	if h.index[id] == nil {
		return nil, errNoDoc
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, id+".json"))
	if err != nil {
		return nil, errNoDoc
	}
	var rec docRecord
	if err := json.Unmarshal(raw, &rec); err != nil || !validDoc(rec.Content) {
		return nil, errNoDoc
	}
	rec.ID = id
	rec.Title = cleanTitle(rec.Title)
	d := &liveDoc{rec: rec, seqs: map[string]int64{}, peers: map[string]*docPeer{}, wake: make(chan struct{}), used: time.Now()}
	h.live[id] = d
	return d, nil
}

func validDoc(content delta) bool {
	n := len(content)
	if n == 0 || !content.onlyInserts() || content.docLength() > maxDocUnits {
		return false
	}
	last := content[n-1]
	if last.image != "" || len(last.text) == 0 || last.text[len(last.text)-1] != '\n' {
		return false
	}
	return validFormats(content) == nil
}

// Images may only point at a file stored for the same document, so a change
// can never pull in a picture from another doc or from outside the app.
func imagesBelong(d delta, docID string) bool {
	for _, op := range d {
		if op.image == "" {
			continue
		}
		m := mediaRe.FindStringSubmatch(op.image)
		if m == nil || m[1] != docID {
			return false
		}
	}
	return true
}

// Only the formats on the editor's toolbar are allowed, so a doc can never
// carry a script link or styling the page does not know how to show.
func validFormats(d delta) error {
	for _, op := range d {
		for k, v := range op.attrs {
			if !formatOK(k, v) {
				return errBadChange
			}
		}
	}
	return nil
}

func formatOK(k string, v any) bool {
	if v == nil {
		switch k {
		case "bold", "italic", "underline", "strike", "code", "link", "header", "list", "indent", "align", "blockquote", "code-block",
			"font", "size", "color", "background", "script":
			return true
		}
		return false
	}
	switch k {
	case "bold", "italic", "underline", "strike", "code", "blockquote":
		b, ok := v.(bool)
		return ok && b
	case "header":
		n, ok := v.(float64)
		return ok && (n == 1 || n == 2 || n == 3)
	case "indent":
		n, ok := v.(float64)
		return ok && n >= 1 && n <= 8 && n == float64(int(n))
	case "list":
		s, _ := v.(string)
		return s == "bullet" || s == "ordered" || s == "checked" || s == "unchecked"
	case "align":
		s, _ := v.(string)
		return s == "center" || s == "right" || s == "justify"
	case "code-block":
		if b, ok := v.(bool); ok {
			return b
		}
		s, _ := v.(string)
		return codeLangRe.MatchString(s)
	case "link":
		s, ok := v.(string)
		return ok && safeLink(s)
	case "font":
		s, _ := v.(string)
		return s == "serif" || s == "monospace"
	case "size":
		s, _ := v.(string)
		return s == "small" || s == "large" || s == "huge"
	case "script":
		s, _ := v.(string)
		return s == "sub" || s == "super"
	case "color", "background":
		s, ok := v.(string)
		return ok && len(s) <= 32 && colorRe.MatchString(s)
	}
	return false
}

func safeLink(s string) bool {
	if s == "about:blank" {
		return true
	}
	if len(s) > maxLinkLength || strings.ContainsAny(s, "\x00\r\n") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "tel", "sms":
		return true
	}
	return false
}

func insertedUnits(d delta) int {
	n := 0
	for _, op := range d {
		if op.kind == opInsert {
			n += len(op.text)
		}
	}
	return n
}

// apply rebases a change written at rev onto everything that landed since,
// then applies it. A change is never applied twice: a retry after a dropped
// connection carries the same client and seq and is acknowledged as is.
func (h *docHub) apply(id, client string, seq, rev int64, change delta, by string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, err := h.loadLocked(id)
	if err != nil {
		return 0, err
	}
	if last, ok := d.seqs[client]; ok && seq <= last {
		return d.rec.Rev, nil
	}
	if rev > d.rec.Rev || rev < d.base() {
		return 0, errResync
	}
	for _, ch := range d.history[rev-d.base():] {
		change = ch.Delta.transform(change, true)
	}
	if change.baseLength() > d.rec.Content.docLength() {
		return 0, errBadChange
	}
	next := d.rec.Content.compose(change)
	if !validDoc(next) || !imagesBelong(change, id) {
		return 0, errBadChange
	}
	now := time.Now()
	d.rec.Content = next
	d.rec.Rev++
	d.rec.Updated = now
	d.rec.By = by
	d.history = append(d.history, docChange{Rev: d.rec.Rev, Delta: change, Client: client, Seq: seq, By: by})
	if extra := len(d.history) - docHistory; extra > 0 {
		d.history = append([]docChange(nil), d.history[extra:]...)
	}
	if len(d.seqs) >= maxSeqsPerDoc {
		d.seqs = map[string]int64{}
	}
	d.seqs[client] = seq
	for c, p := range d.peers {
		if c == client || p.index < 0 {
			continue
		}
		end := change.transformPosition(p.index+p.length, false)
		p.index = change.transformPosition(p.index, false)
		p.length = max(0, end-p.index)
	}
	d.dirty = true
	if m := h.index[id]; m != nil {
		m.Updated, m.By, m.Units = now, by, next.docLength()
	}
	h.lver++
	d.bumpLocked()
	return d.rec.Rev, nil
}

func (h *docHub) touchPeerLocked(d *liveDoc, client, name string) *docPeer {
	p := d.peers[client]
	if p == nil {
		if len(d.peers) >= maxDocPeers {
			return nil
		}
		used := map[int]bool{}
		for _, other := range d.peers {
			used[other.color] = true
		}
		color := 0
		for used[color] && color < 7 {
			color++
		}
		p = &docPeer{client: client, name: name, color: color, index: -1}
		d.peers[client] = p
		d.pver++
		d.bumpLocked()
	}
	p.seen = time.Now()
	if p.name != name {
		p.name = name
		d.pver++
		d.bumpLocked()
	}
	return p
}

// sweep drops editors that went quiet, saves changed docs, and unloads idle ones.
func (h *docHub) sweep(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, d := range h.live {
		for c, p := range d.peers {
			if now.Sub(p.seen) > peerLife {
				delete(d.peers, c)
				d.pver++
				d.bumpLocked()
			}
		}
		if !d.dirty && len(d.peers) == 0 && now.Sub(d.used) > docIdleUnload {
			delete(h.live, id)
		}
	}
}

func (h *docHub) saveDirty() {
	h.mu.Lock()
	type job struct {
		path string
		rec  docRecord
	}
	var jobs []job
	for id, d := range h.live {
		if d.dirty {
			d.dirty = false
			jobs = append(jobs, job{filepath.Join(h.dir, id+".json"), d.rec})
		}
	}
	h.mu.Unlock()
	for _, j := range jobs {
		raw, err := json.Marshal(j.rec)
		if err == nil {
			err = writeAtom(j.path, raw)
		}
		if err != nil {
			h.mu.Lock()
			if d := h.live[j.rec.ID]; d != nil {
				d.dirty = true
			}
			h.mu.Unlock()
		}
	}
}

func (h *docHub) run(stop <-chan struct{}) {
	save := time.NewTicker(docSaveEvery)
	sweep := time.NewTicker(5 * time.Second)
	defer save.Stop()
	defer sweep.Stop()
	for {
		select {
		case <-stop:
			h.saveDirty()
			return
		case <-save.C:
			h.saveDirty()
		case now := <-sweep.C:
			h.sweep(now)
		}
	}
}

func (h *docHub) create(title, by string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == "" {
		return "", errDocsOff
	}
	if len(h.index) >= maxDocs {
		return "", errDocsFull
	}
	id, err := randHex(8)
	if err != nil {
		return "", err
	}
	now := time.Now()
	rec := docRecord{ID: id, Title: cleanTitle(title), Content: blankDoc(), Created: now, Updated: now, By: by}
	raw, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	if err := writeAtom(filepath.Join(h.dir, id+".json"), raw); err != nil {
		return "", err
	}
	h.index[id] = &docMeta{ID: id, Title: rec.Title, Updated: now, By: by, Units: 1}
	h.lver++
	return id, nil
}

func (h *docHub) rename(id, title string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.index[id]
	if m == nil || !docIDRe.MatchString(id) {
		return errNoDoc
	}
	d, err := h.loadLocked(id)
	if err != nil {
		return err
	}
	title = cleanTitle(title)
	m.Title = title
	d.rec.Title = title
	d.dirty = true
	d.pver++
	d.bumpLocked()
	h.lver++
	return nil
}

func (h *docHub) remove(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.index[id] == nil || !docIDRe.MatchString(id) {
		return errNoDoc
	}
	if err := os.Remove(filepath.Join(h.dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.RemoveAll(filepath.Join(h.dir, "media", id))
	delete(h.index, id)
	if d := h.live[id]; d != nil {
		d.dirty = false
		d.bumpLocked()
		delete(h.live, id)
	}
	h.lver++
	return nil
}

type docListItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Age     int64  `json:"age"`
	By      string `json:"by"`
	Units   int    `json:"units"`
	Editing int    `json:"editing"`
}

func (h *docHub) list() []docListItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	out := make([]docListItem, 0, len(h.index))
	for id, m := range h.index {
		item := docListItem{ID: id, Title: m.Title, Age: now.Sub(m.Updated).Milliseconds(), By: m.By, Units: m.Units}
		if d := h.live[id]; d != nil {
			item.Editing = len(d.peers)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Age < out[j].Age })
	return out
}

type peerView struct {
	Client string `json:"client"`
	Name   string `json:"name"`
	Color  int    `json:"color"`
	Index  int    `json:"index"`
	Length int    `json:"length"`
}

func (d *liveDoc) peersFor(client string) []peerView {
	out := make([]peerView, 0, len(d.peers))
	for c, p := range d.peers {
		if c == client {
			continue
		}
		out = append(out, peerView{Client: c, Name: p.name, Color: p.color, Index: p.index, Length: p.length})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Color < out[j].Color })
	return out
}

// Who is editing. The PC is always "PC"; a phone uses its own name.
type docIdentity func(r *http.Request, session string) (string, bool)

func deskIdentity(*http.Request, string) (string, bool) { return "PC", true }

func phoneIdentity(_ *http.Request, session string) (string, bool) {
	name := hub.nameFor(session)
	return name, name != ""
}

func docError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errResync):
		writeJSON(w, http.StatusConflict, map[string]any{"resync": true})
	case errors.Is(err, errNoDoc):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errBadChange):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "resync": true})
	case errors.Is(err, errDocsFull):
		writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "documents are not available"})
	}
}

func docsList(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := who(r, r.URL.Query().Get("session")); !ok {
			notConnected(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"docs": docs.list()})
	}
}

func docsCreate(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !clipLimit.allow(clientIP(r)) {
			slowDown(w)
			return
		}
		var req struct {
			Session string `json:"session"`
			Title   string `json:"title"`
		}
		if !readBody(w, r, &req) {
			return
		}
		by, ok := who(r, req.Session)
		if !ok {
			notConnected(w)
			return
		}
		id, err := docs.create(req.Title, by)
		if err != nil {
			docError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
	}
}

func docsRename(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !clipLimit.allow(clientIP(r)) {
			slowDown(w)
			return
		}
		var req struct {
			Session string `json:"session"`
			ID      string `json:"id"`
			Title   string `json:"title"`
		}
		if !readBody(w, r, &req) {
			return
		}
		if _, ok := who(r, req.Session); !ok {
			notConnected(w)
			return
		}
		if err := docs.rename(req.ID, req.Title); err != nil {
			docError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func docsDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readBody(w, r, &req) {
		return
	}
	if err := docs.remove(req.ID); err != nil {
		docError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func docsOpen(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if _, ok := who(r, q.Get("session")); !ok {
			notConnected(w)
			return
		}
		docs.mu.Lock()
		d, err := docs.loadLocked(q.Get("id"))
		if err != nil {
			docs.mu.Unlock()
			docError(w, err)
			return
		}
		out := map[string]any{"id": d.rec.ID, "title": d.rec.Title, "rev": d.rec.Rev, "content": d.rec.Content, "linked": d.rec.Source != ""}
		if c := q.Get("client"); docClient.MatchString(c) {
			out["seq"] = d.seqs[c]
		}
		docs.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	}
}

func docsEdit(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !typeLimit.allow(clientIP(r)) {
			slowDown(w)
			return
		}
		var req struct {
			Session string `json:"session"`
			ID      string `json:"id"`
			Client  string `json:"client"`
			Seq     int64  `json:"seq"`
			Rev     int64  `json:"rev"`
			Delta   delta  `json:"delta"`
		}
		if !readBody(w, r, &req) {
			return
		}
		by, ok := who(r, req.Session)
		if !ok {
			notConnected(w)
			return
		}
		// An empty change is fine: a format on text someone else just deleted
		// shrinks to nothing, and the editor still needs it confirmed.
		if !docClient.MatchString(req.Client) || req.Seq < 1 || req.Rev < 0 ||
			insertedUnits(req.Delta) > maxOpUnits || validFormats(req.Delta) != nil {
			docError(w, errBadChange)
			return
		}
		rev, err := docs.apply(req.ID, req.Client, req.Seq, req.Rev, req.Delta, by)
		if err != nil {
			docError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"rev": rev})
	}
}

// docsEvents is the long poll each open editor holds. It answers as soon as
// there are changes past the editor's revision or someone moved, joined or left.
func docsEvents(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		name, ok := who(r, q.Get("session"))
		if !ok {
			notConnected(w)
			return
		}
		client := q.Get("client")
		since, err1 := strconv.ParseInt(q.Get("since"), 10, 64)
		pver, err2 := strconv.ParseInt(q.Get("pver"), 10, 64)
		if !docClient.MatchString(client) || err1 != nil || err2 != nil {
			docError(w, errBadChange)
			return
		}
		docs.mu.Lock()
		d, err := docs.loadLocked(q.Get("id"))
		if err != nil {
			docs.mu.Unlock()
			docError(w, err)
			return
		}
		docs.touchPeerLocked(d, client, name)
		if since > d.rec.Rev || since < d.base() {
			docs.mu.Unlock()
			docError(w, errResync)
			return
		}
		if since == d.rec.Rev && pver == d.pver {
			if docs.waiters >= maxDocWaiters {
				docs.mu.Unlock()
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many open connections"})
				return
			}
			docs.waiters++
			wake := d.wake
			docs.mu.Unlock()
			t := time.NewTimer(docPollWait)
			select {
			case <-wake:
			case <-t.C:
			case <-r.Context().Done():
			}
			t.Stop()
			docs.mu.Lock()
			docs.waiters--
			if docs.live[d.rec.ID] != d {
				docs.mu.Unlock()
				docError(w, errNoDoc)
				return
			}
			if since < d.base() {
				docs.mu.Unlock()
				docError(w, errResync)
				return
			}
			if p := d.peers[client]; p != nil {
				p.seen = time.Now()
			}
		}
		changes := append([]docChange{}, d.history[since-d.base():]...)
		out := map[string]any{
			"rev":     d.rec.Rev,
			"changes": changes,
			"pver":    d.pver,
			"title":   d.rec.Title,
			"peers":   d.peersFor(client),
		}
		docs.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	}
}

// docsCursor stores where an editor's caret is. The editor only sends it when
// it has nothing unconfirmed, so the spot is in server terms at that revision.
func docsCursor(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !typeLimit.allow(clientIP(r)) {
			slowDown(w)
			return
		}
		var req struct {
			Session string `json:"session"`
			ID      string `json:"id"`
			Client  string `json:"client"`
			Rev     int64  `json:"rev"`
			Index   int    `json:"index"`
			Length  int    `json:"length"`
		}
		if !readBody(w, r, &req) {
			return
		}
		name, ok := who(r, req.Session)
		if !ok {
			notConnected(w)
			return
		}
		if !docClient.MatchString(req.Client) || req.Index < -1 || req.Length < 0 || req.Index > maxDocUnits || req.Length > maxDocUnits {
			docError(w, errBadChange)
			return
		}
		docs.mu.Lock()
		defer docs.mu.Unlock()
		d, err := docs.loadLocked(req.ID)
		if err != nil {
			docError(w, err)
			return
		}
		if req.Rev > d.rec.Rev || req.Rev < d.base() {
			docError(w, errResync)
			return
		}
		p := docs.touchPeerLocked(d, req.Client, name)
		if p == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		index, end := req.Index, req.Index+req.Length
		if index >= 0 {
			for _, ch := range d.history[req.Rev-d.base():] {
				if ch.Client == req.Client {
					continue
				}
				index = ch.Delta.transformPosition(index, false)
				end = ch.Delta.transformPosition(end, false)
			}
			limit := d.rec.Content.docLength() - 1
			index = min(max(index, 0), limit)
			end = min(max(end, index), limit)
		}
		if p.index != index || p.length != max(0, end-index) {
			p.index = index
			p.length = max(0, end-index)
			d.pver++
			d.bumpLocked()
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func docsLeave(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Session string `json:"session"`
			ID      string `json:"id"`
			Client  string `json:"client"`
		}
		if !readBody(w, r, &req) {
			return
		}
		if _, ok := who(r, req.Session); !ok || !docClient.MatchString(req.Client) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		docs.mu.Lock()
		if d := docs.live[req.ID]; d != nil {
			if _, ok := d.peers[req.Client]; ok {
				delete(d.peers, req.Client)
				d.pver++
				d.bumpLocked()
			}
		}
		docs.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *docHub) snapshot(id string) (docRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	d, err := h.loadLocked(id)
	if err != nil {
		return docRecord{}, err
	}
	return d.rec, nil
}

// Exports are built in memory, pictures included, so they are paced.
var exportLimit = newLimiter(1, 6)

func docsExport(who docIdentity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !exportLimit.allow(clientIP(r)) {
			slowDown(w)
			return
		}
		q := r.URL.Query()
		if _, ok := who(r, q.Get("session")); !ok {
			notConnected(w)
			return
		}
		rec, err := docs.snapshot(q.Get("id"))
		if err != nil {
			docError(w, err)
			return
		}
		body, ctype, ext := renderDoc(rec, q.Get("fmt"))
		if body == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not export it"})
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", attachmentDisposition(fileTitle(rec.Title)+ext))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}

// docsSaveCopy writes an export into the Files folder, where phones can pick it up too.
func docsSaveCopy(w http.ResponseWriter, r *http.Request) {
	if !exportLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		ID  string `json:"id"`
		Fmt string `json:"fmt"`
		To  string `json:"to"`
	}
	if !readBody(w, r, &req) {
		return
	}
	rec, err := docs.snapshot(req.ID)
	if err != nil {
		docError(w, err)
		return
	}
	if req.To == "source" {
		saveToSource(w, rec)
		return
	}
	body, _, ext := renderDoc(rec, req.Fmt)
	if body == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save a copy"})
		return
	}
	dir := filepath.Join(saveRoot(), "files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		docError(w, err)
		return
	}
	base := fileTitle(rec.Title)
	for i := 0; i < 100; i++ {
		name := base + ext
		if i > 0 {
			name = base + " (" + strconv.Itoa(i+1) + ")" + ext
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			break
		}
		_, werr := f.Write(body)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(filepath.Join(dir, name))
			break
		}
		writeJSON(w, http.StatusOK, map[string]string{"name": name, "rel": "files/" + name})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save a copy"})
}

// saveToSource writes a doc back over the Word file it came from, through a
// temp file in the same folder so a failed write never leaves half a file.
func saveToSource(w http.ResponseWriter, rec docRecord) {
	full, _, name, err := regularInside(rec.Source)
	if rec.Source == "" || err != nil || strings.ToLower(filepath.Ext(name)) != ".docx" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the original Word file is gone or moved"})
		return
	}
	body, err := writeDocx(rec, docs.mediaDir(rec.ID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write the Word file"})
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".ft-docx-*.tmp")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write the Word file"})
		return
	}
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write the Word file"})
		return
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		if os.Remove(full) != nil || os.Rename(tmp.Name(), full) != nil {
			_ = os.Remove(tmp.Name())
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the Word file is open somewhere else, close it and try again"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func fileTitle(title string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, title)
	clean = strings.Trim(clean, " .")
	if len(clean) > 80 {
		clean = clean[:80]
	}
	if clean == "" {
		return "Document"
	}
	return clean
}

type docSpan struct {
	text  string
	image string
	attrs map[string]any
}

type docLine struct {
	spans []docSpan
	attrs map[string]any
}

// lines splits a document at its newlines. In Quill the formats of a whole
// line (heading, list, quote) ride on the newline that ends it.
func docLines(content delta) []docLine {
	var lines []docLine
	var cur docLine
	for _, op := range content {
		if op.kind != opInsert {
			continue
		}
		if op.image != "" {
			cur.spans = append(cur.spans, docSpan{image: op.image, attrs: op.attrs})
			continue
		}
		start := 0
		for i, u := range op.text {
			if u != '\n' {
				continue
			}
			if i > start {
				cur.spans = append(cur.spans, docSpan{text: string(utf16.Decode(op.text[start:i])), attrs: op.attrs})
			}
			cur.attrs = op.attrs
			lines = append(lines, cur)
			cur = docLine{}
			start = i + 1
		}
		if start < len(op.text) {
			cur.spans = append(cur.spans, docSpan{text: string(utf16.Decode(op.text[start:])), attrs: op.attrs})
		}
	}
	if len(cur.spans) > 0 {
		lines = append(lines, cur)
	}
	return lines
}

func renderDoc(rec docRecord, format string) ([]byte, string, string) {
	switch format {
	case "md":
		return []byte(docMarkdown(rec)), "text/markdown; charset=utf-8", ".md"
	case "txt":
		return []byte(rec.Content.plainText()), "text/plain; charset=utf-8", ".txt"
	case "docx":
		body, err := writeDocx(rec, docs.mediaDir(rec.ID))
		if err != nil {
			return nil, "", ""
		}
		return body, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"
	default:
		return []byte(docHTML(rec, docs.mediaDir(rec.ID))), "text/html; charset=utf-8", ".html"
	}
}

func attrString(a map[string]any, k string) string {
	s, _ := a[k].(string)
	return s
}

func attrNumber(a map[string]any, k string) int {
	n, _ := a[k].(float64)
	return int(n)
}

func attrOn(a map[string]any, k string) bool {
	v, ok := a[k]
	return ok && v != nil && v != false
}

func docHTML(rec docRecord, mediaDir string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>")
	b.WriteString(html.EscapeString(rec.Title))
	b.WriteString("</title>\n<style>body{max-width:760px;margin:40px auto;padding:0 20px;font:16px/1.6 \"Segoe UI\",system-ui,sans-serif;color:#1a1a1a}blockquote{margin:0;padding-left:16px;border-left:3px solid #e5e5e5;color:#616161}pre{background:#f3f3f3;padding:12px;border-radius:6px;overflow:auto}code{background:#f3f3f3;padding:1px 4px;border-radius:4px}.done{text-decoration:line-through;color:#616161}</style>\n</head>\n<body>\n")
	openList := ""
	closeList := func() {
		if openList != "" {
			b.WriteString("</" + openList + ">\n")
			openList = ""
		}
	}
	inCode := false
	for _, line := range docLines(rec.Content) {
		if attrOn(line.attrs, "code-block") {
			closeList()
			if !inCode {
				b.WriteString("<pre><code>")
				inCode = true
			} else {
				b.WriteString("\n")
			}
			for _, s := range line.spans {
				b.WriteString(html.EscapeString(s.text))
			}
			continue
		}
		if inCode {
			b.WriteString("</code></pre>\n")
			inCode = false
		}
		inner := htmlSpans(line.spans, mediaDir)
		style := ""
		if al := attrString(line.attrs, "align"); al != "" {
			style = ` style="text-align:` + al + `"`
		}
		if n := attrNumber(line.attrs, "indent"); n > 0 {
			style = strings.TrimSuffix(style, `"`)
			if style == "" {
				style = ` style="`
			} else {
				style += ";"
			}
			style += "margin-left:" + strconv.Itoa(n*2) + "em\""
		}
		if list := attrString(line.attrs, "list"); list != "" {
			tag := "ul"
			if list == "ordered" {
				tag = "ol"
			}
			if openList != tag {
				closeList()
				b.WriteString("<" + tag + ">\n")
				openList = tag
			}
			switch list {
			case "checked":
				b.WriteString("<li" + style + "><span class=\"done\">&#9745; " + inner + "</span></li>\n")
			case "unchecked":
				b.WriteString("<li" + style + ">&#9744; " + inner + "</li>\n")
			default:
				b.WriteString("<li" + style + ">" + inner + "</li>\n")
			}
			continue
		}
		closeList()
		tag := "p"
		if n := attrNumber(line.attrs, "header"); n >= 1 && n <= 3 {
			tag = "h" + strconv.Itoa(n)
		} else if attrOn(line.attrs, "blockquote") {
			tag = "blockquote"
		}
		if inner == "" {
			inner = "<br>"
		}
		b.WriteString("<" + tag + style + ">" + inner + "</" + tag + ">\n")
	}
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	closeList()
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

func htmlSpans(spans []docSpan, mediaDir string) string {
	var b strings.Builder
	for _, s := range spans {
		if s.image != "" {
			if src := inlineImage(mediaDir, s.image); src != "" {
				b.WriteString(`<img alt="" style="max-width:100%" src="` + src + `">`)
			}
			continue
		}
		t := html.EscapeString(s.text)
		var style []string
		switch attrString(s.attrs, "font") {
		case "serif":
			style = append(style, "font-family:Georgia,serif")
		case "monospace":
			style = append(style, "font-family:Consolas,monospace")
		}
		switch attrString(s.attrs, "size") {
		case "small":
			style = append(style, "font-size:0.75em")
		case "large":
			style = append(style, "font-size:1.5em")
		case "huge":
			style = append(style, "font-size:2.5em")
		}
		if c := attrString(s.attrs, "color"); colorRe.MatchString(c) {
			style = append(style, "color:"+c)
		}
		if c := attrString(s.attrs, "background"); colorRe.MatchString(c) {
			style = append(style, "background-color:"+c)
		}
		if len(style) > 0 {
			t = `<span style="` + strings.Join(style, ";") + `">` + t + "</span>"
		}
		switch attrString(s.attrs, "script") {
		case "sub":
			t = "<sub>" + t + "</sub>"
		case "super":
			t = "<sup>" + t + "</sup>"
		}
		if attrOn(s.attrs, "code") {
			t = "<code>" + t + "</code>"
		}
		if attrOn(s.attrs, "bold") {
			t = "<strong>" + t + "</strong>"
		}
		if attrOn(s.attrs, "italic") {
			t = "<em>" + t + "</em>"
		}
		if attrOn(s.attrs, "underline") {
			t = "<u>" + t + "</u>"
		}
		if attrOn(s.attrs, "strike") {
			t = "<s>" + t + "</s>"
		}
		if link := attrString(s.attrs, "link"); link != "" && link != "about:blank" && safeLink(link) {
			t = `<a href="` + html.EscapeString(link) + `" rel="noopener noreferrer">` + t + "</a>"
		}
		b.WriteString(t)
	}
	return b.String()
}

func docMarkdown(rec docRecord) string {
	var b strings.Builder
	b.WriteString("# " + rec.Title + "\n\n")
	inCode := false
	n := 0
	for _, line := range docLines(rec.Content) {
		if attrOn(line.attrs, "code-block") {
			if !inCode {
				b.WriteString("```\n")
				inCode = true
			}
			for _, s := range line.spans {
				b.WriteString(s.text)
			}
			b.WriteString("\n")
			continue
		}
		if inCode {
			b.WriteString("```\n")
			inCode = false
		}
		var text strings.Builder
		for _, s := range line.spans {
			t := s.text
			if s.image != "" {
				t = "[image]"
			}
			if attrOn(s.attrs, "code") {
				t = "`" + t + "`"
			}
			if attrOn(s.attrs, "bold") {
				t = "**" + t + "**"
			}
			if attrOn(s.attrs, "italic") {
				t = "_" + t + "_"
			}
			if attrOn(s.attrs, "strike") {
				t = "~~" + t + "~~"
			}
			if link := attrString(s.attrs, "link"); link != "" && link != "about:blank" && safeLink(link) {
				t = "[" + t + "](" + link + ")"
			}
			text.WriteString(t)
		}
		prefix := strings.Repeat("  ", attrNumber(line.attrs, "indent"))
		switch list := attrString(line.attrs, "list"); {
		case list == "bullet":
			prefix += "- "
		case list == "ordered":
			n++
			prefix += strconv.Itoa(n) + ". "
		case list == "checked":
			prefix += "- [x] "
		case list == "unchecked":
			prefix += "- [ ] "
		case attrNumber(line.attrs, "header") > 0:
			prefix += strings.Repeat("#", attrNumber(line.attrs, "header")+1) + " "
		case attrOn(line.attrs, "blockquote"):
			prefix += "> "
		}
		if attrString(line.attrs, "list") != "ordered" {
			n = 0
		}
		b.WriteString(prefix + text.String() + "\n")
	}
	if inCode {
		b.WriteString("```\n")
	}
	return b.String()
}
