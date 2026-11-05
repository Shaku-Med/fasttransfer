package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractBundledToolsPrefersWritableFolder(t *testing.T) {
	beside := t.TempDir()
	fallback := t.TempDir()
	oldBeside, oldFallback := toolsBeside, toolsFallback
	oldDir := bundledToolsDir
	oldYT, oldFF := embeddedYT, embeddedFF
	t.Cleanup(func() {
		toolsBeside, toolsFallback = oldBeside, oldFallback
		bundledToolsDir = oldDir
		embeddedYT, embeddedFF = oldYT, oldFF
	})
	toolsBeside = func() string { return beside }
	toolsFallback = func() string { return fallback }
	embeddedYT = []byte("MZ-yt-dlp-test")
	embeddedFF = []byte("MZ-ffmpeg-test")

	got := extractBundledTools()
	if got != beside {
		t.Fatalf("dir %s", got)
	}
	yt, err := os.ReadFile(filepath.Join(beside, "yt-dlp.exe"))
	if err != nil || string(yt) != "MZ-yt-dlp-test" {
		t.Fatalf("yt %q %v", yt, err)
	}
	ff, err := os.ReadFile(filepath.Join(beside, "ffmpeg.exe"))
	if err != nil || string(ff) != "MZ-ffmpeg-test" {
		t.Fatalf("ff %q %v", ff, err)
	}
	if _, err := os.Stat(filepath.Join(fallback, "yt-dlp.exe")); !os.IsNotExist(err) {
		t.Fatal("wrote the fallback too")
	}

	// A second launch leaves the files already on disk.
	embeddedYT = []byte("MZ-replaced")
	if extractBundledTools() != beside {
		t.Fatal("second extract")
	}
	yt, err = os.ReadFile(filepath.Join(beside, "yt-dlp.exe"))
	if err != nil || string(yt) != "MZ-yt-dlp-test" {
		t.Fatalf("replaced an existing helper: %q", yt)
	}
}

func TestExtractFallsBackWhenBesideIsNotWritable(t *testing.T) {
	fallback := t.TempDir()
	oldBeside, oldFallback := toolsBeside, toolsFallback
	oldDir := bundledToolsDir
	oldYT, oldFF := embeddedYT, embeddedFF
	t.Cleanup(func() {
		toolsBeside, toolsFallback = oldBeside, oldFallback
		bundledToolsDir = oldDir
		embeddedYT, embeddedFF = oldYT, oldFF
	})
	toolsBeside = func() string { return filepath.Join(t.TempDir(), "missing", "nope") }
	toolsFallback = func() string { return fallback }
	embeddedYT = []byte("MZ-yt")
	embeddedFF = []byte("MZ-ff")
	if got := extractBundledTools(); got != fallback {
		t.Fatalf("dir %s", got)
	}
	if _, err := os.Stat(filepath.Join(fallback, "ffmpeg.exe")); err != nil {
		t.Fatal(err)
	}
}

func TestSocialArgsUsesExtractedFFmpegWithoutPath(t *testing.T) {
	dir := t.TempDir()
	ff := filepath.Join(dir, "ffmpeg.exe")
	if err := os.WriteFile(ff, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := bundledToolsDir
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() {
		bundledToolsDir = old
		_ = os.Setenv("PATH", oldPath)
	})
	bundledToolsDir = dir
	_ = os.Setenv("PATH", "")
	got := findBin("ffmpeg")
	if got != ff {
		t.Fatalf("findBin %q", got)
	}
	page := "https://youtu.be/abcdefghijk"
	args := socialArgs(page, filepath.Join(dir, "out"), got)
	if args[len(args)-1] != page {
		t.Fatal(args)
	}
	found := false
	for i, arg := range args {
		if arg == "--ffmpeg-location" && i+1 < len(args) && args[i+1] == ff {
			found = true
		}
	}
	if !found || strings.Contains(strings.Join(args[:len(args)-1], "\n"), "yt-dlp") {
		t.Fatal(args)
	}
}
