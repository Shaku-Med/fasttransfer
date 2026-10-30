package main

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

const (
	sampleIPhoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1"
	sampleAndroidUA = "Mozilla/5.0 (Linux; Android 15; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"
)

func TestPhoneLabels(t *testing.T) {
	if got := describePhone(sampleIPhoneUA, "", "", "", "", "", "", "").Label; got != "iPhone · iOS 18.6" {
		t.Fatalf("iphone label %q", got)
	}
	if got := describePhone(sampleAndroidUA, "", "", "", "", "", "", "").Label; got != "Pixel 8 · Android 15" {
		t.Fatalf("android label %q", got)
	}
	if got := describePhone("", "Android", "15.0.0", "Pixel", "", "", "", "").Label; got != "Pixel · Android 15" {
		t.Fatalf("hint label %q", got)
	}
	reduced := "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"
	if got := describePhone(reduced, "Android", "15.0.0", "Pixel", "", "", "", "").Label; got != "Pixel · Android 15" {
		t.Fatalf("reduced ua label %q", got)
	}
	if got := describePhone(sampleIPhoneUA, "", "", "", "", "", "", "Kitchen").Label; got != "Kitchen · iOS 18.6" {
		t.Fatalf("named iphone label %q", got)
	}
	if got := describePhone("", "", "", "", "", "", "", "Pixel 8").Label; got != "Pixel 8" {
		t.Fatalf("name without a version %q", got)
	}
	if got := describePhone(sampleIPhoneUA, "", "", "", "", "", "", "iPhone AB").Label; got != "iPhone · iOS 18.6" {
		t.Fatalf("invented name stuck %q", got)
	}
}

func TestDesktopTVAndMacLabels(t *testing.T) {
	win := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	if got := describePhone(win, "", "", "", "", "", "", "").Label; got != "Windows PC · Windows 10" {
		t.Fatalf("windows without a hint %q", got)
	}
	if got := describePhone(win, "Windows", "15.0.0", "", "", "", "", "").Label; got != "Windows PC · Windows 11" {
		t.Fatalf("windows 11 hint %q", got)
	}
	if got := describePhone(win, "Windows", "10.0.0", "", "", "", "", "Office PC").Label; got != "Office PC · Windows 10" {
		t.Fatalf("named windows 10 %q", got)
	}
	if got := describePhone(win, "Windows", "15.0.0", "", "", "", "", "Office PC").Label; got != "Office PC · Windows 11" {
		t.Fatalf("named windows 11 %q", got)
	}
	tv := "Mozilla/5.0 (SMART-TV; LINUX; Tizen 6.5) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/4.0 Chrome/76.0.3809.146 TV Safari/537.36"
	if got := describePhone(tv, "", "", "", "", "", "", "").Label; got != "Samsung TV · Tizen 6.5" {
		t.Fatalf("tizen %q", got)
	}
	webos := "Mozilla/5.0 (Web0S; Linux/SmartTV) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.79 Safari/537.36 WebAppManager"
	if got := describePhone(webos, "", "", "", "", "", "", "").Label; got != "LG TV · webOS" {
		t.Fatalf("webos %q", got)
	}
	mac := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	if got := describePhone(mac, "macOS", "15.0.0", "", "", "", "", "").Label; got != "Mac · macOS 15" {
		t.Fatalf("mac hint %q", got)
	}
	if got := describePhone(mac, "", "", "", "", "", "", "").Label; got != "Mac · macOS 10.15.7" {
		t.Fatalf("mac ua %q", got)
	}
}

func TestDesktopReconnectIsOneRow(t *testing.T) {
	mux := testServer(t).routes()
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	key := deviceKey(4)
	first := postHello(t, mux, key, "", ua)
	if first.name != "Windows PC · Windows 10" {
		t.Fatalf("label %q", first.name)
	}
	markAway(t, mux, first.session)
	other := postHello(t, mux, deviceKey(5), "", ua)
	if other.device == first.device {
		t.Fatal("two windows pcs collapsed into one row")
	}
	second := postHello(t, mux, key, "Office PC", ua)
	if second.name != "Office PC · Windows 10" || second.device != first.device {
		t.Fatalf("reconnect %+v", second)
	}
	if book := readDeviceBook(t); len(book.WiFi) != 2 {
		t.Fatalf("rows %d", len(book.WiFi))
	}
}

func TestSamePhoneIsOneRecord(t *testing.T) {
	mux := testServer(t).routes()
	key := deviceKey(9)
	first := postHello(t, mux, key, "", sampleIPhoneUA)
	second := postHello(t, mux, key, "", sampleIPhoneUA)
	if first.session == second.session {
		t.Fatal("a reconnect should get a fresh session")
	}
	if first.name != "iPhone · iOS 18.6" || second.name != first.name {
		t.Fatalf("label %q then %q", first.name, second.name)
	}
	book := readDeviceBook(t)
	if len(book.WiFi) != 1 {
		t.Fatalf("two posts stored %d phones", len(book.WiFi))
	}
	rec, ok := book.WiFi[key]
	if !ok || rec.Label != "iPhone · iOS 18.6" || rec.Handle == "" {
		t.Fatalf("record %+v", rec)
	}
	if got := handleOf(t, mux, "iPhone · iOS 18.6"); got != rec.Handle {
		t.Fatalf("row %s, file %s", got, rec.Handle)
	}

	hub = newHub()
	useDeviceFile(devicesFile)
	again := postHello(t, mux, key, "", sampleIPhoneUA)
	if again.name != "iPhone · iOS 18.6" {
		t.Fatalf("after restart label %q", again.name)
	}
	book = readDeviceBook(t)
	if len(book.WiFi) != 1 || book.WiFi[key].Handle != rec.Handle {
		t.Fatalf("restart made a new phone: %+v", book.WiFi)
	}
	if n := len(devices(t, mux)); n != 1 {
		t.Fatalf("restart showed %d phones", n)
	}

	named := postHello(t, mux, key, "Kitchen", sampleIPhoneUA)
	if named.name != "Kitchen · iOS 18.6" {
		t.Fatalf("custom name %q", named.name)
	}
	kept := postHello(t, mux, key, "", sampleIPhoneUA)
	if kept.name != "Kitchen · iOS 18.6" {
		t.Fatalf("custom name did not survive %q", kept.name)
	}
	book = readDeviceBook(t)
	if len(book.WiFi) != 1 || book.WiFi[key].Handle != rec.Handle || book.WiFi[key].Name != "Kitchen" {
		t.Fatalf("named record %+v", book.WiFi[key])
	}
}

func TestAndroidRecord(t *testing.T) {
	mux := testServer(t).routes()
	key := deviceKey(11)
	got := postHello(t, mux, key, "", sampleAndroidUA)
	if got.name != "Pixel 8 · Android 15" {
		t.Fatalf("android label %q", got.name)
	}
	other := postHello(t, mux, deviceKey(12), "", sampleAndroidUA)
	if other.name != got.name {
		t.Fatalf("second phone %q", other.name)
	}
	book := readDeviceBook(t)
	if len(book.WiFi) != 2 {
		t.Fatalf("two phones stored %d records", len(book.WiFi))
	}
	if book.WiFi[key].Handle == book.WiFi[deviceKey(12)].Handle {
		t.Fatal("two phones shared one row")
	}
}

func TestReconnectSameIDIsOnePhone(t *testing.T) {
	mux := testServer(t).routes()
	key := deviceKey(21)
	first := postHello(t, mux, key, "", sampleIPhoneUA)
	markAway(t, mux, first.session)
	second := postHello(t, mux, key, "", sampleIPhoneUA)
	if second.device != first.device {
		t.Fatalf("reconnect changed id %s -> %s", first.device, second.device)
	}
	list := devices(t, mux)
	if len(list) != 1 {
		t.Fatalf("same id left %d phones", len(list))
	}
	if !list[0].Online || list[0].Name != "iPhone · iOS 18.6" {
		t.Fatalf("row %+v", list[0])
	}
	if book := readDeviceBook(t); len(book.WiFi) != 1 {
		t.Fatalf("file has %d phones", len(book.WiFi))
	}
}

func TestNoIDSameHandsetCollapses(t *testing.T) {
	mux := testServer(t).routes()
	first := postHello(t, mux, "", "", sampleIPhoneUA)
	markAway(t, mux, first.session)
	time.Sleep(2 * time.Second)
	second := postHello(t, mux, "", "", sampleIPhoneUA)
	if second.device == "" || second.device != first.device {
		t.Fatalf("ids %s then %s", first.device, second.device)
	}
	list := devices(t, mux)
	if len(list) != 1 || !list[0].Online {
		t.Fatalf("no-id reconnect left %+v", list)
	}
}

func TestDifferentIDsSameHandsetCollapse(t *testing.T) {
	mux := testServer(t).routes()
	saveFile(t, "files/a.txt", "hello")
	firstKey := deviceKey(31)
	secondKey := deviceKey(32)
	first := postHello(t, mux, firstKey, "", sampleIPhoneUA)
	handle := handleOf(t, mux, "iPhone · iOS 18.6")
	serve(mux, deskReq("POST", "/api/send", `{"devices":["`+handle+`"],"paths":["files/a.txt"]}`))
	markAway(t, mux, first.session)
	second := postHello(t, mux, secondKey, "", sampleIPhoneUA)
	list := devices(t, mux)
	if len(list) != 1 || !list[0].Online || list[0].Handle != handle {
		t.Fatalf("two ids stayed separate: %+v", list)
	}
	if second.device != first.device {
		t.Fatalf("phone was not pointed at the original id (%s vs %s)", second.device, first.device)
	}
	got := mail(t, mux, second.session)
	if len(got.Files) != 1 {
		t.Fatalf("the waiting file was not on the one phone: %+v", got)
	}
	book := readDeviceBook(t)
	if len(book.WiFi) != 1 {
		t.Fatalf("file kept %d phones", len(book.WiFi))
	}
}

func TestTwoConnectedPhonesStayDistinct(t *testing.T) {
	mux := testServer(t).routes()
	postHello(t, mux, deviceKey(41), "", sampleIPhoneUA)
	postHello(t, mux, deviceKey(42), "", sampleIPhoneUA)
	if n := len(devices(t, mux)); n != 2 {
		t.Fatalf("two connected phones became %d", n)
	}
}

func TestBluetoothLabel(t *testing.T) {
	if got := bluetoothLabel("JBL TUNE510BT", "", ""); got != "JBL TUNE510BT" {
		t.Fatal(got)
	}
	if got := bluetoothLabel("JBL TUNE510BT", "Living room", ""); got != "JBL TUNE510BT · Living room" {
		t.Fatal(got)
	}
	if got := bluetoothLabel("Pixel", "Pixel", "1.2.3"); got != "Pixel · 1.2.3" {
		t.Fatal(got)
	}
	if got := bluetoothLabel("Headphones", "Headphones", ""); got != "Headphones" {
		t.Fatal(got)
	}
}

func markAway(t *testing.T, mux http.Handler, session string) {
	t.Helper()
	w := serve(mux, phoneReq("POST", "/api/phone/bye", `{"session":"`+session+`","gone":true}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("bye got %d: %s", w.Code, w.Body.String())
	}
}

type helloReply struct {
	session string
	name    string
	device  string
}

func postHello(t *testing.T, mux http.Handler, key, name, ua string) helloReply {
	t.Helper()
	body, err := json.Marshal(map[string]string{"device": key, "name": name, "kind": "other"})
	if err != nil {
		t.Fatal(err)
	}
	r := phoneReq("POST", "/api/phone/hello", string(body))
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	w := serve(mux, r)
	if w.Code != http.StatusOK {
		t.Fatalf("hello got %d: %s", w.Code, w.Body.String())
	}
	data := decode[map[string]string](t, w)
	return helloReply{session: data["session"], name: data["name"], device: data["device"]}
}

func readDeviceBook(t *testing.T) deviceBook {
	t.Helper()
	raw, err := os.ReadFile(devicesFile)
	if err != nil {
		t.Fatal(err)
	}
	var loaded deviceBook
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	return loaded
}
