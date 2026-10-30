//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed bt/btopp.exe
var btoppBytes []byte

type btDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type btListResult struct {
	OK      bool       `json:"ok"`
	Message string     `json:"message"`
	Devices []btDevice `json:"devices"`
}

func btoppPath() (string, error) {
	dir := filepath.Join(os.TempDir(), "FastTransfer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "btopp.exe")
	existing, err := os.ReadFile(dest)
	if err == nil && bytes.Equal(existing, btoppBytes) {
		return dest, nil
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, btoppBytes, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		alt := filepath.Join(dir, "btopp-run.exe")
		_ = os.Remove(alt)
		if err2 := os.Rename(tmp, alt); err2 != nil {
			return "", err
		}
		return alt, nil
	}
	return dest, nil
}

func runBT(timeout time.Duration, args []string, onLine func([]byte)) error {
	return runBTContext(context.Background(), timeout, args, onLine)
}

func runBTContext(parent context.Context, timeout time.Duration, args []string, onLine func([]byte)) error {
	path, err := btoppPath()
	if err != nil {
		return err
	}
	ctx := parent
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if onLine != nil {
			onLine(append([]byte(nil), sc.Bytes()...))
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return waitErr
}

func listBluetoothPhones() (btListResult, error) {
	out := btListResult{Message: "No paired phone was found.", Devices: []btDevice{}}
	var got *btListResult
	err := runBT(20*time.Second, []string{"list"}, func(line []byte) {
		var parsed btListResult
		if json.Unmarshal(line, &parsed) == nil && (parsed.Message != "" || parsed.Devices != nil || parsed.OK) {
			got = &parsed
		}
	})
	if got != nil {
		if got.Devices == nil {
			got.Devices = []btDevice{}
		}
		if strings.TrimSpace(got.Message) == "" && len(got.Devices) == 0 {
			got.Message = "No paired phone was found."
		}
		return *got, nil
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		out.Message = "No paired phone was found."
	}
	return out, nil
}

func pickFilesForBluetooth() ([]string, error) {
	paths, err := pickFiles()
	if errors.Is(err, errPickCancelled) {
		return nil, nil
	}
	return paths, err
}

func startBluetoothSend(id string, rels []string, outside []string) {
	go func() {
		paths, err := bluetoothPaths(rels, outside)
		if err != nil {
			emitBt(map[string]any{"event": "error", "message": err.Error()})
			emitBt(map[string]any{"event": "finished", "ok": false})
			return
		}
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, "\r\n") {
			emitBt(map[string]any{"event": "error", "message": "Choose a phone."})
			emitBt(map[string]any{"event": "finished", "ok": false})
			return
		}
		args := []string{"send", "--device", id}
		for _, path := range paths {
			args = append(args, "--file", path)
		}
		sawError := false
		err = runBT(0, args, func(line []byte) {
			var m map[string]any
			if json.Unmarshal(line, &m) != nil {
				return
			}
			if ev, _ := m["event"].(string); ev == "error" {
				sawError = true
			}
			emitBt(m)
		})
		if err != nil && !sawError {
			emitBt(map[string]any{"event": "error", "message": "The phone refused the file."})
		}
		emitBt(map[string]any{"event": "finished", "ok": err == nil && !sawError})
	}()
}

func bluetoothPaths(rels, outside []string) ([]string, error) {
	var out []string
	for _, rel := range rels {
		full, _, _, err := regularInside(rel)
		if err != nil {
			return nil, errors.New("That file could not be sent.")
		}
		out = append(out, full)
	}
	for _, path := range outside {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("That file could not be sent.")
		}
		out = append(out, path)
	}
	if len(out) == 0 {
		return nil, errors.New("Select a file")
	}
	return out, nil
}

var (
	btScanMu     sync.Mutex
	btScanCancel context.CancelFunc
)

func startBluetoothScan() {
	stopBluetoothScan()
	ctx, cancel := context.WithCancel(context.Background())
	btScanMu.Lock()
	btScanCancel = cancel
	btScanMu.Unlock()
	go func() {
		sawDone := false
		err := runBTContext(ctx, 25*time.Second, []string{"scan"}, func(line []byte) {
			var m map[string]any
			if json.Unmarshal(line, &m) != nil {
				return
			}
			if ev, _ := m["event"].(string); ev == "scan-done" {
				sawDone = true
			}
			emitBt(m)
		})
		if ctx.Err() != nil {
			return
		}
		if !sawDone {
			if err != nil {
				emitBt(map[string]any{"event": "error", "message": "Bluetooth is off."})
			}
			emitBt(map[string]any{"event": "scan-done"})
		}
	}()
}

func stopBluetoothScan() {
	btScanMu.Lock()
	cancel := btScanCancel
	btScanCancel = nil
	btScanMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func emitBt(v any) {
	if appView == nil {
		return
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	script := "if (window.onBluetoothProgress) window.onBluetoothProgress(" + string(payload) + ");"
	appView.Dispatch(func() {
		appView.Eval(script)
	})
}
