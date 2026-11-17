//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// accentCSS gives the desk the Windows accent color in the same shades
// Windows 11 itself uses: a darker one on light surfaces and a lighter one
// on dark surfaces, so text and fills keep their contrast.
func accentCSS() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\Accent`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	palette, typ, err := k.GetBinaryValue("AccentPalette")
	if err != nil || typ != registry.BINARY || len(palette) < 32 {
		return ""
	}
	// Eight RGBA swatches from lightest to darkest; 1 is Light2, 4 is Dark1.
	onLight, onDark := palette[16:19], palette[4:7]
	return fmt.Sprintf(`<style>:root{--accent:#%02x%02x%02x;--sel:rgba(%d,%d,%d,0.14)}`+
		`html[data-theme="dark"]{--accent:#%02x%02x%02x;--sel:rgba(%d,%d,%d,0.26)}</style>`,
		onLight[0], onLight[1], onLight[2], onLight[0], onLight[1], onLight[2],
		onDark[0], onDark[1], onDark[2], onDark[0], onDark[1], onDark[2])
}
