//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"
	"golang.org/x/sys/windows"
)

var (
	appView       webview2.WebView
	instanceMutex windows.Handle
)

func claimApp() bool {
	name, err := windows.UTF16PtrFromString(`Local\FastTransfer.SingleInstance`)
	if err != nil {
		return true
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")
	handle, _, callErr := proc.Call(0, 0, uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if handle == 0 {
		return true
	}
	instanceMutex = windows.Handle(handle)
	if callErr == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(instanceMutex)
		instanceMutex = 0
		return false
	}
	return true
}

func focusExisting() bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	find := user32.NewProc("FindWindowW")
	show := user32.NewProc("ShowWindow")
	fore := user32.NewProc("SetForegroundWindow")
	title, err := windows.UTF16PtrFromString("FastTransfer")
	if err != nil {
		return false
	}
	hwnd, _, _ := find.Call(0, uintptr(unsafe.Pointer(title)))
	runtime.KeepAlive(title)
	if hwnd == 0 {
		return false
	}
	show.Call(hwnd, 9) // SW_RESTORE
	fore.Call(hwnd)
	return true
}

func report(text string) {
	body, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	title, err := windows.UTF16PtrFromString("FastTransfer")
	if err != nil {
		return
	}
	_, _ = windows.MessageBox(0, body, title, windows.MB_OK|windows.MB_ICONINFORMATION)
}

func stopWindow() {
	if appView != nil {
		appView.Terminate()
	}
}

func runApp(pageURL string) {
	enableDPI()
	data := filepath.Join(os.Getenv("LOCALAPPDATA"), "FastTransfer", "webview")
	if os.Getenv("LOCALAPPDATA") == "" {
		data = filepath.Join(appDir, "webview")
	}
	_ = os.MkdirAll(data, 0o755)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  data,
		WindowOptions: webview2.WindowOptions{
			Title:  "FastTransfer",
			Width:  1120,
			Height: 760,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		openDefaultBrowser(pageURL)
		go func() {
			report("FastTransfer opened in your browser. Click OK to quit.")
			requestStop()
		}()
		<-stopCh
		return
	}
	appView = w
	hwnd := uintptr(w.Window())
	installCaption(hwnd)
	matchTitleBar(hwnd)
	defer w.Destroy()
	w.SetSize(900, 620, webview2.HintMin)
	_ = w.Bind("quitApp", func() error {
		w.Terminate()
		return nil
	})
	_ = w.Bind("windowMin", func() { showWin(hwnd, 6) }) // SW_MINIMIZE
	_ = w.Bind("windowMax", func() { toggleZoom(hwnd) })
	_ = w.Bind("windowClose", func() { postClose(hwnd) })
	_ = w.Bind("windowDrag", func() { dragWindow(hwnd) })
	_ = w.Bind("setCaptionBounds", func(maxB, minB, closeB, header [4]float64, controls [][4]float64) {
		setCaptionBounds(hwnd, maxB, minB, closeB, header, controls)
	})
	_ = w.Bind("setTitleTheme", func(dark bool) { applyTitleTheme(hwnd, dark) })
	_ = w.Bind("setCaptionHeld", func(held bool) { setCaptionHeld(held) })
	_ = w.Bind("copyImageFile", func(rel string) error { return copyImageFile(rel) })
	_ = w.Bind("clipboardRead", readClipboardText)
	_ = w.Bind("clipboardWrite", writeClipboardText)
	_ = w.Bind("pickSaveFolder", pickSaveFolder)
	_ = w.Bind("sendPickedFiles", sendPickedFiles)
	_ = w.Bind("listBluetoothPhones", listBluetoothPhones)
	_ = w.Bind("pickFilesForBluetooth", pickFilesForBluetooth)
	_ = w.Bind("startBluetoothSend", startBluetoothSend)
	_ = w.Bind("startBluetoothScan", startBluetoothScan)
	_ = w.Bind("stopBluetoothScan", stopBluetoothScan)
	w.Navigate(pageURL)
	w.Run()
}

func openDefaultBrowser(pageURL string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", pageURL).Start()
}

// Caption hit-testing is what makes Windows 11 draw the snap-layout flyout.
// HTMAXBUTTON has to come from the top-level WM_NCHITTEST. The WebView2
// renderer is another process, so its window is clipped out of the caption
// buttons and the host window answers the hit test itself.
const (
	wmNCCalcSize   = 0x0083
	wmNCHitTest    = 0x0084
	wmPaint        = 0x000F
	wmEraseBkgnd   = 0x0014
	wmShowWindow   = 0x0018
	wmTimer        = 0x0113
	wmSize         = 0x0005
	wmDpiChanged   = 0x02E0
	htCaption      = 2
	htMinButton    = 8
	htMaxButton    = 9
	htClose        = 20
	htTransparent  = ^uintptr(0) // -1
	gwlpWndProc    = uintptr(0xFFFFFFFFFFFFFFFC)
	gwlStyle       = uintptr(0xFFFFFFFFFFFFFFF0)
	wsCaptionBits  = uintptr(0x00CF0000 | 0x10000000) // WS_OVERLAPPEDWINDOW | WS_VISIBLE
	dwmwaBtnBounds = 5
	dwmwaCorner    = 33
	dwmwaBorder    = 34
	dwmwaCaption   = 35
	dwmwaText      = 36
	dwmRound       = 2
	smCxFrame      = 32
	smCyFrame      = 33
	smPaddedBorder = 92
)

type winRect struct{ l, t, r, b int32 }

type nccalcParams struct {
	rgrc  [3]winRect
	lppos uintptr
}

type margins struct{ left, right, top, bottom int32 }

var (
	chromeOnce    sync.Once
	chromeCb      uintptr
	enumCb        uintptr
	topHwnd       uintptr
	capHwnd       uintptr
	capCb         uintptr
	origProcs     = map[uintptr]uintptr{}
	captionMu     sync.Mutex
	maxBtn        winRect
	minBtn        winRect
	closeBtn      winRect
	headerBox     winRect
	controlBoxes  []winRect
	nativeBtn     winRect
	captionDark   bool
	captionHeld   bool
	lastClip      string
	lastCaptionAt time.Time
	enumKids      []uintptr
)

func enableDPI() {
	// Per-monitor v2 so the button rectangle matches the cursor in WM_NCHITTEST.
	windows.NewLazySystemDLL("user32.dll").NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3))
}

func installCaption(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	topHwnd = hwnd
	chromeOnce.Do(func() {
		chromeCb = syscall.NewCallback(chromeProc)
		enumCb = syscall.NewCallback(enumChildProc)
	})
	user32 := windows.NewLazySystemDLL("user32.dll")
	get := user32.NewProc("GetWindowLongPtrW")
	set := user32.NewProc("SetWindowLongPtrW")
	style, _, _ := get.Call(hwnd, gwlStyle)
	if style != 0 {
		style |= wsCaptionBits
		set.Call(hwnd, gwlStyle, style)
	}
	subclassWindow(hwnd)
	user32.NewProc("SetWindowPos").Call(hwnd, 0, 0, 0, 0, 0, 0x0027)
	roundCorners(hwnd)
	user32.NewProc("SetTimer").Call(hwnd, 1, 400, 0)
}

func subclassWindow(hwnd uintptr) {
	if hwnd == 0 || chromeCb == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	cur, _, _ := user32.NewProc("GetWindowLongPtrW").Call(hwnd, gwlpWndProc)
	if cur == chromeCb {
		return
	}
	prev, _, _ := user32.NewProc("SetWindowLongPtrW").Call(hwnd, gwlpWndProc, chromeCb)
	if prev == 0 || prev == chromeCb {
		return
	}
	captionMu.Lock()
	origProcs[hwnd] = prev
	captionMu.Unlock()
}

func sameProcess(hwnd uintptr) bool {
	var pid uint32
	windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid == uint32(os.Getpid())
}

func enumChildProc(hwnd, lparam uintptr) uintptr {
	if hwnd != 0 && hwnd != topHwnd && hwnd != capHwnd {
		enumKids = append(enumKids, hwnd)
		if sameProcess(hwnd) {
			subclassWindow(hwnd)
		}
	}
	return 1
}

func chromeProc(hwnd, msg, wp, lp uintptr) uintptr {
	top := hwnd == topHwnd
	if top && (msg == 0x00A0 || msg == 0x02A2) {
		invalidateButtons(hwnd)
	}
	if msg == wmNCHitTest {
		if top {
			if hit, ok := nativeSplit(lp); ok {
				return hit
			}
			if res, ok := dwmDefWindowProc(hwnd, msg, wp, lp); ok && captionHit(res) {
				return res
			}
			if hit, ok := borderHit(hwnd, lp); ok {
				return hit
			}
			if hit, ok := buttonHit(lp); ok {
				return hit
			}
			if _, ok := captionArea(lp); ok {
				return htCaption
			}
		} else if _, ok := nativeSplit(lp); ok {
			return htTransparent
		} else if _, ok := buttonHit(lp); ok {
			return htTransparent
		} else if _, ok := captionArea(lp); ok {
			return htTransparent
		}
	}
	if top && msg == wmNCCalcSize && wp != 0 {
		return calcSize(hwnd, wp, lp)
	}
	if top && captionMouse(msg) {
		if res, ok := dwmDefWindowProc(hwnd, msg, wp, lp); ok {
			return res
		}
	}
	result := callOrig(hwnd, msg, wp, lp)
	if top && (msg == wmSize || msg == wmTimer || msg == wmShowWindow || msg == wmDpiChanged) {
		refreshChrome(hwnd)
	}
	return result
}

func captionHit(hit uintptr) bool {
	return hit == htMinButton || hit == htMaxButton || hit == htClose
}

func captionMouse(msg uintptr) bool {
	switch msg {
	case 0x00A0, 0x00A1, 0x00A2, 0x00A3: // NCMOUSEMOVE, NCLBUTTONDOWN, NCLBUTTONUP, NCLBUTTONDBLCLK
		return true
	default:
		return false
	}
}

func callOrig(hwnd, msg, wp, lp uintptr) uintptr {
	captionMu.Lock()
	orig := origProcs[hwnd]
	captionMu.Unlock()
	user32 := windows.NewLazySystemDLL("user32.dll")
	if orig == 0 {
		r, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, msg, wp, lp)
		return r
	}
	r, _, _ := user32.NewProc("CallWindowProcW").Call(orig, hwnd, msg, wp, lp)
	return r
}

func screenPoint(lp uintptr) (int32, int32) {
	return int32(int16(uint16(lp & 0xFFFF))), int32(int16(uint16((lp >> 16) & 0xFFFF)))
}

func buttonHit(lp uintptr) (uintptr, bool) {
	x, y := screenPoint(lp)
	captionMu.Lock()
	maxR, minR, closeR := maxBtn, minBtn, closeBtn
	captionMu.Unlock()
	switch {
	case inside(screenOf(maxR), x, y):
		return htMaxButton, true
	case inside(screenOf(minR), x, y):
		return htMinButton, true
	case inside(screenOf(closeR), x, y):
		return htClose, true
	default:
		return 0, false
	}
}

func captionArea(lp uintptr) (uintptr, bool) {
	x, y := screenPoint(lp)
	captionMu.Lock()
	header := headerBox
	controls := append([]winRect(nil), controlBoxes...)
	captionMu.Unlock()
	header = screenOf(header)
	for i := range controls {
		controls[i] = screenOf(controls[i])
	}
	if inside(header, x, y) && !inAny(controls, x, y) {
		return htCaption, true
	}
	return 0, false
}

// Button rectangles are stored in top-level client pixels. Converting them to
// screen pixels only at the hit test keeps them attached to the window. Storing
// screen pixels and converting again after a move left a second caption behind.
func screenOf(r winRect) winRect {
	if topHwnd == 0 || r.r <= r.l || r.b <= r.t {
		return r
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	to := user32.NewProc("ClientToScreen")
	var tl, br struct{ x, y int32 }
	tl.x, tl.y = r.l, r.t
	br.x, br.y = r.r, r.b
	to.Call(topHwnd, uintptr(unsafe.Pointer(&tl)))
	to.Call(topHwnd, uintptr(unsafe.Pointer(&br)))
	return winRect{tl.x, tl.y, br.x, br.y}
}

func borderHit(hwnd, lp uintptr) (uintptr, bool) {
	x, y := screenPoint(lp)
	var rc winRect
	windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	const edge int32 = 6
	left := x < rc.l+edge
	right := x >= rc.r-edge
	top := y < rc.t+edge
	bottom := y >= rc.b-edge
	switch {
	case top && left:
		return 13, true
	case top && right:
		return 14, true
	case bottom && left:
		return 16, true
	case bottom && right:
		return 17, true
	case left:
		return 10, true
	case right:
		return 11, true
	case top:
		return 12, true
	case bottom:
		return 15, true
	default:
		return 0, false
	}
}

func inside(r winRect, x, y int32) bool {
	return r.r > r.l && r.b > r.t && x >= r.l && x < r.r && y >= r.t && y < r.b
}

func inAny(rs []winRect, x, y int32) bool {
	for _, r := range rs {
		if inside(r, x, y) {
			return true
		}
	}
	return false
}

func dwmDefWindowProc(hwnd, msg, wp, lp uintptr) (uintptr, bool) {
	var result uintptr
	r, _, _ := windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmDefWindowProc").Call(
		hwnd, msg, wp, lp, uintptr(unsafe.Pointer(&result)),
	)
	return result, r != 0
}

func setCaptionBounds(hwnd uintptr, maxB, minB, closeB, header [4]float64, controls [][4]float64) {
	ox, oy := viewOrigin(hwnd)
	maxR := clientBox(maxB, ox, oy)
	minR := clientBox(minB, ox, oy)
	closeR := clientBox(closeB, ox, oy)
	headerR := clientBox(header, ox, oy)
	boxes := make([]winRect, 0, len(controls))
	for _, box := range controls {
		boxes = append(boxes, clientBox(box, ox, oy))
	}
	captionMu.Lock()
	maxBtn, minBtn, closeBtn, headerBox, controlBoxes = maxR, minR, closeR, headerR, boxes
	captionMu.Unlock()
	if hwnd != 0 {
		placeCaption(hwnd)
	}
}

func clientBox(box [4]float64, ox, oy int32) winRect {
	return winRect{
		l: int32(box[0]) + ox,
		t: int32(box[1]) + oy,
		r: int32(box[2]) + ox,
		b: int32(box[3]) + oy,
	}
}

// viewOrigin is where the webview sits inside the top-level client area.
// Page coordinates start there, not at the window's client origin.
func viewOrigin(parent uintptr) (int32, int32) {
	if parent == 0 {
		return 0, 0
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	get := user32.NewProc("GetWindow")
	child, _, _ := get.Call(parent, 5) // GW_CHILD
	var best uintptr
	var bestArea int32
	for n := 0; child != 0 && n < 16; n++ {
		if child != capHwnd {
			var rc winRect
			user32.NewProc("GetWindowRect").Call(child, uintptr(unsafe.Pointer(&rc)))
			area := (rc.r - rc.l) * (rc.b - rc.t)
			if area > bestArea {
				bestArea = area
				best = child
			}
		}
		child, _, _ = get.Call(child, 2) // GW_HWNDNEXT
	}
	if best == 0 {
		return 0, 0
	}
	var rc winRect
	user32.NewProc("GetWindowRect").Call(best, uintptr(unsafe.Pointer(&rc)))
	var pt struct{ x, y int32 }
	pt.x, pt.y = rc.l, rc.t
	user32.NewProc("ScreenToClient").Call(parent, uintptr(unsafe.Pointer(&pt)))
	if pt.x < 0 {
		pt.x = 0
	}
	if pt.y < 0 {
		pt.y = 0
	}
	return pt.x, pt.y
}

func nativeSplit(lp uintptr) (uintptr, bool) {
	x, y := screenPoint(lp)
	captionMu.Lock()
	b := nativeBtn
	held := captionHeld
	captionMu.Unlock()
	if held {
		return 0, false
	}
	if !inside(b, x, y) {
		return 0, false
	}
	w := (b.r - b.l) / 3
	if w <= 0 {
		return 0, false
	}
	switch {
	case x < b.l+w:
		return htMinButton, true
	case x < b.l+2*w:
		return htMaxButton, true
	default:
		return htClose, true
	}
}

func calcSize(hwnd, wp, lp uintptr) uintptr {
	user32 := windows.NewLazySystemDLL("user32.dll")
	if wp == 0 {
		r, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, wmNCCalcSize, wp, lp)
		return r
	}
	params := (*nccalcParams)(unsafe.Pointer(lp))
	window := params.rgrc[0]
	user32.NewProc("DefWindowProcW").Call(hwnd, wmNCCalcSize, wp, lp)
	fx, fy := frameSize(hwnd)
	zoomed, _, _ := user32.NewProc("IsZoomed").Call(hwnd)
	params.rgrc[0].l = window.l + fx
	params.rgrc[0].r = window.r - fx
	params.rgrc[0].b = window.b - fy
	if zoomed != 0 {
		params.rgrc[0].t = window.t + fy
	} else {
		params.rgrc[0].t = window.t
	}
	return 0
}

func frameSize(hwnd uintptr) (int32, int32) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	metric := user32.NewProc("GetSystemMetricsForDpi")
	x, _, _ := metric.Call(smCxFrame, dpi)
	y, _, _ := metric.Call(smCyFrame, dpi)
	pad, _, _ := metric.Call(smPaddedBorder, dpi)
	if x+pad == 0 {
		return 8, 8
	}
	return int32(x + pad), int32(y + pad)
}

func roundCorners(hwnd uintptr) {
	pref := int32(dwmRound)
	dwmAttr(hwnd, dwmwaCorner, unsafe.Pointer(&pref), unsafe.Sizeof(pref))
	m := margins{top: 1}
	windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmExtendFrameIntoClientArea").Call(hwnd, uintptr(unsafe.Pointer(&m)))
}

func dwmAttr(hwnd, attr uintptr, value unsafe.Pointer, size uintptr) {
	windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute").Call(hwnd, attr, uintptr(value), size)
}

var refreshing bool

func refreshChrome(hwnd uintptr) {
	if refreshing || hwnd == 0 {
		return
	}
	refreshing = true
	defer func() { refreshing = false }()
	roundCorners(hwnd)
	placeCaption(hwnd)
	clipCaption(hwnd)
}

func setCaptionHeld(held bool) {
	captionMu.Lock()
	captionHeld = held
	captionMu.Unlock()
	if topHwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	if capHwnd != 0 {
		cmd := uintptr(5) // SW_SHOW
		if held {
			cmd = 0 // SW_HIDE
		}
		user32.NewProc("ShowWindow").Call(capHwnd, cmd)
	}
	lastClip = ""
	if held {
		clearChildRgns(topHwnd)
		lastClip = "off"
		return
	}
	refreshChrome(topHwnd)
}

func clearChildRgns(hwnd uintptr) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumKids = enumKids[:0]
	user32.NewProc("EnumChildWindows").Call(hwnd, enumCb, 0)
	kids := append([]uintptr(nil), enumKids...)
	for _, child := range kids {
		if child == capHwnd {
			continue
		}
		user32.NewProc("SetWindowRgn").Call(child, 0, 1)
	}
}

func clipCaption(hwnd uintptr) {
	captionMu.Lock()
	screen := nativeBtn
	held := captionHeld
	captionMu.Unlock()
	if held {
		if lastClip != "off" {
			clearChildRgns(hwnd)
			lastClip = "off"
		}
		if capHwnd != 0 {
			windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow").Call(capHwnd, 0)
		}
		return
	}
	if screen.r <= screen.l || screen.b <= screen.t || capHwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumKids = enumKids[:0]
	user32.NewProc("EnumChildWindows").Call(hwnd, enumCb, 0)
	kids := append([]uintptr(nil), enumKids...)
	key := clipSignature(screen, kids)
	if key == lastClip {
		return
	}
	lastClip = key
	for _, child := range kids {
		if child == capHwnd {
			continue
		}
		clipChild(child, screen)
	}
	if capHwnd != 0 {
		user32.NewProc("SetWindowPos").Call(capHwnd, 0, 0, 0, 0, 0, 0x0001|0x0002|0x0010|0x0040)
	}
}

func clipSignature(buttons winRect, kids []uintptr) string {
	sig := strconv.Itoa(int(buttons.l)) + ":" + strconv.Itoa(int(buttons.t)) + ":" + strconv.Itoa(int(buttons.r)) + ":" + strconv.Itoa(int(buttons.b))
	user32 := windows.NewLazySystemDLL("user32.dll")
	for _, child := range kids {
		var rc winRect
		user32.NewProc("GetWindowRect").Call(child, uintptr(unsafe.Pointer(&rc)))
		sig += "|" + strconv.FormatUint(uint64(child), 10) + ":" + strconv.Itoa(int(rc.l)) + ":" + strconv.Itoa(int(rc.t)) + ":" + strconv.Itoa(int(rc.r)) + ":" + strconv.Itoa(int(rc.b))
	}
	return sig
}

var capClassName *uint16

func placeCaption(parent uintptr) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	if capCb == 0 {
		capCb = syscall.NewCallback(capProc)
	}
	if capHwnd == 0 {
		if capClassName == nil {
			var err error
			capClassName, err = windows.UTF16PtrFromString("FastTransferCaption")
			if err != nil {
				return
			}
		}
		hinst, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
		wc := struct {
			cbSize   uint32
			style    uint32
			lpfn     uintptr
			clsExtra int32
			wndExtra int32
			inst     uintptr
			icon     uintptr
			cursor   uintptr
			bg       uintptr
			menu     *uint16
			name     *uint16
			iconSm   uintptr
		}{
			lpfn: capCb,
			inst: hinst,
			name: capClassName,
		}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc)))
		runtime.KeepAlive(capClassName)
		capHwnd, _, _ = user32.NewProc("CreateWindowExW").Call(
			0,
			uintptr(unsafe.Pointer(capClassName)),
			0,
			0x40000000, // WS_CHILD, shown once it sits on the header buttons
			0, 0, 0, 0,
			parent,
			0,
			hinst,
			0,
		)
		runtime.KeepAlive(capClassName)
	}
	if capHwnd == 0 {
		return
	}
	x, y, w, h := captionOrigin(parent)
	captionMu.Lock()
	held := captionHeld
	captionMu.Unlock()
	flags := uintptr(0x0010 | 0x0040) // SWP_NOACTIVATE | SWP_SHOWWINDOW
	if held {
		flags = 0x0010 | 0x0080 // SWP_NOACTIVATE | SWP_HIDEWINDOW
	}
	user32.NewProc("SetWindowPos").Call(capHwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), flags)
	var sr winRect
	user32.NewProc("GetWindowRect").Call(capHwnd, uintptr(unsafe.Pointer(&sr)))
	captionMu.Lock()
	nativeBtn = sr
	captionMu.Unlock()
	user32.NewProc("InvalidateRect").Call(capHwnd, 0, 1)
}

func captionOrigin(parent uintptr) (x, y, w, h int32) {
	captionMu.Lock()
	minR, closeR := minBtn, closeBtn
	captionMu.Unlock()
	// These are client coordinates of the header buttons, so they stay put when the window moves.
	if closeR.r > minR.l+60 && closeR.b > minR.t+16 {
		return minR.l, minR.t, closeR.r - minR.l, closeR.b - minR.t
	}
	_, oy := viewOrigin(parent)
	var cr winRect
	user32 := windows.NewLazySystemDLL("user32.dll")
	user32.NewProc("GetClientRect").Call(parent, uintptr(unsafe.Pointer(&cr)))
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(parent)
	if dpi == 0 {
		dpi = 96
	}
	bw := int32(dpi) * 46 / 96
	bh := int32(dpi) * 40 / 96
	if bw < 40 {
		bw = 46
	}
	if bh < 28 {
		bh = 32
	}
	return cr.r - 3*bw, oy, 3 * bw, bh
}

func actCaption(hit uintptr) {
	if hit != htMinButton && hit != htMaxButton && hit != htClose {
		return
	}
	if time.Since(lastCaptionAt) < 300*time.Millisecond {
		return
	}
	lastCaptionAt = time.Now()
	switch hit {
	case htMinButton:
		showWin(topHwnd, 6) // SW_MINIMIZE
	case htMaxButton:
		toggleZoom(topHwnd)
	case htClose:
		postClose(topHwnd)
	}
}

func capProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmNCHitTest:
		// Maximize stays HTMAXBUTTON so Windows can show snap layouts.
		// Minimize and close are handled here: a child window that returns
		// HTMINBUTTON/HTCLOSE never receives the click, so those are HTCLIENT
		// and acted on in WM_LBUTTONDOWN.
		if hit, ok := nativeSplit(lp); ok {
			if hit == htMaxButton {
				return htMaxButton
			}
			return 1 // HTCLIENT
		}
		return htTransparent
	case 0x00A0: // WM_NCMOUSEMOVE
		invalidateButtons(hwnd)
		return 0
	case 0x00A1, 0x00A2, 0x00A3: // NCLBUTTONDOWN, NCLBUTTONUP, NCLBUTTONDBLCLK
		actCaption(wp)
		return 0
	case 0x0201: // WM_LBUTTONDOWN
		var pt struct{ x, y int32 }
		windows.NewLazySystemDLL("user32.dll").NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&pt)))
		packed := uintptr(uint16(pt.x)) | uintptr(uint16(pt.y))<<16
		if hit, ok := nativeSplit(packed); ok {
			actCaption(hit)
		}
		return 0
	case wmEraseBkgnd:
		var rc winRect
		windows.NewLazySystemDLL("user32.dll").NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		color := uintptr(0x00F3F3F3)
		if captionDark {
			color = 0x002B2B2B
		}
		fillRect(wp, rc, color)
		return 1
	case wmPaint:
		drawCaptionButtons(hwnd)
		return 0
	default:
		r, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("DefWindowProcW").Call(hwnd, msg, wp, lp)
		return r
	}
}

func clipChild(child uintptr, screen winRect) bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	gdi := windows.NewLazySystemDLL("gdi32.dll")
	var cr winRect
	user32.NewProc("GetWindowRect").Call(child, uintptr(unsafe.Pointer(&cr)))
	w := cr.r - cr.l
	h := cr.b - cr.t
	if w <= 1 || h <= 1 {
		return false
	}
	il := max32(screen.l, cr.l)
	it := max32(screen.t, cr.t)
	ir := min32(screen.r, cr.r)
	ib := min32(screen.b, cr.b)
	if il >= ir || it >= ib {
		user32.NewProc("SetWindowRgn").Call(child, 0, 1)
		return false
	}
	full, _, _ := gdi.NewProc("CreateRectRgn").Call(0, 0, uintptr(w), uintptr(h))
	hole, _, _ := gdi.NewProc("CreateRectRgn").Call(uintptr(il-cr.l), uintptr(it-cr.t), uintptr(ir-cr.l), uintptr(ib-cr.b))
	if full == 0 || hole == 0 {
		if full != 0 {
			gdi.NewProc("DeleteObject").Call(full)
		}
		if hole != 0 {
			gdi.NewProc("DeleteObject").Call(hole)
		}
		return false
	}
	gdi.NewProc("CombineRgn").Call(full, full, hole, 4) // RGN_DIFF
	gdi.NewProc("DeleteObject").Call(hole)
	r, _, _ := user32.NewProc("SetWindowRgn").Call(child, full, 1)
	return r != 0
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func invalidateButtons(hwnd uintptr) {
	if capHwnd != 0 {
		windows.NewLazySystemDLL("user32.dll").NewProc("InvalidateRect").Call(capHwnd, 0, 1)
	}
}

func drawCaptionButtons(hwnd uintptr) {
	var ps [80]byte
	user32 := windows.NewLazySystemDLL("user32.dll")
	hdc, _, _ := user32.NewProc("BeginPaint").Call(hwnd, uintptr(unsafe.Pointer(&ps[0])))
	defer user32.NewProc("EndPaint").Call(hwnd, uintptr(unsafe.Pointer(&ps[0])))
	if hdc == 0 {
		return
	}
	var rc winRect
	user32.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	if rc.r <= rc.l || rc.b <= rc.t {
		return
	}
	bg := uintptr(0x00F3F3F3)
	ink := uintptr(0x001A1A1A)
	hover := uintptr(0x00E5E5E5)
	if captionDark {
		bg = 0x002B2B2B
		ink = 0x00F3F3F3
		hover = 0x00333333
	}
	fillRect(hdc, rc, bg)
	captionMu.Lock()
	screen := nativeBtn
	captionMu.Unlock()
	var cur struct{ x, y int32 }
	user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&cur)))
	third := (rc.r - rc.l) / 3
	hot := 0
	if inside(screen, cur.x, cur.y) && third > 0 {
		if cur.x < screen.l+((screen.r-screen.l)/3) {
			hot = 1
		} else if cur.x < screen.l+2*((screen.r-screen.l)/3) {
			hot = 2
		} else {
			hot = 3
		}
	}
	for i := int32(0); i < 3; i++ {
		cell := winRect{l: rc.l + third*i, t: rc.t, r: rc.l + third*(i+1), b: rc.b}
		if int(i+1) == hot {
			color := hover
			if hot == 3 {
				color = 0x001C2BC4
			}
			fillRect(hdc, cell, color)
		}
		glyph := ink
		if hot == 3 && int(i+1) == 3 {
			glyph = 0x00FFFFFF
		}
		cx := (cell.l + cell.r) / 2
		cy := (cell.t + cell.b) / 2
		s := (cell.b - cell.t) / 6
		if s < 5 {
			s = 5
		}
		switch i {
		case 0:
			line(hdc, cx-s, cy, cx+s, cy, glyph)
		case 1:
			zoomed, _, _ := user32.NewProc("IsZoomed").Call(topHwnd)
			if zoomed != 0 {
				strokeRect(hdc, cx-s+2, cy-s/2, cx+s/2, cy+s, glyph)
				strokeRect(hdc, cx-s/2, cy-s, cx+s, cy+s/2, glyph)
			} else {
				strokeRect(hdc, cx-s, cy-s, cx+s, cy+s, glyph)
			}
		default:
			line(hdc, cx-s, cy-s, cx+s, cy+s, glyph)
			line(hdc, cx+s, cy-s, cx-s, cy+s, glyph)
		}
	}
}

func fillRect(hdc uintptr, rc winRect, color uintptr) {
	gdi := windows.NewLazySystemDLL("gdi32.dll")
	brush, _, _ := gdi.NewProc("CreateSolidBrush").Call(color)
	if brush == 0 {
		return
	}
	windows.NewLazySystemDLL("user32.dll").NewProc("FillRect").Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	gdi.NewProc("DeleteObject").Call(brush)
}

func line(hdc uintptr, x1, y1, x2, y2 int32, color uintptr) {
	gdi := windows.NewLazySystemDLL("gdi32.dll")
	pen, _, _ := gdi.NewProc("CreatePen").Call(0, 2, color)
	if pen == 0 {
		return
	}
	old, _, _ := gdi.NewProc("SelectObject").Call(hdc, pen)
	gdi.NewProc("MoveToEx").Call(hdc, uintptr(x1), uintptr(y1), 0)
	gdi.NewProc("LineTo").Call(hdc, uintptr(x2), uintptr(y2))
	gdi.NewProc("SelectObject").Call(hdc, old)
	gdi.NewProc("DeleteObject").Call(pen)
}

func strokeRect(hdc uintptr, l, t, r, b int32, color uintptr) {
	line(hdc, l, t, r, t, color)
	line(hdc, r, t, r, b, color)
	line(hdc, r, b, l, b, color)
	line(hdc, l, b, l, t, color)
}

func showWin(hwnd uintptr, cmd uintptr) {
	if hwnd == 0 {
		return
	}
	windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow").Call(hwnd, cmd)
}

func toggleZoom(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	zoomed, _, _ := user32.NewProc("IsZoomed").Call(hwnd)
	cmd := uintptr(3) // SW_MAXIMIZE
	if zoomed != 0 {
		cmd = 9 // SW_RESTORE
	}
	user32.NewProc("ShowWindow").Call(hwnd, cmd)
}

func postClose(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	windows.NewLazySystemDLL("user32.dll").NewProc("PostMessageW").Call(hwnd, 0x0010, 0, 0)
}

func dragWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	user32.NewProc("ReleaseCapture").Call()
	var pt struct{ x, y int32 }
	user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&pt)))
	lp := uintptr(uint16(uint32(pt.x))) | uintptr(uint16(uint32(pt.y)))<<16
	user32.NewProc("SendMessageW").Call(hwnd, 0x00A1, htCaption, lp) // WM_NCLBUTTONDOWN
}

func applyTitleTheme(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	captionDark = dark
	value := int32(0)
	color := uint32(0x00F3F3F3)
	text := uint32(0x001A1A1A)
	if dark {
		value = 1
		color = 0x002B2B2B
		text = 0x00F3F3F3
	}
	dwmAttr(hwnd, 20, unsafe.Pointer(&value), unsafe.Sizeof(value))
	dwmAttr(hwnd, dwmwaCaption, unsafe.Pointer(&color), unsafe.Sizeof(color))
	dwmAttr(hwnd, dwmwaBorder, unsafe.Pointer(&color), unsafe.Sizeof(color))
	dwmAttr(hwnd, dwmwaText, unsafe.Pointer(&text), unsafe.Sizeof(text))
	roundCorners(hwnd)
	user32 := windows.NewLazySystemDLL("user32.dll")
	user32.NewProc("InvalidateRect").Call(hwnd, 0, 1)
	if capHwnd != 0 {
		user32.NewProc("InvalidateRect").Call(capHwnd, 0, 1)
	}
}

// matchTitleBar follows the Windows app theme so a dark window does not sit under a white caption.
func matchTitleBar(hwnd uintptr) {
	applyTitleTheme(hwnd, appsUseDark())
}

func appsUseDark() bool {
	advapi := windows.NewLazySystemDLL("advapi32.dll")
	open := advapi.NewProc("RegOpenKeyExW")
	query := advapi.NewProc("RegQueryValueExW")
	closeKey := advapi.NewProc("RegCloseKey")
	path, err := windows.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`)
	if err != nil {
		return false
	}
	name, err := windows.UTF16PtrFromString("AppsUseLightTheme")
	if err != nil {
		return false
	}
	var key uintptr
	r, _, _ := open.Call(uintptr(windows.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(path)), 0, 0x20019, uintptr(unsafe.Pointer(&key)))
	runtime.KeepAlive(path)
	if r != 0 || key == 0 {
		return false
	}
	defer closeKey.Call(key)
	var typ, val, n uint32 = 0, 1, 4
	r, _, _ = query.Call(key, uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&val)), uintptr(unsafe.Pointer(&n)))
	runtime.KeepAlive(name)
	return r == 0 && val == 0
}

func copyImageFile(rel string) error {
	full, _, name, err := regularInside(rel)
	if err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(name))
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	var img image.Image
	switch ext {
	case ".png":
		img, err = png.Decode(f)
	case ".jpg", ".jpeg":
		img, err = jpeg.Decode(f)
	case ".gif":
		img, err = gif.Decode(f)
	case ".bmp":
		img, err = bmp.Decode(f)
	case ".webp":
		img, err = webp.Decode(f)
	default:
		return errors.New("not an image")
	}
	if err != nil {
		return err
	}
	return putClipboardImage(img)
}

func putClipboardImage(img image.Image) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 || w > 16000 || h > 16000 {
		return errors.New("image size")
	}
	dib := dibBytes(img)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return err
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	pngName, _ := windows.UTF16PtrFromString("PNG")
	pngFmt, _, _ := user32.NewProc("RegisterClipboardFormatW").Call(uintptr(unsafe.Pointer(pngName)))
	runtime.KeepAlive(pngName)

	if !openClipboard() {
		return errors.New("clipboard busy")
	}
	defer user32.NewProc("CloseClipboard").Call()
	user32.NewProc("EmptyClipboard").Call()
	if err := clipboardSet(8, dib); err != nil {
		return err
	}
	if pngFmt != 0 {
		_ = clipboardSet(pngFmt, pngBuf.Bytes())
	}
	return nil
}

const cfUnicodeText = 13

func openClipboard() bool {
	open := windows.NewLazySystemDLL("user32.dll").NewProc("OpenClipboard")
	for i := 0; i < 12; i++ {
		if ok, _, _ := open.Call(topHwnd); ok != 0 {
			return true
		}
		time.Sleep(15 * time.Millisecond)
	}
	return false
}

// The page could use navigator.clipboard, but that needs focus. A clip that
// arrives from the phone while the window sits in the background still lands here.
func writeClipboardText(text string) error {
	if len(text) > maxLiveText {
		return errors.New("too long")
	}
	units, err := windows.UTF16FromString(strings.ReplaceAll(text, "\x00", ""))
	if err != nil {
		return err
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(&units[0])), len(units)*2)
	if !openClipboard() {
		return errors.New("clipboard busy")
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	defer user32.NewProc("CloseClipboard").Call()
	user32.NewProc("EmptyClipboard").Call()
	err = clipboardSet(cfUnicodeText, data)
	runtime.KeepAlive(units)
	return err
}

func readClipboardText() (string, error) {
	if !openClipboard() {
		return "", errors.New("clipboard busy")
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	defer user32.NewProc("CloseClipboard").Call()
	h, _, _ := user32.NewProc("GetClipboardData").Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	ptr, _, _ := kernel32.NewProc("GlobalLock").Call(h)
	if ptr == 0 {
		return "", nil
	}
	defer kernel32.NewProc("GlobalUnlock").Call(h)
	size, _, _ := kernel32.NewProc("GlobalSize").Call(h)
	n := int(size / 2)
	if n > maxLiveText {
		n = maxLiveText
	}
	units := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), n)
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
	}
	return windows.UTF16ToString(units), nil
}

func clipboardSet(format uintptr, data []byte) error {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	user32 := windows.NewLazySystemDLL("user32.dll")
	h, _, _ := kernel32.NewProc("GlobalAlloc").Call(0x0002, uintptr(len(data)))
	if h == 0 {
		return errors.New("alloc")
	}
	ptr, _, _ := kernel32.NewProc("GlobalLock").Call(h)
	if ptr == 0 {
		kernel32.NewProc("GlobalFree").Call(h)
		return errors.New("lock")
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data))
	copy(dst, data)
	kernel32.NewProc("GlobalUnlock").Call(h)
	r, _, _ := user32.NewProc("SetClipboardData").Call(format, h)
	if r == 0 {
		kernel32.NewProc("GlobalFree").Call(h)
		return errors.New("set")
	}
	return nil
}

func dibBytes(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	stride := w * 4
	buf := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(buf[0:], 40)
	binary.LittleEndian.PutUint32(buf[4:], uint32(w))
	binary.LittleEndian.PutUint32(buf[8:], uint32(h))
	binary.LittleEndian.PutUint16(buf[12:], 1)
	binary.LittleEndian.PutUint16(buf[14:], 32)
	binary.LittleEndian.PutUint32(buf[20:], uint32(stride*h))
	off := 40
	for y := h - 1; y >= 0; y-- {
		row := off + (h-1-y)*stride
		for x := 0; x < w; x++ {
			r, g, bv, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := row + x*4
			buf[i] = byte(bv >> 8)
			buf[i+1] = byte(g >> 8)
			buf[i+2] = byte(r >> 8)
			buf[i+3] = byte(a >> 8)
		}
	}
	return buf
}
