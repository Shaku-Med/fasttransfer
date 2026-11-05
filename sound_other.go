//go:build !windows || 386

package main

const loopbackSupported = false

func captureSound(_ <-chan struct{}, ready chan<- soundStart, _ func([]byte)) {
	ready <- soundStart{err: errNoControl}
}

func muteSpeakers(bool) (bool, error) { return false, errNoControl }
