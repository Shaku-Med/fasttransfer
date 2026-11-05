package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var inboxIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

const inboxMax = 100

// A received row is one file that landed on this PC, or a scheduled file
// that was just offered to a device. The list is the same from every page.
type receivedItem struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Rel    string `json:"rel,omitempty"`
	Site   string `json:"site,omitempty"`
	Toast  string `json:"toast"`
	Time   int64  `json:"time"`
	Seen   bool   `json:"seen"`
}

var (
	inboxMu    sync.Mutex
	inboxFile  string
	inboxItems []receivedItem
)

func useInboxFile(path string) {
	inboxMu.Lock()
	defer inboxMu.Unlock()
	inboxFile = path
	inboxItems = nil
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var loaded struct {
		Items []receivedItem `json:"items"`
	}
	if json.Unmarshal(raw, &loaded) != nil {
		return
	}
	for _, item := range loaded.Items {
		if item.ID == "" || item.Name == "" {
			continue
		}
		inboxItems = append(inboxItems, item)
		if len(inboxItems) >= inboxMax {
			break
		}
	}
}

func saveInboxLocked() {
	if inboxFile == "" {
		return
	}
	raw, err := json.MarshalIndent(struct {
		Items []receivedItem `json:"items"`
	}{Items: inboxItems}, "", "  ")
	if err != nil {
		return
	}
	tmp := inboxFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Remove(inboxFile)
	if err := os.Rename(tmp, inboxFile); err != nil {
		_ = os.Remove(tmp)
	}
}

func inboxSnapshot() ([]receivedItem, int) {
	inboxMu.Lock()
	defer inboxMu.Unlock()
	out := append([]receivedItem(nil), inboxItems...)
	if out == nil {
		out = []receivedItem{}
	}
	unseen := 0
	for _, item := range out {
		if !item.Seen {
			unseen++
		}
	}
	return out, unseen
}

func noteReceived(item receivedItem) receivedItem {
	item.Kind = strings.TrimSpace(item.Kind)
	switch item.Kind {
	case "upload", "social", "schedule":
	default:
		return receivedItem{}
	}
	item.Name = oneLine(item.Name, 180)
	item.Source = oneLine(item.Source, 120)
	item.Folder = oneLine(item.Folder, 200)
	item.Site = oneLine(item.Site, 40)
	item.Rel = cleanRel(item.Rel)
	if item.Name == "" || item.Source == "" {
		return receivedItem{}
	}
	item.ID = newInboxID()
	item.Time = time.Now().UnixMilli()
	item.Seen = false
	item.Toast = receivedToast(item)
	inboxMu.Lock()
	inboxItems = append([]receivedItem{item}, inboxItems...)
	if len(inboxItems) > inboxMax {
		inboxItems = inboxItems[:inboxMax]
	}
	saveInboxLocked()
	inboxMu.Unlock()
	hub.emit(feedEvent{
		Kind:   "inbox",
		Handle: item.ID,
		Name:   item.Source,
		Device: item.Kind,
		Text:   item.Toast,
		Dest:   item.Folder,
		Path:   item.Rel,
	})
	return item
}

func receivedToast(item receivedItem) string {
	switch item.Kind {
	case "social":
		from := item.Site
		if from == "" {
			from = "Social media"
		}
		return "Saved from " + from + " · " + item.Name
	case "schedule":
		return "Offered to " + item.Source + " · " + item.Name
	default:
		return "Saved from " + item.Source + " · " + item.Name
	}
}

func receiveUpload(source, dest, name string) receivedItem {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "A device"
	}
	if !destinations[dest] {
		return receivedItem{}
	}
	return noteReceived(receivedItem{
		Kind:   "upload",
		Source: source,
		Name:   name,
		Folder: folderLabel(dest),
		Rel:    dest + "/" + name,
	})
}

func receiveSocial(rel, device string) receivedItem {
	rel = cleanRel(rel)
	name := filepath.Base(rel)
	folder := filepath.ToSlash(filepath.Dir(rel))
	if folder == "." {
		folder = ""
	}
	source := "Social media"
	if who := deviceName(device); who != "" {
		source = who
	}
	return noteReceived(receivedItem{
		Kind:   "social",
		Source: source,
		Name:   name,
		Folder: folder,
		Rel:    rel,
		Site:   siteLabel(filepath.Base(folder)),
	})
}

func receiveOffered(source, full, name string) receivedItem {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "A device"
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(full)
	}
	rel := relFromRoot(full)
	folder := ""
	if rel != "" {
		folder = displayFolder(rel)
	} else if full != "" {
		folder = filepath.Base(filepath.Dir(full))
	}
	return noteReceived(receivedItem{
		Kind:   "schedule",
		Source: source,
		Name:   name,
		Folder: folder,
		Rel:    rel,
	})
}

func displayFolder(rel string) string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "" {
		return ""
	}
	if strings.Contains(dir, "/") {
		return dir
	}
	return folderLabel(dir)
}

func folderLabel(dest string) string {
	switch dest {
	case "iphone":
		return "iPhone"
	case "ipad":
		return "iPad"
	case "android":
		return "Android"
	case "other":
		return "Other"
	case "files":
		return "Files"
	default:
		if dest == "" {
			return ""
		}
		return dest
	}
}

func siteLabel(host string) string {
	switch strings.ToLower(host) {
	case "instagram.com":
		return "Instagram"
	case "youtube.com":
		return "YouTube"
	case "tiktok.com":
		return "TikTok"
	case "facebook.com":
		return "Facebook"
	case "x.com":
		return "X"
	default:
		host = strings.TrimSpace(host)
		if host == "" || host == "." {
			return ""
		}
		base := strings.Split(host, ".")[0]
		if base == "" {
			return host
		}
		return strings.ToUpper(base[:1]) + base[1:]
	}
}

func relFromRoot(full string) string {
	root, err := filepath.Abs(saveRoot())
	if err != nil || full == "" {
		return ""
	}
	abs, err := filepath.Abs(full)
	if err != nil || !sameOrInside(root, abs) {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return ""
	}
	return cleanRel(filepath.ToSlash(rel))
}

func cleanRel(rel string) string {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	rel = strings.Trim(rel, "/")
	if rel == "" || strings.Contains(rel, "..") || strings.ContainsAny(rel, "\\:\x00") {
		return ""
	}
	return rel
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if max > 0 && len(s) > max {
		s = s[:max]
	}
	return s
}

func newInboxID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(buf[:])
}

func markInboxSeen() {
	inboxMu.Lock()
	defer inboxMu.Unlock()
	changed := false
	for i := range inboxItems {
		if !inboxItems[i].Seen {
			inboxItems[i].Seen = true
			changed = true
		}
	}
	if changed {
		saveInboxLocked()
	}
}

func dismissInbox(id string) bool {
	if !inboxIDRe.MatchString(id) {
		return false
	}
	inboxMu.Lock()
	defer inboxMu.Unlock()
	next := inboxItems[:0]
	found := false
	for _, item := range inboxItems {
		if item.ID == id {
			found = true
			continue
		}
		next = append(next, item)
	}
	if !found {
		return false
	}
	inboxItems = next
	saveInboxLocked()
	return true
}

func clearInbox() {
	inboxMu.Lock()
	defer inboxMu.Unlock()
	inboxItems = nil
	saveInboxLocked()
}

func (s *server) inboxList(w http.ResponseWriter, _ *http.Request) {
	items, unseen := inboxSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unseen": unseen})
}

func (s *server) inboxSeen(w http.ResponseWriter, _ *http.Request) {
	markInboxSeen()
	items, unseen := inboxSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unseen": unseen})
}

func (s *server) inboxDismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readBody(w, r, &req) {
		return
	}
	dismissInbox(req.ID)
	items, unseen := inboxSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unseen": unseen})
}

func (s *server) inboxClear(w http.ResponseWriter, _ *http.Request) {
	clearInbox()
	writeJSON(w, http.StatusOK, map[string]any{"items": []receivedItem{}, "unseen": 0})
}
