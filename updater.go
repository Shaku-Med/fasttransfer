package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// FastTransfer looks for a newer release on GitHub and swaps itself for it
// once the person at the PC says yes. Every release carries a small manifest
// signed with a key that only lives on the author's PC. Anything not signed by
// it, or a download that does not match the signed size and hash, is refused,
// so a hijacked account or tampered download cannot put code on anyone's PC.

const (
	updateRepo    = "Shaku-Med/fasttransfer"
	updatePubKey  = "1PbIDLO+8CSitAE+3p6kDV9XdtUjvsb0ByCdxylHnQc="
	updateExeName = "FastTransfer.exe"
	maxUpdateSize = 1 << 30
	maxNotes      = 1500
	updateEvery   = 6 * time.Hour
)

// appVersion is stamped in by cmd/release. A plain go build stays "dev" and
// never updates itself.
var appVersion = "dev"

var (
	semverRe    = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})$`)
	sha256Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	updateLimit = newLimiter(0.2, 3)

	// Kept as variables so tests can point them at a local server and a
	// throwaway file instead of GitHub and the running app.
	releaseAPI  = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	verifyKey   = decodePubKey(updatePubKey)
	trustedHost = githubHost
	selfPath    = currentExe
	relaunch    = startUpdated
	closeApp    = stopWindow
)

func decodePubKey(s string) ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(raw)
}

// githubHost keeps every request on GitHub over https, redirects included.
func githubHost(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "github.com" || h == "api.github.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

func updateHTTP(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !trustedHost(req.URL) {
				return errors.New("update download left GitHub")
			}
			return nil
		},
	}
}

type updateOffer struct {
	Version string
	Notes   string
	Size    int64
	sha     string
	exeURL  string
}

type updater struct {
	mu    sync.Mutex
	state string // idle, checking, latest, available, downloading, restarting, failed
	err   string
	offer *updateOffer
	got   int64
	shown int
}

var updates = &updater{state: "idle"}

type updateView struct {
	Version  string      `json:"version"`
	State    string      `json:"state"`
	Error    string      `json:"error,omitempty"`
	Latest   *latestView `json:"latest,omitempty"`
	Progress float64     `json:"progress,omitempty"`
}

type latestView struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	Size    int64  `json:"size"`
}

func (u *updater) view() updateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := updateView{Version: appVersion, State: u.state, Error: u.err}
	if appVersion == "dev" {
		v.State = "dev"
	}
	if u.offer != nil {
		v.Latest = &latestView{Version: u.offer.Version, Notes: u.offer.Notes, Size: u.offer.Size}
		if u.state == "downloading" && u.offer.Size > 0 {
			v.Progress = float64(u.got) / float64(u.offer.Size)
		}
	}
	return v
}

func (u *updater) set(state, msg string) {
	u.mu.Lock()
	u.state, u.err = state, msg
	u.mu.Unlock()
	hub.emit(feedEvent{Kind: "update", Text: state})
}

func (u *updater) run(stop <-chan struct{}) {
	if appVersion == "dev" {
		return
	}
	wait := time.NewTimer(20 * time.Second)
	defer wait.Stop()
	for {
		select {
		case <-stop:
			return
		case <-wait.C:
		}
		u.check(context.Background(), false)
		wait.Reset(updateEvery)
	}
}

type ghRelease struct {
	Tag    string `json:"tag_name"`
	Body   string `json:"body"`
	Draft  bool   `json:"draft"`
	Pre    bool   `json:"prerelease"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

type signedManifest struct {
	App     string `json:"app"`
	Version string `json:"version"`
	File    string `json:"file"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

var (
	errUpdateBusy = errors.New("an update is already on its way")
	errNoRelease  = errors.New("no release has been published yet")
)

// check asks GitHub about the latest release. A quiet check that cannot reach
// GitHub leaves things as they were; only a check someone asked for says so.
func (u *updater) check(ctx context.Context, asked bool) error {
	u.mu.Lock()
	if u.state == "checking" || u.state == "downloading" || u.state == "restarting" {
		u.mu.Unlock()
		return errUpdateBusy
	}
	prev, prevErr := u.state, u.err
	u.state, u.err = "checking", ""
	u.mu.Unlock()

	offer, err := fetchOffer(ctx)
	switch {
	case err != nil && !asked:
		u.set(prev, prevErr)
	case err != nil:
		u.set("failed", err.Error())
	case offer == nil:
		u.mu.Lock()
		u.offer = nil
		u.mu.Unlock()
		u.set("latest", "")
	default:
		u.mu.Lock()
		u.offer = offer
		u.mu.Unlock()
		u.set("available", "")
	}
	return err
}

func fetchOffer(ctx context.Context) (*updateOffer, error) {
	if appVersion == "dev" {
		return nil, errors.New("this copy was built by hand, so it does not update itself")
	}
	if verifyKey == nil {
		return nil, errors.New("this copy has no key to check updates with")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := updateHTTP(0)
	raw, err := fetchSmall(ctx, client, releaseAPI, 1<<20)
	if errors.Is(err, errNoRelease) {
		return nil, errNoRelease
	}
	if err != nil {
		return nil, errors.New("could not reach GitHub")
	}
	var rel ghRelease
	if err := json.Unmarshal(raw, &rel); err != nil || rel.Draft || rel.Pre {
		return nil, errors.New("GitHub sent a release this cannot read")
	}
	assets := map[string]string{}
	var exeSize int64
	for _, a := range rel.Assets {
		assets[a.Name] = a.URL
		if a.Name == updateExeName {
			exeSize = a.Size
		}
	}
	if assets["update.json"] == "" || assets["update.json.sig"] == "" || assets[updateExeName] == "" {
		return nil, errors.New("the latest release is not signed, so it was skipped")
	}
	body, err := fetchSmall(ctx, client, assets["update.json"], 4<<10)
	if err != nil {
		return nil, errors.New("could not fetch the release details")
	}
	sigText, err := fetchSmall(ctx, client, assets["update.json.sig"], 1<<10)
	if err != nil {
		return nil, errors.New("could not fetch the release signature")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(verifyKey, body, sig) {
		return nil, errors.New("the latest release failed its signature check, so it was skipped")
	}
	var m signedManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, errors.New("the release details are damaged")
	}
	if m.App != "fasttransfer" || !semverRe.MatchString(m.Version) || m.File != updateExeName ||
		m.Size <= 0 || m.Size > maxUpdateSize || !sha256Re.MatchString(m.SHA256) ||
		rel.Tag != "v"+m.Version || exeSize != m.Size {
		return nil, errors.New("the release details do not match the release")
	}
	if !newerVersion(m.Version, appVersion) {
		return nil, nil
	}
	return &updateOffer{Version: m.Version, Notes: cleanNotes(rel.Body), Size: m.Size, sha: m.SHA256, exeURL: assets[updateExeName]}, nil
}

func fetchSmall(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || !trustedHost(u) {
		return nil, errors.New("not a GitHub address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "FastTransfer-updater")
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errNoRelease
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("too big")
	}
	return b, nil
}

// newerVersion says whether a is a later version than b.
func newerVersion(a, b string) bool {
	pa, okA := versionParts(a)
	pb, okB := versionParts(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func versionParts(v string) ([3]int, bool) {
	var out [3]int
	if !semverRe.MatchString(v) {
		return out, false
	}
	for i, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// cleanNotes keeps release notes as short plain text.
func cleanNotes(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range strings.ReplaceAll(s, "\r\n", "\n") {
		if r != '\n' && unicode.IsControl(r) {
			continue
		}
		if n >= maxNotes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// install downloads the offered release next to the running app, checks it
// against the signed size and hash, swaps the files and starts the new copy.
func (u *updater) install() error {
	u.mu.Lock()
	if u.state != "available" || u.offer == nil {
		u.mu.Unlock()
		return errors.New("there is no update to install")
	}
	offer := *u.offer
	u.state, u.err, u.got, u.shown = "downloading", "", 0, 0
	u.mu.Unlock()
	hub.emit(feedEvent{Kind: "update", Text: "downloading"})

	if err := u.swapIn(offer); err != nil {
		u.set("available", err.Error())
		return err
	}
	u.set("restarting", "")
	go func() {
		time.Sleep(600 * time.Millisecond)
		closeApp()
	}()
	return nil
}

func (u *updater) swapIn(offer updateOffer) error {
	exe, err := selfPath()
	if err != nil {
		return errors.New("could not find where FastTransfer is")
	}
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".FastTransfer.update")
	_ = os.Remove(tmp)
	if err := u.download(offer, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(tmp)
		return errors.New("FastTransfer cannot replace itself in this folder. Move it to a folder you own, like Documents, and try again")
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(old, exe)
		_ = os.Remove(tmp)
		return errors.New("could not put the new version in place")
	}
	if err := relaunch(exe); err != nil {
		_ = os.Rename(exe, tmp)
		_ = os.Rename(old, exe)
		_ = os.Remove(tmp)
		return errors.New("could not start the new version")
	}
	return nil
}

func (u *updater) download(offer updateOffer, dest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	src, err := url.Parse(offer.exeURL)
	if err != nil || !trustedHost(src) {
		return errors.New("the download is not on GitHub")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "FastTransfer-updater")
	res, err := updateHTTP(0).Do(req)
	if err != nil {
		return errors.New("the download did not start")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || (res.ContentLength >= 0 && res.ContentLength != offer.Size) {
		return errors.New("the download did not start")
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return errors.New("could not save the download next to FastTransfer")
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h, progressWriter{u}), io.LimitReader(res.Body, offer.Size+1))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return errors.New("the download broke off, try again")
	}
	if n != offer.Size || hex.EncodeToString(h.Sum(nil)) != offer.sha {
		return errors.New("the download does not match the signed release, so it was thrown away")
	}
	return nil
}

type progressWriter struct{ u *updater }

func (p progressWriter) Write(b []byte) (int, error) {
	u := p.u
	u.mu.Lock()
	u.got += int64(len(b))
	pct := 0
	if u.offer != nil && u.offer.Size > 0 {
		pct = int(u.got * 100 / u.offer.Size)
	}
	tell := pct >= u.shown+2
	if tell {
		u.shown = pct
	}
	u.mu.Unlock()
	if tell {
		hub.emit(feedEvent{Kind: "update", Text: "downloading"})
	}
	return len(b), nil
}

func currentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func startUpdated(exe string) error {
	cmd := exec.Command(exe, "--updated")
	cmd.Dir = filepath.Dir(exe)
	return cmd.Start()
}

// afterUpdate tidies up after a swap: the old copy is still running for a
// moment when the new one starts, so its file is removed once it lets go.
func afterUpdate() {
	exe, err := selfPath()
	if err != nil {
		return
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err != nil {
		return
	}
	go func() {
		for i := 0; i < 60; i++ {
			if err := os.Remove(old); err == nil || errors.Is(err, os.ErrNotExist) {
				return
			}
			time.Sleep(time.Second)
		}
	}()
}

func justUpdated(args []string) bool {
	for _, a := range args {
		if a == "--updated" {
			return true
		}
	}
	return false
}

func (s *server) deskUpdate(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, updates.view())
}

func (s *server) deskUpdateSet(w http.ResponseWriter, r *http.Request) {
	if !updateLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	var err error
	switch req.Action {
	case "check":
		err = updates.check(r.Context(), true)
	case "install":
		err = updates.install()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "update": updates.view()})
		return
	}
	writeJSON(w, http.StatusOK, updates.view())
}
