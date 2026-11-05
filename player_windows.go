package main

import (
	"encoding/json"
	"strings"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// The media player gets a real window of its own, made here instead of by
// window.open, which would give it a browser address bar. It is made once
// and only hidden when closed: go-webview2 ends the whole program when any
// of its windows is destroyed, and keeping it also means a second file
// always lands in the same window.

const (
	wmClose     = 0x0010
	swHide      = 0
	swShow      = 5
	swRestore   = 9
	playerTitle = "FastTransfer Player"
)

var (
	webviewData string
	playerView  webview2.WebView
	playerHwnd  uintptr
	playerOrig  uintptr
	playerCb    = windows.NewCallback(playerProc)
)

// showPlayer runs on the UI thread. hash names the file and its folder, the
// same way the page reads it when opened in a browser.
func showPlayer(hash string, dark bool) {
	if !strings.HasPrefix(hash, "#") || len(hash) > 8192 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	if playerView == nil {
		v := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:     webviewDebug(),
			AutoFocus: true,
			DataPath:  webviewData,
			WindowOptions: webview2.WindowOptions{
				Title:  playerTitle,
				Width:  1180,
				Height: 720,
				IconId: 1,
				Center: true,
			},
		})
		// Making the view runs its own message loop, which swallows the
		// desk's queued work; a fresh dispatch wakes that queue again.
		defer appView.Dispatch(func() {})
		if v == nil {
			return
		}
		playerView = v
		playerHwnd = uintptr(v.Window())
		lockWebView(v)
		v.SetSize(640, 400, webview2.HintMin)
		themePlayer(dark)
		playerOrig, _, _ = user32.NewProc("SetWindowLongPtrW").Call(playerHwnd, gwlpWndProc, playerCb)
		v.Navigate("http://127.0.0.1:" + port + "/player" + hash)
		return
	}
	themePlayer(dark)
	quoted, _ := json.Marshal(hash)
	playerView.Eval("location.hash = " + string(quoted))
	iconic, _, _ := user32.NewProc("IsIconic").Call(playerHwnd)
	if iconic != 0 {
		user32.NewProc("ShowWindow").Call(playerHwnd, swRestore)
	} else {
		user32.NewProc("ShowWindow").Call(playerHwnd, swShow)
	}
	user32.NewProc("SetForegroundWindow").Call(playerHwnd)
}

// Closing the player stops the music and tucks the window away for next time.
func playerProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmClose && playerView != nil {
		playerView.Eval("window.playerClosing && window.playerClosing()")
		windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow").Call(hwnd, swHide)
		return 0
	}
	r, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("CallWindowProcW").Call(playerOrig, hwnd, msg, wp, lp)
	return r
}

// themePlayer gives the title bar the player's own colors, so it reads as
// part of the window in both themes.
func themePlayer(dark bool) {
	if playerHwnd == 0 {
		return
	}
	value := int32(0)
	color := uint32(0x00F3F3F3)
	text := uint32(0x001A1A1A)
	if dark {
		value = 1
		color = 0x001C1C1C
		text = 0x00F3F3F3
	}
	dwmAttr(playerHwnd, 20, unsafe.Pointer(&value), unsafe.Sizeof(value))
	dwmAttr(playerHwnd, dwmwaCaption, unsafe.Pointer(&color), unsafe.Sizeof(color))
	dwmAttr(playerHwnd, dwmwaBorder, unsafe.Pointer(&color), unsafe.Sizeof(color))
	dwmAttr(playerHwnd, dwmwaText, unsafe.Pointer(&text), unsafe.Sizeof(text))
}
