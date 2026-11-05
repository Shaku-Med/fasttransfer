//go:build windows

package main

import "testing"

func TestWebViewDebugDefaultOff(t *testing.T) {
	t.Setenv("FASTTRANSFER_DEBUG", "")
	if webviewDebug() {
		t.Fatal("empty FASTTRANSFER_DEBUG enabled DevTools")
	}
	t.Setenv("FASTTRANSFER_DEBUG", "0")
	if webviewDebug() {
		t.Fatal("FASTTRANSFER_DEBUG=0 enabled DevTools")
	}
	for _, on := range []string{"1", "true", "yes", "TRUE"} {
		t.Setenv("FASTTRANSFER_DEBUG", on)
		if !webviewDebug() {
			t.Fatalf("FASTTRANSFER_DEBUG=%s did not enable DevTools", on)
		}
	}
}
