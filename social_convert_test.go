package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlayablePlan(t *testing.T) {
	cases := []struct {
		name         string
		p            mediaProbe
		ext, encoder string
		convert      bool
		outExt       string
		reencode     bool
	}{
		{"h264 mp4 stays", mediaProbe{video: "h264", audio: "aac"}, ".mp4", "libx264", false, "", false},
		{"vp9 in mp4 is re-encoded", mediaProbe{video: "vp9", audio: "aac"}, ".mp4", "libx264", true, ".mp4", true},
		{"av1 webm with opus", mediaProbe{video: "av1", audio: "opus"}, ".webm", "libopenh264", true, ".mp4", true},
		{"h264 in mkv is only rewrapped", mediaProbe{video: "h264", audio: "aac"}, ".mkv", "libx264", true, ".mp4", false},
		{"opus song becomes m4a", mediaProbe{audio: "opus"}, ".webm", "libx264", true, ".m4a", false},
		{"mp3 stays", mediaProbe{audio: "mp3"}, ".mp3", "", false, "", false},
		{"picture stays", mediaProbe{}, ".jpg", "libx264", false, "", false},
		{"no encoder keeps the original", mediaProbe{video: "vp9", audio: "aac"}, ".mp4", "", false, "", false},
	}
	for _, c := range cases {
		args, ext := playablePlan(c.p, c.ext, c.encoder)
		if (args != nil) != c.convert || ext != c.outExt {
			t.Errorf("%s: got %v %q", c.name, args, ext)
			continue
		}
		joined := strings.Join(args, " ")
		if c.reencode != strings.Contains(joined, "-c:v "+c.encoder) {
			t.Errorf("%s: re-encode %v, args %v", c.name, c.reencode, args)
		}
	}
}

// Runs the real ffmpeg on a small VP9 clip, the same kind Instagram serves.
func TestMakePlayableTurnsVP9IntoH264(t *testing.T) {
	ff := findBin("ffmpeg")
	if ff == "" {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.webm")
	make := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=322x240:rate=15:duration=2",
		"-f", "lavfi", "-i", "sine=duration=2", "-c:v", "libvpx-vp9", "-deadline", "realtime", "-c:a", "libopus", "-shortest", src)
	if out, err := make.CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make a VP9 sample: %s", out)
	}
	var last float64
	got, err := makePlayable(context.Background(), ff, dir, "clip.webm", func(p float64) { last = p }, func(*exec.Cmd) {})
	if err != nil {
		t.Fatal(err)
	}
	if got != "clip.mp4" {
		t.Fatalf("got %q", got)
	}
	p := probeMedia(context.Background(), ff, filepath.Join(dir, got))
	if p.video != "h264" || p.audio != "aac" {
		t.Fatalf("converted file holds %+v", p)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("the unplayable original should be gone")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".converting-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if last <= 0 {
		t.Fatal("no progress was reported")
	}
}
