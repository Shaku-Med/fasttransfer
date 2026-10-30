//go:build windows

package main

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errPickCancelled = errors.New("cancelled")

var (
	ole32              = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
	procCoCreate       = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

const (
	coinitApartment     = 0x2
	clsctxInprocServer  = 0x1
	fosPickFolders      = 0x20
	fosForceFilesystem  = 0x40
	fosAllowMultiSelect = 0x200
	fosPathMustExist    = 0x800
	fosFileMustExist    = 0x1000
	fosNoChangeDir      = 0x8
	sigdnFileSysPath    = 0x80058000
	hrCancelled         = uintptr(0x800704C7)
)

func pickSaveFolder() (string, error) {
	path, err := browseFolder(topHwnd)
	if err != nil {
		if errors.Is(err, errPickCancelled) {
			return "", nil
		}
		return "", err
	}
	if err := applySaveRoot(path); err != nil {
		return "", err
	}
	return saveRoot(), nil
}

func browseFolder(owner uintptr) (string, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment)
	if hr == 0 {
		defer procCoUninitialize.Call()
	}
	var dialog uintptr
	hr, _, _ = procCoCreate.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if failed(hr) || dialog == 0 {
		return "", errFolderUse
	}
	defer releaseCOM(dialog)

	setOptions(dialog, fosPickFolders|fosForceFilesystem|fosPathMustExist|fosNoChangeDir)
	title, err := windows.UTF16PtrFromString("Save folder")
	if err == nil {
		setTitle(dialog, title)
		runtime.KeepAlive(title)
	}
	hr = comCall(dialog, 3, owner)
	if hr == hrCancelled {
		return "", errPickCancelled
	}
	if failed(hr) {
		return "", errFolderUse
	}
	item := comOut(dialog, 20)
	if item == 0 {
		return "", errFolderUse
	}
	defer releaseCOM(item)
	return shellItemPath(item)
}

func failed(hr uintptr) bool {
	return hr&^uintptr(0x7FFFFFFF) != 0 && hr != 1
}

func vfunc(obj uintptr, index int) uintptr {
	table := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(table + uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

func releaseCOM(obj uintptr) {
	_, _, _ = syscall.SyscallN(vfunc(obj, 2), obj)
}

func setOptions(obj uintptr, opts uint32) {
	_, _, _ = syscall.SyscallN(vfunc(obj, 9), obj, uintptr(opts))
}

func setTitle(obj uintptr, title *uint16) {
	_, _, _ = syscall.SyscallN(vfunc(obj, 17), obj, uintptr(unsafe.Pointer(title)))
}

func comCall(obj uintptr, index int, arg uintptr) uintptr {
	hr, _, _ := syscall.SyscallN(vfunc(obj, index), obj, arg)
	return hr
}

func comOut(obj uintptr, index int) uintptr {
	var out uintptr
	hr, _, _ := syscall.SyscallN(vfunc(obj, index), obj, uintptr(unsafe.Pointer(&out)))
	if failed(hr) {
		return 0
	}
	return out
}

func pickFiles() ([]string, error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment)
	if hr == 0 {
		defer procCoUninitialize.Call()
	}
	var dialog uintptr
	hr, _, _ = procCoCreate.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if failed(hr) || dialog == 0 {
		return nil, errFolderUse
	}
	defer releaseCOM(dialog)
	setOptions(dialog, fosAllowMultiSelect|fosForceFilesystem|fosPathMustExist|fosFileMustExist|fosNoChangeDir)
	title, err := windows.UTF16PtrFromString("Send files")
	if err == nil {
		setTitle(dialog, title)
		runtime.KeepAlive(title)
	}
	hr = comCall(dialog, 3, topHwnd)
	if hr == hrCancelled {
		return nil, errPickCancelled
	}
	if failed(hr) {
		return nil, errFolderUse
	}
	arr := comOut(dialog, 27)
	if arr != 0 {
		defer releaseCOM(arr)
		return shellItemArrayPaths(arr)
	}
	item := comOut(dialog, 20)
	if item == 0 {
		return nil, errFolderUse
	}
	defer releaseCOM(item)
	path, err := shellItemPath(item)
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

func shellItemArrayPaths(arr uintptr) ([]string, error) {
	var count uint32
	hr, _, _ := syscall.SyscallN(vfunc(arr, 7), arr, uintptr(unsafe.Pointer(&count)))
	if failed(hr) {
		return nil, errFolderUse
	}
	out := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		var item uintptr
		hr, _, _ = syscall.SyscallN(vfunc(arr, 8), arr, uintptr(i), uintptr(unsafe.Pointer(&item)))
		if failed(hr) || item == 0 {
			continue
		}
		path, err := shellItemPath(item)
		releaseCOM(item)
		if err != nil || path == "" {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

func shellItemPath(item uintptr) (string, error) {
	var name uintptr
	hr, _, _ := syscall.SyscallN(vfunc(item, 5), item, sigdnFileSysPath, uintptr(unsafe.Pointer(&name)))
	if failed(hr) || name == 0 {
		return "", errFolderUse
	}
	defer procCoTaskMemFree.Call(name)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(name))), nil
}
