package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay behaves like relay/worker.js: it keeps one listening stream per
// side and hands each side what the other posts.
type fakeRelay struct {
	mu    sync.Mutex
	peers map[string]chan []byte
	seen  [][]byte
}

func (f *fakeRelay) frame(typ byte, body []byte) []byte {
	out := make([]byte, 5+len(body))
	binary.BigEndian.PutUint32(out, uint32(1+len(body)))
	out[4] = typ
	copy(out[5:], body)
	return out
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := regexp.MustCompile(`^/r/([A-Za-z0-9_-]{22})/(down|up)$`).FindStringSubmatch(r.URL.Path)
	as := r.URL.Query().Get("as")
	if m == nil || (as != "pc" && as != "phone") {
		http.NotFound(w, r)
		return
	}
	other := "phone"
	if as == "phone" {
		other = "pc"
	}
	if m[2] == "up" {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, body)
		ch := f.peers[other]
		f.mu.Unlock()
		if ch == nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		ch <- f.frame(1, body)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ch := make(chan []byte, 64)
	f.mu.Lock()
	f.peers[as] = ch
	there := f.peers[other]
	f.mu.Unlock()
	note := func(peer bool) []byte { b, _ := json.Marshal(map[string]bool{"peer": peer}); return f.frame(0, b) }
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(note(there != nil))
	w.(http.Flusher).Flush()
	if there != nil {
		there <- note(true)
	}
	for {
		select {
		case <-r.Context().Done():
			f.mu.Lock()
			if f.peers[as] == ch {
				delete(f.peers, as)
			}
			f.mu.Unlock()
			return
		case b := <-ch:
			_, _ = w.Write(b)
			w.(http.Flusher).Flush()
		}
	}
}

type fakePhone struct {
	t    *testing.T
	base string
	room string
	out  *relaySeal
	in   *relayOpen
	got  chan [2][]byte
	body io.Closer
}

func newFakePhone(t *testing.T, base, link string) *fakePhone {
	t.Helper()
	i := strings.Index(link, "#")
	secret, err := base64.RawURLEncoding.DecodeString(link[i+1:])
	if err != nil || len(secret) != 32 {
		t.Fatalf("bad link %q", link)
	}
	room, key := relayKeys(secret)
	aead := newAEAD(key)
	p := &fakePhone{t: t, base: base, room: room, got: make(chan [2][]byte, 64),
		out: &relaySeal{aead: aead, dir: dirToPC, session: [3]byte{9, 9, 9}},
		in:  &relayOpen{aead: aead, dir: dirToPhone, seen: map[[3]byte]bool{}}}
	res, err := http.Get(base + "/r/" + room + "/down?as=phone")
	if err != nil {
		t.Fatal(err)
	}
	p.body = res.Body
	t.Cleanup(func() { res.Body.Close() })
	go func() {
		r := bufio.NewReader(res.Body)
		head := make([]byte, 4)
		for {
			if _, err := io.ReadFull(r, head); err != nil {
				return
			}
			frame := make([]byte, binary.BigEndian.Uint32(head))
			if _, err := io.ReadFull(r, frame); err != nil {
				return
			}
			if frame[0] == 1 {
				if kind, body, ok := p.in.open(frame[1:]); ok {
					p.got <- [2][]byte{{kind}, body}
				}
			}
		}
	}()
	return p
}

func (p *fakePhone) send(kind byte, body []byte) int {
	res, err := http.Post(p.base+"/r/"+p.room+"/up?as=phone", "application/octet-stream", bytes.NewReader(p.out.seal(kind, body)))
	if err != nil {
		p.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func (p *fakePhone) next(kind byte) []byte {
	p.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-p.got:
			if m[0][0] == kind {
				return m[1]
			}
		case <-timeout:
			p.t.Fatalf("no %q from the PC", kind)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 250; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func relaySetup(t *testing.T) (http.Handler, *fakeRelay, *httptest.Server) {
	t.Helper()
	s := testServer(t)
	relayLimit = newLimiter(1000, 1000)
	fr := &fakeRelay{peers: map[string]chan []byte{}}
	srv := httptest.NewServer(fr)
	relay = &relayClient{base: srv.URL}
	t.Cleanup(func() {
		relay.stop()
		srv.CloseClientConnections()
		srv.Close()
		relay = &relayClient{}
	})
	return s.routes(), fr, srv
}

func TestRelayCarriesFilesAndTextBothWays(t *testing.T) {
	mux, fr, srv := relaySetup(t)
	if w := serve(mux, deskReq("POST", "/api/desk/relay", `{"action":"start"}`)); w.Code != http.StatusOK {
		t.Fatalf("start got %d %s", w.Code, w.Body)
	}
	v := relay.view()
	if !v.On || !strings.HasPrefix(v.Link, srv.URL+"/#") {
		t.Fatalf("got %+v", v)
	}
	phone := newFakePhone(t, srv.URL, v.Link)
	waitFor(t, "the phone to show up", func() bool { return relay.view().Peer })

	phone.next('h')
	if code := phone.send('h', []byte(`{"v":1,"name":"Pixel"}`)); code != http.StatusNoContent {
		t.Fatalf("hello got %d", code)
	}
	phone.send('t', []byte("notes from class"))
	waitFor(t, "the text", func() bool { v := relay.view(); return len(v.Texts) == 1 && v.PeerName == "Pixel" })

	// Phone to PC, bigger than one piece.
	file := make([]byte, relayChunk+12345)
	_, _ = rand.Read(file)
	meta, _ := json.Marshal(map[string]any{"name": "../../lab report.pdf", "size": len(file)})
	phone.send('f', meta)
	phone.send('c', file[:relayChunk])
	phone.send('c', file[relayChunk:])
	phone.send('e', nil)
	var saved string
	waitFor(t, "the file to land", func() bool {
		found, _ := filepath.Glob(filepath.Join(saveRoot(), "files", "*lab_report.pdf"))
		if len(found) == 1 {
			saved = found[0]
		}
		return saved != "" && relay.view().Receiving == nil
	})
	if got, _ := os.ReadFile(saved); !bytes.Equal(got, file) {
		t.Fatal("the saved file does not match what the phone sent")
	}

	// PC to phone, through the desk route.
	back := bytes.Repeat([]byte("slides "), 300000)
	if w := serve(mux, deskReq("POST", "/api/desk/relay/send?name=slides.pptx&type=application/vnd.ms-powerpoint", string(back))); w.Code != http.StatusOK {
		t.Fatalf("send got %d %s", w.Code, w.Body)
	}
	var head struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	_ = json.Unmarshal(phone.next('f'), &head)
	var got []byte
	for int64(len(got)) < head.Size {
		got = append(got, phone.next('c')...)
	}
	phone.next('e')
	if head.Name != "slides.pptx" || !bytes.Equal(got, back) {
		t.Fatalf("phone got %q with %d bytes", head.Name, len(got))
	}

	if w := serve(mux, deskReq("POST", "/api/desk/relay", `{"action":"text","text":"room 204"}`)); w.Code != http.StatusOK {
		t.Fatalf("text got %d", w.Code)
	}
	if string(phone.next('t')) != "room 204" {
		t.Fatal("the phone did not get the text")
	}

	// The relay only ever saw sealed bytes.
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for _, msg := range fr.seen {
		if bytes.Contains(msg, []byte("lab report")) || bytes.Contains(msg, []byte("room 204")) || bytes.Contains(msg, []byte("slides")) || bytes.Contains(msg, []byte("notes from class")) {
			t.Fatal("the relay saw something in the clear")
		}
	}
}

func TestRelayRefusesForgedAndReplayedMessages(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	aead := newAEAD(key)
	sender := &relaySeal{aead: aead, dir: dirToPC, session: [3]byte{1, 2, 3}}
	in := &relayOpen{aead: aead, dir: dirToPC, seen: map[[3]byte]bool{}}
	first := sender.seal('t', []byte("one"))
	second := sender.seal('t', []byte("two"))
	if _, _, ok := in.open(first); !ok {
		t.Fatal("a good message was refused")
	}
	if _, _, ok := in.open(first); ok {
		t.Fatal("a replay was taken")
	}
	if _, _, ok := in.open(second); !ok {
		t.Fatal("the next message was refused")
	}
	later := &relaySeal{aead: aead, dir: dirToPC, session: [3]byte{4, 5, 6}}
	if _, _, ok := in.open(later.seal('t', []byte("new connection"))); !ok {
		t.Fatal("a new connection was refused")
	}
	if _, _, ok := in.open(sender.seal('t', []byte("old connection again"))); ok {
		t.Fatal("an old connection came back")
	}
	tampered := later.seal('t', []byte("hello"))
	tampered[len(tampered)-1] ^= 1
	if _, _, ok := in.open(tampered); ok {
		t.Fatal("a changed message was taken")
	}
	wrongWay := (&relaySeal{aead: aead, dir: dirToPhone, session: [3]byte{7, 7, 7}}).seal('t', []byte("x"))
	if _, _, ok := in.open(wrongWay); ok {
		t.Fatal("a message going the other way was taken")
	}
	otherKey := make([]byte, 32)
	_, _ = rand.Read(otherKey)
	if _, _, ok := in.open((&relaySeal{aead: newAEAD(otherKey), dir: dirToPC, session: [3]byte{8, 8, 8}}).seal('t', []byte("x"))); ok {
		t.Fatal("a message sealed with another key was taken")
	}
}

func TestRelayFileBiggerThanAnnouncedIsDropped(t *testing.T) {
	mux, _, srv := relaySetup(t)
	serve(mux, deskReq("POST", "/api/desk/relay", `{"action":"start"}`))
	phone := newFakePhone(t, srv.URL, relay.view().Link)
	waitFor(t, "the phone", func() bool { return relay.view().Peer })
	phone.send('f', []byte(`{"name":"small.txt","size":3}`))
	phone.send('c', []byte("much more than three"))
	phone.send('e', nil)
	waitFor(t, "the drop", func() bool { return relay.view().Error != "" })
	if found, _ := filepath.Glob(filepath.Join(saveRoot(), "files", "*small.txt")); len(found) != 0 {
		t.Fatalf("an oversized file was kept: %v", found)
	}
}

func TestRelayStaysOnThePC(t *testing.T) {
	mux, _, _ := relaySetup(t)
	for _, r := range []*http.Request{
		phoneReq("GET", "/api/desk/relay", ""),
		phoneReq("POST", "/api/desk/relay", `{"action":"start"}`),
		phoneReq("POST", "/api/desk/relay/send?name=a.txt", "abc"),
		phoneReq("GET", "/relay-qr.png", ""),
	} {
		if w := serve(mux, r); w.Code != http.StatusNotFound {
			t.Fatalf("%s %s from a phone got %d", r.Method, r.URL.Path, w.Code)
		}
	}
	if w := serve(mux, deskReq("GET", "/relay-qr.png", "")); w.Code != http.StatusNotFound {
		t.Fatal("no code should be shown while college mode is off")
	}
	if w := serve(mux, deskReq("POST", "/api/desk/relay/send?name=a.txt", "abc")); w.Code != http.StatusConflict {
		t.Fatalf("sending with no phone got %d", w.Code)
	}
}

func TestCleanRelayURL(t *testing.T) {
	good := map[string]string{
		"https://fasttransfer-relay.me.workers.dev": "https://fasttransfer-relay.me.workers.dev",
		" https://Relay.Example.com/ ":              "https://relay.example.com",
		"http://127.0.0.1:8790":                     "http://127.0.0.1:8790",
	}
	for in, want := range good {
		if got, ok := cleanRelayURL(in); !ok || got != want {
			t.Errorf("%q gave %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "http://relay.example.com", "https://user:pw@relay.example.com", "https://relay.example.com/path", "https://relay.example.com/?q=1", "javascript:alert(1)", "ftp://relay.example.com", "https://" + strings.Repeat("a", 250) + ".com"} {
		if got, ok := cleanRelayURL(bad); ok {
			t.Errorf("%q was taken as %q", bad, got)
		}
	}
}
