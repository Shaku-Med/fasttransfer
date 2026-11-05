package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRelease struct {
	tag      string
	manifest signedManifest
	exe      []byte
	signWith ed25519.PrivateKey
	noSig    bool
}

// fakeGitHub serves one release the way GitHub's API and downloads do.
func fakeGitHub(t *testing.T, rel *fakeRelease) {
	t.Helper()
	body, _ := json.Marshal(rel.manifest)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(rel.signWith, body))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			assets := []map[string]any{
				{"name": "update.json", "browser_download_url": srv.URL + "/update.json", "size": len(body)},
				{"name": "FastTransfer.exe", "browser_download_url": srv.URL + "/FastTransfer.exe", "size": rel.manifest.Size},
			}
			if !rel.noSig {
				assets = append(assets, map[string]any{"name": "update.json.sig", "browser_download_url": srv.URL + "/update.json.sig", "size": len(sig)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": rel.tag, "body": "Faster sends\r\nand fixes", "assets": assets})
		case "/update.json":
			_, _ = w.Write(body)
		case "/update.json.sig":
			_, _ = w.Write([]byte(sig))
		case "/FastTransfer.exe":
			_, _ = w.Write(rel.exe)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	oldAPI, oldKey, oldHost, oldVer, oldSelf, oldRelaunch, oldClose := releaseAPI, verifyKey, trustedHost, appVersion, selfPath, relaunch, closeApp
	t.Cleanup(func() {
		releaseAPI, verifyKey, trustedHost, appVersion, selfPath, relaunch, closeApp = oldAPI, oldKey, oldHost, oldVer, oldSelf, oldRelaunch, oldClose
		updates = &updater{state: "idle"}
	})
	releaseAPI = srv.URL + "/latest"
	trustedHost = func(u *url.URL) bool { return strings.HasPrefix(u.String(), srv.URL) }
	appVersion = "1.0.0"
	closeApp = func() {}
	updates = &updater{state: "idle"}
}

func newRelease(t *testing.T, version string, exe []byte) (*fakeRelease, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(exe)
	return &fakeRelease{
		tag:      "v" + version,
		manifest: signedManifest{App: "fasttransfer", Version: version, File: "FastTransfer.exe", Size: int64(len(exe)), SHA256: hex.EncodeToString(sum[:])},
		exe:      exe,
		signWith: priv,
	}, pub
}

func TestUpdateOffersASignedNewerRelease(t *testing.T) {
	rel, pub := newRelease(t, "1.2.0", []byte("new app"))
	fakeGitHub(t, rel)
	verifyKey = pub
	if err := updates.check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	v := updates.view()
	if v.State != "available" || v.Latest == nil || v.Latest.Version != "1.2.0" || v.Latest.Notes != "Faster sends\nand fixes" {
		t.Fatalf("got %+v", v)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "sha") || strings.Contains(string(raw), "http") {
		t.Fatalf("the view should not carry download details: %s", raw)
	}
}

func TestUpdateRefusesWhatTheKeyDidNotSign(t *testing.T) {
	rel, _ := newRelease(t, "1.2.0", []byte("new app"))
	fakeGitHub(t, rel)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	verifyKey = other
	if err := updates.check(context.Background(), true); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("a release signed by another key must be refused, got %v", err)
	}
	if v := updates.view(); v.State != "failed" || v.Latest != nil {
		t.Fatalf("got %+v", v)
	}
}

func TestUpdateRefusesAnUnsignedRelease(t *testing.T) {
	rel, pub := newRelease(t, "1.2.0", []byte("new app"))
	rel.noSig = true
	fakeGitHub(t, rel)
	verifyKey = pub
	if err := updates.check(context.Background(), true); err == nil {
		t.Fatal("a release with no signature must be refused")
	}
}

// A real signed manifest for an old version must not be passed off under a
// newer tag to roll people back.
func TestUpdateRefusesATagThatDoesNotMatchTheSignedVersion(t *testing.T) {
	rel, pub := newRelease(t, "1.0.0", []byte("old app"))
	rel.tag = "v9.0.0"
	fakeGitHub(t, rel)
	verifyKey = pub
	if err := updates.check(context.Background(), true); err == nil {
		t.Fatal("a tag that does not match the signed version must be refused")
	}
}

func TestUpdateSaysUpToDate(t *testing.T) {
	rel, pub := newRelease(t, "1.0.0", []byte("same app"))
	fakeGitHub(t, rel)
	verifyKey = pub
	if err := updates.check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if v := updates.view(); v.State != "latest" || v.Latest != nil {
		t.Fatalf("got %+v", v)
	}
}

func TestUpdateQuietCheckKeepsStateWhenGitHubIsDown(t *testing.T) {
	rel, pub := newRelease(t, "1.2.0", []byte("new app"))
	fakeGitHub(t, rel)
	verifyKey = pub
	releaseAPI = "http://127.0.0.1:1/latest"
	_ = updates.check(context.Background(), false)
	if v := updates.view(); v.State != "idle" || v.Error != "" {
		t.Fatalf("a background check that fails should stay quiet, got %+v", v)
	}
}

func TestUpdateInstallSwapsTheAppAndRestarts(t *testing.T) {
	rel, pub := newRelease(t, "1.2.0", []byte("brand new app"))
	fakeGitHub(t, rel)
	verifyKey = pub
	dir := t.TempDir()
	exe := filepath.Join(dir, "FastTransfer.exe")
	if err := os.WriteFile(exe, []byte("old app"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfPath = func() (string, error) { return exe, nil }
	var started string
	relaunch = func(p string) error { started = p; return nil }

	if err := updates.check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := updates.install(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "brand new app" {
		t.Fatalf("the app was not replaced, it holds %q", got)
	}
	if got, _ := os.ReadFile(exe + ".old"); string(got) != "old app" {
		t.Fatalf("the old app should be kept aside until the new one starts, got %q", got)
	}
	if started != exe || updates.view().State != "restarting" {
		t.Fatalf("started %q, state %q", started, updates.view().State)
	}
}

func TestUpdateInstallThrowsAwayATamperedDownload(t *testing.T) {
	rel, pub := newRelease(t, "1.2.0", []byte("brand new app"))
	rel.exe = []byte("evil new app!")
	fakeGitHub(t, rel)
	verifyKey = pub
	dir := t.TempDir()
	exe := filepath.Join(dir, "FastTransfer.exe")
	_ = os.WriteFile(exe, []byte("old app"), 0o755)
	selfPath = func() (string, error) { return exe, nil }
	relaunch = func(string) error { t.Fatal("must not start a tampered download"); return nil }

	if err := updates.check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := updates.install(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("got %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old app" {
		t.Fatal("the running app must be left alone")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 1 {
		t.Fatalf("the bad download should be cleaned up, found %v", left)
	}
}

func TestUpdateInstallPutsTheOldAppBackIfTheNewOneWillNotStart(t *testing.T) {
	rel, pub := newRelease(t, "1.2.0", []byte("brand new app"))
	fakeGitHub(t, rel)
	verifyKey = pub
	dir := t.TempDir()
	exe := filepath.Join(dir, "FastTransfer.exe")
	_ = os.WriteFile(exe, []byte("old app"), 0o755)
	selfPath = func() (string, error) { return exe, nil }
	relaunch = func(string) error { return os.ErrPermission }

	_ = updates.check(context.Background(), true)
	if err := updates.install(); err == nil {
		t.Fatal("install should report the failed start")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old app" {
		t.Fatalf("the old app should be back, found %q", got)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.10.0", "1.9.9", true},
		{"2.0.0", "1.99.99", true},
		{"1.0.0", "1.0.0", false},
		{"0.9.0", "1.0.0", false},
		{"1.0.0", "dev", false},
		{"01.0.0", "0.0.1", false},
		{"1.0", "0.0.1", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestUpdateRoutesStayOnThePC(t *testing.T) {
	_, mux, _ := controlServer(t)
	for _, method := range []string{"GET", "POST"} {
		if w := serve(mux, phoneReq(method, "/api/desk/update", `{"action":"check"}`)); w.Code != http.StatusNotFound {
			t.Fatalf("%s from a phone got %d", method, w.Code)
		}
	}
	if w := serve(mux, deskReq("POST", "/api/desk/update", `{"action":"run"}`)); w.Code != http.StatusBadRequest {
		t.Fatalf("an unknown action got %d", w.Code)
	}
}

func TestReleaseKeyIsBuiltIn(t *testing.T) {
	if decodePubKey(updatePubKey) == nil {
		t.Fatal("the release key in updater.go is not a valid public key")
	}
	if !githubHost(&url.URL{Scheme: "https", Host: "objects.githubusercontent.com"}) || githubHost(&url.URL{Scheme: "http", Host: "github.com"}) || githubHost(&url.URL{Scheme: "https", Host: "github.com.evil.io"}) {
		t.Fatal("only GitHub over https is allowed")
	}
}
