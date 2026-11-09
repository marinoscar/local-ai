//go:build ignore

// gen_seven.go draws seven.png, the test photo of drill check 6 (drill.md section 4.2): a 512x512
// white image with a large black digit 7 and a red circle in the top-left corner. It draws everything
// itself from the standard library, so the file has no third-party origin (see README.md).
//
// Run from this directory: go run gen_seven.go
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

const size = 512

var (
	white = color.RGBA{255, 255, 255, 255}
	black = color.RGBA{0, 0, 0, 255}
	red   = color.RGBA{220, 20, 20, 255}
)

type pt struct{ x, y float64 }

// inside reports whether p is inside the convex or concave polygon (even-odd rule).
func inside(poly []pt, p pt) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

func main() {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, white)
		}
	}
	// The 7: a thick top bar and a diagonal stem from its right end down to the lower left.
	seven := []pt{{150, 120}, {450, 120}, {450, 190}, {290, 450}, {205, 450}, {365, 190}, {150, 190}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if inside(seven, pt{float64(x) + 0.5, float64(y) + 0.5}) {
				img.SetRGBA(x, y, black)
			}
		}
	}
	// The red circle, top-left.
	const cx, cy, r = 64.0, 64.0, 44.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, red)
			}
		}
	}
	f, err := os.Create("seven.png")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}
