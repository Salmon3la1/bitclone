// Command icongen draws the IMAGen icon: two overlapping disk platters (the
// original and its image) on a blue tile. It writes PNGs at the standard
// Windows icon sizes and a multi-size .ico. The same shapes are in
// icon.svg, used by the web interfaces. With -syso it also writes the
// Windows resource object that puts the icon into imagen-usb.exe.
//
//	go run ./tools/icongen -out cmd/imagen-usb/winres -syso cmd/imagen-usb/rsrc_windows_amd64.syso
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

// Geometry on a 256×256 canvas (keep in sync with icon.svg).
const (
	tileInset  = 8.0
	tileRadius = 56.0
	backX      = 90.0 // original disk
	backY      = 90.0
	frontX     = 160.0 // image disk
	frontY     = 160.0
	diskR      = 68.0
	gapR       = 77.0 // dark ring around the front disk
	trackR     = 50.0
	trackW     = 5.0
	hubR       = 19.0
	pinR       = 7.0
)

type rgba struct{ r, g, b, a float64 } // premultiplied, 0..1

func col(hex uint32, a float64) rgba {
	return rgba{float64(hex>>16&255) / 255 * a, float64(hex>>8&255) / 255 * a, float64(hex&255) / 255 * a, a}
}

func over(dst, src rgba) rgba {
	k := 1 - src.a
	return rgba{src.r + dst.r*k, src.g + dst.g*k, src.b + dst.b*k, src.a + dst.a*k}
}

func lerp(a, b rgba, t float64) rgba {
	return rgba{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

func inCircle(x, y, cx, cy, r float64) bool { return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r }

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return inCircle(x, y, cx, cy, r)
}

var (
	top     = col(0x4c9aff, 1)
	bottom  = col(0x1f4fa8, 1)
	white   = col(0xffffff, 1)
	ghost   = col(0xffffff, 0.55)
	track   = col(0xc9dcff, 1)
	hub     = col(0x2f6fd1, 1)
	shadowC = col(0x0b2a66, 0.35)
)

// sample returns the colour at canvas point (x, y) in 0..256.
func sample(x, y float64) rgba {
	var c rgba
	if !inRoundRect(x, y, tileInset, tileInset, 256-tileInset, 256-tileInset, tileRadius) {
		return c
	}
	c = lerp(top, bottom, (y-tileInset)/(256-2*tileInset))
	// Original disk: translucent, with its hub punched out.
	if inCircle(x, y, backX, backY, diskR) && !inCircle(x, y, backX, backY, hubR) && !inCircle(x, y, frontX, frontY, gapR) {
		c = over(c, ghost)
	}
	// Soft shadow under the front disk.
	if inCircle(x, y, frontX+3, frontY+5, diskR+2) {
		c = over(c, shadowC)
	}
	if inCircle(x, y, frontX, frontY, diskR) {
		c = white
		d := math.Hypot(x-frontX, y-frontY)
		if math.Abs(d-trackR) <= trackW/2 {
			c = track
		}
		if d <= hubR {
			c = hub
		}
		if d <= pinR {
			c = white
		}
	}
	return c
}

func render(size int) *image.NRGBA {
	ss := 8
	if size >= 64 {
		ss = 4
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 256 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var acc rgba
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/float64(ss)) * scale
					y := (float64(py) + (float64(sy)+0.5)/float64(ss)) * scale
					s := sample(x, y)
					acc.r += s.r
					acc.g += s.g
					acc.b += s.b
					acc.a += s.a
				}
			}
			n := float64(ss * ss)
			a := acc.a / n
			if a == 0 {
				continue
			}
			u8 := func(v float64) uint8 { return uint8(math.Round(math.Min(1, v/n/a) * 255)) }
			img.SetNRGBA(px, py, color.NRGBA{u8(acc.r), u8(acc.g), u8(acc.b), uint8(math.Round(a * 255))})
		}
	}
	return img
}

// writeICO packs PNG images into a .ico file (PNG entries are supported by
// Windows Vista and later at every size).
func writeICO(path string, sizes []int, pngs [][]byte) error {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	off := 6 + 16*len(pngs)
	for i, p := range pngs {
		dim := uint8(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		b.Write([]byte{dim, dim, 0, 0})
		binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(p)), uint32(off)})
		off += len(p)
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func main() {
	out := flag.String("out", ".", "output directory")
	syso := flag.String("syso", "", "also write a Windows resource object (.syso) with the icon and manifest")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	var pngs [][]byte
	for _, s := range sizes {
		var b bytes.Buffer
		if err := png.Encode(&b, render(s)); err != nil {
			log.Fatal(err)
		}
		pngs = append(pngs, b.Bytes())
		if s == 256 || s == 48 || s == 16 {
			if err := os.WriteFile(filepath.Join(*out, fmt.Sprintf("icon-%d.png", s)), b.Bytes(), 0o644); err != nil {
				log.Fatal(err)
			}
		}
	}
	if err := writeICO(filepath.Join(*out, "icon.ico"), sizes, pngs); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", filepath.Join(*out, "icon.ico"))
	if *syso != "" {
		if err := writeSyso(*syso, sizes, pngs); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", *syso)
	}
}
