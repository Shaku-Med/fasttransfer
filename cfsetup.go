package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Setting up a relay for someone who has never touched Cloudflare. They make
// a free account and an API token in their browser, paste the token here,
// and FastTransfer publishes relay/ to their account through Cloudflare's
// API. No Node, no wrangler, nothing installed. The token is used for this
// one setup and never written anywhere.

//go:embed relay/worker.js relay/static/phone.html relay/static/phone.js relay/static/phone.css
var relayFiles embed.FS

const (
	cfScript    = "fasttransfer-relay"
	cfCompat    = "2025-06-01"
	cfMigration = "v1"
)

// The token page opens with the two permissions setup needs already picked.
var (
	cfTokenPage = "https://dash.cloudflare.com/profile/api-tokens?name=FastTransfer%20relay&permissionGroupKeys=" +
		url.QueryEscape(`[{"key":"workers_scripts","type":"edit"},{"key":"account_settings","type":"read"}]`)
	cfSignupPage = "https://dash.cloudflare.com/sign-up/workers-and-pages"
	openLink     = openDefaultBrowser
)

var (
	cfAPI       = "https://api.cloudflare.com/client/v4"
	cfTokenRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{30,120}$`)
	cfAccountRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	cfSubRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	cfLimit     = newLimiter(0.5, 6)
	cfWaitUp    = 6 * time.Minute
	cfPoll      = 5 * time.Second
	// relayOnline checks a fresh relay answers like ours. Tests swap it.
	relayOnline = func(ctx context.Context, base string) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/phone.css", nil)
		if err != nil {
			return false
		}
		res, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: cfHTTP.CheckRedirect}).Do(req)
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK && strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	}
)

type cfStep struct {
	Text  string `json:"text"`
	State string `json:"state"` // running, done, failed
}

type cfSetupView struct {
	Running bool     `json:"running"`
	Steps   []cfStep `json:"steps"`
	URL     string   `json:"url,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type cfSetup struct {
	mu      sync.Mutex
	running bool
	steps   []cfStep
	url     string
	err     string
	cancel  context.CancelFunc
}

var cfJob = &cfSetup{}

func (j *cfSetup) view() cfSetupView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return cfSetupView{Running: j.running, Steps: append([]cfStep{}, j.steps...), URL: j.url, Error: j.err}
}

func (j *cfSetup) step(text string) {
	j.mu.Lock()
	if n := len(j.steps); n > 0 && j.steps[n-1].State == "running" {
		j.steps[n-1].State = "done"
	}
	j.steps = append(j.steps, cfStep{Text: text, State: "running"})
	j.mu.Unlock()
	hub.emit(feedEvent{Kind: "relaysetup"})
}

func (j *cfSetup) note(text string) {
	j.mu.Lock()
	if n := len(j.steps); n > 0 {
		j.steps[n-1].Text = text
	}
	j.mu.Unlock()
	hub.emit(feedEvent{Kind: "relaysetup"})
}

func (j *cfSetup) finish(base string, err error) {
	j.mu.Lock()
	if n := len(j.steps); n > 0 && j.steps[n-1].State == "running" {
		j.steps[n-1].State = "done"
		if err != nil {
			j.steps[n-1].State = "failed"
		}
	}
	j.running, j.cancel = false, nil
	if err != nil {
		j.err = err.Error()
	} else {
		j.url = base
	}
	j.mu.Unlock()
	hub.emit(feedEvent{Kind: "relaysetup"})
}

func (j *cfSetup) start(token string) error {
	token = strings.TrimSpace(token)
	if !cfTokenRe.MatchString(token) {
		return errors.New("that does not look like a Cloudflare API token. Copy the whole token Cloudflare showed you")
	}
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return errors.New("setup is already running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	j.running, j.cancel, j.steps, j.url, j.err = true, cancel, nil, "", ""
	j.mu.Unlock()
	go func() {
		defer cancel()
		base, err := deployRelay(ctx, &cfClient{token: token}, j)
		if err == nil {
			err = relay.setURL(base)
		}
		if err != nil && errors.Is(ctx.Err(), context.Canceled) {
			err = errors.New("setup was stopped")
		} else if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("setup took too long. Press Try again")
		}
		j.finish(base, err)
	}()
	return nil
}

func (j *cfSetup) stop() {
	j.mu.Lock()
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

var errCFDenied = errors.New("Cloudflare did not accept that token. Press Try again and make a new one with the Make token button, so it gets the Workers Scripts edit permission")

type cfClient struct {
	token string
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfReply struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

var cfHTTP = &http.Client{
	Timeout:       90 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// call talks to Cloudflare. The token goes in the Authorization header and
// nowhere else; replies are capped and their messages trimmed before anyone
// sees them.
func (c *cfClient) call(ctx context.Context, method, path, ctype string, body io.Reader, out any) (*cfReply, error) {
	req, err := http.NewRequestWithContext(ctx, method, cfAPI+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "FastTransfer")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := cfHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not reach Cloudflare. Check the internet connection")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, errors.New("Cloudflare's answer broke off")
	}
	var reply cfReply
	if json.Unmarshal(raw, &reply) != nil {
		return nil, fmt.Errorf("Cloudflare answered %d", res.StatusCode)
	}
	if !reply.Success {
		err := reply.failure(res.StatusCode)
		if strings.Contains(err.Error(), c.token) {
			err = errors.New(strings.ReplaceAll(err.Error(), c.token, "[token]"))
		}
		return &reply, err
	}
	if out != nil && len(reply.Result) > 0 {
		if err := json.Unmarshal(reply.Result, out); err != nil {
			return &reply, errors.New("Cloudflare sent something unexpected")
		}
	}
	return &reply, nil
}

func (r *cfReply) has(code int) bool {
	for _, e := range r.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

func (r *cfReply) failure(status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden || r.has(10000) || r.has(9109) || r.has(1000) {
		return errCFDenied
	}
	msg := ""
	if len(r.Errors) > 0 {
		msg = cleanCFText(r.Errors[0].Message, 200)
	}
	if msg == "" {
		return fmt.Errorf("Cloudflare answered %d", status)
	}
	return fmt.Errorf("Cloudflare said: %s", msg)
}

type cfAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// deployRelay publishes relay/ as a Worker with its Durable Object and turns
// on its workers.dev address. Running it again updates the same relay.
func deployRelay(ctx context.Context, c *cfClient, j *cfSetup) (string, error) {
	j.step("Checking the token")
	var accounts []cfAccount
	if _, err := c.call(ctx, http.MethodGet, "/accounts?per_page=50", "", nil, &accounts); err != nil {
		return "", err
	}
	var acct cfAccount
	for _, a := range accounts {
		if cfAccountRe.MatchString(a.ID) {
			acct = a
			break
		}
	}
	if acct.ID == "" {
		return "", errors.New("this token cannot see any Cloudflare account. Press Try again and make a new one with the Make token button")
	}
	j.note("Using the Cloudflare account " + cleanCFText(acct.Name, 60))

	j.step("Getting your workers.dev name")
	sub, err := c.subdomain(ctx, acct.ID)
	if err != nil {
		return "", err
	}

	j.step("Uploading the relay")
	if err := c.upload(ctx, acct.ID); err != nil {
		return "", err
	}

	j.step("Turning on its web address")
	body, _ := json.Marshal(map[string]bool{"enabled": true, "previews_enabled": false})
	if _, err := c.call(ctx, http.MethodPost, "/accounts/"+acct.ID+"/workers/scripts/"+cfScript+"/subdomain", "application/json", bytes.NewReader(body), nil); err != nil {
		return "", err
	}

	base := "https://" + cfScript + "." + sub + ".workers.dev"
	j.step("Waiting for Cloudflare to bring it online. A brand new address can take a few minutes")
	deadline := time.Now().Add(cfWaitUp)
	for !relayOnline(ctx, base) {
		if time.Now().After(deadline) {
			return "", errors.New("the relay is published but not answering yet. Wait a few minutes and press Set up again")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(cfPoll):
		}
	}
	j.step("Saved the relay on this PC")
	return base, nil
}

// subdomain finds the account's workers.dev name, picking one when the
// account has never used Workers.
func (c *cfClient) subdomain(ctx context.Context, account string) (string, error) {
	var got struct {
		Subdomain string `json:"subdomain"`
	}
	reply, err := c.call(ctx, http.MethodGet, "/accounts/"+account+"/workers/subdomain", "", nil, &got)
	if err == nil && cfSubRe.MatchString(got.Subdomain) {
		return got.Subdomain, nil
	}
	if err != nil && (reply == nil || !reply.has(10007)) {
		return "", err
	}
	for i := 0; i < 4; i++ {
		name := "ft-" + randomLower(10)
		body, _ := json.Marshal(map[string]string{"subdomain": name})
		reply, err := c.call(ctx, http.MethodPut, "/accounts/"+account+"/workers/subdomain", "application/json", bytes.NewReader(body), &got)
		if err == nil {
			if cfSubRe.MatchString(got.Subdomain) {
				return got.Subdomain, nil
			}
			return name, nil
		}
		if reply == nil || errors.Is(err, errCFDenied) {
			return "", err
		}
	}
	return "", errors.New("could not pick a workers.dev name. Open Workers in the Cloudflare dashboard once, then try again")
}

// cleanCFText keeps text from Cloudflare printable and short before it is
// shown in the desk.
func cleanCFText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func randomLower(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

type cfScriptInfo struct {
	ID           string `json:"id"`
	MigrationTag string `json:"migration_tag"`
}

func (c *cfClient) upload(ctx context.Context, account string) error {
	var scripts []cfScriptInfo
	if _, err := c.call(ctx, http.MethodGet, "/accounts/"+account+"/workers/scripts", "", nil, &scripts); err != nil {
		return err
	}
	tag := ""
	for _, s := range scripts {
		if s.ID == cfScript {
			tag = s.MigrationTag
		}
	}
	body, ctype, err := relayUpload(tag)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, http.MethodPut, "/accounts/"+account+"/workers/scripts/"+cfScript, ctype, bytes.NewReader(body), nil)
	return err
}

// relayUpload builds the form Cloudflare takes for a module Worker: the
// settings, worker.js as the main module, and the page files as text
// modules under the names worker.js imports them by.
func relayUpload(migrationTag string) ([]byte, string, error) {
	meta := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": cfCompat,
		"bindings":           []map[string]string{{"type": "durable_object_namespace", "name": "ROOMS", "class_name": "Room"}},
	}
	switch migrationTag {
	case cfMigration:
	case "":
		meta["migrations"] = map[string]any{"new_tag": cfMigration, "new_sqlite_classes": []string{"Room"}}
	default:
		return nil, "", errors.New("your account already has a different Worker called " + cfScript + ". Delete it in the Cloudflare dashboard, then try again")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	add := func(name, ctype string, data []byte) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name))
		h.Set("Content-Type", ctype)
		part, err := w.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = part.Write(data)
		return err
	}
	metaJSON, _ := json.Marshal(meta)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(metaJSON); err != nil {
		return nil, "", err
	}
	files := []struct{ name, ctype string }{
		{"worker.js", "application/javascript+module"},
		{"static/phone.html", "text/plain"},
		{"static/phone.js", "text/plain"},
		{"static/phone.css", "text/plain"},
	}
	for _, f := range files {
		data, err := relayFiles.ReadFile("relay/" + f.name)
		if err != nil {
			return nil, "", err
		}
		if err := add(f.name, f.ctype, data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func (s *server) deskRelaySetup(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, cfJob.view())
}

func (s *server) deskRelaySetupSet(w http.ResponseWriter, r *http.Request) {
	if !cfLimit.allow(clientIP(r)) {
		slowDown(w)
		return
	}
	var req struct {
		Action string `json:"action"`
		Token  string `json:"token"`
		Open   string `json:"open"`
	}
	if !readBody(w, r, &req) {
		return
	}
	var err error
	switch req.Action {
	case "start":
		err = cfJob.start(req.Token)
	case "stop":
		cfJob.stop()
	case "open":
		// Only these two pages, so the desk cannot be used to open anything else.
		switch req.Open {
		case "signup":
			if openLink(cfSignupPage) != nil {
				err = errors.New("could not open a browser. Go to dash.cloudflare.com and sign up there")
			}
		case "token":
			if openLink(cfTokenPage) != nil {
				err = errors.New("could not open a browser. On dash.cloudflare.com go to My Profile, API Tokens, and use the Edit Cloudflare Workers template")
			}
		default:
			err = errors.New("bad request")
		}
	default:
		err = errors.New("bad request")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "setup": cfJob.view()})
		return
	}
	writeJSON(w, http.StatusOK, cfJob.view())
}
