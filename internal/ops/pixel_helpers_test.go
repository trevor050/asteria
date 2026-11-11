package ops

import (
	"context"
	"math"
	"testing"

	"asteria/internal/img"
)

// Shared helpers for image-skill tests. Build inputs in code so expected
// values are known exactly, run handlers through RunPixel (what the engine
// does), and compare against independently computed values.

// scene is a 48x32 test picture: a horizontal grey ramp on top, saturated
// colour patches in the middle, and a half-transparent band at the bottom.
func scene() *img.Image {
	m := img.New(48, 32)
	for y := 0; y < 32; y++ {
		for x := 0; x < 48; x++ {
			switch {
			case y < 10:
				v := uint16(x * 65535 / 47)
				m.Set(x, y, v, v, v, 0xffff)
			case y < 22:
				pal := [][3]uint16{{0xe000, 0x3000, 0x2000}, {0x2000, 0xc000, 0x4000}, {0x3000, 0x5000, 0xe000}, {0xf000, 0xd000, 0x1000}}
				c := pal[(x/12)%4]
				m.Set(x, y, c[0], c[1], c[2], 0xffff)
			default:
				m.Set(x, y, 0x8000, 0x4000, 0xc000, 0x8000)
			}
		}
	}
	return m
}

// solid returns a w x h image of one straight-alpha colour (0..1 floats).
func solid(w, h int, r, g, b, a float64) *img.Image {
	m := img.New(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, img.Q(float32(r)), img.Q(float32(g)), img.Q(float32(b)), img.Q(float32(a)))
		}
	}
	return m
}

// run applies a registered Pixel handler.
func apply(t *testing.T, handler string, m *img.Image, args Args) *img.Image {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok || h.Pixel == nil {
		t.Fatalf("no pixel handler %s", handler)
	}
	out, err := RunPixel(context.Background(), h, m, args)
	if err != nil {
		t.Fatalf("%s %v: %v", handler, args, err)
	}
	return out
}

// px returns pixel (x,y) as 0..1 floats.
func px(m *img.Image, x, y int) [4]float64 {
	r, g, b, a := m.At(x, y)
	return [4]float64{float64(r) / 65535, float64(g) / 65535, float64(b) / 65535, float64(a) / 65535}
}

// close reports whether two pixels match within tol per channel.
func closePx(a, b [4]float64, tol float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > tol {
			return false
		}
	}
	return true
}

// srgbToLin and linToSRGB are reference transfer functions, written
// independently of the img package so tests don't trust the code under test.
func srgbToLin(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linToSRGB(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// oklch returns OKLab lightness and chroma of an sRGB-encoded pixel, using
// the published matrices (reference implementation).
func oklch(p [4]float64) (L, C float64) {
	r, g, b := srgbToLin(p[0]), srgbToLin(p[1]), srgbToLin(p[2])
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	L = 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	A := 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	B := 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	return L, math.Hypot(A, B)
}
