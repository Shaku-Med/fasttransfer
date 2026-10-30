//go:build !windows

package main

func sendPickedFiles(session string) (int, error) {
	return 0, errNoPhone
}
