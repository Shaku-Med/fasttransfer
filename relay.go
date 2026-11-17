package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/skip2/go-qrcode"
)

// College mode. When the phone cannot reach this PC (a lab whose firewall
// needs admin, guest Wi-Fi that keeps devices apart), both reach out to a
// small relay on the internet instead. The relay only passes sealed pieces
// along: the key sits in the link's #fragment, which browsers never send, so
// whoever runs the relay sees sizes and timing but no names or contents.

// relayDefault is the relay a release points at. cmd/release stamps it in.
var relayDefault = ""

const (
	relayChunk     = 1 << 20
	relayMaxFrame  = relayChunk + 2048
	relayMaxText   = 20000
	relayKeepTexts = 30
	relaySource    = "College mode"
	dirToPhone     = 1
	dirToPC        = 2
)

var (
	errRelayPeer = errors.New("the phone is not connected")
	errRelayOff  = errors.New("college mode is off")
	relayRoomRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	relayLimit   = newLimiter(3, 20)
	relayHTTP    = &http.Client{Timeout: 90 * time.Second}
)

// relayKeys turns the link secret into the room name the relay sees and the
// key it never sees. relay/static/phone.js does the same.
func relayKeys(secret []byte) (room string, key []byte) {
	r := sha256.Sum256(append([]byte("fasttransfer room\x00"), secret...))
	k := sha256.Sum256(append([]byte("fasttransfer key\x00"), secret...))
	return base64.RawURLEncoding.EncodeToString(r[:])[:22], k[:]
}

// relaySeal and relayOpen frame one message: a 12 byte nonce made of the
// direction, a random tag for this connection and a counter, then the
// AES-GCM sealed kind byte and body.
type relaySeal struct {
	aead    cipher.AEAD
	dir     byte
	session [3]byte
	counter uint64
}

func (s *relaySeal) seal(kind byte, body []byte) []byte {
	nonce := make([]byte, 12, 12+1+len(body)+16)
	nonce[0] = s.dir
	copy(nonce[1:4], s.session[:])
	binary.BigEndian.PutUint64(nonce[4:], s.counter)
	s.counter++
	plain := append([]byte{kind}, body...)
	return s.aead.Seal(nonce, nonce, plain, nil)
}

type relayOpen struct {
	aead    cipher.AEAD
	dir     byte
	current [3]byte
	have    bool
	last    uint64
	seen    map[[3]byte]bool
}

// open refuses anything not sealed with the key, sent the wrong way, or
// replayed: counters only go up, and a connection tag never comes back.
func (o *relayOpen) open(msg []byte) (byte, []byte, bool) {
	if len(msg) < 12+1+16 || msg[0] != o.dir {
		return 0, nil, false
	}
	plain, err := o.aead.Open(nil, msg[:12], msg[12:], nil)
	if err != nil || len(plain) == 0 {
		return 0, nil, false
	}
	var tag [3]byte
	copy(tag[:], msg[1:4])
	n := binary.BigEndian.Uint64(msg[4:12])
	if !o.have || tag != o.current {
		if o.seen[tag] {
			return 0, nil, false
		}
		o.seen[tag] = true
		o.current, o.have = tag, true
	} else if n <= o.last {
		return 0, nil, false
	}
	o.last = n
	return plain[0], plain[1:], true
}

func newAEAD(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

type relayText struct {
	Mine bool      `json:"mine"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type relayProgress struct {
	Name  string `json:"name"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	shown int64
}

type relayIncoming struct {
	name  string
	total int64
	got   int64
	pw    *io.PipeWriter
	done  chan relaySaved
}

type relaySaved struct {
	name string
	err  error
}

type relayClient struct {
	mu        sync.Mutex
	base      string
	on        bool
	secret    string
	room      string
	gen       int
	cancel    context.CancelFunc
	out       *relaySeal
	in        *relayOpen
	peer      bool
	peerName  string
	texts     []relayText
	sending   *relayProgress
	receiving *relayProgress
	err       string
	incoming  *relayIncoming
	postMu    sync.Mutex
	sendMu    sync.Mutex
}

var relay = &relayClient{}

type relayView struct {
	URL       string         `json:"url"`
	On        bool           `json:"on"`
	Peer      bool           `json:"peer"`
	PeerName  string         `json:"peerName,omitempty"`
	Link      string         `json:"link,omitempty"`
	Texts     []relayText    `json:"texts"`
	Sending   *relayProgress `json:"sending,omitempty"`
	Receiving *relayProgress `json:"receiving,omitempty"`
	Error     string         `json:"error,omitempty"`
}

func (c *relayClient) view() relayView {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := relayView{URL: c.baseLocked(), On: c.on, Peer: c.peer, PeerName: c.peerName, Error: c.err, Texts: append([]relayText{}, c.texts...)}
	if c.on {
		v.Link = c.linkLocked()
	}
	if c.sending != nil {
		p := *c.sending
		v.Sending = &p
	}
	if c.receiving != nil {
		p := *c.receiving
		v.Receiving = &p
	}
	return v
}

func (c *relayClient) baseLocked() string {
	if c.base == "" {
		c.base = loadRelayURL()
	}
	return c.base
}

func (c *relayClient) linkLocked() string {
	return c.baseLocked() + "/#" + c.secret
}

func (c *relayClient) changed() { hub.emit(feedEvent{Kind: "relay"}) }

// cleanRelayURL accepts only a bare https origin. Plain http is allowed for
// this computer alone, which is how the relay is tried out locally.
func cleanRelayURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || len(raw) > 200 {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1"):
	default:
		return "", false
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), true
}

func relayFile() string { return filepath.Join(appDir, "relay.json") }

func loadRelayURL() string {
	if appDir != "" {
		if raw, err := os.ReadFile(relayFile()); err == nil {
			var cfg struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(raw, &cfg) == nil {
				if u, ok := cleanRelayURL(cfg.URL); ok {
					return u
				}
			}
		}
	}
	if u, ok := cleanRelayURL(relayDefault); ok {
		return u
	}
	return ""
}

func (c *relayClient) setURL(raw string) error {
	u := ""
	if strings.TrimSpace(raw) != "" {
		var ok bool
		if u, ok = cleanRelayURL(raw); !ok {
			return errors.New("use the relay's https address, like https://fasttransfer-relay.you.workers.dev")
		}
	}
	body, _ := json.Marshal(map[string]string{"url": u})
	if appDir != "" {
		if err := writeAtom(relayFile(), body); err != nil {
			return errors.New("could not save the relay address")
		}
	}
	c.stop()
	c.mu.Lock()
	c.base = u
	if u == "" {
		c.base = loadRelayURL()
	}
	c.mu.Unlock()
	c.changed()
	return nil
}

func (c *relayClient) start() error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	var session [3]byte
	if _, err := rand.Read(session[:]); err != nil {
		return err
	}
	room, key := relayKeys(raw)
	aead := newAEAD(key)
	c.stop()
	c.mu.Lock()
	if c.baseLocked() == "" {
		c.mu.Unlock()
		return errors.New("add the relay address first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.gen++
	c.on = true
	c.secret = base64.RawURLEncoding.EncodeToString(raw)
	c.room = room
	c.cancel = cancel
	c.out = &relaySeal{aead: aead, dir: dirToPhone, session: session}
	c.in = &relayOpen{aead: aead, dir: dirToPC, seen: map[[3]byte]bool{}}
	c.peer, c.peerName, c.err = false, "", ""
	c.texts = nil
	gen, base := c.gen, c.base
	c.mu.Unlock()
	go c.listen(ctx, gen, base, room)
	c.changed()
	return nil
}

func (c *relayClient) stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.on, c.cancel, c.secret, c.room, c.peer = false, nil, "", "", false
	c.out, c.in = nil, nil
	c.sending, c.receiving = nil, nil
	c.gen++
	in := c.incoming
	c.incoming = nil
	c.mu.Unlock()
	if in != nil {
		in.pw.CloseWithError(errRelayOff)
	}
	c.changed()
}

func (c *relayClient) current(gen int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.on && c.gen == gen
}

func (c *relayClient) listen(ctx context.Context, gen int, base, room string) {
	wait := time.Second
	for c.current(gen) {
		err := c.listenOnce(ctx, gen, base, room)
		if ctx.Err() != nil || !c.current(gen) {
			return
		}
		c.mu.Lock()
		c.peer = false
		if err != nil {
			c.err = "Cannot reach the relay. Trying again."
		}
		c.mu.Unlock()
		c.dropIncoming("The connection dropped")
		c.changed()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 20*time.Second)
	}
}

func (c *relayClient) listenOnce(ctx context.Context, gen int, base, room string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/r/"+room+"/down?as=pc", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "FastTransfer")
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("relay answered %d", res.StatusCode)
	}
	c.mu.Lock()
	if c.gen == gen {
		c.err = ""
	}
	c.mu.Unlock()
	c.changed()
	// Pings come every 15 seconds; a silent minute means the line is dead.
	alive := time.AfterFunc(time.Minute, func() { res.Body.Close() })
	defer alive.Stop()
	r := bufio.NewReaderSize(res.Body, 64<<10)
	head := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			return err
		}
		alive.Reset(time.Minute)
		n := binary.BigEndian.Uint32(head)
		if n < 1 || n > relayMaxFrame {
			return errors.New("bad frame from the relay")
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(r, frame); err != nil {
			return err
		}
		if !c.current(gen) {
			return nil
		}
		switch frame[0] {
		case 0:
			var note struct {
				Peer *bool `json:"peer"`
			}
			if json.Unmarshal(frame[1:], &note) == nil && note.Peer != nil {
				c.mu.Lock()
				c.peer = *note.Peer
				c.mu.Unlock()
				if *note.Peer {
					go c.hello(gen)
				} else {
					c.dropIncoming("The phone went away")
				}
				c.changed()
			}
		case 1:
			c.mu.Lock()
			in := c.in
			var kind byte
			var body []byte
			ok := false
			if in != nil && c.gen == gen {
				kind, body, ok = in.open(frame[1:])
			}
			c.mu.Unlock()
			if ok {
				c.handle(kind, body)
			}
		}
	}
}

func (c *relayClient) hello(gen int) {
	name, _ := os.Hostname()
	name = cleanPeerName(name)
	if name == "" {
		name = "this PC"
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "name": name})
	if c.current(gen) {
		_ = c.post('h', body)
	}
}

func cleanPeerName(s string) string {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func (c *relayClient) handle(kind byte, body []byte) {
	switch kind {
	case 'h':
		var hello struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &hello) == nil {
			c.mu.Lock()
			c.peerName = cleanPeerName(hello.Name)
			c.mu.Unlock()
			c.changed()
		}
	case 't':
		if len(body) == 0 || len(body) > relayMaxText || !utf8.Valid(body) {
			return
		}
		c.mu.Lock()
		c.texts = append(c.texts, relayText{Text: string(body), At: time.Now()})
		if len(c.texts) > relayKeepTexts {
			c.texts = c.texts[len(c.texts)-relayKeepTexts:]
		}
		c.mu.Unlock()
		hub.emit(feedEvent{Kind: "relay", Text: "text"})
	case 'f':
		var meta struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		}
		if json.Unmarshal(body, &meta) != nil || meta.Size < 0 || meta.Size > maxOneFile {
			return
		}
		c.dropIncoming("Stopped partway")
		c.beginIncoming(meta.Name, meta.Size)
	case 'c':
		c.mu.Lock()
		in := c.incoming
		c.mu.Unlock()
		if in == nil {
			return
		}
		if in.got+int64(len(body)) > in.total {
			c.dropIncoming("The file was bigger than announced")
			return
		}
		if _, err := in.pw.Write(body); err != nil {
			c.dropIncoming("Could not save the file")
			return
		}
		in.got += int64(len(body))
		c.progress(func() *relayProgress { return c.receiving }, in.got)
	case 'e':
		c.finishIncoming()
	case 'x':
		c.dropIncoming("The phone stopped sending")
	}
}

func (c *relayClient) beginIncoming(name string, total int64) {
	dir := filepath.Join(saveRoot(), "files")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	pr, pw := io.Pipe()
	in := &relayIncoming{name: name, total: total, pw: pw, done: make(chan relaySaved, 1)}
	go func() {
		saved, err := saveNamed(dir, time.Now().Format("20060102-150405"), 1, name, pr, true)
		pr.CloseWithError(errRelayOff)
		in.done <- relaySaved{saved, err}
	}()
	c.mu.Lock()
	c.incoming = in
	c.receiving = &relayProgress{Name: filepath.Base(strings.ReplaceAll(name, "\\", "/")), Total: total}
	c.mu.Unlock()
	c.changed()
}

func (c *relayClient) finishIncoming() {
	c.mu.Lock()
	in := c.incoming
	c.incoming = nil
	c.mu.Unlock()
	if in == nil {
		return
	}
	if in.got != in.total {
		c.settleIncoming(in, "Stopped partway")
		return
	}
	_ = in.pw.Close()
	res := <-in.done
	if res.err == nil {
		receiveUpload(relaySource, "files", res.name)
	}
	c.mu.Lock()
	c.receiving = nil
	c.mu.Unlock()
	c.changed()
}

func (c *relayClient) dropIncoming(why string) {
	c.mu.Lock()
	in := c.incoming
	c.incoming = nil
	c.mu.Unlock()
	if in != nil {
		c.settleIncoming(in, why)
	}
}

// settleIncoming stops a file partway. The half written file is gone by the
// time the desk hears about it.
func (c *relayClient) settleIncoming(in *relayIncoming, why string) {
	in.pw.CloseWithError(errors.New(why))
	<-in.done
	c.mu.Lock()
	c.receiving = nil
	c.err = why
	c.mu.Unlock()
	c.changed()
}

// progress tells the desk about every couple of percent, not every piece.
func (c *relayClient) progress(which func() *relayProgress, done int64) {
	c.mu.Lock()
	p := which()
	tell := false
	if p != nil {
		p.Done = done
		if p.Total == 0 || (done-p.shown)*50 >= p.Total || done == p.Total {
			p.shown = done
			tell = true
		}
	}
	c.mu.Unlock()
	if tell {
		c.changed()
	}
}

// post sends one sealed message. Messages go out one at a time so they reach
// the phone in the order they were sealed.
func (c *relayClient) post(kind byte, body []byte) error {
	c.postMu.Lock()
	defer c.postMu.Unlock()
	c.mu.Lock()
	if !c.on || c.out == nil {
		c.mu.Unlock()
		return errRelayOff
	}
	msg := c.out.seal(kind, body)
	target := c.base + "/r/" + c.room + "/up?as=pc"
	c.mu.Unlock()
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "FastTransfer")
	res, err := relayHTTP.Do(req)
	if err != nil {
		return errors.New("cannot reach the relay")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusConflict:
		c.mu.Lock()
		c.peer = false
		c.mu.Unlock()
		c.changed()
		return errRelayPeer
	case res.StatusCode >= 300:
		return fmt.Errorf("the relay answered %d", res.StatusCode)
	}
	return nil
}

func (c *relayClient) sendText(text string) error {
	if strings.TrimSpace(text) == "" || len(text) > relayMaxText || !utf8.ValidString(text) {
		return errors.New("that text cannot be sent")
	}
	if err := c.post('t', []byte(text)); err != nil {
		return err
	}
	c.mu.Lock()
	c.texts = append(c.texts, relayText{Mine: true, Text: text, At: time.Now()})
	if len(c.texts) > relayKeepTexts {
		c.texts = c.texts[len(c.texts)-relayKeepTexts:]
	}
	c.mu.Unlock()
	c.changed()
	return nil
}

func (c *relayClient) sendFile(name, ctype string, size int64, src io.Reader) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	ready := c.on && c.peer
	c.mu.Unlock()
	if !ready {
		return errRelayPeer
	}
	meta, _ := json.Marshal(map[string]any{"name": name, "size": size, "type": ctype})
	if err := c.post('f', meta); err != nil {
		return err
	}
	c.mu.Lock()
	c.sending = &relayProgress{Name: name, Total: size}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.sending = nil
		c.mu.Unlock()
		c.changed()
	}()
	buf := make([]byte, relayChunk)
	var sent int64
	for sent < size {
		n, err := io.ReadFull(src, buf[:min(int64(relayChunk), size-sent)])
		if n > 0 {
			if perr := c.post('c', buf[:n]); perr != nil {
				_ = c.post('x', nil)
				return perr
			}
			sent += int64(n)
			c.progress(func() *relayProgress { return c.sending }, sent)
		}
		if err != nil {
			_ = c.post('x', nil)
			return errors.New("the file stopped partway")
		}
	}
	return c.post('e', nil)
}

var relayTypeRe = regexp.MustCompile(`^[\w.+-]{1,60}/[\w.+-]{1,80}$`)

func (s *server) deskRelay(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, relay.view())
}

func (s *server) deskRelaySet(w http.ResponseWriter, r *http.Request) {
	if !relayLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Action string `json:"action"`
		URL    string `json:"url"`
		Text   string `json:"text"`
	}
	if !readBody(w, r, &req) {
		return
	}
	var err error
	switch req.Action {
	case "start":
		err = relay.start()
	case "stop":
		relay.stop()
	case "url":
		err = relay.setURL(req.URL)
	case "text":
		err = relay.sendText(req.Text)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "relay": relay.view()})
		return
	}
	writeJSON(w, http.StatusOK, relay.view())
}

// deskRelaySend takes one file as the raw request body and streams it to the
// phone. The desk's upload progress follows the relay, since reading the
// next piece waits until the last one went out.
func (s *server) deskRelaySend(w http.ResponseWriter, r *http.Request) {
	if !relayLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	name := cleanFileName(r.URL.Query().Get("name"))
	ctype := r.URL.Query().Get("type")
	if !relayTypeRe.MatchString(ctype) {
		ctype = ""
	}
	if name == "" || r.ContentLength < 0 || r.ContentLength > maxOneFile {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pick one file up to 4 GB"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, r.ContentLength)
	if err := relay.sendFile(name, ctype, r.ContentLength, r.Body); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// cleanFileName keeps the name the phone shows for a file to plain text
// without folders. The phone cleans it again before saving.
func cleanFileName(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	s = filepath.Base(strings.ReplaceAll(strings.TrimSpace(s), "\\", "/"))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if s == "." || s == ".." || s == "/" {
		return ""
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	return s
}

func (s *server) relayQR(w http.ResponseWriter, r *http.Request) {
	v := relay.view()
	if v.Link == "" {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(v.Link, qrcode.Medium, 360)
	if err != nil {
		http.Error(w, "could not make the code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}
