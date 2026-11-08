package media

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func decode(t *testing.T) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(SevenPNG()))
	if err != nil {
		t.Fatalf("seven.png does not decode: %v", err)
	}
	return img
}

func rgb(c color.Color) (r, g, b uint8) {
	R, G, B, _ := c.RGBA()
	return uint8(R >> 8), uint8(G >> 8), uint8(B >> 8)
}

func TestSevenPNGDimensions(t *testing.T) {
	img := decode(t)
	if b := img.Bounds(); b.Dx() != 512 || b.Dy() != 512 {
		t.Fatalf("seven.png is %dx%d, want 512x512", b.Dx(), b.Dy())
	}
}

func TestSevenPNGContent(t *testing.T) {
	img := decode(t)
	is := func(name string, x, y int, want func(r, g, b uint8) bool) {
		t.Helper()
		r, g, b := rgb(img.At(x, y))
		if !want(r, g, b) {
			t.Errorf("%s: pixel (%d,%d) = (%d,%d,%d)", name, x, y, r, g, b)
		}
	}
	white := func(r, g, b uint8) bool { return r > 250 && g > 250 && b > 250 }
	black := func(r, g, b uint8) bool { return r < 10 && g < 10 && b < 10 }
	red := func(r, g, b uint8) bool { return r > 180 && g < 60 && b < 60 }

	is("background, bottom-left corner", 5, 506, white)
	is("background, bottom-right corner", 506, 506, white)
	is("background, left of the bar", 100, 150, white)
	is("red circle, top-left", 64, 64, red)
	is("the top bar of the 7", 300, 150, black)
	is("the stem of the 7", 330, 300, black)
	is("the foot of the 7", 247, 440, black)
	is("white inside the angle of the 7", 200, 300, white)
}

func TestSevenPNGIsACopy(t *testing.T) {
	a := SevenPNG()
	a[0] ^= 0xff
	if b := SevenPNG(); b[0] == a[0] {
		t.Fatal("SevenPNG returned the embedded slice, not a copy")
	}
}
