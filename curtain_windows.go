package main

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The curtain blacks out every screen on the PC itself while a phone keeps
// seeing the real desktop: Windows leaves a window marked exclude from capture
// out of screen copies. The pointer is hidden and the speakers muted too, and
// a key or click from the PC's own keyboard or mouse locks it, since only a
// person sitting there makes input that was not injected.

var (
	procCreateWindow   = ctlUser32.NewProc("CreateWindowExW")
	procRegisterClass  = ctlUser32.NewProc("RegisterClassExW")
	procDefWindowProc  = ctlUser32.NewProc("DefWindowProcW")
	procDestroyWindow  = ctlUser32.NewProc("DestroyWindow")
	procShowWindow     = ctlUser32.NewProc("ShowWindow")
	procSetWindowPos   = ctlUser32.NewProc("SetWindowPos")
	procLayeredAttrs   = ctlUser32.NewProc("SetLayeredWindowAttributes")
	procDisplayAff     = ctlUser32.NewProc("SetWindowDisplayAffinity")
	procGetMessage     = ctlUser32.NewProc("GetMessageW")
	procDispatch       = ctlUser32.NewProc("DispatchMessageW")
	procPostThreadMsg  = ctlUser32.NewProc("PostThreadMessageW")
	procSetTimer       = ctlUser32.NewProc("SetTimer")
	procSetHook        = ctlUser32.NewProc("SetWindowsHookExW")
	procUnhook         = ctlUser32.NewProc("UnhookWindowsHookEx")
	procNextHook       = ctlUser32.NewProc("CallNextHookEx")
	procLoadCursor     = ctlUser32.NewProc("LoadCursorW")
	procCopyIcon       = ctlUser32.NewProc("CopyIcon")
	procDestroyIcon    = ctlUser32.NewProc("DestroyIcon")
	procCreateCursor   = ctlUser32.NewProc("CreateCursor")
	procSetSysCursor   = ctlUser32.NewProc("SetSystemCursor")
	procSysParamsInfo  = ctlUser32.NewProc("SystemParametersInfoW")
	procStockObject    = ctlGdi32.NewProc("GetStockObject")
	procModuleHandle   = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procCurrentThread  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
	procInputDesktop   = ctlUser32.NewProc("OpenInputDesktop")
	procCloseDesktop   = ctlUser32.NewProc("CloseDesktop")
	errCurtainOld      = errors.New("this PC's Windows is too old to hide its screen from itself")
	errCurtainNoWindow = errors.New("could not cover the PC screen")
)

// Every system pointer shape, so none of them shows over the black.
var cursorIDs = []uintptr{32512, 32513, 32514, 32515, 32516, 32642, 32643, 32644, 32645, 32646, 32648, 32649, 32650, 32651, 32671, 32672}

type curtainState struct {
	thread uint32
	done   chan struct{}
	muted  bool
}

var (
	curtainMu  sync.Mutex
	curtainNow *curtainState
	// Armed while the curtain is up, and spent by the first touch.
	curtainArmed atomic.Bool
	// The real look of each hidden pointer, so screen copies still draw it.
	lookMu   sync.Mutex
	realLook map[uintptr]uintptr

	curtainProcCb = syscall.NewCallback(curtainProc)
	keyHookCb     = syscall.NewCallback(keyHook)
	mouseHookCb   = syscall.NewCallback(mouseHook)
	curtainClass  = func() *uint16 { p, _ := windows.UTF16PtrFromString("FastTransferCurtain"); return p }()
	classOnce     sync.Once
)

// A crash while the curtain was up would leave the pointer invisible, so every
// start puts the user's own pointers back.
func init() { procSysParamsInfo.Call(0x0057, 0, 0, 0) }

// onCurtainTouch runs once when someone at the PC presses a key or clicks
// while the curtain is up.
var onCurtainTouch = func() {}

func curtainUp() error {
	curtainMu.Lock()
	defer curtainMu.Unlock()
	if curtainNow != nil {
		return nil
	}
	cs := &curtainState{done: make(chan struct{})}
	ready := make(chan error, 1)
	go cs.run(ready)
	if err := <-ready; err != nil {
		return err
	}
	curtainNow = cs
	if was, err := muteSpeakers(true); err == nil {
		cs.muted = !was
	}
	return nil
}

// curtainDown takes the curtain away. quiet is for when the PC has just locked
// itself with a video maybe still playing: the speakers stay muted until
// someone unlocks it.
func curtainDown(quiet bool) {
	curtainMu.Lock()
	defer curtainMu.Unlock()
	cs := curtainNow
	if cs == nil {
		return
	}
	curtainNow = nil
	curtainArmed.Store(false)
	procPostThreadMsg.Call(uintptr(cs.thread), 0x0012, 0, 0) // WM_QUIT
	<-cs.done
	switch {
	case cs.muted && quiet:
		go unmuteAfterUnlock()
	case cs.muted:
		_, _ = muteSpeakers(false)
	}
}

// unmuteAfterUnlock waits to see the PC locked and then unlocked again before
// the speakers come back. If the lock never shows, they stay muted.
func unmuteAfterUnlock() {
	locked := false
	for i := 0; i < 24*3600; i++ {
		open := inputDesktopOpen()
		switch {
		case !open:
			locked = true
		case locked:
			_, _ = muteSpeakers(false)
			return
		case i >= 10:
			return
		}
		time.Sleep(time.Second)
	}
}

// inputDesktopOpen is false while Windows shows its lock screen.
func inputDesktopOpen() bool {
	h, _, _ := procInputDesktop.Call(0, 0, 0x0100)
	if h == 0 {
		return false
	}
	procCloseDesktop.Call(h)
	return true
}

func curtainShown() bool {
	curtainMu.Lock()
	defer curtainMu.Unlock()
	return curtainNow != nil
}

func (cs *curtainState) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(cs.done)
	tid, _, _ := procCurrentThread.Call()
	cs.thread = uint32(tid)
	hinst, _, _ := procModuleHandle.Call(0)

	classOnce.Do(func() {
		black, _, _ := procStockObject.Call(4)
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
		}{lpfn: curtainProcCb, inst: hinst, bg: black, name: curtainClass}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		procRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
	})

	// Topmost, see through for clicks, never focused and off the taskbar, so
	// the phone's clicks land on the windows underneath.
	const exStyle = 0x8 | 0x20 | 0x80 | 0x80000 | 0x8000000
	vx, vy, vw, vh := metric(76), metric(77), metric(78), metric(79)
	hwnd, _, _ := procCreateWindow.Call(exStyle, uintptr(unsafe.Pointer(curtainClass)), 0, 0x80000000,
		uintptr(vx), uintptr(vy), uintptr(vw), uintptr(vh), 0, 0, hinst, 0)
	if hwnd == 0 {
		ready <- errCurtainNoWindow
		return
	}
	defer procDestroyWindow.Call(hwnd)
	procLayeredAttrs.Call(hwnd, 0, 255, 2)
	// Without exclude from capture the phone would see the black too, so
	// there is no curtain at all rather than one that blinds the phone.
	const excludeFromCapture = 0x11
	if ok, _, _ := procDisplayAff.Call(hwnd, excludeFromCapture); ok == 0 {
		ready <- errCurtainOld
		return
	}
	procShowWindow.Call(hwnd, 4) // SW_SHOWNOACTIVATE
	procSetTimer.Call(hwnd, 1, 400, 0)

	hideCursors(hinst)
	defer showCursors()
	kh, _, _ := procSetHook.Call(13, keyHookCb, hinst, 0)
	mh, _, _ := procSetHook.Call(14, mouseHookCb, hinst, 0)
	defer func() {
		if kh != 0 {
			procUnhook.Call(kh)
		}
		if mh != 0 {
			procUnhook.Call(mh)
		}
	}()
	curtainArmed.Store(true)
	ready <- nil

	var msg [48]byte
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			return
		}
		procDispatch.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
}

func curtainProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case 0x0113, 0x007E: // WM_TIMER, WM_DISPLAYCHANGE
		// Other always on top windows come and go, and screens get plugged
		// in, so the curtain keeps itself above them and over all of them.
		vx, vy, vw, vh := metric(76), metric(77), metric(78), metric(79)
		procSetWindowPos.Call(hwnd, ^uintptr(0), uintptr(vx), uintptr(vy), uintptr(vw), uintptr(vh), 0x0010|0x0400) // HWND_TOPMOST, SWP_NOACTIVATE|SWP_NOSENDCHANGING
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wp, lp)
	return r
}

type keyHookInfo struct {
	VK, Scan, Flags, Time uint32
	Extra                 uintptr
}

type mouseHookInfo struct {
	X, Y              int32
	Data, Flags, Time uint32
	Extra             uintptr
}

func keyHook(code int32, wp, lp uintptr) uintptr {
	if code == 0 {
		info := (*keyHookInfo)(unsafe.Pointer(lp))
		if info.Flags&0x10 == 0 {
			if wp == 0x0100 || wp == 0x0104 { // WM_KEYDOWN, WM_SYSKEYDOWN
				curtainTouched()
			}
			return 1
		}
	}
	r, _, _ := procNextHook.Call(0, uintptr(code), wp, lp)
	return r
}

func mouseHook(code int32, wp, lp uintptr) uintptr {
	if code == 0 {
		info := (*mouseHookInfo)(unsafe.Pointer(lp))
		if info.Flags&0x1 == 0 {
			switch wp {
			case 0x0201, 0x0204, 0x0207, 0x020B, 0x020A, 0x020E: // button downs and wheels
				curtainTouched()
			}
			// Even a nudge is swallowed, so the hidden pointer stays where
			// the phone left it.
			return 1
		}
	}
	r, _, _ := procNextHook.Call(0, uintptr(code), wp, lp)
	return r
}

func curtainTouched() {
	if curtainArmed.Swap(false) {
		go onCurtainTouch()
	}
}

func hideCursors(hinst uintptr) {
	and := make([]byte, 128)
	for i := range and {
		and[i] = 0xFF
	}
	xor := make([]byte, 128)
	blank, _, _ := procCreateCursor.Call(hinst, 0, 0, 32, 32, uintptr(unsafe.Pointer(&and[0])), uintptr(unsafe.Pointer(&xor[0])))
	if blank == 0 {
		return
	}
	defer procDestroyIcon.Call(blank)
	looks := map[uintptr]uintptr{}
	for _, id := range cursorIDs {
		shared, _, _ := procLoadCursor.Call(0, id)
		if shared == 0 {
			continue
		}
		look, _, _ := procCopyIcon.Call(shared)
		if look == 0 {
			continue
		}
		hidden, _, _ := procCopyIcon.Call(blank)
		if hidden == 0 {
			procDestroyIcon.Call(look)
			continue
		}
		looks[shared] = look
		procSetSysCursor.Call(hidden, id)
	}
	lookMu.Lock()
	realLook = looks
	lookMu.Unlock()
}

// showCursors puts the user's own pointer scheme back.
func showCursors() {
	procSysParamsInfo.Call(0x0057, 0, 0, 0) // SPI_SETCURSORS
	lookMu.Lock()
	looks := realLook
	realLook = nil
	lookMu.Unlock()
	for _, h := range looks {
		procDestroyIcon.Call(h)
	}
}

// pointerLook swaps a hidden pointer for its real look while the curtain is
// up, so the phone still sees the pointer the PC no longer shows.
func pointerLook(h uintptr) uintptr {
	lookMu.Lock()
	defer lookMu.Unlock()
	if look, ok := realLook[h]; ok {
		return look
	}
	return h
}
