package ops

import (
	"context"
	"math"
	"reflect"
	"testing"

	"asteria/internal/img"
)

// Helpers (fxT prefix) -------------------------------------------------------

// fxTHalves returns a w x h opaque grey image with value l for x < split and
// r for the rest.
func fxTHalves(w, h, split int, l, r float64) *img.Image {
	m := solid(w, h, r, r, r, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < split; x++ {
			m.Set(x, y, img.Q(float32(l)), img.Q(float32(l)), img.Q(float32(l)), 0xffff)
		}
	}
	return m
}

// fxTDot returns a black w x h image with one white pixel at (x, y).
func fxTDot(w, h, x, y int) *img.Image {
	m := solid(w, h, 0, 0, 0, 1)
	m.Set(x, y, 0xffff, 0xffff, 0xffff, 0xffff)
	return m
}

// fxTBleed is opaque blue on the left half and transparent red on the right.
func fxTBleed() *img.Image {
	m := solid(16, 8, 1, 0, 0, 0)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			m.Set(x, y, 0, 0, 0xffff, 0xffff)
		}
	}
	return m
}

func fxTMeanLin(m *img.Image, c int) float64 {
	var s float64
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			s += srgbToLin(px(m, x, y)[c])
		}
	}
	return s / float64(m.W*m.H)
}

// fxTStd is the standard deviation of encoded channel c in [x0,x1) x [y0,y1).
func fxTStd(m *img.Image, c, x0, y0, x1, y1 int) float64 {
	var s, s2, n float64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			v := px(m, x, y)[c]
			s += v
			s2 += v * v
			n++
		}
	}
	mean := s / n
	return math.Sqrt(math.Max(0, s2/n-mean*mean))
}

func fxTSame(a, b *img.Image) bool {
	return a.W == b.W && a.H == b.H && reflect.DeepEqual(a.Pix, b.Pix)
}

func fxTNear(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s: got %.4f, want %.4f (tol %.4f)", what, got, want, tol)
	}
}

// fxTBlockAvg is the reference block average: mean of premultiplied linear
// colour and alpha over [x0,x1) x [y0,y1), returned as straight linear colour.
func fxTBlockAvg(m *img.Image, x0, y0, x1, y1 int) (rgb [3]float64, a float64) {
	var s [3]float64
	n := float64((x1 - x0) * (y1 - y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p := px(m, x, y)
			for c := 0; c < 3; c++ {
				s[c] += srgbToLin(p[c]) * p[3]
			}
			a += p[3]
		}
	}
	a /= n
	for c := 0; c < 3; c++ {
		rgb[c] = s[c] / n / a
	}
	return
}

// Tests ----------------------------------------------------------------------

func TestEffectsAllHandlers(t *testing.T) {
	handlers := []string{
		"effect.sharpen", "effect.boxblur", "effect.motionblur", "effect.zoomblur", "effect.noise",
		"effect.grain", "effect.denoise", "effect.pixelate", "effect.mosaic", "effect.halftone",
		"effect.dither", "effect.vignette", "effect.glow", "effect.emboss", "effect.edges",
		"effect.aberration", "effect.scanlines", "effect.glitch",
	}
	for _, name := range handlers {
		in := scene()
		before := append([]uint16(nil), in.Pix...)
		out := apply(t, name, in, Args{})
		if !reflect.DeepEqual(before, in.Pix) {
			t.Fatalf("%s modified its input", name)
		}
		if out.W != in.W || out.H != in.H {
			t.Fatalf("%s changed size to %dx%d", name, out.W, out.H)
		}
		if reflect.DeepEqual(before, out.Pix) {
			t.Fatalf("%s: bare command did not change the picture", name)
		}
	}
}

func TestEffectsVideoFilters(t *testing.T) {
	want := map[string]string{
		"effect.sharpen":  "unsharp=5:5:0.80:5:5:0.0",
		"effect.boxblur":  "boxblur=6:1",
		"effect.noise":    "noise=alls=12:allf=t",
		"effect.pixelate": "pixelize=w=12:h=12",
		"effect.edges":    "edgedetect",
	}
	for name, f := range want {
		h, _ := Lookup(name)
		if h.Video == nil {
			t.Fatalf("%s has no video filter", name)
		}
		got, err := h.Video(Args{})
		if err != nil || got != f {
			t.Fatalf("%s video = %q %v, want %q", name, got, err, f)
		}
	}
	if h, _ := Lookup("effect.vignette"); h.Video == nil {
		t.Fatal("vignette has no video filter")
	}
	for _, name := range []string{"effect.emboss", "effect.glow", "effect.halftone", "effect.dither", "effect.glitch"} {
		if h, _ := Lookup(name); h.Video != nil {
			t.Fatalf("%s should not map to an ffmpeg filter", name)
		}
	}
}

func TestEffectsNoColourBleed(t *testing.T) {
	cases := []struct {
		name string
		args Args
	}{
		{"effect.sharpen", Args{}},
		{"effect.boxblur", Args{"radius": "2"}},
		{"effect.motionblur", Args{"length": "6"}},
		{"effect.zoomblur", Args{}},
		{"effect.pixelate", Args{"size": "5"}},
		{"effect.mosaic", Args{"size": "5"}},
		{"effect.glow", Args{"strength": "100"}},
		{"effect.denoise", Args{}},
		{"effect.aberration", Args{}},
		{"effect.glitch", Args{"intensity": "100"}},
	}
	for _, c := range cases {
		out := apply(t, c.name, fxTBleed(), c.args)
		for y := 0; y < out.H; y++ {
			for x := 0; x < out.W; x++ {
				p := px(out, x, y)
				if p[3] > 0 && p[0] > 0.004 {
					t.Fatalf("%s: red bled into (%d,%d): %v", c.name, x, y, p)
				}
			}
		}
	}
}

func TestSharpen(t *testing.T) {
	in := fxTHalves(32, 8, 16, 0.3, 0.7)
	out := apply(t, "effect.sharpen", in, Args{})
	// Flat areas away from the edge are unchanged.
	if !closePx(px(out, 3, 4), px(in, 3, 4), 0.002) || !closePx(px(out, 28, 4), px(in, 28, 4), 0.002) {
		t.Fatalf("flat areas changed: %v %v", px(out, 3, 4), px(out, 28, 4))
	}
	// Reference: gaussian (sigma 1.2) unsharp mask on the linear step, k = 0.8.
	sigma, k := 1.2, 0.8
	rad := int(math.Ceil(3 * sigma))
	var ker []float64
	var sum float64
	for i := -rad; i <= rad; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		ker = append(ker, v)
		sum += v
	}
	lin := func(x int) float64 {
		x = min(max(x, 0), 31)
		if x < 16 {
			return srgbToLin(0.3)
		}
		return srgbToLin(0.7)
	}
	for x := 12; x <= 19; x++ {
		var b float64
		for i := -rad; i <= rad; i++ {
			b += ker[i+rad] / sum * lin(x+i)
		}
		want := linToSRGB(lin(x) + k*(lin(x)-b))
		fxTNear(t, "sharpen step", px(out, x, 4)[0], want, 0.004)
	}
	if px(out, 15, 4)[0] >= 0.29 || px(out, 16, 4)[0] <= 0.71 {
		t.Fatal("edge contrast did not increase")
	}
	// Amount: 100% is a no-op, more is stronger.
	if apply(t, "effect.sharpen", in, Args{"amount": "100%"}) != in {
		t.Fatal("100% should return the input")
	}
	strong := apply(t, "effect.sharpen", in, Args{"amount": "+200"})
	if px(strong, 16, 4)[0] <= px(out, 16, 4)[0] {
		t.Fatal("larger amount is not stronger")
	}
	if px(apply(t, "effect.sharpen", scene(), Args{}), 5, 28)[3] != px(scene(), 5, 28)[3] {
		t.Fatal("alpha changed")
	}
	if _, err := (Args{"amount": "lots"}).Amount("amount", 1.8, 1, 5); err == nil {
		t.Fatal("accepted a non-number")
	}
}

func TestBoxBlur(t *testing.T) {
	in := fxTDot(21, 11, 10, 5)
	out := apply(t, "effect.boxblur", in, Args{"radius": "2"})
	want := linToSRGB(1.0 / 25)
	for _, p := range [][2]int{{10, 5}, {8, 3}, {12, 7}, {8, 7}} {
		fxTNear(t, "inside box", px(out, p[0], p[1])[0], want, 0.003)
	}
	for _, p := range [][2]int{{13, 5}, {7, 5}, {10, 8}, {10, 2}} {
		fxTNear(t, "outside box", px(out, p[0], p[1])[0], 0, 0.002)
	}
	flat := solid(12, 12, 0.3, 0.5, 0.7, 1)
	if !closePx(px(apply(t, "effect.boxblur", flat, Args{}), 6, 6), px(flat, 6, 6), 0.002) {
		t.Fatal("flat area changed")
	}
	if apply(t, "effect.boxblur", in, Args{"radius": "0"}) != in {
		t.Fatal("radius 0 should be a no-op")
	}
}

func TestMotionBlur(t *testing.T) {
	in := fxTDot(21, 9, 10, 4)
	out := apply(t, "effect.motionblur", in, Args{"length": "8"})
	want := linToSRGB(1.0 / 9)
	for _, x := range []int{6, 8, 10, 12, 14} {
		fxTNear(t, "horizontal tap", px(out, x, 4)[0], want, 0.003)
	}
	fxTNear(t, "past the end", px(out, 5, 4)[0], 0, 0.002)
	fxTNear(t, "other row", px(out, 10, 3)[0], 0, 0.002)

	vin := fxTDot(9, 21, 4, 10)
	vout := apply(t, "effect.motionblur", vin, Args{"length": "8", "angle": "90"})
	for _, y := range []int{6, 10, 14} {
		fxTNear(t, "vertical tap", px(vout, 4, y)[0], want, 0.003)
	}
	fxTNear(t, "vertical other column", px(vout, 3, 10)[0], 0, 0.002)
}

func TestZoomBlur(t *testing.T) {
	in := fxTDot(64, 64, 48, 32)
	out := apply(t, "effect.zoomblur", in, Args{})
	if v := px(out, 48, 32)[0]; v >= 0.95 || v < 0.3 {
		t.Fatalf("dot not spread: %v", v)
	}
	for _, x := range []int{47, 49} {
		if px(out, x, 32)[0] <= 0.1 {
			t.Fatalf("no smear at x=%d", x)
		}
	}
	fxTNear(t, "off the ray", px(out, 44, 32)[0], 0, 0.002)
	fxTNear(t, "perpendicular", px(out, 48, 20)[0], 0, 0.002)
	flat := solid(16, 16, 0.3, 0.5, 0.7, 1)
	if !closePx(px(apply(t, "effect.zoomblur", flat, Args{}), 3, 12), px(flat, 3, 12), 0.002) {
		t.Fatal("flat area changed")
	}
	if apply(t, "effect.zoomblur", in, Args{"strength": "0"}) != in {
		t.Fatal("strength 0 should be a no-op")
	}
}

func TestNoise(t *testing.T) {
	in := solid(64, 64, 0.5, 0.5, 0.5, 1)
	a := apply(t, "effect.noise", in, Args{})
	b := apply(t, "effect.noise", in, Args{})
	if !fxTSame(a, b) {
		t.Fatal("noise is not deterministic")
	}
	want := srgbToLin(0.5)
	for c := 0; c < 3; c++ {
		fxTNear(t, "mean linear light", fxTMeanLin(a, c), want, 0.004)
	}
	sd := fxTStd(a, 0, 0, 0, 64, 64)
	if sd < 0.03 || sd > 0.1 {
		t.Fatalf("std %.4f outside the expected ~12/255 range", sd)
	}
	differ := false
	for x := 0; x < 64 && !differ; x++ {
		p := px(a, x, 0)
		differ = math.Abs(p[0]-p[1]) > 0.005
	}
	if !differ {
		t.Fatal("noise is not per-channel (colour)")
	}
	if fxTStd(apply(t, "effect.noise", in, Args{"amount": "40"}), 0, 0, 0, 64, 64) <= sd {
		t.Fatal("larger amount is not noisier")
	}
	s := scene()
	if px(apply(t, "effect.noise", s, Args{}), 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
	if apply(t, "effect.noise", in, Args{"amount": "0"}) != in {
		t.Fatal("amount 0 should be a no-op")
	}
}

func TestGrain(t *testing.T) {
	in := solid(64, 64, 0.5, 0.5, 0.5, 1)
	a := apply(t, "effect.grain", in, Args{})
	if !fxTSame(a, apply(t, "effect.grain", in, Args{})) {
		t.Fatal("grain is not deterministic")
	}
	fxTNear(t, "mean linear light", fxTMeanLin(a, 0), srgbToLin(0.5), 0.004)
	if fxTStd(a, 0, 0, 0, 64, 64) < 0.003 {
		t.Fatal("no visible grain")
	}
	for y := 0; y < 64; y += 7 {
		for x := 0; x < 64; x += 5 {
			if p := px(a, x, y); p[0] != p[1] || p[1] != p[2] {
				t.Fatalf("grain is not luminance-only at (%d,%d): %v", x, y, p)
			}
		}
	}
	// Neighbouring grain values correlate (slightly blurred) but are not equal.
	var same int
	for x := 1; x < 64; x++ {
		if px(a, x, 10)[0] == px(a, x-1, 10)[0] {
			same++
		}
	}
	if same > 40 {
		t.Fatal("grain is flat")
	}
}

func TestDenoise(t *testing.T) {
	noisy := apply(t, "effect.noise", solid(64, 64, 0.5, 0.5, 0.5, 1), Args{})
	out := apply(t, "effect.denoise", noisy, Args{})
	before := fxTStd(noisy, 0, 8, 8, 56, 56)
	after := fxTStd(out, 0, 8, 8, 56, 56)
	if after > 0.6*before {
		t.Fatalf("noise std %.4f -> %.4f, expected a clear reduction", before, after)
	}
	// Edges survive: a hard step is unchanged, and so are the flats around it.
	step := fxTHalves(32, 16, 16, 0.2, 0.8)
	so := apply(t, "effect.denoise", step, Args{})
	for _, x := range []int{2, 14, 15, 16, 17, 29} {
		if !closePx(px(so, x, 8), px(step, x, 8), 0.01) {
			t.Fatalf("edge changed at x=%d: %v -> %v", x, px(step, x, 8), px(so, x, 8))
		}
	}
	if apply(t, "effect.denoise", step, Args{"strength": "0"}) != step {
		t.Fatal("strength 0 should be a no-op")
	}
}

func TestPixelate(t *testing.T) {
	for _, size := range []int{8, 10} {
		in := scene()
		out := apply(t, "effect.pixelate", in, Args{"size": size})
		for by := 0; by < in.H; by += size {
			for bx := 0; bx < in.W; bx += size {
				x1, y1 := min(bx+size, in.W), min(by+size, in.H)
				rgb, a := fxTBlockAvg(in, bx, by, x1, y1)
				for y := by; y < y1; y++ {
					for x := bx; x < x1; x++ {
						got := px(out, x, y)
						want := [4]float64{linToSRGB(rgb[0]), linToSRGB(rgb[1]), linToSRGB(rgb[2]), a}
						if !closePx(got, want, 0.004) {
							t.Fatalf("size %d pixel (%d,%d): got %v want %v", size, x, y, got, want)
						}
						if got != px(out, bx, by) {
							t.Fatalf("size %d: block at (%d,%d) is not a single colour", size, bx, by)
						}
					}
				}
			}
		}
	}
	sc := scene()
	if apply(t, "effect.pixelate", sc, Args{"size": "1"}) != sc {
		t.Fatal("size 1 should be a no-op")
	}
}

func TestMosaic(t *testing.T) {
	in := scene()
	out := apply(t, "effect.mosaic", in, Args{})
	for by := 0; by < in.H; by += 16 {
		for bx := 0; bx < in.W; bx += 16 {
			rgb, a := fxTBlockAvg(in, bx, by, bx+16, by+16)
			inner := [4]float64{linToSRGB(rgb[0]), linToSRGB(rgb[1]), linToSRGB(rgb[2]), a}
			grout := [4]float64{linToSRGB(rgb[0] * 0.55), linToSRGB(rgb[1] * 0.55), linToSRGB(rgb[2] * 0.55), a}
			if !closePx(px(out, bx+7, by+7), inner, 0.004) {
				t.Fatalf("tile (%d,%d) interior: %v want %v", bx, by, px(out, bx+7, by+7), inner)
			}
			if !closePx(px(out, bx, by+7), grout, 0.004) || !closePx(px(out, bx+7, by), grout, 0.004) {
				t.Fatalf("tile (%d,%d) grout: %v / %v want %v", bx, by, px(out, bx, by+7), px(out, bx+7, by), grout)
			}
		}
	}
}

func TestHalftone(t *testing.T) {
	white := apply(t, "effect.halftone", solid(64, 64, 1, 1, 1, 1), Args{})
	black := apply(t, "effect.halftone", solid(64, 64, 0, 0, 0, 1), Args{})
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if px(white, x, y)[0] < 0.999 {
				t.Fatalf("white input has ink at (%d,%d)", x, y)
			}
			if px(black, x, y)[0] > 0.02 {
				t.Fatalf("black input has a gap at (%d,%d): %v", x, y, px(black, x, y))
			}
		}
	}
	mid := apply(t, "effect.halftone", solid(64, 64, 0.5, 0.5, 0.5, 1), Args{})
	fxTNear(t, "mean light of mid grey", fxTMeanLin(mid, 0), srgbToLin(0.5), 0.07)
	var dark, light int
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			v := px(mid, x, y)
			if v[0] != v[1] || v[1] != v[2] {
				t.Fatal("halftone is not grey")
			}
			if v[0] < 0.05 {
				dark++
			}
			if v[0] > 0.95 {
				light++
			}
		}
	}
	if dark < 200 || light < 100 {
		t.Fatalf("mid grey is not made of dots on paper (dark %d, light %d)", dark, light)
	}
	darker := apply(t, "effect.halftone", solid(64, 64, 0.3, 0.3, 0.3, 1), Args{})
	lighter := apply(t, "effect.halftone", solid(64, 64, 0.7, 0.7, 0.7, 1), Args{})
	if fxTMeanLin(darker, 0) >= fxTMeanLin(lighter, 0) {
		t.Fatal("darker input does not get bigger dots")
	}
	s := scene()
	if px(apply(t, "effect.halftone", s, Args{}), 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
}

func TestDither(t *testing.T) {
	hexes := map[string][]uint32{
		"bw":      {0x000000, 0xffffff},
		"gameboy": {0x0f380f, 0x306230, 0x8bac0f, 0x9bbc0f},
		"cga":     {0x000000, 0x55ffff, 0xff55ff, 0xffffff},
		"pico8": {0x000000, 0x1d2b53, 0x7e2553, 0x008751, 0xab5236, 0x5f574f, 0xc2c3c7, 0xfff1e8,
			0xff004d, 0xffa300, 0xffec27, 0x00e436, 0x29adff, 0x83769c, 0xff77a8, 0xffccaa},
	}
	var web []uint32
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for b := 0; b < 6; b++ {
				web = append(web, uint32(r*51)<<16|uint32(g*51)<<8|uint32(b*51))
			}
		}
	}
	hexes["web"] = web
	in := scene()
	for name, list := range hexes {
		set := map[[3]float64]bool{}
		for _, h := range list {
			set[[3]float64{float64(h >> 16 & 0xff), float64(h >> 8 & 0xff), float64(h & 0xff)}] = true
		}
		out := apply(t, "effect.dither", in, Args{"palette": name})
		for y := 0; y < in.H; y++ {
			for x := 0; x < in.W; x++ {
				p := px(out, x, y)
				if p[3] != px(in, x, y)[3] {
					t.Fatalf("%s: alpha changed at (%d,%d)", name, x, y)
				}
				k := [3]float64{math.Round(p[0] * 255), math.Round(p[1] * 255), math.Round(p[2] * 255)}
				if !set[k] {
					t.Fatalf("%s: (%d,%d) = %v is not a palette colour", name, x, y, k)
				}
				if math.Abs(p[0]*255-k[0]) > 0.01 {
					t.Fatalf("%s: not exact 8-bit palette value %v", name, p)
				}
			}
		}
	}
	// Error diffusion in linear light: the mean light of a flat grey is kept.
	grey := solid(48, 48, 0.5, 0.5, 0.5, 1)
	bw := apply(t, "effect.dither", grey, Args{})
	fxTNear(t, "bw mean light", fxTMeanLin(bw, 0), srgbToLin(0.5), 0.03)
	// A palette colour maps to itself.
	gb := solid(8, 8, 48.0/255, 98.0/255, 48.0/255, 1) // #306230
	if p := px(apply(t, "effect.dither", gb, Args{"palette": "gameboy"}), 3, 3); !closePx(p, px(gb, 3, 3), 0.001) {
		t.Fatalf("palette colour changed: %v", p)
	}
	if apply(t, "effect.dither", in, Args{"palette": "gb"}) == nil {
		t.Fatal("alias failed")
	}
	h, _ := Lookup("effect.dither")
	if _, err := RunPixel(context.Background(), h, in, Args{"palette": "neon"}); err == nil {
		t.Fatal("accepted an unknown palette")
	}
}

func TestVignette(t *testing.T) {
	in := solid(64, 64, 0.6, 0.6, 0.6, 1)
	out := apply(t, "effect.vignette", in, Args{})
	if !closePx(px(out, 32, 32), px(in, 32, 32), 0.002) {
		t.Fatalf("centre changed: %v", px(out, 32, 32))
	}
	// Corner pixel (0,0): normalised radius, smoothstep(0.3, 1) falloff, 50% strength.
	d := (0.5 - 32) / 32.0
	r := math.Hypot(d, d) / math.Sqrt2
	u := (r - 0.3) / 0.7
	f := 1 - 0.5*(u*u*(3-2*u))
	want := linToSRGB(srgbToLin(0.6) * f)
	fxTNear(t, "corner", px(out, 0, 0)[0], want, 0.003)
	if px(out, 0, 0)[0] >= px(out, 20, 20)[0] || px(out, 20, 20)[0] > px(out, 32, 32)[0]+0.001 {
		t.Fatal("falloff is not monotonic toward the corners")
	}
	full := apply(t, "effect.vignette", in, Args{"amount": "100"})
	if px(full, 0, 0)[0] > 0.1 {
		t.Fatalf("100%% corner should be near black, got %v", px(full, 0, 0))
	}
	// Non-square frames reach the same corner darkness.
	wide := apply(t, "effect.vignette", solid(96, 32, 0.6, 0.6, 0.6, 1), Args{})
	if wide.W != 96 || px(wide, 0, 0)[0] > px(out, 32, 32)[0]-0.1 {
		t.Fatalf("wide corner not darkened: %v", px(wide, 0, 0))
	}
	s := scene()
	if px(apply(t, "effect.vignette", s, Args{}), 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
	h, _ := Lookup("effect.vignette")
	if _, err := RunPixel(context.Background(), h, in, Args{"amount": "250"}); err == nil {
		t.Fatal("accepted an out-of-range amount")
	}
}

func TestGlow(t *testing.T) {
	m := solid(64, 64, 0, 0, 0, 1)
	for y := 28; y < 36; y++ {
		for x := 28; x < 36; x++ {
			m.Set(x, y, 0xffff, 0xffff, 0xffff, 0xffff)
		}
	}
	out := apply(t, "effect.glow", m, Args{})
	if px(out, 27, 32)[0] <= 0.02 {
		t.Fatalf("no halo next to the bright square: %v", px(out, 27, 32))
	}
	if px(out, 24, 32)[0] <= px(out, 20, 32)[0] {
		t.Fatal("halo does not fall off with distance")
	}
	fxTNear(t, "far corner", px(out, 2, 2)[0], 0, 0.002)
	fxTNear(t, "bright pixel stays bright", px(out, 31, 31)[0], 1, 0.002)
	dark := solid(32, 32, 0.15, 0.2, 0.25, 1)
	if !closePx(px(apply(t, "effect.glow", dark, Args{}), 10, 10), px(dark, 10, 10), 0.002) {
		t.Fatal("dark image should not glow")
	}
	if px(apply(t, "effect.glow", m, Args{"strength": "100"}), 27, 32)[0] <= px(out, 27, 32)[0] {
		t.Fatal("larger strength is not stronger")
	}
}

func TestEmboss(t *testing.T) {
	flat := apply(t, "effect.emboss", solid(16, 8, 0.3, 0.5, 0.7, 1), Args{})
	fxTNear(t, "flat is mid grey", px(flat, 8, 4)[0], 0.5, 0.002)
	in := fxTHalves(16, 8, 8, 0.4, 0.6)
	out := apply(t, "effect.emboss", in, Args{})
	// Kernel [-1 -1 0; -1 0 1; 0 1 1] on a 0.4 | 0.6 step gives d = 0.4; gain 0.5.
	fxTNear(t, "step left", px(out, 7, 4)[0], 0.7, 0.003)
	fxTNear(t, "step right", px(out, 8, 4)[0], 0.7, 0.003)
	fxTNear(t, "grey", px(out, 7, 4)[1], px(out, 7, 4)[0], 0.0001)
	s := scene()
	if px(apply(t, "effect.emboss", s, Args{}), 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
}

func TestEdges(t *testing.T) {
	flat := apply(t, "effect.edges", solid(16, 8, 0.3, 0.5, 0.7, 1), Args{})
	fxTNear(t, "flat is black", px(flat, 8, 4)[0], 0, 0.002)
	strong := apply(t, "effect.edges", fxTHalves(16, 8, 8, 0.2, 0.8), Args{})
	if px(strong, 7, 4)[0] < 0.99 || px(strong, 8, 4)[0] < 0.99 {
		t.Fatalf("step edge not white: %v %v", px(strong, 7, 4), px(strong, 8, 4))
	}
	fxTNear(t, "away from edge", px(strong, 2, 4)[0], 0, 0.002)
	// Mild step: Sobel on linear luminance, normalised by 4, times gain 2.
	mild := apply(t, "effect.edges", fxTHalves(16, 8, 8, 0.5, 0.55), Args{})
	want := linToSRGB(2 * (srgbToLin(0.55) - srgbToLin(0.5)))
	fxTNear(t, "mild step", px(mild, 7, 4)[0], want, 0.004)
	fxTNear(t, "grey output", px(mild, 7, 4)[1], px(mild, 7, 4)[0], 0.0001)
	s := scene()
	if px(apply(t, "effect.edges", s, Args{}), 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
}

func TestAberration(t *testing.T) {
	in := fxTDot(64, 64, 48, 32)
	out := apply(t, "effect.aberration", in, Args{})
	// Radius at x=48 is 16.5 of 44.5: red shifts ~1.5px outward (+x), blue inward.
	fxTNear(t, "red left the original spot", px(out, 48, 32)[0], 0, 0.01)
	fxTNear(t, "green stays", px(out, 48, 32)[1], 1, 0.002)
	if px(out, 50, 32)[0] < 0.5 || px(out, 49, 32)[0] < 0.3 {
		t.Fatalf("red did not move outward: x=49 %v x=50 %v", px(out, 49, 32), px(out, 50, 32))
	}
	if px(out, 46, 32)[2] < 0.3 {
		t.Fatalf("blue did not move inward: %v", px(out, 46, 32))
	}
	fxTNear(t, "no red inward", px(out, 46, 32)[0], 0, 0.002)
	fxTNear(t, "no blue outward", px(out, 50, 32)[2], 0, 0.002)
	flat := solid(16, 16, 0.3, 0.5, 0.7, 1)
	if !closePx(px(apply(t, "effect.aberration", flat, Args{}), 3, 12), px(flat, 3, 12), 0.002) {
		t.Fatal("flat area changed")
	}
	if apply(t, "effect.aberration", in, Args{"shift": "0"}) != in {
		t.Fatal("shift 0 should be a no-op")
	}
}

func TestScanlines(t *testing.T) {
	in := solid(16, 16, 0.6, 0.6, 0.6, 1)
	out := apply(t, "effect.scanlines", in, Args{})
	lin := srgbToLin(0.6)
	for y := 0; y < 8; y++ {
		line := 1.0
		if (y/2)%2 == 1 {
			line = 0.65
		}
		for x := 0; x < 6; x++ {
			for c := 0; c < 3; c++ {
				mask := 0.88
				if x%3 == c {
					mask = 1
				}
				fxTNear(t, "scanline pixel", px(out, x, y)[c], linToSRGB(lin*line*mask), 0.003)
			}
		}
	}
	s := scene()
	if px(apply(t, "effect.scanlines", s, Args{}), 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
}

func TestGlitch(t *testing.T) {
	in := img.New(64, 48)
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			in.Set(x, y, uint16(x*1000), uint16(x*1000), uint16(x*1000), 0xffff)
		}
	}
	a := apply(t, "effect.glitch", in, Args{"intensity": "100"})
	if !fxTSame(a, apply(t, "effect.glitch", in, Args{"intensity": "100"})) {
		t.Fatal("glitch is not deterministic")
	}
	shifted, split := 0, false
	for y := 0; y < 48; y++ {
		k := -1
		for s := 0; s < 64 && k < 0; s++ {
			ok := true
			for x := 0; x < 64 && ok; x++ {
				ok = math.Abs(px(a, x, y)[1]-px(in, (x+s)%64, y)[1]) < 0.004
			}
			if ok {
				k = s
			}
		}
		if k < 0 {
			t.Fatalf("row %d is not a circular shift of the input", y)
		}
		if k != 0 {
			shifted++
		}
		for x := 0; x < 64; x++ {
			if math.Abs(px(a, x, y)[0]-px(a, x, y)[1]) > 0.004 {
				split = true
			}
		}
	}
	if shifted == 0 || !split {
		t.Fatalf("expected displaced rows and an RGB split (shifted %d, split %v)", shifted, split)
	}
	if apply(t, "effect.glitch", in, Args{"intensity": "0"}) != in {
		t.Fatal("intensity 0 should be a no-op")
	}
}
