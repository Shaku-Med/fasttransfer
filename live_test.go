package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "abcdefghijklmnop"

func testServer() *server {
	pad = &liveHub{wake: make(chan struct{})}
	typeLimit = newLimiter(20, 40)
	clipLimit = newLimiter(2, 12)
	return &server{key: testKey, misses: map[string][]time.Time{}, phones: map[string]time.Time{}}
}

func deskReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:" + port
	return r
}

func phoneReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:50000"
	r.Host = "192.168.1.5:" + port
	r.Header.Set("X-Transfer-Key", testKey)
	return r
}

func serve(mux *http.ServeMux, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func readLive(t *testing.T, w *httptest.ResponseRecorder) liveView {
	t.Helper()
	var v liveView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("bad live json: %v (%s)", err, w.Body.String())
	}
	return v
}

func TestLiveNeedsKeyOrLocal(t *testing.T) {
	mux := testServer().routes()

	r := phoneReq("GET", "/api/live?since=-1", "")
	r.Header.Del("X-Transfer-Key")
	if w := serve(mux, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("phone without key got %d", w.Code)
	}

	r = deskReq("GET", "/api/desk/live?since=-1", "")
	r.RemoteAddr = "192.168.1.20:50000"
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("desk route from the network got %d", w.Code)
	}

	r = deskReq("POST", "/api/desk/live", `{"text":"x","tab":"abcdefgh","name":""}`)
	r.Header.Set("Origin", "http://evil.example")
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("desk post from another origin got %d", w.Code)
	}

	r = phoneReq("POST", "/api/live", `{"text":"x","tab":"abcdefgh","name":"iPhone"}`)
	r.Header.Set("Origin", "http://evil.example")
	if w := serve(mux, r); w.Code != http.StatusForbidden {
		t.Fatalf("phone post from another origin got %d", w.Code)
	}
}

func TestTypingWakesPoll(t *testing.T) {
	mux := testServer().routes()
	first := readLive(t, serve(mux, phoneReq("GET", "/api/live?since=-1", "")))

	got := make(chan liveView, 1)
	go func() {
		got <- readLive(t, serve(mux, deskReq("GET", "/api/desk/live?since="+itoa(first.Ver), "")))
	}()
	time.Sleep(50 * time.Millisecond)

	w := serve(mux, phoneReq("POST", "/api/live", `{"text":"hello from the phone","tab":"phonetab1","name":"iPhone"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("typing got %d: %s", w.Code, w.Body.String())
	}
	select {
	case v := <-got:
		if v.Text != "hello from the phone" || v.By != "iPhone" || v.Tab != "phonetab1" || v.Ver != first.Ver+1 {
			t.Fatalf("unexpected state %+v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting poll never woke up")
	}

	serve(mux, phoneReq("POST", "/api/live", `{"text":"hello from the phone","tab":"phonetab1","name":"iPhone"}`))
	v := readLive(t, serve(mux, deskReq("GET", "/api/desk/live?since=-1", "")))
	if v.Ver != first.Ver+1 {
		t.Fatalf("same text should not bump the version, got %d", v.Ver)
	}
}

func TestLiveRejectsBadInput(t *testing.T) {
	mux := testServer().routes()
	cases := []string{
		`{"text":"x","tab":"NOPE","name":""}`,
		`{"text":"x","tab":"abcdefgh","name":"","extra":1}`,
		`{"text":"` + strings.Repeat("a", maxLiveText+1) + `","tab":"abcdefgh","name":""}`,
		`not json`,
	}
	for _, body := range cases {
		if w := serve(mux, deskReq("POST", "/api/desk/live", body)); w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %.40q, got %d", body, w.Code)
		}
	}
	if w := serve(mux, deskReq("POST", "/api/desk/clip", `{"text":"   ","name":""}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("blank clip got %d", w.Code)
	}
	if w := serve(mux, deskReq("POST", "/api/desk/clip", `{"text":"`+strings.Repeat("b", maxClipText+1)+`","name":""}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("huge clip got %d", w.Code)
	}
}

func TestClipsAddAndRemove(t *testing.T) {
	mux := testServer().routes()
	serve(mux, phoneReq("POST", "/api/clip", `{"text":"first","name":"Android"}`))
	serve(mux, deskReq("POST", "/api/desk/clip", `{"text":"second","name":"ignored"}`))
	v := readLive(t, serve(mux, deskReq("GET", "/api/desk/live?since=-1", "")))
	if len(v.Clips) != 2 || v.Clips[0].Text != "second" || v.Clips[0].From != "pc" || v.Clips[0].By != "PC" {
		t.Fatalf("unexpected clips %+v", v.Clips)
	}
	if v.Clips[1].By != "Android" || v.Clips[1].From != "phone" {
		t.Fatalf("phone clip lost its name %+v", v.Clips[1])
	}

	serve(mux, phoneReq("POST", "/api/clip/remove", `{"id":"`+v.Clips[0].ID+`","all":false}`))
	v = readLive(t, serve(mux, deskReq("GET", "/api/desk/live?since=-1", "")))
	if len(v.Clips) != 1 || v.Clips[0].Text != "first" {
		t.Fatalf("remove one failed %+v", v.Clips)
	}

	serve(mux, deskReq("POST", "/api/desk/clip/remove", `{"id":"","all":true}`))
	v = readLive(t, serve(mux, deskReq("GET", "/api/desk/live?since=-1", "")))
	if len(v.Clips) != 0 {
		t.Fatalf("clear all failed %+v", v.Clips)
	}
}

func TestClipsAreCapped(t *testing.T) {
	testServer()
	clipLimit = newLimiter(1000, 1000)
	mux := (&server{key: testKey, misses: map[string][]time.Time{}, phones: map[string]time.Time{}}).routes()
	for i := 0; i < maxClips+5; i++ {
		serve(mux, deskReq("POST", "/api/desk/clip", `{"text":"clip `+itoa(int64(i))+`","name":""}`))
	}
	v := readLive(t, serve(mux, deskReq("GET", "/api/desk/live?since=-1", "")))
	if len(v.Clips) != maxClips || v.Clips[0].Text != "clip "+itoa(int64(maxClips+4)) {
		t.Fatalf("expected the newest %d clips, got %d starting with %q", maxClips, len(v.Clips), v.Clips[0].Text)
	}
}

func TestClipRateLimit(t *testing.T) {
	mux := testServer().routes()
	limited := false
	for i := 0; i < 30; i++ {
		w := serve(mux, phoneReq("POST", "/api/clip", `{"text":"spam","name":"iPhone"}`))
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("thirty clips in a row were never limited")
	}
}

func TestPollBacksOffWhenFull(t *testing.T) {
	mux := testServer().routes()
	pad.waits.Store(maxLiveWaits)
	defer pad.waits.Store(0)
	if w := serve(mux, deskReq("GET", "/api/desk/live?since=0", "")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a full hub should ask the client to back off, got %d", w.Code)
	}
	if w := serve(mux, deskReq("GET", "/api/desk/live?since=-1", "")); w.Code != http.StatusOK {
		t.Fatalf("a client that is behind should still get the state, got %d", w.Code)
	}
}

func TestClipRemoveChecksID(t *testing.T) {
	mux := testServer().routes()
	if w := serve(mux, deskReq("POST", "/api/desk/clip/remove", `{"id":"../../x","all":false}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("bad clip id got %d", w.Code)
	}
}

func TestLongPollOverLoopback(t *testing.T) {
	s := testServer()
	ts := httptest.NewServer(s.routes())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/api/live?since=-1", nil)
	req.Header.Set("X-Transfer-Key", testKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("loopback request failed: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("loopback poll got %d", res.StatusCode)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
