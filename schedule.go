package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A scheduled send is one file for one phone. On Windows the Task Scheduler
// folder \FastTransfer holds a task named send-{guid}. The task action is this
// program and that guid. The path, the device id, and the optional time live
// in a json file next to the exe. The sidebar reads the tasks, not a second queue.

const scheduleFolder = `\FastTransfer`

var (
	scheduleRoot    string
	scheduleExe     string
	scheduleMu      sync.Mutex
	scheduleIDRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	errScheduled    = errors.New("Could not schedule that send. It was not queued.")
	errScheduleRead = errors.New("Could not read Task Scheduler.")
	errWhen         = errors.New("That time is not valid. It was not queued.")
)

type scheduledSend struct {
	ID      string    `json:"id"`
	Device  string    `json:"device"`
	Label   string    `json:"label,omitempty"`
	Path    string    `json:"path"`
	Name    string    `json:"name,omitempty"`
	When    time.Time `json:"when,omitempty"`
	Created time.Time `json:"created"`
	Task    string    `json:"-"`
}

func schedulesDir() string {
	if scheduleRoot != "" {
		return scheduleRoot
	}
	if appDir == "" {
		return ""
	}
	return filepath.Join(appDir, "schedules")
}

func jobPath(id string) string {
	dir := schedulesDir()
	if dir == "" || !scheduleIDRe.MatchString(id) {
		return ""
	}
	return filepath.Join(dir, id+".json")
}

func validSchedule(job scheduledSend) bool {
	return scheduleIDRe.MatchString(job.ID) && deviceKeyRe.MatchString(job.Device) && filepath.IsAbs(job.Path)
}

func newGUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func taskName(id string) string {
	return "send-" + id
}

func writeJob(job scheduledSend) error {
	path := jobPath(job.ID)
	if path == "" {
		return errScheduled
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errScheduled
	}
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return errScheduled
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return errScheduled
	}
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errScheduled
	}
	return nil
}

func readJob(id string) (scheduledSend, bool) {
	path := jobPath(id)
	if path == "" {
		return scheduledSend{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return scheduledSend{}, false
	}
	var job scheduledSend
	if json.Unmarshal(raw, &job) != nil || job.ID != id || !validSchedule(job) {
		return scheduledSend{}, false
	}
	return job, true
}

func removeJob(id string) {
	if path := jobPath(id); path != "" {
		_ = os.Remove(path)
		_ = os.Remove(path + ".tmp")
	}
}

func hasSchedulePayloads() bool {
	dir := schedulesDir()
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if scheduleIDRe.MatchString(id) {
			found = true
			continue
		}
		if legacyScheduleID.MatchString(id) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return found
}

var legacyScheduleID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func deliverArg(args []string) (string, bool) {
	for _, arg := range args {
		id := strings.ToLower(strings.Trim(strings.TrimSpace(arg), `"`))
		if scheduleIDRe.MatchString(id) {
			return id, true
		}
	}
	return "", false
}

func tryDeliverRemote(id string) bool {
	body := `{"id":"` + id + `"}`
	client := &http.Client{Timeout: 15 * time.Second}
	for i := 0; i < 3; i++ {
		req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/api/schedule/run", strings.NewReader(body))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func parseWhen(raw string) (time.Time, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, errWhen
}

func due(job scheduledSend) bool {
	return job.When.IsZero() || !job.When.After(time.Now())
}

func isRegularFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func sameDevice(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return canonicalDeviceKey(a) == canonicalDeviceKey(b)
}

type phoneTarget struct {
	key, name, handle string
	online            bool
}

func (h *deviceHub) scheduleTargets(handles []string, all bool) []phoneTarget {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []phoneTarget
	seen := map[*device]bool{}
	add := func(d *device) {
		if d == nil || seen[d] || !deviceKeyRe.MatchString(d.key) {
			return
		}
		seen[d] = true
		out = append(out, phoneTarget{key: d.key, name: d.name, handle: d.handle, online: d.online})
	}
	if all {
		for _, d := range h.byKey {
			add(d)
		}
		return out
	}
	for _, handle := range handles {
		add(h.byHandleLocked(handle))
	}
	return out
}

func schedulePath(file *sendFile) (string, string, bool) {
	if file == nil {
		return "", "", false
	}
	full := ""
	name := strings.TrimSpace(file.Name)
	if file.Outside && strings.TrimSpace(file.Abs) != "" {
		abs, err := filepath.Abs(file.Abs)
		if err != nil {
			return "", "", false
		}
		full = abs
	} else if file.Rel != "" {
		abs, info, base, err := regularInside(file.Rel)
		if err != nil || info.IsDir() {
			return "", "", false
		}
		full = abs
		if name == "" {
			name = base
		}
	}
	if !isRegularFile(full) {
		return "", "", false
	}
	if name == "" {
		name = filepath.Base(full)
	}
	return full, name, true
}

func canonicalKey(key string) string {
	if canon := canonicalDeviceKey(key); deviceKeyRe.MatchString(canon) {
		return canon
	}
	return key
}

// dispatchSend gives files to phones that are connected now. An away phone, or
// a time that has not arrived, becomes a scheduled task instead of an offer.
func dispatchSend(files []*sendFile, handles []string, all bool, when time.Time, timed bool) (int, int, error) {
	if len(files) == 0 {
		return 0, 0, errors.New("could not send those files")
	}
	targets := hub.scheduleTargets(handles, all)
	if len(targets) == 0 {
		return 0, 0, errNoTargets
	}
	future := timed && when.After(time.Now())
	sent := 0
	if !future {
		var online []string
		for _, phone := range targets {
			if phone.online {
				online = append(online, phone.handle)
			}
		}
		if len(online) > 0 {
			n, err := hub.deliver(files, online, false)
			if err != nil {
				return 0, 0, err
			}
			sent = n
		}
	}
	scheduled := 0
	for _, phone := range targets {
		if !future && phone.online {
			continue
		}
		for _, file := range files {
			path, name, ok := schedulePath(file)
			if !ok {
				continue
			}
			id, err := newGUID()
			if err != nil {
				return sent, scheduled, errScheduled
			}
			job := scheduledSend{
				ID:      id,
				Device:  canonicalKey(phone.key),
				Label:   phone.name,
				Path:    path,
				Name:    name,
				Created: time.Now(),
			}
			if future {
				job.When = when
			}
			if err := registerSchedule(job); err != nil {
				return sent, scheduled, err
			}
			scheduled++
		}
	}
	if sent == 0 && scheduled == 0 {
		return 0, 0, errors.New("could not send those files")
	}
	return sent, scheduled, nil
}

func writeSendResult(w http.ResponseWriter, files, devices, scheduled int, err error) {
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": files, "devices": devices, "scheduled": scheduled})
}

func scheduledPhone(id string) (handle, name string, online bool) {
	if !deviceKeyRe.MatchString(id) {
		return "", "", false
	}
	find := func(key string) *device {
		if d := hub.byKey[key]; d != nil {
			return d
		}
		return nil
	}
	hub.mu.Lock()
	d := find(id)
	hub.mu.Unlock()
	if d == nil {
		canon := canonicalDeviceKey(id)
		if canon != id {
			hub.mu.Lock()
			d = find(canon)
			hub.mu.Unlock()
		}
	}
	if d == nil {
		return "", "", false
	}
	return d.handle, d.name, d.online
}

func offerJob(job scheduledSend) bool {
	if !due(job) || !isRegularFile(job.Path) {
		return false
	}
	handle, _, online := scheduledPhone(job.Device)
	if !online || handle == "" {
		return false
	}
	info, err := os.Stat(job.Path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	name := job.Name
	if name == "" {
		name = filepath.Base(job.Path)
	}
	file := &sendFile{Name: name, Size: info.Size(), Abs: job.Path, Outside: true}
	_, err = hub.deliver([]*sendFile{file}, []string{handle}, false)
	if err != nil {
		return false
	}
	who := strings.TrimSpace(job.Label)
	if who == "" {
		_, who, _ = scheduledPhone(job.Device)
	}
	receiveOffered(who, job.Path, name)
	return true
}

// deliverPending offers every due file for this phone. A missing file stays
// scheduled so the sidebar can show it. A future time stays scheduled too.
func deliverPending(deviceID string) int {
	if !deviceKeyRe.MatchString(deviceID) || !hasSchedulePayloads() {
		return 0
	}
	scheduleMu.Lock()
	defer scheduleMu.Unlock()
	jobs, err := listSchedules()
	if err != nil {
		return 0
	}
	offered := 0
	for _, job := range jobs {
		if !sameDevice(job.Device, deviceID) || !due(job) || !isRegularFile(job.Path) {
			continue
		}
		if !offerJob(job) {
			continue
		}
		if cancelSchedule(job.ID) == nil {
			offered++
		}
	}
	return offered
}

func deliverDueOnline() {
	if !hasSchedulePayloads() {
		return
	}
	scheduleMu.Lock()
	defer scheduleMu.Unlock()
	jobs, err := listSchedules()
	if err != nil {
		return
	}
	for _, job := range jobs {
		if !due(job) || !isRegularFile(job.Path) || !offerJob(job) {
			continue
		}
		_ = cancelSchedule(job.ID)
	}
}

func deliverScheduled(id string) error {
	if !scheduleIDRe.MatchString(id) {
		return errScheduled
	}
	scheduleMu.Lock()
	defer scheduleMu.Unlock()
	jobs, err := listSchedules()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.ID != id {
			continue
		}
		if !due(job) || !isRegularFile(job.Path) || !offerJob(job) {
			return nil
		}
		return cancelSchedule(job.ID)
	}
	return nil
}

func (s *server) scheduleList(w http.ResponseWriter, _ *http.Request) {
	jobs, err := listSchedules()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	dropMissingJobs(jobs)
	rows := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		_, live, _ := scheduledPhone(job.Device)
		label := live
		if label == "" {
			label = job.Label
		}
		if label == "" {
			label = "Phone"
		}
		file := job.Name
		if file == "" && job.Path != "" {
			file = filepath.Base(job.Path)
		}
		when := ""
		if !job.When.IsZero() {
			when = job.When.Format(time.RFC3339)
		}
		rows = append(rows, map[string]any{
			"id":      job.ID,
			"label":   label,
			"file":    file,
			"when":    when,
			"missing": !isRegularFile(job.Path),
			"created": job.Created.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": rows})
}

func (s *server) scheduleCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readBody(w, r, &req) {
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.ID))
	if !scheduleIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that scheduled send is gone"})
		return
	}
	if err := cancelSchedule(id); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) scheduleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readBody(w, r, &req) {
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.ID))
	if err := deliverScheduled(id); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
