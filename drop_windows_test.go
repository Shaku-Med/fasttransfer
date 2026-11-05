//go:build windows

package main

import (
	"testing"
	"unsafe"
)

func TestDropLayout(t *testing.T) {
	if !dropReady {
		t.Fatalf("format size %d offset %d medium size %d data %d", unsafe.Sizeof(formatEtc{}), unsafe.Offsetof(formatEtc{}.ptd), unsafe.Sizeof(stgMedium{}), unsafe.Offsetof(stgMedium{}.data))
	}
}
