//go:build windows

package main

import (
	"os"
	"reflect"
	"strings"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
)

// webviewDebug is the only way a shipped window grows DevTools. It stays
// off unless FASTTRANSFER_DEBUG is 1, true, or yes. Dist builds do not set it.
func webviewDebug() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FASTTRANSFER_DEBUG"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// lockWebView turns off the browser chrome go-webview2 does not cover
// through WebViewOptions. Debug already sets DevTools and the default
// context menu. AreBrowserAcceleratorKeysEnabled lives on the settings
// object (ICoreWebView2Settings3) but not on the public WebView, so this
// reaches the same Chromium the constructor just created.
func lockWebView(w webview2.WebView) {
	if w == nil {
		return
	}
	debug := webviewDebug()
	if debug {
		w.Init(`window.__ftDebug=true`)
	}
	defer func() { _ = recover() }()
	chromium := chromiumOf(w)
	if chromium == nil {
		return
	}
	settings, err := chromium.GetSettings()
	if err != nil || settings == nil {
		return
	}
	_ = settings.PutAreDevToolsEnabled(debug)
	_ = settings.PutAreDefaultContextMenusEnabled(debug)
	_ = settings.PutAreBrowserAcceleratorKeysEnabled(debug)
	_ = settings.PutIsStatusBarEnabled(false)
}

func chromiumOf(w webview2.WebView) *edge.Chromium {
	rv := reflect.ValueOf(w)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil
	}
	field := rv.Elem().FieldByName("browser")
	if !field.IsValid() || !field.CanAddr() || field.Kind() != reflect.Interface {
		return nil
	}
	header := (*[2]unsafe.Pointer)(unsafe.Pointer(field.UnsafeAddr()))
	if header[1] == nil {
		return nil
	}
	return (*edge.Chromium)(header[1])
}
