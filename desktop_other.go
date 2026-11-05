//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

var instanceLock *os.File

func claimApp() bool {
	if appDir == "" {
		return true
	}
	f, err := os.OpenFile(filepath.Join(appDir, "fasttransfer.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return false
	}
	instanceLock = f
	return true
}

func focusExisting() bool {
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Chromium", "Brave Browser"} {
			script := fmt.Sprintf(`tell application "System Events"
				if exists process %q then
					set frontmost of process %q to true
				else
					error "not running"
				end if
			end tell`, app, app)
			if exec.Command("osascript", "-e", script).Run() == nil {
				return true
			}
		}
	default:
		if p, err := exec.LookPath("wmctrl"); err == nil && exec.Command(p, "-a", "FastTransfer").Run() == nil {
			return true
		}
		if p, err := exec.LookPath("xdotool"); err == nil && exec.Command(p, "search", "--name", "FastTransfer", "windowactivate").Run() == nil {
			return true
		}
	}
	return false
}

func report(text string) {
	fmt.Fprintln(os.Stderr, text)
}

func stopWindow() {}

func runApp(pageURL string) {
	bin := findChromium()
	if bin == "" {
		_ = openDefaultBrowser(pageURL)
		fmt.Fprintln(os.Stderr, "FastTransfer is open in your browser. Use Quit on the page to stop it.")
		<-stopCh
		return
	}
	// An app-mode window still accepts the browser's own DevTools shortcuts.
	// There is no real flag that turns them off; --disable-dev-tools is not one.
	cmd := exec.Command(bin,
		"--app="+pageURL,
		"--user-data-dir="+browserProfile(),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		"--disable-background-mode",
		"--autoplay-policy=no-user-gesture-required",
	)
	if err := cmd.Start(); err != nil {
		_ = openDefaultBrowser(pageURL)
		<-stopCh
		return
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-stopCh:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-exited
	}
}

func findChromium() string {
	var candidates []string
	if runtime.GOOS == "darwin" {
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		for _, candidate := range candidates {
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() {
				return candidate
			}
		}
		return ""
	}
	candidates = []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
		"microsoft-edge-stable",
		"brave-browser",
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return ""
}

func browserProfile() string {
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "FastTransfer", "browser")
	default:
		if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
			base = filepath.Join(cache, "FastTransfer", "browser")
		} else {
			base = filepath.Join(os.Getenv("HOME"), ".cache", "FastTransfer", "browser")
		}
	}
	_ = os.MkdirAll(base, 0o755)
	return base
}

func openDefaultBrowser(pageURL string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", pageURL).Start()
	}
	return exec.Command("xdg-open", pageURL).Start()
}
