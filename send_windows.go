//go:build windows

package main

import "errors"

func sendPickedFiles(devices []string, all bool, when string) (map[string]any, error) {
	paths, err := pickFiles()
	if err != nil {
		if errors.Is(err, errPickCancelled) {
			return map[string]any{"count": 0, "devices": 0, "scheduled": 0}, nil
		}
		return nil, err
	}
	if len(paths) == 0 {
		return map[string]any{"count": 0, "devices": 0, "scheduled": 0}, nil
	}
	return queueOutside(devices, all, paths, when)
}
