//go:build windows && !386

package main

import (
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The PC's sound reaches the phone through WASAPI loopback: Windows hands a
// copy of whatever the speakers are playing, which is turned into 16 bit
// stereo and passed on as is. No encoder, so nothing extra to ship.

const loopbackSupported = true

var (
	clsidEnumerator   = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidEnumerator     = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidAudioClient    = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4C32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidCaptureClient  = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48A0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
	subtypeFloat      = windows.GUID{Data1: 0x00000003, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	subtypePCM        = windows.GUID{Data1: 0x00000001, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	soundCoCreate     = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
	errSoundFormat    = errors.New("the PC plays sound in a format this cannot read")
	errSoundNoSpeaker = errors.New("the PC has no speakers to listen to")
)

// soundCall calls method idx on a COM object. The pointers it is handed as
// uintptr stay alive and in place for the call, as uintptrescapes promises.
//
//go:uintptrescapes
func soundCall(obj unsafe.Pointer, idx int, args ...uintptr) int32 {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, idx*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return int32(r)
}

func soundRelease(obj unsafe.Pointer) {
	if obj != nil {
		soundCall(obj, 2)
	}
}

type sampleFormat int

const (
	sampleFloat32 sampleFormat = iota
	sampleInt16
	sampleInt24
	sampleInt32
)

func readMixFormat(p unsafe.Pointer) (rate, channels, block int, kind sampleFormat, err error) {
	b := unsafe.Slice((*byte)(p), 18)
	tag := binary.LittleEndian.Uint16(b[0:])
	channels = int(binary.LittleEndian.Uint16(b[2:]))
	rate = int(binary.LittleEndian.Uint32(b[4:]))
	block = int(binary.LittleEndian.Uint16(b[12:]))
	bits := binary.LittleEndian.Uint16(b[14:])
	extra := binary.LittleEndian.Uint16(b[16:])
	isFloat, isPCM := tag == 3, tag == 1
	if tag == 0xFFFE && extra >= 22 {
		sub := *(*windows.GUID)(unsafe.Add(p, 24))
		isFloat, isPCM = sub == subtypeFloat, sub == subtypePCM
	}
	if channels < 1 || rate < 8000 || rate > 384000 || block < channels {
		return 0, 0, 0, 0, errSoundFormat
	}
	switch {
	case isFloat && bits == 32:
		kind = sampleFloat32
	case isPCM && bits == 16:
		kind = sampleInt16
	case isPCM && bits == 24:
		kind = sampleInt24
	case isPCM && bits == 32:
		kind = sampleInt32
	default:
		return 0, 0, 0, 0, errSoundFormat
	}
	return rate, channels, block, kind, nil
}

func sampleAt(b []byte, kind sampleFormat) int16 {
	switch kind {
	case sampleFloat32:
		v := math.Float32frombits(binary.LittleEndian.Uint32(b)) * 32767
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		return int16(v)
	case sampleInt16:
		return int16(binary.LittleEndian.Uint16(b))
	case sampleInt24:
		return int16(uint16(b[1]) | uint16(b[2])<<8)
	default:
		return int16(binary.LittleEndian.Uint16(b[2:]))
	}
}

// captureSound listens to the default speakers until stop closes. It reports
// the sample rate, or why it could not start, on ready before sending sound.
func captureSound(stop <-chan struct{}, ready chan<- soundStart, push func([]byte)) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fail := func(err error) { ready <- soundStart{err: err} }
	// A thread that already joined COM another way still works, it just
	// must not be uninitialized here.
	switch err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); {
	case err == nil, err == syscall.Errno(windows.S_FALSE):
		defer windows.CoUninitialize()
	case err == syscall.Errno(windows.RPC_E_CHANGED_MODE):
	default:
		fail(err)
		return
	}

	var enum, device, client, capture, mix unsafe.Pointer
	defer func() {
		if mix != nil {
			windows.CoTaskMemFree(mix)
		}
		soundRelease(capture)
		if client != nil {
			soundCall(client, 11)
		}
		soundRelease(client)
		soundRelease(device)
		soundRelease(enum)
	}()
	if hr, _, _ := soundCoCreate.Call(uintptr(unsafe.Pointer(&clsidEnumerator)), 0, 0x17, uintptr(unsafe.Pointer(&iidEnumerator)), uintptr(unsafe.Pointer(&enum))); int32(hr) < 0 || enum == nil {
		fail(errSoundNoSpeaker)
		return
	}
	if soundCall(enum, 4, 0, 0, uintptr(unsafe.Pointer(&device))) < 0 || device == nil {
		fail(errSoundNoSpeaker)
		return
	}
	if soundCall(device, 3, uintptr(unsafe.Pointer(&iidAudioClient)), 0x17, 0, uintptr(unsafe.Pointer(&client))) < 0 || client == nil {
		fail(errSoundNoSpeaker)
		return
	}
	if soundCall(client, 8, uintptr(unsafe.Pointer(&mix))) < 0 || mix == nil {
		fail(errSoundFormat)
		return
	}
	rate, channels, block, kind, err := readMixFormat(mix)
	if err != nil {
		fail(err)
		return
	}
	const loopback = 0x00020000
	if soundCall(client, 3, 0, loopback, 2_000_000, 0, uintptr(mix), 0) < 0 {
		fail(errSoundNoSpeaker)
		return
	}
	if soundCall(client, 14, uintptr(unsafe.Pointer(&iidCaptureClient)), uintptr(unsafe.Pointer(&capture))) < 0 || capture == nil {
		fail(errSoundNoSpeaker)
		return
	}
	if soundCall(client, 10) < 0 {
		fail(errSoundNoSpeaker)
		return
	}
	ready <- soundStart{rate: rate}

	width := block / channels
	chunk := rate / 50 * 4
	out := make([]byte, 0, chunk*2)
	for {
		select {
		case <-stop:
			return
		default:
		}
		var next uint32
		if soundCall(capture, 5, uintptr(unsafe.Pointer(&next))) < 0 {
			return
		}
		if next == 0 {
			time.Sleep(8 * time.Millisecond)
			continue
		}
		var data unsafe.Pointer
		var frames, flags uint32
		if soundCall(capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0) < 0 {
			return
		}
		silent := flags&2 != 0 || data == nil
		var raw []byte
		if !silent {
			raw = unsafe.Slice((*byte)(data), int(frames)*block)
		}
		for i := 0; i < int(frames); i++ {
			var l, r int16
			if !silent {
				f := raw[i*block:]
				l = sampleAt(f, kind)
				r = l
				if channels > 1 {
					r = sampleAt(f[width:], kind)
				}
			}
			out = binary.LittleEndian.AppendUint16(out, uint16(l))
			out = binary.LittleEndian.AppendUint16(out, uint16(r))
		}
		soundCall(capture, 4, uintptr(frames))
		if len(out) >= chunk {
			push(append([]byte(nil), out...))
			out = out[:0]
		}
	}
}

var iidEndpointVolume = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}

// muteSpeakers mutes or unmutes the default speakers and says whether they
// were muted before. Loopback copies the sound ahead of the mute, so a phone
// listening still hears everything.
func muteSpeakers(mute bool) (was bool, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); {
	case err == nil, err == syscall.Errno(windows.S_FALSE):
		defer windows.CoUninitialize()
	case err == syscall.Errno(windows.RPC_E_CHANGED_MODE):
	default:
		return false, err
	}
	var enum, device, vol unsafe.Pointer
	defer func() {
		soundRelease(vol)
		soundRelease(device)
		soundRelease(enum)
	}()
	if hr, _, _ := soundCoCreate.Call(uintptr(unsafe.Pointer(&clsidEnumerator)), 0, 0x17, uintptr(unsafe.Pointer(&iidEnumerator)), uintptr(unsafe.Pointer(&enum))); int32(hr) < 0 || enum == nil {
		return false, errSoundNoSpeaker
	}
	if soundCall(enum, 4, 0, 0, uintptr(unsafe.Pointer(&device))) < 0 || device == nil {
		return false, errSoundNoSpeaker
	}
	if soundCall(device, 3, uintptr(unsafe.Pointer(&iidEndpointVolume)), 0x17, 0, uintptr(unsafe.Pointer(&vol))) < 0 || vol == nil {
		return false, errSoundNoSpeaker
	}
	var muted int32
	if soundCall(vol, 15, uintptr(unsafe.Pointer(&muted))) < 0 {
		return false, errSoundNoSpeaker
	}
	want := uintptr(0)
	if mute {
		want = 1
	}
	if soundCall(vol, 14, want, 0) < 0 {
		return muted != 0, errSoundNoSpeaker
	}
	return muted != 0, nil
}
