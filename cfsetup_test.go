package main

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeToken = "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
const fakeAccount = "0123456789abcdef0123456789abcdef"

// fakeCloudflare answers the handful of API calls setup makes.
type fakeCloudflare struct {
	mu         sync.Mutex
	subdomain  string
	scriptTag  string
	uploads    []map[string]string
	meta       map[string]any
	enabled    bool
	badToken   bool
	uploadFail string
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, result any, errs ...cfError) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": status < 300, "errors": errs, "result": json.RawMessage(raw)})
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeToken || f.badToken {
		reply(http.StatusUnauthorized, nil, cfError{Code: 10000, Message: "Authentication error"})
		return
	}
	acct := "/client/v4/accounts/" + fakeAccount
	script := acct + "/workers/scripts/" + cfScript
	switch {
	case r.Method == "GET" && r.URL.Path == "/client/v4/accounts":
		reply(200, []cfAccount{{ID: fakeAccount, Name: "Student\x07 account"}})
	case r.Method == "GET" && r.URL.Path == acct+"/workers/subdomain":
		if f.subdomain == "" {
			reply(404, nil, cfError{Code: 10007, Message: "This subdomain does not exist"})
			return
		}
		reply(200, map[string]string{"subdomain": f.subdomain})
	case r.Method == "PUT" && r.URL.Path == acct+"/workers/subdomain":
		var body struct{ Subdomain string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.subdomain = body.Subdomain
		reply(200, map[string]string{"subdomain": f.subdomain})
	case r.Method == "GET" && r.URL.Path == acct+"/workers/scripts":
		var list []cfScriptInfo
		if f.scriptTag != "" {
			list = append(list, cfScriptInfo{ID: cfScript, MigrationTag: f.scriptTag})
		}
		reply(200, list)
	case r.Method == "PUT" && r.URL.Path == script:
		if f.uploadFail != "" {
			reply(400, nil, cfError{Code: 10021, Message: f.uploadFail})
			return
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			reply(400, nil, cfError{Code: 1, Message: "bad form"})
			return
		}
		parts := map[string]string{}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			parts[p.FormName()] = p.Header.Get("Content-Type") + "\n" + string(data)
		}
		f.uploads = append(f.uploads, parts)
		f.meta = nil
		_ = json.Unmarshal([]byte(strings.SplitN(parts["metadata"], "\n", 2)[1]), &f.meta)
		f.scriptTag = cfMigration
		reply(200, map[string]string{"id": cfScript})
	case r.Method == "POST" && r.URL.Path == script+"/subdomain":
		f.enabled = true
		reply(200, nil)
	default:
		reply(404, nil, cfError{Code: 7003, Message: "no route " + r.Method + " " + r.URL.Path})
	}
}

func cfTest(t *testing.T) (http.Handler, *fakeCloudflare, *[]string) {
	t.Helper()
	s := testServer(t)
	fc := &fakeCloudflare{}
	srv := httptest.NewServer(fc)
	var probed []string
	var probeMu sync.Mutex
	oldAPI, oldOnline, oldPoll, oldOpen, oldDir, oldLimit := cfAPI, relayOnline, cfPoll, openLink, appDir, cfLimit
	cfAPI = srv.URL + "/client/v4"
	cfPoll = 10 * time.Millisecond
	cfLimit = newLimiter(1000, 1000)
	relayOnline = func(_ context.Context, base string) bool {
		probeMu.Lock()
		defer probeMu.Unlock()
		probed = append(probed, base)
		return len(probed) > 2
	}
	openLink = func(string) error { return nil }
	appDir = t.TempDir()
	relay = &relayClient{}
	cfJob = &cfSetup{}
	t.Cleanup(func() {
		cfJob.stop()
		waitFor(t, "setup to stop", func() bool { return !cfJob.view().Running })
		srv.Close()
		cfAPI, relayOnline, cfPoll, openLink, appDir, cfLimit = oldAPI, oldOnline, oldPoll, oldOpen, oldDir, oldLimit
		relay = &relayClient{}
	})
	return s.routes(), fc, &probed
}

func TestRelaySetupOnAFreshAccount(t *testing.T) {
	mux, fc, probed := cfTest(t)
	w := serve(mux, deskReq("POST", "/api/desk/relay/setup", `{"action":"start","token":"`+fakeToken+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("start got %d %s", w.Code, w.Body)
	}
	waitFor(t, "setup to finish", func() bool { return !cfJob.view().Running })
	v := cfJob.view()
	if v.Error != "" {
		t.Fatalf("setup failed: %+v", v)
	}
	fc.mu.Lock()
	sub, enabled, uploads, meta := fc.subdomain, fc.enabled, fc.uploads, fc.meta
	fc.mu.Unlock()
	if !cfSubRe.MatchString(sub) || !strings.HasPrefix(sub, "ft-") {
		t.Fatalf("picked subdomain %q", sub)
	}
	want := "https://" + cfScript + "." + sub + ".workers.dev"
	if v.URL != want || relay.view().URL != want {
		t.Fatalf("got %q and %q, want %q", v.URL, relay.view().URL, want)
	}
	if len(*probed) < 3 || (*probed)[0] != want {
		t.Fatalf("probed %v", *probed)
	}
	if !enabled || len(uploads) != 1 {
		t.Fatalf("enabled %v uploads %d", enabled, len(uploads))
	}
	up := uploads[0]
	if !strings.HasPrefix(up["worker.js"], "application/javascript+module\n") || !strings.Contains(up["worker.js"], "export class Room") {
		t.Fatal("worker.js was not sent as the main module")
	}
	for _, name := range []string{"static/phone.html", "static/phone.js", "static/phone.css"} {
		if !strings.HasPrefix(up[name], "text/plain\n") || len(up[name]) < 100 {
			t.Fatalf("%s missing or wrong type", name)
		}
	}
	if meta["main_module"] != "worker.js" || meta["migrations"] == nil {
		t.Fatalf("metadata %v", meta)
	}
	for _, s := range v.Steps {
		if s.State != "done" {
			t.Fatalf("steps %+v", v.Steps)
		}
		if strings.ContainsRune(s.Text, '\x07') {
			t.Fatal("account name was not cleaned")
		}
	}
	saved, err := os.ReadFile(filepath.Join(appDir, "relay.json"))
	if err != nil || !strings.Contains(string(saved), want) {
		t.Fatalf("relay.json %q %v", saved, err)
	}

	// Running it again updates the same relay and skips the migration.
	if err := cfJob.start(fakeToken); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second setup", func() bool { return !cfJob.view().Running })
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if cfJob.view().Error != "" || fc.subdomain != sub || len(fc.uploads) != 2 || fc.meta["migrations"] != nil {
		t.Fatalf("rerun: %+v sub %q uploads %d meta %v", cfJob.view(), fc.subdomain, len(fc.uploads), fc.meta)
	}
}

func TestRelaySetupNeverKeepsTheToken(t *testing.T) {
	mux, fc, _ := cfTest(t)
	fc.subdomain = "student"
	fc.uploadFail = "Bearer " + fakeToken + " was echoed back"
	serve(mux, deskReq("POST", "/api/desk/relay/setup", `{"action":"start","token":"`+fakeToken+`"}`))
	waitFor(t, "setup to fail", func() bool { return !cfJob.view().Running })
	v := cfJob.view()
	if v.Error == "" || v.URL != "" || v.Steps[len(v.Steps)-1].State != "failed" {
		t.Fatalf("got %+v", v)
	}
	w := serve(mux, deskReq("GET", "/api/desk/relay/setup", ""))
	if strings.Contains(w.Body.String(), fakeToken) || !strings.Contains(v.Error, "[token]") {
		t.Fatalf("token leaked: %s", w.Body)
	}
	files, _ := os.ReadDir(appDir)
	for _, f := range files {
		raw, _ := os.ReadFile(filepath.Join(appDir, f.Name()))
		if strings.Contains(string(raw), fakeToken) {
			t.Fatalf("token written to %s", f.Name())
		}
	}
	if relay.view().URL != "" {
		t.Fatal("a failed setup must not save an address")
	}
}

func TestRelaySetupRejectsBadInput(t *testing.T) {
	mux, fc, _ := cfTest(t)
	for _, body := range []string{
		`{"action":"start","token":"short"}`,
		`{"action":"start","token":"` + strings.Repeat("a", 40) + `\"; rm"}`,
		`{"action":"open","open":"https://evil.example"}`,
		`{"action":"launch"}`,
	} {
		if w := serve(mux, deskReq("POST", "/api/desk/relay/setup", body)); w.Code != http.StatusBadRequest {
			t.Errorf("%s got %d", body, w.Code)
		}
	}
	r := phoneReq("POST", "/api/desk/relay/setup", `{"action":"start","token":"`+fakeToken+`"}`)
	if w := serve(mux, r); w.Code == http.StatusOK {
		t.Fatal("a phone must not start setup")
	}
	fc.mu.Lock()
	fc.badToken = true
	fc.mu.Unlock()
	serve(mux, deskReq("POST", "/api/desk/relay/setup", `{"action":"start","token":"`+fakeToken+`"}`))
	waitFor(t, "setup to fail", func() bool { return !cfJob.view().Running })
	if e := cfJob.view().Error; !strings.Contains(e, "permission") {
		t.Fatalf("bad token said %q", e)
	}
}

func TestRelayUploadRefusesSomeoneElsesScript(t *testing.T) {
	if _, _, err := relayUpload("v7"); err == nil {
		t.Fatal("a Worker with another migration history must not be overwritten")
	}
}
