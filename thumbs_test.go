package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// withOrientation slips an EXIF block with the given orientation in after the JPEG start marker.
func withOrientation(t *testing.T, jpg []byte, o uint16) []byte {
	t.Helper()
	tiff := make([]byte, 8+2+12+4)
	copy(tiff, "II")
	binary.LittleEndian.PutUint16(tiff[2:], 42)
	binary.LittleEndian.PutUint32(tiff[4:], 8)
	binary.LittleEndian.PutUint16(tiff[8:], 1)
	binary.LittleEndian.PutUint16(tiff[10:], 0x0112)
	binary.LittleEndian.PutUint16(tiff[12:], 3)
	binary.LittleEndian.PutUint32(tiff[14:], 1)
	binary.LittleEndian.PutUint16(tiff[18:], o)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// landscape makes a wide photo with a red left half, so turning it is easy to see.
func landscape(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOrientationIsRead(t *testing.T) {
	plain := landscape(t, 40, 20)
	if o := jpegOrientation(bytes.NewReader(plain)); o != 1 {
		t.Fatalf("a photo without EXIF should read as 1, got %d", o)
	}
	for _, o := range []uint16{3, 6, 8} {
		if got := jpegOrientation(bytes.NewReader(withOrientation(t, plain, o))); got != int(o) {
			t.Fatalf("orientation %d read back as %d", o, got)
		}
	}
	if o := jpegOrientation(bytes.NewReader([]byte("not a jpeg at all"))); o != 1 {
		t.Fatalf("junk should read as 1, got %d", o)
	}
}

func TestThumbTurnsSidewaysPhotos(t *testing.T) {
	testServer(t)
	path := filepath.Join(saveRoot(), "files", "side.jpg")
	if err := os.WriteFile(path, withOrientation(t, landscape(t, 800, 400), 6), 0o644); err != nil {
		t.Fatal(err)
	}
	data, ctype, err := makeThumb(path, ".jpg", 160)
	if err != nil || ctype != "image/jpeg" {
		t.Fatalf("thumb failed: %v %s", err, ctype)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 160 || b.Dy() != 320 {
		t.Fatalf("a sideways photo should come out tall at 160x320, got %dx%d", b.Dx(), b.Dy())
	}
	r, _, bl, _ := img.At(80, 20).RGBA()
	if r < bl {
		t.Fatalf("after turning, the red half should be on top")
	}

	info, _ := os.Stat(path)
	if w, h := imageDims(path, info); w != 400 || h != 800 {
		t.Fatalf("listed size should be turned too, got %dx%d", w, h)
	}
}

func TestThumbKeepsTransparency(t *testing.T) {
	testServer(t)
	img := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	img.Set(10, 10, color.NRGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(saveRoot(), "files", "logo.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ctype, err := makeThumb(path, ".png", 96)
	if err != nil || ctype != "image/png" {
		t.Fatalf("a see-through PNG should stay PNG, got %v %s", err, ctype)
	}
}

func TestThumbBounds(t *testing.T) {
	cases := []struct{ w, h, size, tw, th int }{
		{4032, 3024, 160, 213, 160},
		{3024, 4032, 160, 160, 213},
		{100, 80, 160, 100, 80},
		{10000, 500, 96, 288, 14},
	}
	for _, c := range cases {
		if tw, th := thumbBounds(c.w, c.h, c.size); tw != c.tw || th != c.th {
			t.Fatalf("thumbBounds(%d,%d,%d) = %dx%d, want %dx%d", c.w, c.h, c.size, tw, th, c.tw, c.th)
		}
	}
	if thumbSize("33") != 96 || thumbSize("200") != 320 || thumbSize("9999") != 640 || thumbSize("x") != 160 {
		t.Fatal("thumb sizes should snap to the cached buckets")
	}
}

func TestThumbRoute(t *testing.T) {
	mux := testServer(t).routes()
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	saveFile(t, "files/a.jpg", string(landscape(t, 600, 300)))
	saveFile(t, "files/notes.txt", "hello")

	w := serve(mux, deskReq("GET", "/api/thumb?path=files/a.jpg&w=96", ""))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("ETag") == "" {
		t.Fatalf("thumb route got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	again := deskReq("GET", "/api/thumb?path=files/a.jpg&w=96", "")
	again.Header.Set("If-None-Match", w.Header().Get("ETag"))
	if w2 := serve(mux, again); w2.Code != http.StatusNotModified {
		t.Fatalf("a cached thumb should answer 304, got %d", w2.Code)
	}
	if w := serve(mux, deskReq("GET", "/api/thumb?path=files/notes.txt", "")); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text has no thumb, got %d", w.Code)
	}
	if w := serve(mux, deskReq("GET", "/api/thumb?path=../../secret.jpg", "")); w.Code != http.StatusNotFound {
		t.Fatalf("a path outside the save folder got %d", w.Code)
	}
	r := deskReq("GET", "/api/thumb?path=files/a.jpg", "")
	r.RemoteAddr = "192.168.1.20:5000"
	if w := serve(mux, r); w.Code != http.StatusNotFound {
		t.Fatalf("thumbs are for this PC only, got %d", w.Code)
	}
}
