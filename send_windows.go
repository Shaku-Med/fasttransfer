//go:build windows

package main

import "errors"

func sendPickedFiles(devices []string, all bool) (int, error) {
	paths, err := pickFiles()
	if err != nil {
		if errors.Is(err, errPickCancelled) {
			return 0, nil
		}
		return 0, err
	}
	if len(paths) == 0 {
		return 0, nil
	}
	return queueOutside(devices, all, paths)
}
