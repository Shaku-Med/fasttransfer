package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Thumbnails keep the list, the tiles and the preview strip from decoding a
// full 12 MP photo for every 32 pixel icon. Each one is made once, kept on
// disk, and served with a long cache life.
const (
	maxThumbPixels = 120_000_000
	thumbCacheCap  = 256 << 20
	thumbCacheKeep = 192 << 20
)

var (
	thumbSizes   = []int{96, 160, 320, 640}
	thumbSlots   = make(chan struct{}, thumbWorkers())
	thumbWrites  atomic.Int64
	thumbPruneMu sync.Mutex
	errTooBig    = errors.New("image too large")
	errNoThumb   = errors.New("no thumbnail")
)

func thumbWorkers() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

func thumbSize(raw string) int {
	want, err := strconv.Atoi(raw)
	if err != nil {
		want = 160
	}
	for _, s := range thumbSizes {
		if want <= s {
			return s
		}
	}
	return thumbSizes[len(thumbSizes)-1]
}

func thumbDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "FastTransfer", "thumbs")
}

func thumbKey(full string, info os.FileInfo, size int) string {
	sum := sha256.Sum256([]byte(full + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00" + strconv.Itoa(size)))
	return hex.EncodeToString(sum[:16])
}

func (s *server) thumb(w http.ResponseWriter, r *http.Request) {
	full, info, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	video := videoExts[ext]
	if !video && !thumbable(ext) {
		http.Error(w, "no thumbnail", http.StatusUnsupportedMediaType)
		return
	}
	size := thumbSize(r.URL.Query().Get("w"))
	key := thumbKey(full, info, size)
	etag := `"` + key + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	data, ctype := readThumb(key)
	if data == nil {
		if !takeSlot(r, thumbSlots) {
			return
		}
		data, ctype = readThumb(key)
		if data == nil {
			if video {
				data, err = videoThumb(r.Context(), full, size)
				ctype = "image/jpeg"
			} else {
				data, ctype, err = makeThumb(full, ext, size)
			}
			if err == nil {
				saveThumb(key, ctype, data)
			}
		}
		<-thumbSlots
		if err != nil {
			http.Error(w, "no thumbnail", http.StatusUnsupportedMediaType)
			return
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write(data)
}

func thumbable(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

func readThumb(key string) ([]byte, string) {
	dir := thumbDir()
	if dir == "" {
		return nil, ""
	}
	for _, kind := range []struct{ ext, ctype string }{{".jpg", "image/jpeg"}, {".png", "image/png"}} {
		if b, err := os.ReadFile(filepath.Join(dir, key+kind.ext)); err == nil && len(b) > 0 {
			return b, kind.ctype
		}
	}
	return nil, ""
}

func saveThumb(key, ctype string, data []byte) {
	dir := thumbDir()
	if dir == "" || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	ext := ".jpg"
	if ctype == "image/png" {
		ext = ".png"
	}
	tmp, err := os.CreateTemp(dir, ".t-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(dir, key+ext)) != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if thumbWrites.Add(1)%200 == 0 {
		go pruneThumbs()
	}
}

// pruneThumbs drops the oldest thumbnails once the cache grows past its cap.
func pruneThumbs() {
	if !thumbPruneMu.TryLock() {
		return
	}
	defer thumbPruneMu.Unlock()
	dir := thumbDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		size int64
		at   time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= thumbCacheCap {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	for _, it := range items {
		if total <= thumbCacheKeep {
			break
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}

// The short side lands on the requested size so cover crops stay sharp, and
// a panorama's long side is held to three times that.
func thumbBounds(w, h, size int) (int, int) {
	short, long := w, h
	if short > long {
		short, long = long, short
	}
	scale := float64(size) / float64(short)
	if limit := float64(3*size) / float64(long); limit < scale {
		scale = limit
	}
	if scale >= 1 {
		return w, h
	}
	tw, th := int(float64(w)*scale+0.5), int(float64(h)*scale+0.5)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	return tw, th
}

func makeThumb(full, ext string, size int) ([]byte, string, error) {
	f, err := os.Open(full)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, "", err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > maxThumbPixels {
		return nil, "", errTooBig
	}
	orientation := 1
	if ext == ".jpg" || ext == ".jpeg" {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			orientation = jpegOrientation(f)
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, "", err
	}
	b := src.Bounds()
	tw, th := thumbBounds(b.Dx(), b.Dy(), size)
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	dst = orient(dst, orientation)

	var buf bytes.Buffer
	if opaque, ok := src.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 84})
		return buf.Bytes(), "image/jpeg", err
	}
	err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, dst)
	return buf.Bytes(), "image/png", err
}

func videoThumb(parent context.Context, full string, size int) ([]byte, error) {
	bin := findBin("ffmpeg")
	if bin == "" {
		return nil, errNeedFFmpeg
	}
	for _, at := range []string{"1", "0"} {
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		cmd := exec.CommandContext(ctx, bin,
			"-hide_banner", "-loglevel", "error",
			"-ss", at, "-i", full,
			"-frames:v", "1",
			"-vf", "scale='if(gt(iw,ih),-2,"+strconv.Itoa(size)+")':'if(gt(iw,ih),"+strconv.Itoa(size)+",-2)'",
			"-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "4", "-")
		hideConsole(cmd)
		out, err := cmd.Output()
		cancel()
		if err == nil && len(out) > 0 && len(out) < 8<<20 {
			return out, nil
		}
	}
	return nil, errNoThumb
}

// jpegOrientation reads the EXIF orientation tag, or 1 when there is none.
// Phones store photos sideways and leave the turning to this tag.
func jpegOrientation(r io.Reader) int {
	br := bufio.NewReader(io.LimitReader(r, 512<<10))
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		var m [4]byte
		if _, err := io.ReadFull(br, m[:]); err != nil || m[0] != 0xFF {
			return 1
		}
		marker := m[1]
		size := int(binary.BigEndian.Uint16(m[2:])) - 2
		if marker == 0xDA || marker == 0xD9 || size < 0 {
			return 1
		}
		seg := make([]byte, size)
		if _, err := io.ReadFull(br, seg); err != nil {
			return 1
		}
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
	}
}

func tiffOrientation(b []byte) int {
	if len(b) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(b[4:8]))
	if off < 8 || off+2 > len(b) {
		return 1
	}
	n := int(bo.Uint16(b[off:]))
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(b) {
			return 1
		}
		if bo.Uint16(b[e:]) == 0x0112 {
			if v := int(bo.Uint16(b[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

func orient(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			si := src.PixOffset(x, y)
			di := dst.PixOffset(nx, ny)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// Image sizes go into the folder listing so the preview knows where a photo
// will land before it has loaded. Reading one header is cheap, and the answer
// is remembered until the file changes.
type dimsKey struct {
	path string
	size int64
	mod  int64
}

var (
	dimsMu    sync.Mutex
	dimsCache = map[dimsKey][2]int{}
)

func imageDims(full string, info os.FileInfo) (int, int) {
	ext := strings.ToLower(filepath.Ext(full))
	if !thumbable(ext) {
		return 0, 0
	}
	k := dimsKey{full, info.Size(), info.ModTime().UnixNano()}
	dimsMu.Lock()
	if d, ok := dimsCache[k]; ok {
		dimsMu.Unlock()
		return d[0], d[1]
	}
	dimsMu.Unlock()

	f, err := os.Open(full)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil {
		return 0, 0
	}
	w, h := cfg.Width, cfg.Height
	if ext == ".jpg" || ext == ".jpeg" {
		if _, err := f.Seek(0, io.SeekStart); err == nil && jpegOrientation(f) >= 5 {
			w, h = h, w
		}
	}
	dimsMu.Lock()
	if len(dimsCache) > 20000 {
		dimsCache = map[dimsKey][2]int{}
	}
	dimsCache[k] = [2]int{w, h}
	dimsMu.Unlock()
	return w, h
}
