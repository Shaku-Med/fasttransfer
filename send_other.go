//go:build !windows

package main

func sendPickedFiles(devices []string, all bool) (int, error) {
	return 0, errNoPhone
}
