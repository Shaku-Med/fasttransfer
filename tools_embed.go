//go:build bundletools

package main

import _ "embed"

// Official Windows builds placed in tools/ before compiling.
// They are not downloaded again when the app runs.

//go:embed tools/yt-dlp.exe
var embeddedYT []byte

//go:embed tools/ffmpeg.exe
var embeddedFF []byte
