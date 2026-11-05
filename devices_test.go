package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "abcdefghijklmnop"

func testServer(t *testing.T) *server {
	t.Helper()
	hub = newHub()
	uploads = &recvTracker{active: map[int64]*upload{}}
	typeLimit = newLimiter(20, 40)
	clipLimit = newLimiter(2, 12)
	helloLimit = newLimiter(1, 6)
	dir := t.TempDir()
	for name := range destinations {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setRootPath(dir)
	scheduleRoot = filepath.Join(dir, "schedules")
	useDeviceFile(filepath.Join(dir, "devices.json"))
	useInboxFile(filepath.Join(dir, "received.json"))
	return &server{key: testKey, misses: map[string][]time.Time{}, phones: map[string]time.Time{}}
}

func deskReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:" + port
	return r
}

func phoneReqFrom(ip, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = ip + ":50000"
	r.Host = "192.168.1.5:" + port
	r.Header.Set("X-Transfer-Key", testKey)
	return r
}

func phoneReq(method, path, body string) *http.Request {
	return phoneReqFrom("192.168.1.20", method, path, body)
}

func serve(mux http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("bad json (%d): %v %s", w.Code, err, w.Body.String())
	}
	return v
}

func deviceKey(n int) string { return fmt.Sprintf("%032x", n+1) }

func hello(t *testing.T, mux http.Handler, ip, key, name string) string {
	t.Helper()
	w := serve(mux, phoneReqFrom(ip, "POST", "/api/phone/hello", `{"device":"`+key+`","name":"`+name+`","kind":"iphone"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("hello got %d: %s", w.Code, w.Body.String())
	}
	return decode[map[string]string](t, w)["session"]
}

func mail(t *testing.T, mux http.Handler, session string) mailView {
	t.Helper()
	return decode[mailView](t, serve(mux, phoneReq("GET", "/api/phone/events?session="+session+"&since=-1", "")))
}

func devices(t *testing.T, mux http.Handler) []deviceView {
	t.Helper()
	return decode[struct {
		Devices []deviceView `json:"devices"`
	}](t, serve(mux, deskReq("GET", "/api/desk/devices", ""))).Devices
}

func handleOf(t *testing.T, mux http.Handler, name string) string {
	t.Helper()
	for _, d := range devices(t, mux) {
		if d.Name == name {
			return d.Handle
		}
	}
	t.Fatalf("no device named %q", name)
	return ""
}

func saveFile(t *testing.T, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(saveRoot(), filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAuthAndOrigin(t *testing.T) {
	mux := testServer(t).routes()

	r := phoneReq("POST", "/api/phone/hello", `{"device":"`+deviceKey(1)+`","name":"x","kind":"iphone"}`)
	r.Header.Del("X-Transfer-Key")
	if w := serve(mux, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("hello without key got %d", w.Code)
	}
	r = deskReq("GET", "/api/desk/devices", "")
	r.RemoteAddr = "192.168.1.20:50000"
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("desk route from the network got %d", w.Code)
	}
	r = deskReq("POST", "/api/send", `{"devices":[],"all":true,"paths":[]}`)
	r.Header.Set("Origin", "http://evil.example")
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("desk send from another origin got %d", w.Code)
	}
	r = phoneReq("POST", "/api/phone/clip", `{"session":"x","text":"y"}`)
	r.Header.Set("Origin", "http://evil.example")
	if w := serve(mux, r); w.Code != http.StatusForbidden {
		t.Fatalf("phone post from another origin got %d", w.Code)
	}
	if w := serve(mux, phoneReq("GET", "/api/phone/events?session=nope&since=-1", "")); w.Code != http.StatusNotFound {
		t.Fatalf("unknown session got %d", w.Code)
	}
}

func TestDirectoryHidesSecrets(t *testing.T) {
	mux := testServer(t).routes()
	key := deviceKey(7)
	session := hello(t, mux, "192.168.1.30", key, "Amara's iPhone")
	body := serve(mux, deskReq("GET", "/api/desk/devices", "")).Body.String()
	if strings.Contains(body, key) || strings.Contains(body, session) {
		t.Fatalf("the directory leaked a device key or session: %s", body)
	}
	if !strings.Contains(body, "Amara's iPhone") {
		t.Fatalf("the directory lost the name: %s", body)
	}
}

func TestReloadKeepsTheSameDevice(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "hello")
	first := hello(t, mux, "192.168.1.30", deviceKey(1), "Pixel")
	h := handleOf(t, mux, "Pixel")
	serve(mux, deskReq("POST", "/api/send", `{"devices":["`+h+`"],"paths":["files/a.txt"]}`))

	second := hello(t, mux, "192.168.1.30", deviceKey(1), "Pixel 8")
	if first == second {
		t.Fatal("a reload should get a fresh session")
	}
	if n := len(devices(t, mux)); n != 1 {
		t.Fatalf("a reload made %d devices", n)
	}
	m := mail(t, mux, second)
	if len(m.Files) != 1 || m.Name != "Pixel 8" {
		t.Fatalf("the waiting file or the new name was lost: %+v", m)
	}
	if w := serve(mux, phoneReq("GET", "/api/phone/events?session="+first+"&since=-1", "")); w.Code != http.StatusOK {
		t.Fatalf("a second open tab should keep working, got %d", w.Code)
	}
	for i := 0; i < maxTabsEach; i++ {
		hello(t, mux, "192.168.1.31", deviceKey(1), "Pixel 8")
	}
	if w := serve(mux, phoneReq("GET", "/api/phone/events?session="+first+"&since=-1", "")); w.Code != http.StatusNotFound {
		t.Fatalf("the oldest tab should be dropped once there are too many, got %d", w.Code)
	}
}

func TestSendOnlyReachesPickedPhones(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "hello")
	sessions := map[string]string{}
	for i, name := range []string{"One", "Two", "Three"} {
		sessions[name] = hello(t, mux, fmt.Sprintf("192.168.1.%d", 40+i), deviceKey(i), name)
	}
	one, three := handleOf(t, mux, "One"), handleOf(t, mux, "Three")
	w := serve(mux, deskReq("POST", "/api/send", `{"devices":["`+one+`","`+three+`","`+one+`","bogus"],"paths":["files/a.txt","../../etc/passwd"]}`))
	res := decode[map[string]int](t, w)
	if res["count"] != 1 || res["devices"] != 2 {
		t.Fatalf("unexpected send result %v", res)
	}
	for name, want := range map[string]int{"One": 1, "Two": 0, "Three": 1} {
		if got := len(mail(t, mux, sessions[name]).Files); got != want {
			t.Fatalf("%s got %d files, want %d", name, got, want)
		}
	}

	serve(mux, deskReq("POST", "/api/send", `{"devices":[],"all":true,"paths":["files/a.txt"]}`))
	if got := len(mail(t, mux, sessions["Two"]).Files); got != 1 {
		t.Fatalf("send to all missed a phone, got %d", got)
	}

	m := mail(t, mux, sessions["One"])
	r := phoneReq("GET", "/api/phone/file?session="+sessions["Two"]+"&id="+m.Files[0].ID, "")
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("a phone downloaded another phone's file, got %d", w.Code)
	}
	r = phoneReq("GET", "/api/phone/file?session="+sessions["One"]+"&id="+m.Files[0].ID, "")
	if w := serve(mux, r); w.Code != http.StatusOK || w.Body.String() != "hello" {
		t.Fatalf("download failed: %d %q", w.Code, w.Body.String())
	}

	serve(mux, phoneReq("POST", "/api/phone/dismiss", `{"session":"`+sessions["One"]+`","id":"","all":true}`))
	if got := len(mail(t, mux, sessions["One"]).Files); got != 0 {
		t.Fatalf("dismiss all left %d files", got)
	}
}

func TestPadsArePrivatePerPhone(t *testing.T) {
	mux := testServer(t).routes()
	a := hello(t, mux, "192.168.1.50", deviceKey(1), "A")
	b := hello(t, mux, "192.168.1.51", deviceKey(2), "B")
	serve(mux, phoneReq("POST", "/api/phone/live", `{"session":"`+a+`","text":"only for the PC","tab":"tabaaaaa"}`))
	if got := mail(t, mux, b).Text; got != "" {
		t.Fatalf("phone B saw phone A's pad: %q", got)
	}
	hb := handleOf(t, mux, "B")
	serve(mux, deskReq("POST", "/api/desk/live", `{"device":"`+hb+`","text":"hi B","tab":"deskdesk"}`))
	if m := mail(t, mux, b); m.Text != "hi B" || m.By != "PC" {
		t.Fatalf("PC typing did not reach B: %+v", m)
	}
	ha := handleOf(t, mux, "A")
	v := decode[deskLiveView](t, serve(mux, deskReq("GET", "/api/desk/live?device="+ha+"&since=-1", "")))
	if v.Text != "only for the PC" || v.By != "A" {
		t.Fatalf("the PC did not see A's pad: %+v", v)
	}
}

func TestClipsForOneAndForEveryone(t *testing.T) {
	mux := testServer(t).routes()
	a := hello(t, mux, "192.168.1.50", deviceKey(1), "A")
	b := hello(t, mux, "192.168.1.51", deviceKey(2), "B")
	ha := handleOf(t, mux, "A")
	serve(mux, deskReq("POST", "/api/desk/clip", `{"device":"`+ha+`","text":"just A"}`))
	serve(mux, deskReq("POST", "/api/desk/clip", `{"device":"all","text":"everyone"}`))

	if m := mail(t, mux, a); len(m.Clips) != 2 || m.Clips[0].Text != "everyone" || m.Clips[0].From != "all" {
		t.Fatalf("A should have both clips, newest first: %+v", m.Clips)
	}
	mb := mail(t, mux, b)
	if len(mb.Clips) != 1 || mb.Clips[0].Text != "everyone" {
		t.Fatalf("B should only have the shared clip: %+v", mb.Clips)
	}
	serve(mux, phoneReq("POST", "/api/phone/clip/remove", `{"session":"`+b+`","id":"`+mb.Clips[0].ID+`"}`))
	if got := len(mail(t, mux, b).Clips); got != 1 {
		t.Fatal("a phone removed a clip meant for everyone")
	}
	if w := serve(mux, deskReq("POST", "/api/desk/clip/remove", `{"device":"all","id":"../x","all":false}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("bad clip id got %d", w.Code)
	}
	serve(mux, deskReq("POST", "/api/desk/clip/remove", `{"device":"all","id":"","all":true}`))
	if got := len(mail(t, mux, b).Clips); got != 0 {
		t.Fatalf("clearing shared clips left %d", got)
	}
}

func TestFeedTellsTheDesk(t *testing.T) {
	mux := testServer(t).routes()
	start := decode[struct {
		Seq int64 `json:"seq"`
	}](t, serve(mux, deskReq("GET", "/api/desk/devices", ""))).Seq

	got := make(chan []feedEvent, 1)
	go func() {
		w := serve(mux, deskReq("GET", fmt.Sprintf("/api/desk/feed?since=%d", start), ""))
		var v struct {
			Events []feedEvent `json:"events"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		got <- v.Events
	}()
	time.Sleep(50 * time.Millisecond)
	session := hello(t, mux, "192.168.1.60", deviceKey(3), "Galaxy")
	select {
	case evs := <-got:
		if len(evs) == 0 || evs[0].Kind != "join" || evs[0].Name != "Galaxy" {
			t.Fatalf("expected a join event, got %+v", evs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the desk feed never woke up")
	}

	serve(mux, phoneReq("POST", "/api/phone/clip", `{"session":"`+session+`","text":"from the galaxy"}`))
	v := decode[struct {
		Events []feedEvent `json:"events"`
	}](t, serve(mux, deskReq("GET", fmt.Sprintf("/api/desk/feed?since=%d", start+1), "")))
	if len(v.Events) != 1 || v.Events[0].Kind != "clip" || v.Events[0].Text != "from the galaxy" {
		t.Fatalf("expected a clip event, got %+v", v.Events)
	}

	reset := decode[struct {
		Reset bool `json:"reset"`
	}](t, serve(mux, deskReq("GET", "/api/desk/feed?since=99999", "")))
	if !reset.Reset {
		t.Fatal("a desk ahead of the server should be told to reset")
	}
}

func TestSweepMarksAwayAndForgets(t *testing.T) {
	mux := testServer(t).routes()
	hello(t, mux, "192.168.1.70", deviceKey(4), "Old phone")
	hub.sweep(time.Now().Add(awayAfter + time.Second))
	if d := devices(t, mux); len(d) != 1 || d[0].Online {
		t.Fatalf("the phone should be away now: %+v", d)
	}
	hub.sweep(time.Now().Add(forgetAfter + time.Hour))
	if d := devices(t, mux); len(d) != 0 {
		t.Fatalf("the phone should be forgotten: %+v", d)
	}
}

func online(t *testing.T, mux http.Handler, name string) bool {
	t.Helper()
	for _, d := range devices(t, mux) {
		if d.Name == name {
			return d.Online
		}
	}
	t.Fatalf("no device named %q", name)
	return false
}

func TestByeMarksAwayRightAway(t *testing.T) {
	mux := testServer(t).routes()
	s := hello(t, mux, "192.168.1.71", deviceKey(21), "Closer")
	if !online(t, mux, "Closer") {
		t.Fatal("a fresh phone should be connected")
	}
	serve(mux, phoneReq("POST", "/api/phone/bye", `{"session":"`+s+`","gone":false}`))
	if online(t, mux, "Closer") {
		t.Fatal("a phone that went to the background should be away at once")
	}
	mail(t, mux, s)
	if !online(t, mux, "Closer") {
		t.Fatal("a phone that comes back should be connected again")
	}
	serve(mux, phoneReq("POST", "/api/phone/bye", `{"session":"`+s+`","gone":true}`))
	if online(t, mux, "Closer") {
		t.Fatal("a closed page should be away at once")
	}
	if w := serve(mux, phoneReq("GET", "/api/phone/events?session="+s+"&since=-1", "")); w.Code != http.StatusNotFound {
		t.Fatalf("a closed tab's session should be gone, got %d", w.Code)
	}
}

func TestOtherTabKeepsPhoneConnected(t *testing.T) {
	mux := testServer(t).routes()
	a := hello(t, mux, "192.168.1.72", deviceKey(22), "Two tabs")
	hello(t, mux, "192.168.1.72", deviceKey(22), "Two tabs")
	serve(mux, phoneReq("POST", "/api/phone/bye", `{"session":"`+a+`","gone":true}`))
	if !online(t, mux, "Two tabs") {
		t.Fatal("closing one tab should not mark the phone away while another is open")
	}
}

func TestDroppedPollMarksAway(t *testing.T) {
	srv := testServer(t)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	mux := srv.routes()
	s := hello(t, mux, "192.168.1.73", deviceKey(23), "Dropper")
	ver := mail(t, mux, s).Ver

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/phone/events?session=%s&since=%d", ts.URL, s, ver), nil)
	req.Header.Set("X-Transfer-Key", testKey)
	done := make(chan struct{})
	go func() {
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for hub.polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	deadline = time.Now().Add(2 * time.Second)
	for online(t, mux, "Dropper") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if online(t, mux, "Dropper") {
		t.Fatal("a phone whose poll dropped should be away without waiting for the sweep")
	}
}

func TestByeWorksWithTheCookieAlone(t *testing.T) {
	srv := testServer(t)
	mux := srv.routes()
	w := serve(mux, phoneReq("POST", "/api/phone/hello", `{"device":"`+deviceKey(24)+`","name":"Beacon","kind":"iphone"}`))
	session := decode[map[string]string](t, w)["session"]
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == phoneCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("hello should hand out the phone cookie")
	}
	r := phoneReq("POST", "/api/phone/bye", `{"session":"`+session+`","gone":true}`)
	r.Header.Del("X-Transfer-Key")
	r.Header.Set("Content-Type", "text/plain")
	r.AddCookie(cookie)
	if w := serve(mux, r); w.Code != http.StatusNoContent {
		t.Fatalf("a beacon with only the cookie got %d", w.Code)
	}
	if online(t, mux, "Beacon") {
		t.Fatal("the beacon should have marked the phone away")
	}
}

func TestBadInputIsRejected(t *testing.T) {
	mux := testServer(t).routes()
	s := hello(t, mux, "192.168.1.80", deviceKey(5), "X")
	cases := []struct{ path, body string }{
		{"/api/phone/hello", `{"device":"short","name":"x","kind":"iphone"}`},
		{"/api/phone/live", `{"session":"` + s + `","text":"x","tab":"NOPE"}`},
		{"/api/phone/live", `{"session":"` + s + `","text":"x","tab":"abcdefgh","extra":1}`},
		{"/api/phone/live", `{"session":"` + s + `","text":"` + strings.Repeat("a", maxLiveText+1) + `","tab":"abcdefgh"}`},
		{"/api/phone/clip", `{"session":"` + s + `","text":"   "}`},
		{"/api/phone/dismiss", `{"session":"` + s + `","id":"zz","all":false}`},
	}
	for _, c := range cases {
		if w := serve(mux, phoneReq("POST", c.path, c.body)); w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s %.50q, got %d", c.path, c.body, w.Code)
		}
	}
	if w := serve(mux, deskReq("POST", "/api/desk/device", `{"handle":"0000000000000000","name":"x","forget":false}`)); w.Code != http.StatusNotFound {
		t.Fatalf("renaming a missing phone got %d", w.Code)
	}
	h := handleOf(t, mux, "X")
	if w := serve(mux, deskReq("POST", "/api/desk/device", `{"handle":"`+h+`","name":"","forget":true}`)); w.Code != http.StatusConflict {
		t.Fatalf("forgetting a connected phone got %d", w.Code)
	}
}

func TestRateLimits(t *testing.T) {
	mux := testServer(t).routes()
	limited := false
	for i := 0; i < 30; i++ {
		w := serve(mux, phoneReq("POST", "/api/phone/hello", `{"device":"`+deviceKey(i)+`","name":"spam","kind":"other"}`))
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("thirty hellos in a row from one address were never limited")
	}
}

func TestTextBudget(t *testing.T) {
	mux := testServer(t).routes()
	s := hello(t, mux, "192.168.1.95", deviceKey(11), "Budget")
	serve(mux, phoneReq("POST", "/api/phone/live", `{"session":"`+s+`","text":"hello there","tab":"tabbbbbb"}`))
	serve(mux, phoneReq("POST", "/api/phone/clip", `{"session":"`+s+`","text":"a clip"}`))
	if hub.textBytes != int64(len("hello there")+len("a clip")) {
		t.Fatalf("text bytes are off: %d", hub.textBytes)
	}
	m := mail(t, mux, s)
	serve(mux, phoneReq("POST", "/api/phone/clip/remove", `{"session":"`+s+`","id":"`+m.Clips[0].ID+`"}`))
	serve(mux, phoneReq("POST", "/api/phone/live", `{"session":"`+s+`","text":"","tab":"tabbbbbb"}`))
	if hub.textBytes != 0 {
		t.Fatalf("text bytes should be back to zero, got %d", hub.textBytes)
	}

	hub.textBytes = maxTextBudget - 3
	if w := serve(mux, phoneReq("POST", "/api/phone/clip", `{"session":"`+s+`","text":"too much"}`)); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("a clip past the budget got %d", w.Code)
	}
	if w := serve(mux, phoneReq("POST", "/api/phone/live", `{"session":"`+s+`","text":"also too much","tab":"tabbbbbb"}`)); w.Code != http.StatusInsufficientStorage {
		t.Fatalf("a pad past the budget got %d", w.Code)
	}
	hub.textBytes = 0

	serve(mux, phoneReq("POST", "/api/phone/clip", `{"session":"`+s+`","text":"kept"}`))
	hub.sweep(time.Now().Add(forgetAfter + time.Hour))
	if hub.textBytes != 0 {
		t.Fatalf("forgetting a phone should free its text, got %d", hub.textBytes)
	}
}

func TestExpiredCookieIsRefused(t *testing.T) {
	srv := testServer(t)
	mux := srv.routes()
	token := strings.Repeat("c", 64)
	srv.phones[token] = time.Now().Add(-time.Minute)
	srv.pruned = time.Now()
	r := phoneReq("GET", "/api/get?path=files/nothing.txt", "")
	r.Header.Del("X-Transfer-Key")
	r.AddCookie(&http.Cookie{Name: phoneCookie, Value: token})
	if w := serve(mux, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("an expired cookie got %d", w.Code)
	}
}

func TestPollBacksOffWhenFull(t *testing.T) {
	mux := testServer(t).routes()
	s := hello(t, mux, "192.168.1.90", deviceKey(9), "Full")
	ver := mail(t, mux, s).Ver
	hub.polls.Store(maxPolls)
	defer hub.polls.Store(0)
	if w := serve(mux, phoneReq("GET", fmt.Sprintf("/api/phone/events?session=%s&since=%d", s, ver), "")); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a full hub should ask the phone to back off, got %d", w.Code)
	}
}
