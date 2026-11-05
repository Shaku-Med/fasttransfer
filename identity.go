package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Phones are remembered in devices.json next to the exe. A Wi-Fi phone is
// keyed by the id it stores in the browser. A Bluetooth phone is keyed by
// its radio address. The file is local data, not part of the app.

var (
	devicesFile string
	bookMu      sync.Mutex
	book        = deviceBook{WiFi: map[string]savedDevice{}, Bluetooth: map[string]savedDevice{}}

	iphoneOS     = regexp.MustCompile(`(?i)iPhone; CPU iPhone OS (\d+(?:_\d+)*)`)
	ipadOS       = regexp.MustCompile(`(?i)iPad; CPU (?:iPhone )?OS (\d+(?:_\d+)*)`)
	androidUARe  = regexp.MustCompile(`(?i)Android (\d+(?:\.\d+)*)(?:; ([^);]+))?`)
	macOSUA      = regexp.MustCompile(`(?i)Mac OS X (\d+[._]\d+(?:[._]\d+)*)`)
	winNTUA      = regexp.MustCompile(`(?i)Windows NT (\d+\.\d+)`)
	tizenUA      = regexp.MustCompile(`(?i)Tizen[/ ](\d+(?:\.\d+)*)`)
	webOSUA      = regexp.MustCompile(`(?i)webOS[/ .]?(\d+(?:\.\d+)*)`)
	appleTVUA    = regexp.MustCompile(`(?i)Apple ?TV; CPU (?:iPhone )?OS (\d+(?:_\d+)*)`)
	playStation  = regexp.MustCompile(`(?i)PlayStation(?: (\d+))?`)
	fireTVUA     = regexp.MustCompile(`\bAFT[A-Z0-9]{1,8}\b`)
	chromeOSUA   = regexp.MustCompile(`\bCrOS\b`)
	inventedName = regexp.MustCompile(`^(?i)(iphone|ipad|android|phone) [0-9a-f]{2,8}$`)
	macFind      = regexp.MustCompile(`(?i)[0-9a-f]{2}(?::[0-9a-f]{2}){5}`)
)

type savedDevice struct {
	Handle   string    `json:"handle,omitempty"`
	Name     string    `json:"name,omitempty"`
	Friendly string    `json:"friendly,omitempty"`
	Model    string    `json:"model,omitempty"`
	OS       string    `json:"os,omitempty"`
	Version  string    `json:"version,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Label    string    `json:"label"`
	Paired   bool      `json:"paired,omitempty"`
	ID       string    `json:"id,omitempty"`
	Seen     time.Time `json:"seen,omitempty"`
}

type deviceBook struct {
	WiFi      map[string]savedDevice `json:"wifi"`
	Alias     map[string]string      `json:"alias,omitempty"`
	Bluetooth map[string]savedDevice `json:"bluetooth"`
}

type phoneFacts struct {
	Given string
	Model string
	OS    string
	Kind  string
	Label string
}

func useDeviceFile(path string) {
	bookMu.Lock()
	defer bookMu.Unlock()
	devicesFile = path
	book = deviceBook{WiFi: map[string]savedDevice{}, Bluetooth: map[string]savedDevice{}}
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var loaded deviceBook
	if json.Unmarshal(raw, &loaded) != nil {
		return
	}
	if loaded.WiFi != nil {
		book.WiFi = loaded.WiFi
	}
	if loaded.Alias != nil {
		book.Alias = loaded.Alias
	}
	if loaded.Bluetooth != nil {
		book.Bluetooth = loaded.Bluetooth
	}
	if collapseSavedTwinsLocked() {
		saveBookLocked()
	}
}

func canonicalDeviceKey(key string) string {
	if key == "" {
		return ""
	}
	bookMu.Lock()
	defer bookMu.Unlock()
	seen := map[string]bool{}
	for book.Alias[key] != "" && !seen[key] {
		seen[key] = true
		next := book.Alias[key]
		if next == key || !deviceKeyRe.MatchString(next) {
			break
		}
		key = next
	}
	return key
}

func savedWiFi(key string) (savedDevice, bool) {
	bookMu.Lock()
	defer bookMu.Unlock()
	rec, ok := book.WiFi[key]
	return rec, ok
}

func persistPhoneMerge(keepKey string, dropped []string) {
	if keepKey == "" || len(dropped) == 0 {
		return
	}
	hub.mu.Lock()
	d := hub.byKey[keepKey]
	hub.mu.Unlock()
	retargetWiFi(d, dropped)
}

func retargetWiFi(keep *device, from []string) {
	if keep == nil || !deviceKeyRe.MatchString(keep.key) {
		return
	}
	rec := savedDevice{
		Handle: keep.handle,
		Name:   keep.given,
		Model:  keep.model,
		OS:     keep.os,
		Kind:   keep.kind,
		Label:  keep.name,
		Seen:   keep.seen,
	}
	bookMu.Lock()
	defer bookMu.Unlock()
	if book.WiFi == nil {
		book.WiFi = map[string]savedDevice{}
	}
	if book.Alias == nil {
		book.Alias = map[string]string{}
	}
	book.WiFi[keep.key] = rec
	for _, key := range from {
		if key == "" || key == keep.key || !deviceKeyRe.MatchString(key) {
			continue
		}
		delete(book.WiFi, key)
		book.Alias[key] = keep.key
	}
	trimSaved(book.WiFi, 500)
	saveBookLocked()
}

// collapseSavedTwinsLocked keeps one record when the file has two ids for the
// same handset seen within a minute of each other. A later id points at the
// earlier row. Two phones that were not seen that close stay separate.
func collapseSavedTwinsLocked() bool {
	if len(book.WiFi) < 2 {
		return false
	}
	type item struct {
		key string
		rec savedDevice
	}
	items := make([]item, 0, len(book.WiFi))
	for key, rec := range book.WiFi {
		items = append(items, item{key, rec})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].rec.Seen.Before(items[j].rec.Seen)
	})
	if book.Alias == nil {
		book.Alias = map[string]string{}
	}
	changed := false
	for i := 1; i < len(items); i++ {
		later := items[i]
		if _, gone := book.WiFi[later.key]; !gone {
			continue
		}
		var canon string
		var canonRec savedDevice
		for j := 0; j < i; j++ {
			prev := items[j]
			rec, ok := book.WiFi[prev.key]
			if !ok || !sameSavedPhone(rec, later.rec) {
				continue
			}
			if later.rec.Seen.Sub(rec.Seen) > time.Minute || rec.Seen.Sub(later.rec.Seen) > time.Minute {
				continue
			}
			canon = prev.key
			for book.Alias[canon] != "" && book.WiFi[book.Alias[canon]].Handle != "" {
				canon = book.Alias[canon]
			}
			if kept, ok := book.WiFi[canon]; ok {
				canonRec = kept
			} else {
				canonRec = rec
				canon = prev.key
			}
			break
		}
		if canon == "" || canon == later.key {
			continue
		}
		if canonRec.Name == "" && later.rec.Name != "" {
			canonRec.Name = later.rec.Name
		}
		if later.rec.Seen.After(canonRec.Seen) {
			canonRec.Seen = later.rec.Seen
			if later.rec.Label != "" {
				canonRec.Label = later.rec.Label
			}
			if later.rec.Model != "" {
				canonRec.Model = later.rec.Model
			}
			if later.rec.OS != "" {
				canonRec.OS = later.rec.OS
			}
		}
		book.WiFi[canon] = canonRec
		delete(book.WiFi, later.key)
		book.Alias[later.key] = canon
		changed = true
	}
	return changed
}

func sameSavedPhone(a, b savedDevice) bool {
	if a.Model == "" || b.Model == "" || a.OS == "" || b.OS == "" {
		return false
	}
	if !strings.EqualFold(a.Model, b.Model) || !strings.EqualFold(a.OS, b.OS) {
		return false
	}
	if a.Name != "" && b.Name != "" && !strings.EqualFold(a.Name, b.Name) {
		return false
	}
	return true
}

func rememberWiFi(key string, d *device) {
	if d == nil || !deviceKeyRe.MatchString(key) {
		return
	}
	rec := savedDevice{
		Handle: d.handle,
		Name:   d.given,
		Model:  d.model,
		OS:     d.os,
		Kind:   d.kind,
		Label:  d.name,
		Seen:   d.seen,
	}
	bookMu.Lock()
	defer bookMu.Unlock()
	if book.WiFi == nil {
		book.WiFi = map[string]savedDevice{}
	}
	book.WiFi[key] = rec
	trimSaved(book.WiFi, 500)
	saveBookLocked()
}

func rememberBluetooth(address, id, name, friendly, version, label string, paired bool) {
	address = canonicalMAC(address)
	if address == "" {
		return
	}
	bookMu.Lock()
	defer bookMu.Unlock()
	if book.Bluetooth == nil {
		book.Bluetooth = map[string]savedDevice{}
	}
	prev := book.Bluetooth[address]
	if id == "" {
		id = prev.ID
	}
	if name == "" {
		name = prev.Name
	}
	if friendly == "" {
		friendly = prev.Friendly
	}
	if version == "" {
		version = prev.Version
	}
	if label == "" {
		label = bluetoothLabel(name, friendly, version)
	}
	book.Bluetooth[address] = savedDevice{
		Name:     name,
		Friendly: friendly,
		Version:  version,
		Label:    label,
		Paired:   paired,
		ID:       id,
		Seen:     time.Now(),
	}
	trimSaved(book.Bluetooth, 200)
	saveBookLocked()
}

func listSavedBluetooth() map[string]any {
	bookMu.Lock()
	defer bookMu.Unlock()
	devices := make([]map[string]any, 0, len(book.Bluetooth))
	for address, rec := range book.Bluetooth {
		if rec.ID == "" {
			continue
		}
		label := rec.Label
		if label == "" {
			label = bluetoothLabel(rec.Name, rec.Friendly, rec.Version)
		}
		devices = append(devices, map[string]any{
			"id":       rec.ID,
			"name":     rec.Name,
			"friendly": rec.Friendly,
			"version":  rec.Version,
			"label":    label,
			"address":  address,
			"paired":   rec.Paired,
		})
	}
	return map[string]any{"devices": devices}
}

func noteBluetoothEvent(m map[string]any) {
	if ev, _ := m["event"].(string); ev != "device" {
		return
	}
	name, _ := m["name"].(string)
	friendly, _ := m["friendly"].(string)
	version, _ := m["version"].(string)
	address, _ := m["address"].(string)
	id, _ := m["id"].(string)
	paired, _ := m["paired"].(bool)
	address = canonicalMAC(address)
	if address == "" {
		address = macFromID(id)
	}
	if address != "" {
		m["address"] = address
	}
	label := bluetoothLabel(name, friendly, version)
	m["label"] = label
	rememberBluetooth(address, id, name, friendly, version, label, paired)
}

func bluetoothLabel(name, friendly, version string) string {
	name = strings.TrimSpace(name)
	friendly = strings.TrimSpace(friendly)
	version = strings.TrimSpace(version)
	head := name
	if head == "" {
		head = friendly
		friendly = ""
	}
	if friendly != "" && !strings.EqualFold(friendly, head) {
		head += " · " + friendly
	}
	if version != "" && !strings.Contains(strings.ToLower(head), strings.ToLower(version)) {
		if head == "" {
			return version
		}
		head += " · " + version
	}
	if head == "" {
		return "Bluetooth"
	}
	return head
}

func saveBookLocked() {
	if devicesFile == "" {
		return
	}
	raw, err := json.MarshalIndent(book, "", "  ")
	if err != nil {
		return
	}
	tmp := devicesFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Remove(devicesFile)
	if err := os.Rename(tmp, devicesFile); err != nil {
		_ = os.Remove(tmp)
	}
}

func trimSaved(m map[string]savedDevice, max int) {
	for len(m) > max {
		var oldKey string
		var oldTime time.Time
		first := true
		for key, rec := range m {
			if first || rec.Seen.Before(oldTime) {
				first = false
				oldKey = key
				oldTime = rec.Seen
			}
		}
		delete(m, oldKey)
	}
}

func (h *deviceHub) restoreSaved() {
	bookMu.Lock()
	wifi := make(map[string]savedDevice, len(book.WiFi))
	for key, rec := range book.WiFi {
		wifi[key] = rec
	}
	bookMu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	for key, rec := range wifi {
		if !deviceKeyRe.MatchString(key) || h.byKey[key] != nil {
			continue
		}
		handle := rec.Handle
		if !handleRe.MatchString(handle) || h.byHandle[handle] != nil {
			continue
		}
		label := rec.Label
		if label == "" {
			label = composeLabel(rec.Name, rec.Model, rec.OS)
		}
		if label == "" {
			continue
		}
		kind := rec.Kind
		if !kinds[kind] {
			kind = "other"
		}
		seen := rec.Seen
		if seen.IsZero() {
			seen = time.Now().Add(-time.Hour)
		}
		d := &device{
			key:    key,
			handle: handle,
			name:   label,
			given:  rec.Name,
			model:  rec.Model,
			os:     rec.OS,
			kind:   kind,
			seen:   seen,
			wake:   make(chan struct{}),
		}
		h.byKey[key] = d
		h.byHandle[handle] = d
	}
}

func chosenName(raw string) string {
	name := strings.TrimSpace(raw)
	var b strings.Builder
	n := 0
	for _, r := range name {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
		n++
		if n >= 24 {
			break
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" || inventedName.MatchString(name) {
		return ""
	}
	return name
}

func describePhone(ua, hintPlatform, hintVersion, hintModel, bodyPlatform, bodyVersion, bodyModel, given string) phoneFacts {
	platform := firstText(bodyPlatform, hintPlatform)
	versionHint := firstText(bodyVersion, hintVersion)
	modelHint := firstText(bodyModel, hintModel)

	kind, version, model := uaKind(ua)
	plat := strings.ToLower(platform)
	switch {
	case strings.Contains(plat, "windows") || kind == "windows":
		kind = "windows"
		if model == "" {
			model = "Windows PC"
		}
	case strings.Contains(plat, "mac") || kind == "mac":
		kind = "mac"
		if model == "" {
			model = "Mac"
		}
	case strings.Contains(plat, "chrome os") || strings.Contains(plat, "chromeos") || kind == "chromeos":
		kind = "chromeos"
		model = "Chromebook"
	case strings.Contains(plat, "linux") && kind == "":
		kind = "linux"
		model = "Linux PC"
	case strings.Contains(plat, "android") && kind != "tizen" && kind != "webos" && kind != "chromecast":
		kind = "android"
	case strings.Contains(plat, "ipad") || strings.Contains(strings.ToLower(modelHint), "ipad"):
		kind = "ipad"
	case kind == "" && (plat == "ios" || strings.Contains(plat, "iphone")):
		kind = "iphone"
	}
	osName := ""
	if kind == "windows" {
		if rel, ok := windowsRelease(versionHint); ok {
			osName = rel
		} else {
			osName = osLabel(kind, version)
		}
	} else {
		// ChromeOS reports a build number here, not the version people know.
		if v := shortVersion(versionHint); v != "" && kind != "chromecast" && kind != "chromeos" {
			version = v
		}
		osName = osLabel(kind, version)
	}
	if m := usefulModel(modelHint); m != "" && kind != "windows" && kind != "mac" && kind != "linux" && kind != "tizen" && kind != "webos" && kind != "chromecast" {
		model = m
		low := strings.ToLower(m)
		if strings.Contains(low, "ipad") {
			kind = "ipad"
			osName = osLabel(kind, version)
		} else if strings.EqualFold(m, "iphone") {
			kind = "iphone"
			osName = osLabel(kind, version)
		}
	}
	switch kind {
	case "iphone":
		if model == "" {
			model = "iPhone"
		}
	case "ipad":
		if model == "" {
			model = "iPad"
		}
	case "windows":
		if model == "" {
			model = "Windows PC"
		}
	case "mac":
		if model == "" {
			model = "Mac"
		}
	case "linux":
		if model == "" {
			model = "Linux PC"
		}
	}
	storedKind := kind
	switch kind {
	case "iphone", "ipad", "android":
	default:
		storedKind = "other"
	}
	given = chosenName(given)
	return phoneFacts{
		Given: given,
		Model: model,
		OS:    osName,
		Kind:  storedKind,
		Label: composeLabel(given, model, osName),
	}
}

func uaKind(ua string) (kind, version, model string) {
	low := strings.ToLower(ua)
	if m := iphoneOS.FindStringSubmatch(ua); m != nil {
		return "iphone", strings.ReplaceAll(m[1], "_", "."), "iPhone"
	}
	if m := ipadOS.FindStringSubmatch(ua); m != nil {
		return "ipad", strings.ReplaceAll(m[1], "_", "."), "iPad"
	}
	if strings.Contains(ua, "iPhone") {
		return "iphone", "", "iPhone"
	}
	if strings.Contains(ua, "iPad") {
		return "ipad", "", "iPad"
	}
	if m := appleTVUA.FindStringSubmatch(ua); m != nil {
		return "tvos", strings.ReplaceAll(m[1], "_", "."), "Apple TV"
	}
	if strings.Contains(low, "apple tv") || strings.Contains(low, "appletv") {
		return "tvos", "", "Apple TV"
	}
	if m := playStation.FindStringSubmatch(ua); m != nil {
		name := "PlayStation"
		if len(m) > 1 && m[1] != "" {
			name = "PlayStation " + m[1]
		}
		return "console", "", name
	}
	if strings.Contains(low, "xbox") {
		name := "Xbox"
		switch {
		case strings.Contains(low, "xbox series x"):
			name = "Xbox Series X"
		case strings.Contains(low, "xbox series s"):
			name = "Xbox Series S"
		case strings.Contains(low, "xbox one"):
			name = "Xbox One"
		}
		return "console", "", name
	}
	if strings.Contains(low, "crkey") {
		return "chromecast", "", "Chromecast"
	}
	if strings.Contains(low, "tizen") {
		ver := ""
		if m := tizenUA.FindStringSubmatch(ua); m != nil {
			ver = m[1]
		}
		return "tizen", ver, "Samsung TV"
	}
	if strings.Contains(low, "web0s") || strings.Contains(low, "webos") || strings.Contains(low, "netcast") {
		ver := ""
		if m := webOSUA.FindStringSubmatch(ua); m != nil {
			ver = m[1]
		}
		return "webos", ver, "LG TV"
	}
	// Fire TV sticks name their model as AFTxxx. Plain Silk is also on Fire
	// tablets, so it is not enough on its own.
	if fireTVUA.MatchString(ua) || strings.Contains(low, "fire tv") {
		ver := ""
		if m := androidUARe.FindStringSubmatch(ua); m != nil {
			ver = m[1]
		}
		return "android", ver, "Fire TV"
	}
	if strings.Contains(low, "android tv") || strings.Contains(low, "bravia") || (strings.Contains(low, "smart-tv") && strings.Contains(low, "android") && !strings.Contains(low, "tizen")) {
		ver := ""
		if m := androidUARe.FindStringSubmatch(ua); m != nil {
			ver = m[1]
		}
		return "android", ver, "Android TV"
	}
	if m := androidUARe.FindStringSubmatch(ua); m != nil {
		model = ""
		if len(m) > 2 {
			model = usefulModel(m[2])
		}
		if model == "" && !strings.Contains(ua, "Mobile") {
			model = "Android tablet"
		}
		return "android", m[1], model
	}
	if m := winNTUA.FindStringSubmatch(ua); m != nil {
		return "windows", ntRelease(m[1]), "Windows PC"
	}
	if strings.Contains(low, "windows") {
		return "windows", "", "Windows PC"
	}
	if m := macOSUA.FindStringSubmatch(ua); m != nil {
		v := strings.ReplaceAll(m[1], "_", ".")
		// Safari and Chrome froze this at 10.15.7 on every newer Mac, so it
		// says nothing about the real version. Only a client hint does.
		if strings.HasPrefix(v, "10.15") {
			v = ""
		}
		return "mac", v, "Mac"
	}
	if strings.Contains(low, "macintosh") {
		return "mac", "", "Mac"
	}
	if chromeOSUA.MatchString(ua) {
		return "chromeos", "", "Chromebook"
	}
	if strings.Contains(low, "linux") {
		return "linux", "", "Linux PC"
	}
	return "", "", ""
}

func ntRelease(v string) string {
	switch v {
	case "10.0":
		return "10"
	case "6.3":
		return "8.1"
	case "6.2":
		return "8"
	case "6.1":
		return "7"
	default:
		return ""
	}
}

func windowsRelease(hint string) (string, bool) {
	major := leadingInt(shortVersion(hint))
	if major >= 13 {
		return "Windows 11", true
	}
	if major > 0 {
		return "Windows 10", true
	}
	return "", false
}

func leadingInt(v string) int {
	n := 0
	seen := false
	for _, r := range v {
		if r < '0' || r > '9' {
			break
		}
		seen = true
		n = n*10 + int(r-'0')
	}
	if !seen {
		return 0
	}
	return n
}

func genericDevice(model string) bool {
	low := strings.ToLower(strings.TrimSpace(model))
	switch low {
	case "windows pc", "mac", "linux pc", "chromebook", "samsung tv", "lg tv", "android tv", "fire tv", "chromecast", "apple tv", "xbox", "playstation", "android tablet":
		return true
	default:
		return strings.HasPrefix(low, "xbox ") || strings.HasPrefix(low, "playstation ")
	}
}

func usefulModel(model string) string {
	model = strings.Trim(strings.TrimSpace(model), `"`)
	if model == "" || strings.EqualFold(model, "K") || strings.EqualFold(model, "Mobile") {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(model), "build/") {
		return ""
	}
	return model
}

func osLabel(kind, version string) string {
	version = shortVersion(version)
	switch kind {
	case "iphone":
		if version == "" {
			return "iOS"
		}
		return "iOS " + version
	case "ipad":
		if version == "" {
			return "iPadOS"
		}
		return "iPadOS " + version
	case "android":
		if version == "" {
			return "Android"
		}
		return "Android " + version
	case "windows":
		switch version {
		case "11":
			return "Windows 11"
		case "10":
			return "Windows 10"
		case "8.1":
			return "Windows 8.1"
		case "8":
			return "Windows 8"
		case "7":
			return "Windows 7"
		case "":
			return "Windows"
		default:
			if strings.HasPrefix(version, "Windows") {
				return version
			}
			return "Windows " + version
		}
	case "mac":
		if version == "" {
			return "macOS"
		}
		return "macOS " + version
	case "tizen":
		if version == "" {
			return "Tizen"
		}
		return "Tizen " + version
	case "webos":
		if version == "" {
			return "webOS"
		}
		return "webOS " + version
	case "tvos":
		if version == "" {
			return "tvOS"
		}
		return "tvOS " + version
	case "chromeos":
		return "ChromeOS"
	default:
		return ""
	}
}

func shortVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, `"`)
	v = strings.ReplaceAll(v, "_", ".")
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ".")
	clean := parts[:0]
	for _, part := range parts {
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(part); err != nil {
			return strings.Trim(v, ".")
		}
		clean = append(clean, part)
	}
	for len(clean) > 1 && clean[len(clean)-1] == "0" {
		clean = clean[:len(clean)-1]
	}
	out := strings.Join(clean, ".")
	if out == "0" {
		return ""
	}
	return out
}

func composeLabel(given, model, osName string) string {
	given = strings.TrimSpace(given)
	model = strings.TrimSpace(model)
	osName = strings.TrimSpace(osName)
	if given != "" {
		if osName != "" && !strings.Contains(strings.ToLower(given), strings.ToLower(osName)) {
			return given + " · " + osName
		}
		return given
	}
	if model != "" && osName != "" {
		if strings.EqualFold(model, osName) {
			return osName
		}
		if strings.EqualFold(model, "Android") && strings.HasPrefix(strings.ToLower(osName), "android") {
			return osName
		}
		return model + " · " + osName
	}
	if osName != "" {
		return osName
	}
	return model
}

func firstText(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

func headerHint(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
	}
	return strings.Trim(v, `"`)
}

func canonicalMAC(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if macFind.MatchString(raw) {
		found := macFind.FindString(raw)
		if strings.EqualFold(found, strings.ReplaceAll(raw, "-", ":")) || strings.EqualFold(found, raw) {
			return strings.ToUpper(found)
		}
	}
	hexOnly := strings.ReplaceAll(strings.ReplaceAll(raw, ":", ""), "-", "")
	if len(hexOnly) == 12 && macHex(hexOnly) {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(strings.ToUpper(hexOnly[i : i+2]))
		}
		return b.String()
	}
	return ""
}

func macFromID(id string) string {
	found := macFind.FindAllString(id, -1)
	if len(found) == 0 {
		return ""
	}
	return strings.ToUpper(found[len(found)-1])
}

func macHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func applyPhoneLabel(d *device, given string) string {
	d.given = given
	label := composeLabel(given, d.model, d.os)
	if label == "" {
		label = "This device"
	}
	d.name = label
	return label
}
