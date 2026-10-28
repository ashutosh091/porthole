package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// trayIcon draws a 44x44 globe (22pt @2x) as a black template image; macOS tints it for light and dark menu bars.
func trayIcon() []byte {
	const size = 44
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size-1) / 2
	r := 17.0
	stroke := 2.6
	// coverage approximates anti-aliasing: 1 inside the stroke, fading over one pixel.
	coverage := func(d float64) float64 { return math.Max(0, math.Min(1, stroke/2+0.5-math.Abs(d))) }
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			dist := math.Hypot(dx, dy)
			a := coverage(dist - r) // outer circle
			if dist < r {
				a = math.Max(a, coverage(dx))                                       // central meridian
				a = math.Max(a, coverage(dy))                                       // equator
				a = math.Max(a, coverage(math.Hypot(dx/0.5, dy)-r)*0.9)             // inner meridian ellipse
				a = math.Max(a, coverage(math.Abs(dy)-r*0.5)*0.8*boolf(dist < r-2)) // two latitude lines
			}
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{A: uint8(a * 255)})
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
