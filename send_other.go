//go:build !windows

package main

func sendPickedFiles(devices []string, all bool, when string) (map[string]any, error) {
	return map[string]any{"count": 0, "devices": 0, "scheduled": 0, "error": errNoPhone.Error()}, nil
}
