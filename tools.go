package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// bundledToolsDir is where this process extracted yt-dlp.exe and ffmpeg.exe.
// Empty until extractBundledTools runs, or when this build has no embedded copies.
var bundledToolsDir string

var (
	toolsBeside = func() string {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		return filepath.Dir(exe)
	}
	toolsFallback = func() string {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("USERPROFILE")
		}
		if base == "" {
			base = os.TempDir()
		}
		return filepath.Join(base, "FastTransfer", "tools")
	}
)

// extractBundledTools writes the embedded helpers once. An existing file is left
// alone, and nothing is fetched from the internet.
func extractBundledTools() string {
	if len(embeddedYT) == 0 && len(embeddedFF) == 0 {
		return ""
	}
	dir := chooseToolsDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	if err := writeToolIfMissing(filepath.Join(dir, toolFile("yt-dlp")), embeddedYT); err != nil {
		return ""
	}
	if err := writeToolIfMissing(filepath.Join(dir, toolFile("ffmpeg")), embeddedFF); err != nil {
		return ""
	}
	bundledToolsDir = dir
	return dir
}

func chooseToolsDir() string {
	if dir := toolsBeside(); dir != "" && dirWritable(dir) {
		return dir
	}
	return toolsFallback()
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".ft-write-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func writeToolIfMissing(path string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

func toolFile(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func bundledTool(name string) string {
	if bundledToolsDir == "" {
		return ""
	}
	candidate := filepath.Join(bundledToolsDir, toolFile(name))
	st, err := os.Stat(candidate)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return ""
	}
	return candidate
}
