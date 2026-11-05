package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSocialHosts(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"instagram.com", "instagram.com"},
		{"www.instagram.com", "instagram.com"},
		{"youtu.be", "youtube.com"},
		{"www.youtube.com", "youtube.com"},
		{"vm.tiktok.com", "tiktok.com"},
		{"www.tiktok.com", "tiktok.com"},
		{"fb.watch", "facebook.com"},
		{"www.facebook.com", "facebook.com"},
		{"m.facebook.com", "facebook.com"},
		{"x.com", "x.com"},
		{"twitter.com", "x.com"},
		{"vimeo.com", "vimeo.com"},
		{"www.vimeo.com", "vimeo.com"},
	}
	for _, tc := range cases {
		got, ok := socialHost(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("%s -> %q ok=%v", tc.in, got, ok)
		}
	}
	for _, bad := range []string{"", "..", "foo/bar", "con", "youtube.com/../windows"} {
		if _, ok := socialHost(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestMediaURLRejectsNonHTTP(t *testing.T) {
	if u, ok := mediaURL("https://youtu.be/abcdefghijk"); !ok || u.Hostname() != "youtu.be" {
		t.Fatal(u, ok)
	}
	if u, ok := mediaURL("https://www.instagram.com/reel/abc"); !ok || u.Hostname() != "www.instagram.com" {
		t.Fatal(u, ok)
	}
	for _, bad := range []string{"", "ftp://example.com/a", "file:///C:/secret.mp4", "javascript:alert(1)", "-o", "http://\nexample.com", "not a url", "https://example.com/a b"} {
		if _, ok := mediaURL(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestSocialDirStaysInsideSaveRoot(t *testing.T) {
	mux := testServer(t).routes()
	_ = mux
	dir, rel, err := socialDir("youtu.be")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "Social media/youtube.com" {
		t.Fatal(rel)
	}
	root, err := filepath.Abs(saveRoot())
	if err != nil {
		t.Fatal(err)
	}
	if !sameOrInside(root, dir) {
		t.Fatal(dir)
	}
	if _, _, err := socialDir(".."); err == nil {
		t.Fatal("escaped")
	}
}

func TestSocialArgsAreFixed(t *testing.T) {
	page := "https://youtu.be/abcdefghijk?x=1&--exec=calc"
	dir := filepath.Join(string(filepath.Separator), "save", "Social media", "youtube.com")
	args := socialArgs(page, dir, filepath.Join("app", "ffmpeg.exe"))
	if args[len(args)-2] != "--" || args[len(args)-1] != page {
		t.Fatalf("url was not a single argument: %v", args)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{"--ignore-config", "--newline", "--restrict-filenames", "--no-playlist", "--ffmpeg-location"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, args)
		}
	}
	for _, arg := range args[:len(args)-1] {
		if arg == "--yes-playlist" || arg == "--exec" || strings.HasPrefix(arg, "--exec=") {
			t.Fatal(args)
		}
	}
	found := false
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			found = strings.Contains(filepath.ToSlash(args[i+1]), "/Social media/youtube.com/")
		}
	}
	if !found {
		t.Fatal(args)
	}
}

func TestSocialMissingTool(t *testing.T) {
	mux := testServer(t).routes()
	old := lookupYT
	lookupYT = func() string { return "" }
	t.Cleanup(func() { lookupYT = old })
	w := serve(mux, deskReq("GET", "/api/social", ""))
	view := decode[socialView](t, w)
	if view.YT || !strings.Contains(view.Hint, "yt-dlp.exe") {
		t.Fatalf("%+v", view)
	}
	w = serve(mux, deskReq("POST", "/api/social", `{"url":"https://youtu.be/abcdefghijk","all":true}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "yt-dlp.exe") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if social.running {
		t.Fatal("started without yt-dlp")
	}
	w = serve(mux, deskReq("POST", "/api/social", `{"url":"file:///C:/secret.mp4"}`))
	if w.Code != 400 || strings.Contains(w.Body.String(), "yt-dlp.exe") {
		t.Fatalf("non-http was not rejected first: %d %s", w.Code, w.Body.String())
	}
}

func TestSocialSendOnlyWhenConnected(t *testing.T) {
	mux := testServer(t).routes()
	if _, _, err := socialDir("instagram.com"); err != nil {
		t.Fatal(err)
	}
	saveFile(t, "Social media/instagram.com/clip.txt", "clip")
	if n, note := offerSocial("Social media/instagram.com/clip.txt", true, ""); n != 0 || note != "Saved. No device is connected." {
		t.Fatalf("empty %d %q", n, note)
	}
	session := hello(t, mux, "192.168.1.70", deviceKey(7), "Kitchen")
	handle := handleOf(t, mux, "Kitchen")
	n, note := offerSocial("Social media/instagram.com/clip.txt", false, handle)
	if n != 1 || note != "Saved and sent to Kitchen." {
		t.Fatalf("connected %d %q", n, note)
	}
	if got := len(mail(t, mux, session).Files); got != 1 {
		t.Fatalf("inbox %d", got)
	}
	markAway(t, mux, session)
	n, note = offerSocial("Social media/instagram.com/clip.txt", false, handle)
	if n != 0 || note != "Saved. That device is not connected." {
		t.Fatalf("away %d %q", n, note)
	}
	hub.mu.Lock()
	left := len(hub.byHandle[handle].offers)
	hub.mu.Unlock()
	if left != 1 {
		t.Fatalf("away device gained a file, inbox %d", left)
	}
}

func TestPhoneSocialUsesTheSameDownload(t *testing.T) {
	mux := testServer(t).routes()
	kitchen := hello(t, mux, "192.168.1.71", deviceKey(11), "Kitchen")
	other := hello(t, mux, "192.168.1.72", deviceKey(12), "Pixel")
	kitchenHandle := handleOf(t, mux, "Kitchen")
	pixel := handleOf(t, mux, "Pixel")
	page := "https://youtu.be/abcdefghijk"
	body := `{"session":"` + other + `","url":"` + page + `"}`

	r := phoneReq("POST", "/api/phone/social", body)
	r.Header.Del("X-Transfer-Key")
	if w := serve(mux, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key %d %s", w.Code, w.Body.String())
	}
	r = phoneReq("GET", "/api/phone/social?session="+other, "")
	r.Header.Del("X-Transfer-Key")
	if w := serve(mux, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("status no key %d", w.Code)
	}

	w := serve(mux, phoneReq("POST", "/api/phone/social", `{"session":"`+other+`","url":"file:///C:/secret.mp4"}`))
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "yt-dlp.exe") {
		t.Fatalf("non-http %d %s", w.Code, w.Body.String())
	}
	if social.running {
		t.Fatal("non-http started a download")
	}

	type hit struct {
		all    bool
		device string
		rel    string
		args   []string
	}
	started := make(chan hit, 1)
	finished := make(chan struct{})
	old := launchSocial
	launchSocial = func(ctx context.Context, cancel context.CancelFunc, bin string, args []string, dir, rel string, before map[string]bool, all bool, device string) {
		defer close(finished)
		defer cancel()
		started <- hit{all: all, device: device, rel: rel, args: append([]string(nil), args...)}
		<-ctx.Done()
		social.mu.Lock()
		social.running = false
		social.phase = "cancelled"
		social.mu.Unlock()
	}
	t.Cleanup(func() {
		launchSocial = old
		stopSocial()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
		}
		social.mu.Lock()
		social.running = false
		social.phase = ""
		social.cancel = nil
		social.mu.Unlock()
	})

	w = serve(mux, phoneReq("POST", "/api/phone/social", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	view := decode[socialView](t, w)
	if !view.Running || view.Folder != "Social media/youtube.com" {
		t.Fatalf("%+v", view)
	}
	var got hit
	select {
	case got = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("download was not started")
	}
	if got.all || got.device != pixel || got.device == kitchenHandle {
		t.Fatalf("target all=%v device=%s pixel=%s kitchen=%s", got.all, got.device, pixel, kitchenHandle)
	}
	if len(got.args) < 2 || got.args[len(got.args)-2] != "--" || got.args[len(got.args)-1] != page {
		t.Fatalf("args %v", got.args)
	}
	joined := strings.Join(got.args[:len(got.args)-1], "\n")
	if !strings.Contains(joined, "--no-playlist") || !strings.Contains(filepath.ToSlash(got.rel), "Social media/youtube.com") {
		t.Fatalf("rel %s args %v", got.rel, got.args)
	}
	w = serve(mux, phoneReq("POST", "/api/phone/social", `{"session":"`+kitchen+`","url":"`+page+`"}`))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already running") {
		t.Fatalf("second %d %s", w.Code, w.Body.String())
	}
	w = serve(mux, phoneReq("GET", "/api/phone/social?session="+other, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	view = decode[socialView](t, w)
	if !view.Running || view.Folder != "Social media/youtube.com" {
		t.Fatalf("status %+v", view)
	}
}
