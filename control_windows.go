package main

import (
	"errors"
	"image"
	"math"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const controlSupported = true

var (
	ctlUser32       = windows.NewLazySystemDLL("user32.dll")
	ctlGdi32        = windows.NewLazySystemDLL("gdi32.dll")
	procSendInput   = ctlUser32.NewProc("SendInput")
	procMapVK       = ctlUser32.NewProc("MapVirtualKeyW")
	procMetrics     = ctlUser32.NewProc("GetSystemMetrics")
	procGetDC       = ctlUser32.NewProc("GetDC")
	procReleaseDC   = ctlUser32.NewProc("ReleaseDC")
	procCursorInfo  = ctlUser32.NewProc("GetCursorInfo")
	procIconInfo    = ctlUser32.NewProc("GetIconInfo")
	procDrawIconEx  = ctlUser32.NewProc("DrawIconEx")
	procLockWS      = ctlUser32.NewProc("LockWorkStation")
	procFlash       = ctlUser32.NewProc("FlashWindowEx")
	procIsIconic    = ctlUser32.NewProc("IsIconic")
	procForeground  = ctlUser32.NewProc("SetForegroundWindow")
	procMemDC       = ctlGdi32.NewProc("CreateCompatibleDC")
	procDeleteDC    = ctlGdi32.NewProc("DeleteDC")
	procDIBSection  = ctlGdi32.NewProc("CreateDIBSection")
	procSelect      = ctlGdi32.NewProc("SelectObject")
	procDeleteObj   = ctlGdi32.NewProc("DeleteObject")
	procStretchMode = ctlGdi32.NewProc("SetStretchBltMode")
	procBrushOrg    = ctlGdi32.NewProc("SetBrushOrgEx")
	procStretchBlt  = ctlGdi32.NewProc("StretchBlt")
	procGdiFlush    = ctlGdi32.NewProc("GdiFlush")
	procSuspend     = windows.NewLazySystemDLL("powrprof.dll").NewProc("SetSuspendState")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000
	mouseVirtual    = 0x4000
	mouseAbsolute   = 0x8000

	keyExtended = 0x0001
	keyUp       = 0x0002
	keyUnicode  = 0x0004
)

// The C INPUT union is as big as MOUSEINPUT, which is eight bytes longer
// than KEYBDINPUT on both 32 and 64 bit, so the keyboard form pads by eight.
type mouseInput struct {
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type keyInput struct {
	Vk, Scan  uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type mouseRecord struct {
	Type uint32
	Mi   mouseInput
}

type keyRecord struct {
	Type uint32
	Ki   keyInput
	_    [8]byte
}

var extendedKeys = map[uint16]bool{
	0x2E: true, 0x2D: true, 0x24: true, 0x23: true, 0x21: true, 0x22: true,
	0x25: true, 0x26: true, 0x27: true, 0x28: true, 0x2C: true, 0x5D: true, 0x5B: true,
	0xB3: true, 0xB0: true, 0xB1: true, 0xB2: true, 0xAD: true, 0xAE: true, 0xAF: true,
}

var buttonFlags = map[string][2]uint32{
	"l": {mouseLeftDown, mouseLeftUp},
	"r": {mouseRightDown, mouseRightUp},
	"m": {mouseMiddleDown, mouseMiddleUp},
}

func sendMouse(recs []mouseRecord) {
	if len(recs) == 0 {
		return
	}
	procSendInput.Call(uintptr(len(recs)), uintptr(unsafe.Pointer(&recs[0])), unsafe.Sizeof(recs[0]))
}

func sendKeys(recs []keyRecord) {
	if len(recs) == 0 {
		return
	}
	procSendInput.Call(uintptr(len(recs)), uintptr(unsafe.Pointer(&recs[0])), unsafe.Sizeof(recs[0]))
}

func vkRecord(vk uint16, up bool) keyRecord {
	scan, _, _ := procMapVK.Call(uintptr(vk), 0)
	var flags uint32
	if extendedKeys[vk] {
		flags |= keyExtended
	}
	if up {
		flags |= keyUp
	}
	return keyRecord{Type: inputKeyboard, Ki: keyInput{Vk: vk, Scan: uint16(scan), Flags: flags}}
}

func pressRecords(vk uint16, mods []string) []keyRecord {
	recs := make([]keyRecord, 0, 2+2*len(mods))
	for _, m := range mods {
		recs = append(recs, vkRecord(keyCodes[m], false))
	}
	recs = append(recs, vkRecord(vk, false), vkRecord(vk, true))
	for i := len(mods) - 1; i >= 0; i-- {
		recs = append(recs, vkRecord(keyCodes[mods[i]], true))
	}
	return recs
}

func textRecords(s string) []keyRecord {
	recs := make([]keyRecord, 0, len(s)*2)
	for _, r := range s {
		switch r {
		case '\n':
			recs = append(recs, vkRecord(0x0D, false), vkRecord(0x0D, true))
			continue
		case '\t':
			recs = append(recs, vkRecord(0x09, false), vkRecord(0x09, true))
			continue
		}
		for _, unit := range utf16.Encode([]rune{r}) {
			recs = append(recs,
				keyRecord{Type: inputKeyboard, Ki: keyInput{Scan: unit, Flags: keyUnicode}},
				keyRecord{Type: inputKeyboard, Ki: keyInput{Scan: unit, Flags: keyUnicode | keyUp}})
		}
	}
	return recs
}

func metric(i uintptr) int32 {
	v, _, _ := procMetrics.Call(i)
	return int32(v)
}

func injectEvents(events []ctlEvent) error {
	for _, ev := range events {
		switch ev.T {
		case "m":
			sendMouse([]mouseRecord{{Type: inputMouse, Mi: mouseInput{Dx: int32(math.Round(ev.X)), Dy: int32(math.Round(ev.Y)), Flags: mouseMove}}})
		case "a":
			sendMouse([]mouseRecord{{Type: inputMouse, Mi: mouseInput{
				Dx:    int32(math.Round(ev.X * 65535)),
				Dy:    int32(math.Round(ev.Y * 65535)),
				Flags: mouseMove | mouseAbsolute | mouseVirtual,
			}}})
		case "w":
			var recs []mouseRecord
			if y := int32(math.Round(ev.Y)); y != 0 {
				recs = append(recs, mouseRecord{Type: inputMouse, Mi: mouseInput{MouseData: uint32(-y), Flags: mouseWheel}})
			}
			if x := int32(math.Round(ev.X)); x != 0 {
				recs = append(recs, mouseRecord{Type: inputMouse, Mi: mouseInput{MouseData: uint32(x), Flags: mouseHWheel}})
			}
			sendMouse(recs)
		case "b":
			f := buttonFlags[ev.B]
			down := mouseRecord{Type: inputMouse, Mi: mouseInput{Flags: f[0]}}
			up := mouseRecord{Type: inputMouse, Mi: mouseInput{Flags: f[1]}}
			switch ev.D {
			case "down":
				sendMouse([]mouseRecord{down})
			case "up":
				sendMouse([]mouseRecord{up})
			case "click":
				sendMouse([]mouseRecord{down, up})
			case "double":
				sendMouse([]mouseRecord{down, up, down, up})
			}
		case "k":
			sendKeys(pressRecords(keyCodes[ev.K], ev.M))
		case "s":
			sendKeys(textRecords(ev.S))
		}
	}
	return nil
}

func powerAction(kind string) {
	switch kind {
	case "lock":
		procLockWS.Call()
	case "sleep":
		procSuspend.Call(0, 0, 0)
	}
}

type bitmapHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
	Colors        [4]byte
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	X, Y   int32
}

type iconInfo struct {
	Icon     int32
	HotX     uint32
	HotY     uint32
	MaskBmp  uintptr
	ColorBmp uintptr
}

var (
	shotMu   sync.Mutex
	shotLast *image.RGBA
	shotAt   time.Time
	shotW    int
)

// captureScreen grabs every screen at once, scaled to width, with the pointer
// drawn in. Two phones watching at once share a frame taken moments ago.
func captureScreen(width int) (*image.RGBA, error) {
	shotMu.Lock()
	defer shotMu.Unlock()
	if shotLast != nil && shotW == width && time.Since(shotAt) < 40*time.Millisecond {
		return shotLast, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vx, vy := metric(76), metric(77)
	vw, vh := metric(78), metric(79)
	if vw <= 0 || vh <= 0 {
		return nil, errors.New("no screen")
	}
	tw := int32(width)
	if tw > vw {
		tw = vw
	}
	th := int32(int64(vh) * int64(tw) / int64(vw))
	if th < 1 {
		th = 1
	}

	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("no screen")
	}
	defer procReleaseDC.Call(0, screen)
	mem, _, _ := procMemDC.Call(screen)
	if mem == 0 {
		return nil, errors.New("no memory dc")
	}
	defer procDeleteDC.Call(mem)

	hdr := bitmapHeader{Size: 40, Width: tw, Height: -th, Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bmp, _, _ := procDIBSection.Call(screen, uintptr(unsafe.Pointer(&hdr)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, errors.New("no bitmap")
	}
	defer procDeleteObj.Call(bmp)
	old, _, _ := procSelect.Call(mem, bmp)
	defer procSelect.Call(mem, old)

	procStretchMode.Call(mem, 4)
	procBrushOrg.Call(mem, 0, 0, 0)
	ok, _, _ := procStretchBlt.Call(mem, 0, 0, uintptr(tw), uintptr(th), screen, uintptr(vx), uintptr(vy), uintptr(vw), uintptr(vh), 0x00CC0020)
	if ok == 0 {
		return nil, errors.New("could not copy the screen")
	}
	drawPointer(mem, vx, vy, float64(tw)/float64(vw))
	procGdiFlush.Call()

	n := int(tw) * int(th) * 4
	src := unsafe.Slice((*byte)(bits), n)
	img := image.NewRGBA(image.Rect(0, 0, int(tw), int(th)))
	dst := img.Pix
	for i := 0; i < n; i += 4 {
		dst[i] = src[i+2]
		dst[i+1] = src[i+1]
		dst[i+2] = src[i]
		dst[i+3] = 255
	}
	shotLast, shotAt, shotW = img, time.Now(), width
	return img, nil
}

func drawPointer(dc uintptr, vx, vy int32, scale float64) {
	ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if ok, _, _ := procCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ok == 0 || ci.Flags&1 == 0 || ci.Cursor == 0 {
		return
	}
	var ii iconInfo
	if ok, _, _ := procIconInfo.Call(ci.Cursor, uintptr(unsafe.Pointer(&ii))); ok != 0 {
		if ii.MaskBmp != 0 {
			procDeleteObj.Call(ii.MaskBmp)
		}
		if ii.ColorBmp != 0 {
			procDeleteObj.Call(ii.ColorBmp)
		}
	}
	// Scaled down with the screen, the pointer gets hard to find on a phone,
	// so it never drops below a size a thumb can follow.
	size := float64(metric(13)) * scale
	if size < 20 {
		size = 20
	}
	k := size / float64(metric(13))
	x := int32(float64(ci.X-vx)*scale - float64(ii.HotX)*k)
	y := int32(float64(ci.Y-vy)*scale - float64(ii.HotY)*k)
	procDrawIconEx.Call(dc, uintptr(x), uintptr(y), ci.Cursor, uintptr(int32(size)), uintptr(int32(size)), 0, 0, 3)
}

type flashInfo struct {
	Size    uint32
	Hwnd    uintptr
	Flags   uint32
	Count   uint32
	Timeout uint32
}

// raiseDesk pulls the PC window up when a phone asks to take over, so the
// question is seen rather than waiting behind other windows.
func raiseDesk() {
	view := appView
	if view == nil {
		return
	}
	view.Dispatch(func() {
		hwnd := uintptr(view.Window())
		if hwnd == 0 {
			return
		}
		if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
			showWin(hwnd, swRestore)
		}
		procForeground.Call(hwnd)
		fi := flashInfo{Hwnd: hwnd, Flags: 3 | 12}
		fi.Size = uint32(unsafe.Sizeof(fi))
		procFlash.Call(uintptr(unsafe.Pointer(&fi)))
	})
}

var (
	procThreadInfo     = ctlUser32.NewProc("GetGUIThreadInfo")
	procClientToScreen = ctlUser32.NewProc("ClientToScreen")
)

type guiThreadInfo struct {
	Size      uint32
	Flags     uint32
	Active    uintptr
	Focus     uintptr
	Capture   uintptr
	MenuOwner uintptr
	MoveSize  uintptr
	Caret     uintptr
	Left      int32
	Top       int32
	Right     int32
	Bottom    int32
}

// pointerAt says where the mouse pointer is, as fractions of the whole desktop.
func pointerAt() (x, y float64, ok bool) {
	ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if r, _, _ := procCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
		return 0, 0, false
	}
	vx, vy := metric(76), metric(77)
	vw, vh := metric(78), metric(79)
	if vw <= 0 || vh <= 0 {
		return 0, 0, false
	}
	return float64(ci.X-vx) / float64(vw), float64(ci.Y-vy) / float64(vh), true
}

// caretBox finds the blinking text cursor of the window in front, when it
// has one, as fractions of the whole desktop.
func caretBox() (x, y, w, h float64, ok bool) {
	gi := guiThreadInfo{Size: uint32(unsafe.Sizeof(guiThreadInfo{}))}
	if r, _, _ := procThreadInfo.Call(0, uintptr(unsafe.Pointer(&gi))); r == 0 || gi.Caret == 0 {
		return 0, 0, 0, 0, false
	}
	pt := [2]int32{gi.Left, gi.Top}
	if r, _, _ := procClientToScreen.Call(gi.Caret, uintptr(unsafe.Pointer(&pt))); r == 0 {
		return 0, 0, 0, 0, false
	}
	vx, vy := metric(76), metric(77)
	vw, vh := metric(78), metric(79)
	if vw <= 0 || vh <= 0 {
		return 0, 0, 0, 0, false
	}
	cw, ch := gi.Right-gi.Left, gi.Bottom-gi.Top
	if cw < 2 {
		cw = 2
	}
	if ch < 8 {
		ch = 8
	}
	x = float64(pt[0]-vx) / float64(vw)
	y = float64(pt[1]-vy) / float64(vh)
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return 0, 0, 0, 0, false
	}
	return x, y, float64(cw) / float64(vw), float64(ch) / float64(vh), true
}
