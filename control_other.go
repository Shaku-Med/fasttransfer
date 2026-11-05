//go:build !windows

package main

import "image"

const controlSupported = false

func injectEvents([]ctlEvent) error { return errNoControl }

func captureScreen(int) (*image.RGBA, error) { return nil, errNoControl }

func powerAction(string) {}

func raiseDesk() {}

func caretBox() (x, y, w, h float64, ok bool) { return 0, 0, 0, 0, false }

func pointerAt() (x, y float64, ok bool) { return 0, 0, false }

func stayAwake() {}

func curtainUp() error { return errNoControl }

func curtainDown(bool) {}

var onCurtainTouch = func() {}
