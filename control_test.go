package main

import (
	"image"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sentInput struct {
	mu     sync.Mutex
	events []ctlEvent
	power  []string
}

func (s *sentInput) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func controlServer(t *testing.T) (*server, http.Handler, *sentInput) {
	t.Helper()
	s := testServer(t)
	control = &controlHub{asks: map[string]*controlAsk{}, grants: map[string]*controlGrant{}, held: map[string]bool{}}
	useControlFile(filepath.Join(t.TempDir(), "control.json"))
	askLimit = newLimiter(100, 100)
	inputLimit = newLimiter(1000, 1000)
	screenLimit = newLimiter(1000, 1000)
	sent := &sentInput{}
	inject = func(evs []ctlEvent) error {
		sent.mu.Lock()
		defer sent.mu.Unlock()
		sent.events = append(sent.events, evs...)
		return nil
	}
	power = func(k string) {
		sent.mu.Lock()
		defer sent.mu.Unlock()
		sent.power = append(sent.power, k)
	}
	awake = func() {}
	t.Cleanup(func() {
		inject, capture, power, caret, pointer, awake = injectEvents, captureScreen, powerAction, caretBox, pointerAt, stayAwake
	})
	return s, s.routes(), sent
}

func ask(t *testing.T, mux http.Handler, session, trust string) map[string]string {
	t.Helper()
	w := serve(mux, phoneReq("POST", "/api/phone/control/ask", `{"session":"`+session+`","trust":"`+trust+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("ask got %d: %s", w.Code, w.Body.String())
	}
	return decode[map[string]string](t, w)
}

func answer(t *testing.T, mux http.Handler, id string, allow, always bool) {
	t.Helper()
	body := `{"ask":"` + id + `","allow":` + boolText(allow) + `,"always":` + boolText(always) + `}`
	if w := serve(mux, deskReq("POST", "/api/desk/control/answer", body)); w.Code != http.StatusOK {
		t.Fatalf("answer got %d: %s", w.Code, w.Body.String())
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func wait(t *testing.T, mux http.Handler, session, id string) map[string]string {
	t.Helper()
	return decode[map[string]string](t, serve(mux, phoneReq("POST", "/api/phone/control/wait", `{"session":"`+session+`","ask":"`+id+`"}`)))
}

func sendInput(mux http.Handler, session, token, events string) int {
	r := phoneReq("POST", "/api/phone/control/input", `{"session":"`+session+`","events":`+events+`}`)
	if token != "" {
		r.Header.Set("X-Control-Token", token)
	}
	return serve(mux, r).Code
}

func TestControlNeedsTheDeskToSayYes(t *testing.T) {
	_, mux, sent := controlServer(t)
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	other := hello(t, mux, "192.168.1.21", deviceKey(2), "Tablet")
	move := `[{"t":"m","x":5,"y":3}]`

	if code := sendInput(mux, phone, strings.Repeat("a", 64), move); code != http.StatusForbidden {
		t.Fatalf("input before any yes got %d", code)
	}
	got := ask(t, mux, phone, "")
	if got["state"] != "waiting" || got["ask"] == "" {
		t.Fatalf("ask = %v", got)
	}
	if again := ask(t, mux, phone, ""); again["ask"] != got["ask"] {
		t.Fatal("asking twice made a second request on the PC")
	}
	if w := serve(mux, phoneReq("POST", "/api/desk/control/answer", `{"ask":"`+got["ask"]+`","allow":true,"always":false}`)); w.Code == http.StatusOK {
		t.Fatal("a phone answered its own request")
	}
	if w := wait(t, mux, other, got["ask"]); w["state"] != "expired" {
		t.Fatalf("another phone could wait on this request: %v", w)
	}
	twin := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	if twin == phone {
		t.Fatal("a second tab got the same session")
	}
	if w := wait(t, mux, twin, got["ask"]); w["state"] != "expired" {
		t.Fatalf("a second tab of the same phone could wait on the first tab's request: %v", w)
	}

	answer(t, mux, got["ask"], true, false)
	res := wait(t, mux, phone, got["ask"])
	token := res["token"]
	if res["state"] != "granted" || !controlHex.MatchString(token) || res["trust"] != "" {
		t.Fatalf("wait = %v", res)
	}
	if code := sendInput(mux, phone, token, move); code != http.StatusOK {
		t.Fatalf("input with the token got %d", code)
	}
	if code := sendInput(mux, other, token, move); code != http.StatusForbidden {
		t.Fatalf("another phone reused the token and got %d", code)
	}
	if code := sendInput(mux, phone, "", move); code != http.StatusForbidden {
		t.Fatalf("input without the token got %d", code)
	}
	if sent.count() != 1 {
		t.Fatalf("injected %d events, want 1", sent.count())
	}

	if w := serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`)); w.Code != http.StatusOK {
		t.Fatalf("stop got %d", w.Code)
	}
	if code := sendInput(mux, phone, token, move); code != http.StatusForbidden {
		t.Fatalf("input after stop got %d", code)
	}
}

func TestControlDenyAndOff(t *testing.T) {
	_, mux, _ := controlServer(t)
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	got := ask(t, mux, phone, "")
	answer(t, mux, got["ask"], false, false)
	if res := wait(t, mux, phone, got["ask"]); res["state"] != "denied" || res["token"] != "" {
		t.Fatalf("denied wait = %v", res)
	}
	if w := serve(mux, deskReq("POST", "/api/desk/control", `{"off":true}`)); w.Code != http.StatusOK {
		t.Fatalf("turning off got %d", w.Code)
	}
	if w := serve(mux, phoneReq("POST", "/api/phone/control/ask", `{"session":"`+phone+`","trust":""}`)); w.Code != http.StatusForbidden {
		t.Fatalf("ask while off got %d", w.Code)
	}
}

func TestControlAlwaysAllow(t *testing.T) {
	_, mux, _ := controlServer(t)
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	got := ask(t, mux, phone, "")
	answer(t, mux, got["ask"], true, true)
	res := wait(t, mux, phone, got["ask"])
	trust := res["trust"]
	if !controlHex.MatchString(trust) {
		t.Fatalf("no trust token: %v", res)
	}
	serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`))

	if again := ask(t, mux, phone, trust); again["state"] != "granted" || again["token"] == "" {
		t.Fatalf("trusted phone still had to wait: %v", again)
	}
	if fake := ask(t, mux, phone, strings.Repeat("b", 64)); fake["state"] != "waiting" {
		t.Fatalf("a made up trust token was let in: %v", fake)
	}

	view := decode[controlView](t, serve(mux, deskReq("GET", "/api/desk/control", "")))
	if len(view.Trusted) != 1 || len(view.Active) != 1 {
		t.Fatalf("view = %+v", view)
	}
	if strings.Contains(serve(mux, deskReq("GET", "/api/desk/control", "")).Body.String(), hashTrust(trust)) {
		t.Fatal("the desk view leaks the stored hash")
	}
	serve(mux, deskReq("POST", "/api/desk/control", `{"forget":"`+view.Trusted[0].ID+`","stop":"all"}`))
	if after := ask(t, mux, phone, trust); after["state"] != "waiting" {
		t.Fatalf("forgotten phone was let in: %v", after)
	}

	useControlFile(control.path)
	if len(control.conf.Trusted) != 0 {
		t.Fatal("forgetting did not reach the file")
	}
}

func TestControlLockEndsControl(t *testing.T) {
	_, mux, sent := controlServer(t)
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	got := ask(t, mux, phone, "")
	answer(t, mux, got["ask"], true, false)
	token := wait(t, mux, phone, got["ask"])["token"]
	if code := sendInput(mux, phone, token, `[{"t":"b","b":"l","d":"down"},{"t":"p","k":"lock"}]`); code != http.StatusOK {
		t.Fatalf("lock got %d", code)
	}
	if code := sendInput(mux, phone, token, `[{"t":"m","x":1,"y":1}]`); code != http.StatusForbidden {
		t.Fatalf("still in control after locking: %d", code)
	}
	sent.mu.Lock()
	last := sent.events[len(sent.events)-1]
	sent.mu.Unlock()
	if last.T != "b" || last.D != "up" {
		t.Fatalf("the held button was not let go, last event %+v", last)
	}
}

func TestControlDeskRoutesStayLocal(t *testing.T) {
	_, mux, _ := controlServer(t)
	for _, path := range []string{"/api/desk/control", "/api/desk/control/answer"} {
		if w := serve(mux, phoneReq("POST", path, `{}`)); w.Code != http.StatusNotFound {
			t.Fatalf("%s from a phone got %d", path, w.Code)
		}
	}
}

func TestControlScreenSkipsSameFrame(t *testing.T) {
	_, mux, _ := controlServer(t)
	img := image.NewRGBA(image.Rect(0, 0, 32, 18))
	capture = func(int) (*image.RGBA, error) { return img, nil }
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	got := ask(t, mux, phone, "")
	answer(t, mux, got["ask"], true, false)
	token := wait(t, mux, phone, got["ask"])["token"]

	r := phoneReq("GET", "/api/phone/control/screen?session="+phone+"&w=640", "")
	if w := serve(mux, r); w.Code != http.StatusForbidden {
		t.Fatalf("screen without token got %d", w.Code)
	}
	r = phoneReq("GET", "/api/phone/control/screen?session="+phone+"&w=640", "")
	r.Header.Set("X-Control-Token", token)
	w := serve(mux, r)
	tag := w.Header().Get("X-Frame")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || !frameTagRe.MatchString(tag) {
		t.Fatalf("first frame: %d %q", w.Code, tag)
	}
	r = phoneReq("GET", "/api/phone/control/screen?session="+phone+"&w=640&last="+tag, "")
	r.Header.Set("X-Control-Token", token)
	if w := serve(mux, r); w.Code != http.StatusNoContent {
		t.Fatalf("unchanged frame got %d", w.Code)
	}
}

func TestCleanEvents(t *testing.T) {
	good := []ctlEvent{
		{T: "m", X: 10, Y: -4}, {T: "a", X: 0.5, Y: 1}, {T: "w", Y: 120},
		{T: "b", B: "r", D: "click"}, {T: "k", K: "c", M: []string{"ctrl"}},
		{T: "k", K: "f5"}, {T: "s", S: "héllo\n\tthere"}, {T: "p", K: "lock"},
	}
	if _, err := cleanEvents(good); err != nil {
		t.Fatalf("good events refused: %v", err)
	}
	bad := map[string]ctlEvent{
		"unknown kind":   {T: "x"},
		"huge move":      {T: "m", X: maxMove + 1},
		"off screen":     {T: "a", X: 1.2},
		"not a number":   {T: "m", X: math.NaN()},
		"odd button":     {T: "b", B: "x", D: "click"},
		"odd key":        {T: "k", K: "sysrq"},
		"odd modifier":   {T: "k", K: "a", M: []string{"fn"}},
		"twice modifier": {T: "k", K: "a", M: []string{"ctrl", "ctrl"}},
		"control char":   {T: "s", S: "a\x1bb"},
		"shutdown":       {T: "p", K: "shutdown"},
	}
	for name, ev := range bad {
		if _, err := cleanEvents([]ctlEvent{ev}); err == nil {
			t.Errorf("%s was let through", name)
		}
	}
	if _, err := cleanEvents([]ctlEvent{{T: "s", S: strings.Repeat("a", maxInputText+1)}}); err == nil {
		t.Error("too much text was let through")
	}
	if _, err := cleanEvents(make([]ctlEvent, maxInputEvents+1)); err == nil {
		t.Error("too many events were let through")
	}
}

func TestControlSoundStream(t *testing.T) {
	_, mux, _ := controlServer(t)
	sound = &soundHub{subs: map[chan []byte]bool{}}
	soundLimit = newLimiter(100, 100)
	stopped := make(chan struct{})
	startSound = func(stop <-chan struct{}, ready chan<- soundStart, push func([]byte)) {
		ready <- soundStart{rate: 48000}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				close(stopped)
				return
			case <-tick.C:
				push([]byte{1, 0, 2, 0})
			}
		}
	}
	t.Cleanup(func() { startSound = captureSound })

	srv := httptest.NewServer(mux)
	defer srv.Close()
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	listen := func(token string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+"/api/phone/control/sound?session="+phone, nil)
		req.Header.Set("X-Transfer-Key", testKey)
		if token != "" {
			req.Header.Set("X-Control-Token", token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := listen(""); res.StatusCode != http.StatusForbidden {
		t.Fatalf("sound without control got %d", res.StatusCode)
	}

	got := ask(t, mux, phone, "")
	answer(t, mux, got["ask"], true, false)
	token := wait(t, mux, phone, got["ask"])["token"]
	res := listen(token)
	if res.StatusCode != http.StatusOK || res.Header.Get("X-Audio-Rate") != "48000" {
		t.Fatalf("sound got %d rate %q", res.StatusCode, res.Header.Get("X-Audio-Rate"))
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(res.Body, buf); err != nil || buf[0] != 1 || buf[2] != 2 {
		t.Fatalf("read %v %v", buf, err)
	}

	serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`))
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, res.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the sound kept going after control was stopped")
	}
	res.Body.Close()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the capture kept running with nobody listening")
	}
}

func grantPhone(t *testing.T, mux http.Handler, ip string, n int, name string) (string, string) {
	t.Helper()
	session := hello(t, mux, ip, deviceKey(n), name)
	got := ask(t, mux, session, "")
	answer(t, mux, got["ask"], true, false)
	return session, wait(t, mux, session, got["ask"])["token"]
}

func TestControlOneDriverAtATime(t *testing.T) {
	_, mux, _ := controlServer(t)
	a, ta := grantPhone(t, mux, "192.168.1.20", 1, "Pixel")
	b, tb := grantPhone(t, mux, "192.168.1.21", 2, "Tablet")
	move := `[{"t":"m","x":2,"y":2}]`
	if code := sendInput(mux, a, ta, move); code != http.StatusOK {
		t.Fatalf("first phone got %d", code)
	}
	if code := sendInput(mux, b, tb, move); code != http.StatusConflict {
		t.Fatalf("second phone cut in and got %d", code)
	}
	if code := sendInput(mux, b, tb, `[]`); code != http.StatusOK {
		t.Fatalf("a watching phone's check got %d", code)
	}
	time.Sleep(driverHold + 100*time.Millisecond)
	if code := sendInput(mux, b, tb, move); code != http.StatusOK {
		t.Fatalf("second phone could not take over after a pause: %d", code)
	}
	if code := sendInput(mux, a, ta, move); code != http.StatusConflict {
		t.Fatalf("first phone cut back in and got %d", code)
	}
}

func TestControlScreenSaysWhereTheCaretIs(t *testing.T) {
	_, mux, _ := controlServer(t)
	capture = func(int) (*image.RGBA, error) { return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil }
	caret = func() (float64, float64, float64, float64, bool) { return 0.25, 0.5, 0.001, 0.02, true }
	session, token := grantPhone(t, mux, "192.168.1.20", 1, "Pixel")
	r := phoneReq("GET", "/api/phone/control/screen?session="+session+"&w=640", "")
	r.Header.Set("X-Control-Token", token)
	if got := serve(mux, r).Header().Get("X-Caret"); got != "0.2500,0.5000,0.0010,0.0200" {
		t.Fatalf("caret header = %q", got)
	}
}

func TestControlScreenSaysWhereThePointerIs(t *testing.T) {
	_, mux, _ := controlServer(t)
	capture = func(int) (*image.RGBA, error) { return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil }
	pointer = func() (float64, float64, bool) { return 0.75, 0.125, true }
	session, token := grantPhone(t, mux, "192.168.1.20", 1, "Pixel")
	r := phoneReq("GET", "/api/phone/control/screen?session="+session+"&w=640", "")
	r.Header.Set("X-Control-Token", token)
	if got := serve(mux, r).Header().Get("X-Pointer"); got != "0.7500,0.1250" {
		t.Fatalf("pointer header = %q", got)
	}
	pointer = func() (float64, float64, bool) { return 1.4, 0.5, true }
	r = phoneReq("GET", "/api/phone/control/screen?session="+session+"&w=640", "")
	r.Header.Set("X-Control-Token", token)
	if got := serve(mux, r).Header().Get("X-Pointer"); got != "" {
		t.Fatalf("pointer off the screen should send nothing, got %q", got)
	}
}

// A phone that lost its saved pass and is allowed for good again keeps one
// entry on the PC, and the lost pass stops working.
func TestControlAlwaysAllowAgainReplacesTheOldPass(t *testing.T) {
	_, mux, _ := controlServer(t)
	phone := hello(t, mux, "192.168.1.20", deviceKey(1), "Pixel")
	got := ask(t, mux, phone, "")
	answer(t, mux, got["ask"], true, true)
	old := wait(t, mux, phone, got["ask"])["trust"]
	serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`))

	got = ask(t, mux, phone, "")
	answer(t, mux, got["ask"], true, true)
	fresh := wait(t, mux, phone, got["ask"])["trust"]
	serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`))

	view := decode[controlView](t, serve(mux, deskReq("GET", "/api/desk/control", "")))
	if len(view.Trusted) != 1 {
		t.Fatalf("the same phone is listed %d times", len(view.Trusted))
	}
	if again := ask(t, mux, phone, old); again["state"] != "waiting" {
		t.Fatalf("the replaced pass still works: %v", again)
	}
	serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`))
	if again := ask(t, mux, phone, fresh); again["state"] != "granted" {
		t.Fatalf("the new pass does not work: %v", again)
	}
}

func TestControlKeepsThePCAwakeOnlyWhileControlled(t *testing.T) {
	_, mux, _ := controlServer(t)
	var calls atomic.Int32
	awake = func() { calls.Add(1) }
	stop := make(chan struct{})
	go control.run(stop)
	defer close(stop)

	time.Sleep(2500 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("kept the PC awake %d times with nobody in control", n)
	}
	grantPhone(t, mux, "192.168.1.20", 1, "Pixel")
	time.Sleep(2500 * time.Millisecond)
	if calls.Load() == 0 {
		t.Fatal("the PC was not kept awake while a phone had control")
	}
	serve(mux, deskReq("POST", "/api/desk/control", `{"stop":"all"}`))
	time.Sleep(200 * time.Millisecond)
	before := calls.Load()
	time.Sleep(2500 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("still keeping the PC awake after control ended")
	}
}
