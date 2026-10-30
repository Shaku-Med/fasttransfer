//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type formatEtc struct {
	cfFormat uint16
	ptd      uintptr
	dwAspect uint32
	lindex   int32
	tymed    uint32
}

type stgMedium struct {
	tymed uint32
	data  uintptr
	unk   uintptr
}

type comObject struct {
	vt uintptr
}

type winGUID struct {
	d1 uint32
	d2 uint16
	d3 uint16
	d4 [8]byte
}

var (
	dropObj     comObject
	dropRefs    int32 = 1
	dropReady   bool
	dropSeen    = map[uintptr]bool{}
	dropKids    []uintptr
	ole32Drop   = windows.NewLazySystemDLL("ole32.dll")
	shell32Drop = windows.NewLazySystemDLL("shell32.dll")
	kernelDrop  = windows.NewLazySystemDLL("kernel32.dll")
	iidUnknown  = winGUID{d4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidDrop     = winGUID{d1: 0x00000122, d4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

var dropVTable = [7]uintptr{
	syscall.NewCallback(dropQI),
	syscall.NewCallback(dropAddRef),
	syscall.NewCallback(dropRelease),
	syscall.NewCallback(dropEnter),
	syscall.NewCallback(dropOver),
	syscall.NewCallback(dropLeave),
	syscall.NewCallback(dropDrop),
}

var dropEnum = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
	dropKids = append(dropKids, hwnd)
	return 1
})

func init() {
	dropObj.vt = uintptr(unsafe.Pointer(&dropVTable[0]))
	dropReady = unsafe.Sizeof(formatEtc{}) == 32 && unsafe.Offsetof(formatEtc{}.ptd) == 8 &&
		unsafe.Sizeof(stgMedium{}) == 24 && unsafe.Offsetof(stgMedium{}.data) == 8
}

func offerFileDrop(hwnd uintptr) {
	if !dropReady || hwnd == 0 {
		return
	}
	ole32Drop.NewProc("OleInitialize").Call(0)
	registerDrop(hwnd)
	user32 := windows.NewLazySystemDLL("user32.dll")
	dropKids = dropKids[:0]
	user32.NewProc("EnumChildWindows").Call(hwnd, dropEnum, 0)
	kids := append([]uintptr(nil), dropKids...)
	for _, child := range kids {
		if child == capHwnd {
			continue
		}
		registerDrop(child)
	}
}

func registerDrop(hwnd uintptr) {
	if hwnd == 0 || dropSeen[hwnd] {
		return
	}
	ole32Drop.NewProc("RevokeDragDrop").Call(hwnd)
	hr, _, _ := ole32Drop.NewProc("RegisterDragDrop").Call(hwnd, uintptr(unsafe.Pointer(&dropObj)))
	if hr == 0 || uint32(hr) == 0x80040101 {
		dropSeen[hwnd] = true
	}
}

func dropQI(this, riid, ppv uintptr) uintptr {
	if ppv == 0 {
		return 0x80004003
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = 0
	if riid == 0 {
		return 0x80004002
	}
	id := *(*winGUID)(unsafe.Pointer(riid))
	if id != iidUnknown && id != iidDrop {
		return 0x80004002
	}
	*(*uintptr)(unsafe.Pointer(ppv)) = this
	dropAddRef(this)
	return 0
}

func dropAddRef(uintptr) uintptr {
	return uintptr(atomic.AddInt32(&dropRefs, 1))
}

func dropRelease(uintptr) uintptr {
	n := atomic.AddInt32(&dropRefs, -1)
	if n < 1 {
		atomic.StoreInt32(&dropRefs, 1)
		return 1
	}
	return uintptr(n)
}

func dropEnter(_, data, _, _, effect uintptr) uintptr {
	allowDrop(effect, data)
	return 0
}

func dropOver(_, _, _, effect uintptr) uintptr {
	if effect != 0 {
		*(*uint32)(unsafe.Pointer(effect)) = 1
	}
	return 0
}

func dropLeave(uintptr) uintptr { return 0 }

func dropDrop(_, data, _, _, effect uintptr) uintptr {
	allowDrop(effect, data)
	deliverDropped(dataObjectPaths(data))
	return 0
}

func allowDrop(effect, data uintptr) {
	if effect == 0 {
		return
	}
	got := uint32(0)
	if dropHasFiles(data) {
		got = 1
	}
	*(*uint32)(unsafe.Pointer(effect)) = got
}

func dropHasFiles(obj uintptr) bool {
	if obj == 0 {
		return false
	}
	fmtetc := formatEtc{cfFormat: 15, dwAspect: 1, lindex: -1, tymed: 1}
	return callCom(obj, 5, uintptr(unsafe.Pointer(&fmtetc))) == 0
}

func dataObjectPaths(obj uintptr) []string {
	if obj == 0 {
		return nil
	}
	fmtetc := formatEtc{cfFormat: 15, dwAspect: 1, lindex: -1, tymed: 1}
	if callCom(obj, 5, uintptr(unsafe.Pointer(&fmtetc))) != 0 {
		return nil
	}
	var medium stgMedium
	if callCom(obj, 3, uintptr(unsafe.Pointer(&fmtetc)), uintptr(unsafe.Pointer(&medium))) != 0 {
		return nil
	}
	defer ole32Drop.NewProc("ReleaseStgMedium").Call(uintptr(unsafe.Pointer(&medium)))
	if medium.data == 0 {
		return nil
	}
	return hdropPaths(medium.data)
}

func callCom(obj uintptr, index int, args ...uintptr) uintptr {
	vt := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vt + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, obj)
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

func hdropPaths(hdrop uintptr) []string {
	query := shell32Drop.NewProc("DragQueryFileW")
	n, _, _ := query.Call(hdrop, 0xFFFFFFFF, 0, 0)
	if n == 0 || n > 100 {
		if n > 100 {
			n = 100
		} else {
			return nil
		}
	}
	buf := make([]uint16, 32768)
	out := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		query.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		path := syscall.UTF16ToString(buf)
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

func deliverDropped(paths []string) {
	paths = cleanDropped(paths)
	if len(paths) == 0 || appView == nil {
		return
	}
	raw, err := json.Marshal(paths)
	if err != nil {
		return
	}
	script := "window.addDroppedPaths&&window.addDroppedPaths(" + string(raw) + ")"
	appView.Dispatch(func() {
		if appView != nil {
			appView.Eval(script)
		}
	})
}

func cleanDropped(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if len(out) >= 100 {
			break
		}
		path = strings.TrimSpace(path)
		if path == "" || strings.ContainsAny(path, "\r\n") || !filepath.IsAbs(path) {
			continue
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		key := strings.ToLower(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, path)
	}
	return out
}
