//go:build !bundletools

package main

// Empty unless this build was made with -tags bundletools.
var (
	embeddedYT []byte
	embeddedFF []byte
)
