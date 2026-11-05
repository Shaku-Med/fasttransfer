//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killer := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	hideConsole(killer)
	_ = killer.Run()
	_ = cmd.Process.Kill()
}
