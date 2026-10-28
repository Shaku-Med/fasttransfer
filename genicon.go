//go:build ignore

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"math"
	"os"
)

func main() {
	sizes := []int{16, 24, 32, 48, 64, 256}
	var blobs [][]byte
	for _, n := range sizes {
		img := render(n)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			panic(err)
		}
		name := "icon" + itoa(n) + ".png"
		if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
			panic(err)
		}
		blobs = append(blobs, buf.Bytes())
	}
	if err := writeICO("icon.ico", sizes, blobs); err != nil {
		panic(err)
	}
}

func render(n int) *image.NRGBA {
	const ss = 8
	const units = 16.0
	half := 0.75
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	sumR := make([]uint32, n*n)
	sumG := make([]uint32, n*n)
	sumB := make([]uint32, n*n)
	sumA := make([]uint32, n*n)
	big := n * ss
	for y := 0; y < big; y++ {
		fy := (float64(y) + 0.5) * units / float64(big)
		oy := y / ss
		for x := 0; x < big; x++ {
			fx := (float64(x) + 0.5) * units / float64(big)
			if !inRound(fx, fy, 16, 16, 3.5) {
				continue
			}
			i := oy*n + x/ss
			sumA[i] += 255
			if onMark(fx, fy, half) {
				sumR[i] += 255
				sumG[i] += 255
				sumB[i] += 255
			} else {
				sumR[i] += 0x12
				sumG[i] += 0x68
				sumB[i] += 0xd2
			}
		}
	}
	samples := uint32(ss * ss)
	for i := range dst.Pix {
		if i%4 != 0 {
			continue
		}
		p := i / 4
		a := sumA[p]
		if a == 0 {
			continue
		}
		dst.Pix[i] = uint8(sumR[p] / samples)
		dst.Pix[i+1] = uint8(sumG[p] / samples)
		dst.Pix[i+2] = uint8(sumB[p] / samples)
		dst.Pix[i+3] = uint8(a / samples)
	}
	return dst
}

func inRound(x, y, w, h, r float64) bool {
	if x < 0 || y < 0 || x > w || y > h {
		return false
	}
	cx, cy := x, y
	if x < r {
		cx = r
	} else if x > w-r {
		cx = w - r
	} else {
		return true
	}
	if y < r {
		cy = r
	} else if y > h-r {
		cy = h - r
	} else {
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func onMark(x, y, half float64) bool {
	segs := [...][4]float64{
		{8, 2.4, 8, 6.8},
		{5.2, 5, 8, 7.8},
		{8, 7.8, 10.8, 5},
		{3.4, 10.4, 3.4, 13.4},
		{3.4, 13.4, 12.6, 13.4},
		{12.6, 13.4, 12.6, 10.4},
	}
	for _, s := range segs {
		if dist(x, y, s[0], s[1], s[2], s[3]) <= half {
			return true
		}
	}
	return false
}

func dist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = ((px-ax)*dx + (py-ay)*dy) / l2
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func writeICO(path string, sizes []int, pngs [][]byte) error {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(pngs)))
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		side := sizes[i]
		var b byte
		if side < 256 {
			b = byte(side)
		}
		buf.WriteByte(b)
		buf.WriteByte(b)
		buf.WriteByte(0)
		buf.WriteByte(0)
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(p)))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(p)
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [8]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
