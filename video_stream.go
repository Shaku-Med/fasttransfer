package main

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var (
	errNeedFFmpeg = errors.New("need ffmpeg")
	errNoPicture  = errors.New("no picture")
	durationRE    = regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
)

// findBin prefers a helper extracted from this app, then PATH, then a copy
// beside the executable. It never downloads anything.
func findBin(name string) string {
	if p := bundledTool(name); p != "" {
		return p
	}
	file := name
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(file), ".exe") {
		file += ".exe"
	}
	if p, err := exec.LookPath(file); err == nil {
		return p
	}
	if file != name {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(exe), file)
	st, err := os.Stat(candidate)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return candidate
}

func (s *server) videoInfo(w http.ResponseWriter, r *http.Request) {
	full, _, name, err := regularInside(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !videoExts[ext] {
		http.NotFound(w, r)
		return
	}
	mode := "direct"
	var duration float64
	if directVideo[ext] == "" || r.URL.Query().Get("force") == "1" {
		if findBin("ffmpeg") == "" {
			mode = "need-ffmpeg"
		} else {
			mode = "ffmpeg"
			duration = probeDuration(full)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "duration": duration})
}

// streamTranscoded sends fragmented MP4 (H.264 + AAC) produced by ffmpeg.
// The source path is a file already inside uploads. ffmpeg is not given a URL.
func streamTranscoded(w http.ResponseWriter, r *http.Request, full string) {
	if findBin("ffmpeg") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = w.Write([]byte("This video needs ffmpeg"))
		return
	}
	start := streamStart(r.URL.Query().Get("ss"))
	ctx := r.Context()
	var first []byte
	var rest io.Reader
	var cleanup func()
	var err error
	if canRemux(full) {
		first, rest, cleanup, err = startFFmpeg(ctx, ffmpegArgs(full, start, true))
		if err != nil && ctx.Err() == nil {
			first, rest, cleanup, err = startFFmpeg(ctx, ffmpegArgs(full, start, false))
		}
	} else {
		first, rest, cleanup, err = startFFmpeg(ctx, ffmpegArgs(full, start, false))
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		http.Error(w, "could not play this video", http.StatusBadGateway)
		return
	}
	defer cleanup()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if _, err := w.Write(first); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return
		}
		n, rerr := rest.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

func streamStart(raw string) float64 {
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 200000 {
		return 0
	}
	return v
}

func ffmpegArgs(path string, start float64, copy bool) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-protocol_whitelist", "file,crypto,pipe",
	}
	if start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 3, 64))
	}
	args = append(args, "-i", path, "-map", "0:V:0", "-map", "0:a:0?", "-sn", "-dn")
	if copy {
		args = append(args, "-c", "copy")
	} else {
		args = append(args,
			"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k", "-ac", "2",
		)
	}
	args = append(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	return args
}

func canRemux(path string) bool {
	video := probeCodec(path, "V:0")
	if video != "h264" {
		return false
	}
	audio := probeCodec(path, "a:0")
	return audio == "" || audio == "aac"
}

func startFFmpeg(ctx context.Context, args []string) ([]byte, io.Reader, func(), error) {
	bin := findBin("ffmpeg")
	if bin == "" {
		return nil, nil, func() {}, errNeedFFmpeg
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	hideConsole(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, func() {}, err
	}
	cmd.Stderr = &capBuffer{max: 4096}
	if err := cmd.Start(); err != nil {
		return nil, nil, func() {}, err
	}
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}
	buf := make([]byte, 64*1024)
	n, rerr := readFirst(ctx, stdout, buf, 20*time.Second)
	if n == 0 {
		cleanup()
		if rerr == nil {
			rerr = errNoPicture
		}
		return nil, nil, func() {}, rerr
	}
	return buf[:n], stdout, cleanup, nil
}

func readFirst(ctx context.Context, r io.Reader, buf []byte, d time.Duration) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := r.Read(buf)
		ch <- result{n, err}
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.n, out.err
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
		return 0, errNoPicture
	}
}

func probeDuration(path string) float64 {
	if p := findBin("ffprobe"); p != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, p, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
		hideConsole(cmd)
		if out, err := cmd.Output(); err == nil {
			if d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && d > 0 && d < 1e7 {
				return d
			}
		}
	}
	if p := findBin("ffmpeg"); p != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, p, "-hide_banner", "-protocol_whitelist", "file,crypto", "-i", path)
		hideConsole(cmd)
		out, _ := cmd.CombinedOutput()
		return parseDuration(string(out))
	}
	return 0
}

func probeCodec(path, stream string) string {
	p := findBin("ffprobe")
	if p == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "-v", "error", "-select_streams", stream, "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", path)
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return strings.ToLower(line)
}

func parseDuration(text string) float64 {
	m := durationRE.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	sec, _ := strconv.ParseFloat(m[3], 64)
	return float64(h*3600+min*60) + sec
}

type capBuffer struct {
	buf []byte
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if len(c.buf) < c.max {
		room := c.max - len(c.buf)
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
		} else {
			c.buf = append(c.buf, p...)
		}
	}
	return len(p), nil
}
