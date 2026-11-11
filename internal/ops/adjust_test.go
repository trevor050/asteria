package ops

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"asteria/internal/img"
)

func TestSaturate(t *testing.T) {
	in := solid(4, 4, 0.55, 0.40, 0.35, 1) // a muted, in-gamut colour
	L0, C0 := oklch(px(in, 0, 0))

	// Bare command: +40% chroma, same lightness.
	out := apply(t, "adjust.saturate", in, Args{})
	L1, C1 := oklch(px(out, 0, 0))
	if math.Abs(L1-L0) > 0.003 || math.Abs(C1/C0-1.4) > 0.02 {
		t.Fatalf("default: L %.4f->%.4f C %.4f->%.4f (want x1.4)", L0, L1, C0, C1)
	}
	// "150%" is an absolute level, "-30" a relative change.
	for in2, want := range map[string]float64{"150%": 1.5, "-30": 0.7, "+20": 1.2, "2x": 2} {
		_, C := oklch(px(apply(t, "adjust.saturate", in, Args{"amount": in2}), 0, 0))
		if math.Abs(C/C0-want) > 0.03 {
			t.Fatalf("%s: chroma ratio %.3f, want %.2f", in2, C/C0, want)
		}
	}
	// -100 is neutral grey of the same lightness.
	g := px(apply(t, "adjust.saturate", in, Args{"amount": "-100"}), 0, 0)
	if math.Abs(g[0]-g[1]) > 0.002 || math.Abs(g[1]-g[2]) > 0.002 {
		t.Fatalf("-100 not grey: %v", g)
	}
	// Greys and alpha are untouched.
	s := scene()
	o := apply(t, "adjust.saturate", s, Args{"amount": "+80"})
	if !closePx(px(o, 20, 3), px(s, 20, 3), 0.002) {
		t.Fatalf("grey changed: %v -> %v", px(s, 20, 3), px(o, 20, 3))
	}
	if px(o, 5, 28)[3] != px(s, 5, 28)[3] {
		t.Fatal("alpha changed")
	}
	if _, err := (Args{"amount": "lots"}).Amount("amount", 1, 0, 4); err == nil {
		t.Fatal("accepted a non-number")
	}
}

func TestAmountParsing(t *testing.T) {
	for in, want := range map[string]float64{"150%": 1.5, "30%": 0.3, "+50": 1.5, "-30": 0.7, "50": 1.5, "1.5x": 1.5, " +10 % ": 1.1} {
		got, err := (Args{"a": in}).Amount("a", 1, 0, 10)
		if err != nil || math.Abs(got-want) > 1e-9 {
			t.Fatalf("%q -> %v %v, want %v", in, got, err, want)
		}
	}
	if got, _ := (Args{"a": 40.0}).Amount("a", 1, 0, 10); math.Abs(got-1.4) > 1e-9 {
		t.Fatalf("JSON number 40 -> %v, want 1.4 (relative)", got)
	}
	if got, _ := (Args{"a": "-500"}).Amount("a", 1, 0, 10); got != 0 {
		t.Fatalf("clamp -> %v", got)
	}
}

// ---------------------------------------------------------------------------
// Colour and tone family. References are written independently of the code
// under test (hand-computed numbers, or formulas using the reference
// transfer functions in pixel_helpers_test.go).
// ---------------------------------------------------------------------------

// adjTLab is a reference OKLab conversion of an sRGB-encoded pixel.
func adjTLab(p [4]float64) (L, A, B float64) {
	r, g, b := srgbToLin(p[0]), srgbToLin(p[1]), srgbToLin(p[2])
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	L = 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	A = 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	B = 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	return
}

// adjTRow builds a one-row image from straight-alpha 0..1 pixels.
func adjTRow(ps ...[4]float64) *img.Image {
	m := img.New(len(ps), 1)
	for x, p := range ps {
		m.Set(x, 0, img.Q(float32(p[0])), img.Q(float32(p[1])), img.Q(float32(p[2])), img.Q(float32(p[3])))
	}
	return m
}

func adjTErr(t *testing.T, handler string, args Args) {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok {
		t.Fatalf("no handler %s", handler)
	}
	if _, err := RunPixel(context.Background(), h, solid(2, 2, 0.5, 0.4, 0.3, 1), args); err == nil {
		t.Fatalf("%s %v: expected an error", handler, args)
	}
}

func adjTWant(t *testing.T, label string, got, want [4]float64, tol float64) {
	t.Helper()
	if !closePx(got, want, tol) {
		t.Fatalf("%s: got %.4f, want %.4f", label, got, want)
	}
}

func adjTAlphaKept(t *testing.T, handler string, args Args) {
	t.Helper()
	s := scene()
	o := apply(t, handler, s, args)
	for _, pt := range [][2]int{{5, 28}, {30, 15}, {2, 2}} {
		if px(o, pt[0], pt[1])[3] != px(s, pt[0], pt[1])[3] {
			t.Fatalf("%s changed alpha at %v", handler, pt)
		}
	}
}

func TestVibrance(t *testing.T) {
	in := solid(2, 2, 0.55, 0.40, 0.35, 1)
	L0, A0, B0 := adjTLab(px(in, 0, 0))
	c0 := math.Hypot(A0, B0)
	wantScale := func(a float64) float64 { return 1 + a*(1-math.Min(c0/0.32, 1)) }
	for args, a := range map[string]float64{"": 0.4, "+80": 0.8, "-50": -0.5, "115%": 0.15} {
		out := apply(t, "adjust.vibrance", in, Args{"amount": args})
		L1, A1, B1 := adjTLab(px(out, 0, 0))
		if math.Abs(L1-L0) > 0.004 || math.Abs(math.Hypot(A1, B1)/c0-wantScale(a)) > 0.03 {
			t.Fatalf("amount %q: L %.4f->%.4f chroma ratio %.3f, want %.3f", args, L0, L1, math.Hypot(A1, B1)/c0, wantScale(a))
		}
	}
	// Vivid colours move less than muted ones; greys do not move.
	vivid := solid(2, 2, 0.85, 0.15, 0.15, 1)
	_, Av, Bv := adjTLab(px(vivid, 0, 0))
	_, Av1, Bv1 := adjTLab(px(apply(t, "adjust.vibrance", vivid, Args{}), 0, 0))
	if r := math.Hypot(Av1, Bv1) / math.Hypot(Av, Bv); r > 1.15 {
		t.Fatalf("vivid red boosted too much: x%.3f", r)
	}
	s := scene()
	o := apply(t, "adjust.vibrance", s, Args{})
	if !closePx(px(o, 20, 3), px(s, 20, 3), 0.002) {
		t.Fatal("grey changed")
	}
	adjTAlphaKept(t, "adjust.vibrance", Args{})
	adjTErr(t, "adjust.vibrance", Args{"amount": "lots"})
}

func TestBrightness(t *testing.T) {
	in := [4]float64{0.5, 0.3, 0.2, 1}
	lin := func(k float64) [4]float64 {
		return [4]float64{linToSRGB(srgbToLin(in[0]) * k), linToSRGB(srgbToLin(in[1]) * k), linToSRGB(srgbToLin(in[2]) * k), 1}
	}
	for amount, k := range map[string]float64{"": 1.2, "+50": 1.5, "50%": 0.5, "-30": 0.7, "2x": 2} {
		out := apply(t, "adjust.brightness", adjTRow(in), Args{"amount": amount})
		adjTWant(t, "brightness "+amount, px(out, 0, 0), lin(k), 0.002)
	}
	// Clamps at white; black stays black.
	out := apply(t, "adjust.brightness", adjTRow([4]float64{0.9, 0.9, 0.9, 1}, [4]float64{0, 0, 0, 1}), Args{"amount": "4x"})
	adjTWant(t, "clamp", px(out, 0, 0), [4]float64{1, 1, 1, 1}, 0.001)
	adjTWant(t, "black", px(out, 1, 0), [4]float64{0, 0, 0, 1}, 0.001)
	adjTAlphaKept(t, "adjust.brightness", Args{})
	adjTErr(t, "adjust.brightness", Args{"amount": "bright"})
}

func TestExposure(t *testing.T) {
	in := [4]float64{0.4, 0.3, 0.2, 1}
	for stops, ev := range map[string]float64{"": 1, "-1": -1, "+2": 2, "0.5": 0.5, "-3": -3} {
		out := px(apply(t, "adjust.exposure", adjTRow(in), Args{"stops": stops}), 0, 0)
		for c := 0; c < 3; c++ {
			if got, want := srgbToLin(out[c]), srgbToLin(in[c])*math.Pow(2, ev); math.Abs(got-want) > 0.004 && want < 1 {
				t.Fatalf("stops %q channel %d: linear %.4f, want %.4f", stops, c, got, want)
			}
		}
	}
	// -1 halves linear light exactly (reference check on a mid value).
	h := px(apply(t, "adjust.exposure", adjTRow([4]float64{0.5, 0.5, 0.5, 1}), Args{"stops": "-1"}), 0, 0)
	if math.Abs(srgbToLin(h[0])-srgbToLin(0.5)/2) > 0.001 {
		t.Fatalf("-1 stop: %v", h)
	}
	adjTAlphaKept(t, "adjust.exposure", Args{})
	adjTErr(t, "adjust.exposure", Args{"stops": "9"})
	adjTErr(t, "adjust.exposure", Args{"stops": "bright"})
}

func TestContrast(t *testing.T) {
	in := [4]float64{0.3, 0.5, 0.8, 1}
	f := func(k float64) [4]float64 {
		o := [4]float64{0, 0, 0, 1}
		for c := 0; c < 3; c++ {
			o[c] = math.Max(0, math.Min(1, (in[c]-0.5)*k+0.5))
		}
		return o
	}
	for amount, k := range map[string]float64{"": 1.25, "150%": 1.5, "-50": 0.5, "+100": 2} {
		adjTWant(t, "contrast "+amount, px(apply(t, "adjust.contrast", adjTRow(in), Args{"amount": amount}), 0, 0), f(k), 0.001)
	}
	adjTAlphaKept(t, "adjust.contrast", Args{})
	adjTErr(t, "adjust.contrast", Args{"amount": "high"})
}

func TestGamma(t *testing.T) {
	cases := map[string][2]float64{"": {0.5, math.Pow(0.5, 1/1.2)}, "2": {0.25, 0.5}, "0.5": {0.5, 0.25}, "1": {0.7, 0.7}}
	for g, c := range cases {
		out := px(apply(t, "adjust.gamma", adjTRow([4]float64{c[0], c[0], c[0], 1}), Args{"gamma": g}), 0, 0)
		if math.Abs(out[0]-c[1]) > 0.001 {
			t.Fatalf("gamma %q: %.4f, want %.4f", g, out[0], c[1])
		}
	}
	adjTAlphaKept(t, "adjust.gamma", Args{})
	for _, bad := range []string{"0", "9", "x"} {
		adjTErr(t, "adjust.gamma", Args{"gamma": bad})
	}
}

func TestShadowsHighlights(t *testing.T) {
	smooth := func(x float64) float64 { x = math.Max(0, math.Min(1, x)); return x * x * (3 - 2*x) }
	// A neutral grey stays neutral, so the result is L'^3 in linear light.
	ref := func(v, a float64, shadows bool) float64 {
		L, _, _ := adjTLab([4]float64{v, v, v, 1})
		w := smooth((L - 0.6) / 0.4)
		if shadows {
			w = 1 - smooth(L/0.6)
		}
		Ln := L + 0.25*a*w
		return linToSRGB(Ln * Ln * Ln)
	}
	for _, v := range []float64{0.05, 0.15, 0.3, 0.5, 0.8, 0.95} {
		g := [4]float64{v, v, v, 1}
		for amount, a := range map[string]float64{"": 0.3, "+80": 0.8, "-50": -0.5} {
			got := px(apply(t, "adjust.shadows", adjTRow(g), Args{"amount": amount}), 0, 0)[0]
			if math.Abs(got-ref(v, a, true)) > 0.004 {
				t.Fatalf("shadows %q on %.2f: %.4f, want %.4f", amount, v, got, ref(v, a, true))
			}
		}
		for amount, a := range map[string]float64{"": -0.3, "-80": -0.8, "+40": 0.4} {
			got := px(apply(t, "adjust.highlights", adjTRow(g), Args{"amount": amount}), 0, 0)[0]
			if math.Abs(got-ref(v, a, false)) > 0.004 {
				t.Fatalf("highlights %q on %.2f: %.4f, want %.4f", amount, v, got, ref(v, a, false))
			}
		}
	}
	// Direction and masks: shadows lifts darks, leaves brights; highlights the reverse.
	dark, bright := 0.12, 0.92
	if px(apply(t, "adjust.shadows", adjTRow([4]float64{dark, dark, dark, 1}), Args{}), 0, 0)[0] <= dark+0.02 {
		t.Fatal("default shadows did not lift a dark grey")
	}
	if math.Abs(px(apply(t, "adjust.shadows", adjTRow([4]float64{bright, bright, bright, 1}), Args{"amount": "100"}), 0, 0)[0]-bright) > 0.003 {
		t.Fatal("shadows touched a bright grey")
	}
	if px(apply(t, "adjust.highlights", adjTRow([4]float64{bright, bright, bright, 1}), Args{}), 0, 0)[0] >= bright-0.02 {
		t.Fatal("default highlights did not pull down a bright grey")
	}
	if math.Abs(px(apply(t, "adjust.highlights", adjTRow([4]float64{dark, dark, dark, 1}), Args{"amount": "-100"}), 0, 0)[0]-dark) > 0.003 {
		t.Fatal("highlights touched a dark grey")
	}
	// Hue is preserved.
	c := [4]float64{0.30, 0.25, 0.22, 1}
	_, A0, B0 := adjTLab(c)
	_, A1, B1 := adjTLab(px(apply(t, "adjust.shadows", adjTRow(c), Args{"amount": "60"}), 0, 0))
	if math.Abs(math.Atan2(B1, A1)-math.Atan2(B0, A0)) > 0.05 {
		t.Fatalf("shadows changed hue: %.3f -> %.3f", math.Atan2(B0, A0), math.Atan2(B1, A1))
	}
	adjTAlphaKept(t, "adjust.shadows", Args{})
	adjTAlphaKept(t, "adjust.highlights", Args{})
	adjTErr(t, "adjust.shadows", Args{"amount": "500"})
	adjTErr(t, "adjust.highlights", Args{"amount": "dim"})
}

func TestLevels(t *testing.T) {
	clamp := func(x float64) float64 { return math.Max(0, math.Min(1, x)) }
	vals := []float64{0.02, 0.2, 0.5, 0.99}
	row := func() *img.Image {
		ps := make([][4]float64, len(vals))
		for i, v := range vals {
			ps[i] = [4]float64{v, v, v, 1}
		}
		return adjTRow(ps...)
	}
	cases := map[string]func(v float64) float64{
		"":           func(v float64) float64 { return clamp((v - 10.0/255) / (235.0 / 255)) },
		"10 245":     func(v float64) float64 { return clamp((v - 10.0/255) / (235.0 / 255)) },
		"5% 95%":     func(v float64) float64 { return clamp((v - 0.05) / 0.9) },
		"10,245":     func(v float64) float64 { return clamp((v - 10.0/255) / (235.0 / 255)) },
		"10 245 1.2": func(v float64) float64 { return math.Pow(clamp((v-10.0/255)/(235.0/255)), 1/1.2) },
		"0 255 2":    func(v float64) float64 { return math.Pow(v, 0.5) },
	}
	for lv, f := range cases {
		out := apply(t, "adjust.levels", row(), Args{"levels": lv})
		for i, v := range vals {
			if got := px(out, i, 0)[0]; math.Abs(got-f(v)) > 0.002 {
				t.Fatalf("levels %q on %.2f: %.4f, want %.4f", lv, v, got, f(v))
			}
		}
	}
	adjTAlphaKept(t, "adjust.levels", Args{})
	for _, bad := range []string{"10", "245 10", "abc def", "300 400", "10 245 0", "10 245 1 1", "50% 20%"} {
		adjTErr(t, "adjust.levels", Args{"levels": bad})
	}
}

// adjTRamp is 20x10: a grey ramp 0.2..0.8 with a black and a white outlier,
// and one coloured pixel (0.6, 0.4, 0.4) at index 100. It has 200 pixels, so
// the 0.5% percentiles are the 2nd smallest and 2nd largest.
func adjTRamp() *img.Image {
	m := img.New(20, 10)
	for i := 0; i < 200; i++ {
		v := 0.2 + 0.6*float64(i-1)/197
		switch i {
		case 0:
			v = 0
		case 199:
			v = 1
		}
		q := img.Q(float32(v))
		m.Set(i%20, i/20, q, q, q, 0xffff)
	}
	m.Set(0, 5, img.Q(0.6), img.Q(0.4), img.Q(0.4), 0xffff)
	return m
}

func TestAutoLevels(t *testing.T) {
	// Channels with different ranges, a constant channel, and a row of
	// transparent pixels that must not count.
	m := img.New(20, 11)
	for i := 0; i < 200; i++ {
		r, g := 0.2+0.6*float64(i-1)/197, 0.3+0.4*float64(i-1)/197
		if i == 0 {
			r, g = 0, 0
		}
		if i == 199 {
			r, g = 1, 1
		}
		m.Set(i%20, i/20, img.Q(float32(r)), img.Q(float32(g)), img.Q(0.5), 0xffff)
	}
	for x := 0; x < 20; x++ {
		m.Set(x, 10, 0xffff, 0xffff, 0, 0)
	}
	out := apply(t, "adjust.autolevels", m, Args{})
	for _, i := range []int{1, 50, 100, 198} {
		in := px(m, i%20, i/20)
		want := [4]float64{(in[0] - 0.2) / 0.6, (in[1] - 0.3) / 0.4, 0.5, 1}
		adjTWant(t, "autolevels", px(out, i%20, i/20), want, 0.003)
	}
	adjTWant(t, "outlier low", px(out, 0, 0), [4]float64{0, 0, 0.5, 1}, 0.003)
	adjTWant(t, "outlier high", px(out, 19, 9), [4]float64{1, 1, 0.5, 1}, 0.003)
	if px(out, 3, 10)[3] != 0 {
		t.Fatal("transparent pixel alpha changed")
	}
	// An image that already spans 0..1 is returned unchanged.
	s := scene()
	if got := apply(t, "adjust.autolevels", s, Args{}); got != s {
		t.Fatal("full-range image should be returned as is")
	}
}

func TestAutoContrast(t *testing.T) {
	m := adjTRamp()
	out := apply(t, "adjust.autocontrast", m, Args{})
	// Luma percentiles are 0.2 and 0.8; the same scale hits R, G and B.
	adjTWant(t, "colour pixel", px(out, 0, 5), [4]float64{(0.6 - 0.2) / 0.6, (0.4 - 0.2) / 0.6, (0.4 - 0.2) / 0.6, 1}, 0.003)
	adjTWant(t, "low", px(out, 1, 0), [4]float64{0, 0, 0, 1}, 0.003)
	adjTWant(t, "high", px(out, 18, 9), [4]float64{1, 1, 1, 1}, 0.003)
	in := px(m, 7, 3)
	g := (in[0] - 0.2) / 0.6
	adjTWant(t, "mid grey", px(out, 7, 3), [4]float64{g, g, g, 1}, 0.003)
	// Flat image: nothing to stretch.
	f := solid(4, 4, 0.5, 0.5, 0.5, 1)
	if apply(t, "adjust.autocontrast", f, Args{}) != f {
		t.Fatal("flat image should be returned as is")
	}
}

func TestEnhance(t *testing.T) {
	out := apply(t, "adjust.enhance", adjTRamp(), Args{})
	// Colour pixel: auto contrast gives (2/3, 1/3, 1/3), then vibrance +15%.
	pre := [4]float64{0.4 / 0.6, 0.2 / 0.6, 0.2 / 0.6, 1}
	L0, A0, B0 := adjTLab(pre)
	c0 := math.Hypot(A0, B0)
	L1, A1, B1 := adjTLab(px(out, 0, 5))
	want := 1 + 0.15*(1-math.Min(c0/0.32, 1))
	if math.Abs(L1-L0) > 0.005 || math.Abs(math.Hypot(A1, B1)/c0-want) > 0.03 {
		t.Fatalf("enhance: L %.4f->%.4f chroma ratio %.3f, want %.3f", L0, L1, math.Hypot(A1, B1)/c0, want)
	}
	adjTWant(t, "grey stays grey", px(out, 1, 0), [4]float64{0, 0, 0, 1}, 0.003)
	adjTAlphaKept(t, "adjust.enhance", Args{})
}

func TestWhiteBalance(t *testing.T) {
	a, b := [4]float64{0.6, 0.4, 0.3, 1}, [4]float64{0.4, 0.3, 0.2, 1}
	out := apply(t, "adjust.whitebalance", adjTRow(a, b), Args{})
	var mean, outMean [3]float64
	for c := 0; c < 3; c++ {
		mean[c] = (srgbToLin(a[c]) + srgbToLin(b[c])) / 2
		outMean[c] = (srgbToLin(px(out, 0, 0)[c]) + srgbToLin(px(out, 1, 0)[c])) / 2
	}
	target := 0.2126*mean[0] + 0.7152*mean[1] + 0.0722*mean[2]
	for c := 0; c < 3; c++ {
		if math.Abs(outMean[c]-target) > 0.002 {
			t.Fatalf("channel %d mean %.4f, want neutral %.4f", c, outMean[c], target)
		}
	}
	// Each pixel is a pure channel gain.
	g0 := target / mean[0]
	if got := srgbToLin(px(out, 0, 0)[0]); math.Abs(got-srgbToLin(a[0])*g0) > 0.002 {
		t.Fatalf("red gain: %.4f, want %.4f", got, srgbToLin(a[0])*g0)
	}
	grey := solid(3, 3, 0.5, 0.5, 0.5, 1)
	if apply(t, "adjust.whitebalance", grey, Args{}) != grey {
		t.Fatal("neutral image should be returned as is")
	}
	adjTAlphaKept(t, "adjust.whitebalance", Args{})
}

func TestTemperatureAndTint(t *testing.T) {
	in := [4]float64{0.5, 0.5, 0.5, 1}
	gain := func(c int, k float64) float64 { return linToSRGB(srgbToLin(in[c]) * k) }
	for amount, tt := range map[string]float64{"": 25, "-25": -25, "+100": 100, "-100": -100} {
		out := px(apply(t, "adjust.temperature", adjTRow(in), Args{"amount": amount}), 0, 0)
		k := math.Pow(2, 0.5*tt/100)
		adjTWant(t, "temperature "+amount, out, [4]float64{gain(0, k), gain(1, 1), gain(2, 1/k), 1}, 0.002)
	}
	if o := px(apply(t, "adjust.temperature", adjTRow(in), Args{}), 0, 0); o[0] <= o[2] {
		t.Fatalf("default temperature is not warm: %v", o)
	}
	for amount, tt := range map[string]float64{"": 15, "-40": -40, "+100": 100} {
		out := px(apply(t, "adjust.tint", adjTRow(in), Args{"amount": amount}), 0, 0)
		adjTWant(t, "tint "+amount, out, [4]float64{gain(0, 1), gain(1, math.Pow(2, -0.5*tt/100)), gain(2, 1), 1}, 0.002)
	}
	if o := px(apply(t, "adjust.tint", adjTRow(in), Args{}), 0, 0); o[1] >= o[0] {
		t.Fatalf("default tint does not push toward magenta: %v", o)
	}
	for _, h := range []string{"adjust.temperature", "adjust.tint"} {
		adjTAlphaKept(t, h, Args{})
		adjTErr(t, h, Args{"amount": "250"})
		adjTErr(t, h, Args{"amount": "warm"})
	}
}

func TestHueRotate(t *testing.T) {
	in := solid(2, 2, 0.55, 0.40, 0.35, 1)
	L0, A0, B0 := adjTLab(px(in, 0, 0))
	c0, h0 := math.Hypot(A0, B0), math.Atan2(B0, A0)
	for arg, deg := range map[string]float64{"": 30, "90": 90, "-90deg": -90, "0.25turn": 90, "45°": 45} {
		L1, A1, B1 := adjTLab(px(apply(t, "adjust.hue", in, Args{"degrees": arg}), 0, 0))
		dh := math.Mod(math.Atan2(B1, A1)-h0-deg*math.Pi/180+3*math.Pi, 2*math.Pi) - math.Pi
		if math.Abs(L1-L0) > 0.004 || math.Abs(math.Hypot(A1, B1)/c0-1) > 0.03 || math.Abs(dh) > 0.03 {
			t.Fatalf("degrees %q: L %.4f->%.4f chroma x%.3f hue error %.3f rad", arg, L0, L1, math.Hypot(A1, B1)/c0, dh)
		}
	}
	if apply(t, "adjust.hue", in, Args{"degrees": "360"}) != in {
		t.Fatal("360 degrees should be a no-op")
	}
	adjTAlphaKept(t, "adjust.hue", Args{})
	adjTErr(t, "adjust.hue", Args{"degrees": "teal"})
}

func TestInvert(t *testing.T) {
	out := apply(t, "adjust.invert", adjTRow([4]float64{0.2, 0.4, 0.6, 0.5}, [4]float64{0, 1, 0.5, 1}), Args{})
	adjTWant(t, "invert", px(out, 0, 0), [4]float64{0.8, 0.6, 0.4, 0.5}, 0.0005)
	adjTWant(t, "invert 2", px(out, 1, 0), [4]float64{1, 0, 0.5, 1}, 0.0005)
	adjTAlphaKept(t, "adjust.invert", Args{})
}

func TestBlackAndWhite(t *testing.T) {
	in := solid(2, 2, 0.8, 0.3, 0.2, 1)
	L0, _, _ := adjTLab(px(in, 0, 0))
	out := px(apply(t, "adjust.lightness", in, Args{}), 0, 0)
	if math.Abs(out[0]-out[1]) > 0.002 || math.Abs(out[1]-out[2]) > 0.002 {
		t.Fatalf("not grey: %v", out)
	}
	if L1, _, _ := adjTLab(out); math.Abs(L1-L0) > 0.003 {
		t.Fatalf("OKLab lightness changed: %.4f -> %.4f", L0, L1)
	}
	// Differs from the linear-luminance grey (image.grayscale) for saturated red.
	y := linToSRGB(0.2126*srgbToLin(0.8) + 0.7152*srgbToLin(0.3) + 0.0722*srgbToLin(0.2))
	if math.Abs(out[0]-y) < 0.01 {
		t.Fatalf("expected perceptual grey, got luminance grey %.3f", y)
	}
	s := scene()
	if !closePx(px(apply(t, "adjust.lightness", s, Args{}), 20, 3), px(s, 20, 3), 0.002) {
		t.Fatal("grey changed")
	}
	adjTAlphaKept(t, "adjust.lightness", Args{})
}

func TestChannel(t *testing.T) {
	in := adjTRow([4]float64{0.8, 0.3, 0.1, 0.5})
	for arg, v := range map[string]float64{"": 0.8, "red": 0.8, "g": 0.3, "Green": 0.3, "blue": 0.1, "b": 0.1} {
		adjTWant(t, "channel "+arg, px(apply(t, "adjust.channel", in, Args{"channel": arg}), 0, 0), [4]float64{v, v, v, 0.5}, 0.0005)
	}
	adjTAlphaKept(t, "adjust.channel", Args{})
	adjTErr(t, "adjust.channel", Args{"channel": "alpha"})
}

func TestSepia(t *testing.T) {
	in := [4]float64{0.5, 0.4, 0.3, 0.8}
	full := [4]float64{
		0.393*0.5 + 0.769*0.4 + 0.189*0.3,
		0.349*0.5 + 0.686*0.4 + 0.168*0.3,
		0.272*0.5 + 0.534*0.4 + 0.131*0.3, 0.8}
	for _, arg := range []string{"", "100", "100%"} {
		adjTWant(t, "sepia "+arg, px(apply(t, "adjust.sepia", adjTRow(in), Args{"amount": arg}), 0, 0), full, 0.001)
	}
	half := [4]float64{(in[0] + full[0]) / 2, (in[1] + full[1]) / 2, (in[2] + full[2]) / 2, 0.8}
	for _, arg := range []string{"50", "50%"} {
		adjTWant(t, "sepia "+arg, px(apply(t, "adjust.sepia", adjTRow(in), Args{"amount": arg}), 0, 0), half, 0.001)
	}
	adjTWant(t, "white clamps", px(apply(t, "adjust.sepia", adjTRow([4]float64{1, 1, 1, 1}), Args{}), 0, 0), [4]float64{1, 1, 0.937, 1}, 0.001)
	adjTAlphaKept(t, "adjust.sepia", Args{})
	adjTErr(t, "adjust.sepia", Args{"amount": "lots"})
	adjTErr(t, "adjust.sepia", Args{"amount": "150"})
}

func TestDuotone(t *testing.T) {
	pair := func(a, b [3]float64, grey float64) [4]float64 {
		tt := srgbToLin(grey)
		var o [4]float64
		for c := 0; c < 3; c++ {
			o[c] = linToSRGB(srgbToLin(a[c]) + (srgbToLin(b[c])-srgbToLin(a[c]))*tt)
		}
		o[3] = 1
		return o
	}
	navy := [3]float64{0x1d / 255.0, 0x2b / 255.0, 0x53 / 255.0}
	peach := [3]float64{1, 0xd2 / 255.0, 0x8f / 255.0}
	row := adjTRow([4]float64{0, 0, 0, 1}, [4]float64{1, 1, 1, 1}, [4]float64{0.5, 0.5, 0.5, 0.6})
	out := apply(t, "adjust.duotone", row, Args{})
	adjTWant(t, "black -> shadow", px(out, 0, 0), [4]float64{navy[0], navy[1], navy[2], 1}, 0.002)
	adjTWant(t, "white -> highlight", px(out, 1, 0), [4]float64{peach[0], peach[1], peach[2], 1}, 0.002)
	mid := pair(navy, peach, 0.5)
	mid[3] = 0.6
	adjTWant(t, "mid grey", px(out, 2, 0), mid, 0.003)
	// Names and hex.
	gold := [3]float64{1, 0xd7 / 255.0, 0}
	navyN := [3]float64{0, 0, 0x80 / 255.0}
	for _, arg := range []string{"navy gold", "navy to gold", "#000080 #ffd700", "rgb(0, 0, 128) rgb(255, 215, 0)"} {
		o := px(apply(t, "adjust.duotone", adjTRow([4]float64{0.3, 0.3, 0.3, 1}), Args{"colors": arg}), 0, 0)
		adjTWant(t, "duotone "+arg, o, pair(navyN, gold, 0.3), 0.003)
	}
	// Black to white reproduces the plain luminance grey.
	c := [4]float64{0.8, 0.3, 0.2, 1}
	y := linToSRGB(0.2126*srgbToLin(c[0]) + 0.7152*srgbToLin(c[1]) + 0.0722*srgbToLin(c[2]))
	adjTWant(t, "black->white", px(apply(t, "adjust.duotone", adjTRow(c), Args{"colors": "black white"}), 0, 0), [4]float64{y, y, y, 1}, 0.002)
	adjTAlphaKept(t, "adjust.duotone", Args{})
	adjTErr(t, "adjust.duotone", Args{"colors": "navy"})
	adjTErr(t, "adjust.duotone", Args{"colors": "foo bar"})
}

func TestColorize(t *testing.T) {
	grey := [4]float64{0.5, 0.5, 0.5, 0.7}
	L0, _, _ := adjTLab(grey)
	tc := [4]float64{1, 0x9a / 255.0, 0x3c / 255.0, 1}
	_, At, Bt := adjTLab(tc)
	for arg, mix := range map[string]float64{"": 0.5, "#ff9a3c": 0.5, "#ff9a3c 70%": 0.7, "#ff9a3c 0%": 0} {
		L1, A1, B1 := adjTLab(px(apply(t, "adjust.colorize", adjTRow(grey), Args{"color": arg}), 0, 0))
		if math.Abs(L1-L0) > 0.004 || math.Abs(A1-mix*At) > 0.004 || math.Abs(B1-mix*Bt) > 0.004 {
			t.Fatalf("colorize %q: L %.4f->%.4f a,b = %.4f,%.4f want %.4f,%.4f", arg, L0, L1, A1, B1, mix*At, mix*Bt)
		}
	}
	// A coloured pixel keeps its lightness and moves toward the target.
	in := [4]float64{0.3, 0.5, 0.7, 1}
	Lc, Ac, Bc := adjTLab(in)
	L1, A1, B1 := adjTLab(px(apply(t, "adjust.colorize", adjTRow(in), Args{}), 0, 0))
	if math.Abs(L1-Lc) > 0.004 || math.Abs(A1-(Ac+(At-Ac)*0.5)) > 0.004 || math.Abs(B1-(Bc+(Bt-Bc)*0.5)) > 0.004 {
		t.Fatalf("colour pixel: %.4f %.4f %.4f", L1, A1, B1)
	}
	if got := px(apply(t, "adjust.colorize", adjTRow(grey), Args{}), 0, 0)[3]; got != 0.7 && math.Abs(got-0.7) > 0.0001 {
		t.Fatalf("alpha changed: %v", got)
	}
	adjTErr(t, "adjust.colorize", Args{"color": "notacolour"})
	adjTErr(t, "adjust.colorize", Args{"color": "red 200%"})
}

func TestPosterize(t *testing.T) {
	vals := []float64{0.1, 0.3, 0.6, 0.9}
	row := adjTRow([4]float64{vals[0], vals[0], vals[0], 1}, [4]float64{vals[1], vals[1], vals[1], 1}, [4]float64{vals[2], vals[2], vals[2], 1}, [4]float64{vals[3], vals[3], vals[3], 1})
	check := func(arg string, want []float64) {
		out := apply(t, "adjust.posterize", row, Args{"levels": arg})
		for i, w := range want {
			if got := px(out, i, 0)[0]; math.Abs(got-w) > 0.001 {
				t.Fatalf("posterize %q on %.2f: %.4f, want %.4f", arg, vals[i], got, w)
			}
		}
	}
	check("", []float64{0, 1.0 / 3, 2.0 / 3, 1})
	check("2", []float64{0, 0, 1, 1})
	check("3", []float64{0, 0, 0.5, 1})
	check("32", []float64{3.0 / 31, 9.0 / 31, 19.0 / 31, 28.0 / 31})
	adjTAlphaKept(t, "adjust.posterize", Args{})
	for _, bad := range []string{"1", "33", "many"} {
		adjTErr(t, "adjust.posterize", Args{"levels": bad})
	}
}

func TestThreshold(t *testing.T) {
	g := func(v float64) [4]float64 { return [4]float64{v, v, v, 0.9} }
	row := adjTRow(g(0.49), g(0.51), [4]float64{0, 1, 0, 1}, [4]float64{0, 0, 1, 1})
	out := apply(t, "adjust.threshold", row, Args{})
	adjTWant(t, "0.49", px(out, 0, 0), [4]float64{0, 0, 0, 0.9}, 0.001)
	adjTWant(t, "0.51", px(out, 1, 0), [4]float64{1, 1, 1, 0.9}, 0.001)
	// Pure green has linear luminance 0.7152 (encoded 0.86); pure blue 0.0722 (encoded 0.29).
	adjTWant(t, "green", px(out, 2, 0), [4]float64{1, 1, 1, 1}, 0.001)
	adjTWant(t, "blue", px(out, 3, 0), [4]float64{0, 0, 0, 1}, 0.001)
	hi := apply(t, "adjust.threshold", row, Args{"level": "90%"})
	adjTWant(t, "green at 90", px(hi, 2, 0), [4]float64{0, 0, 0, 1}, 0.001)
	lo := apply(t, "adjust.threshold", row, Args{"level": "20"})
	adjTWant(t, "blue at 20", px(lo, 3, 0), [4]float64{1, 1, 1, 1}, 0.001)
	adjTWant(t, "0.49 at 45", px(apply(t, "adjust.threshold", row, Args{"level": "45"}), 0, 0), [4]float64{1, 1, 1, 0.9}, 0.001)
	adjTAlphaKept(t, "adjust.threshold", Args{})
	adjTErr(t, "adjust.threshold", Args{"level": "150"})
	adjTErr(t, "adjust.threshold", Args{"level": "half"})
}

func TestSwapChannels(t *testing.T) {
	in := adjTRow([4]float64{0.1, 0.2, 0.3, 0.6})
	for arg, w := range map[string][3]float64{"": {0.3, 0.2, 0.1}, "bgr": {0.3, 0.2, 0.1}, "grb": {0.2, 0.1, 0.3}, "RBG": {0.1, 0.3, 0.2}, "rgb": {0.1, 0.2, 0.3}, "rrr": {0.1, 0.1, 0.1}} {
		adjTWant(t, "swap "+arg, px(apply(t, "adjust.swap", in, Args{"order": arg}), 0, 0), [4]float64{w[0], w[1], w[2], 0.6}, 0.0005)
	}
	adjTAlphaKept(t, "adjust.swap", Args{})
	for _, bad := range []string{"xyz", "br", "bgra"} {
		adjTErr(t, "adjust.swap", Args{"order": bad})
	}
}

func TestReplaceColor(t *testing.T) {
	row := adjTRow(
		[4]float64{1, 0, 0, 1},         // exact match
		[4]float64{0, 1, 0, 1},         // far away
		[4]float64{0.9, 0.1, 0.1, 1},   // near red
		[4]float64{1, 0, 0, 0.5},       // half-transparent red
		[4]float64{0.6, 0.05, 0.05, 1}, // darker red keeps being darker
	)
	for _, arg := range []string{"", "red blue", "#ff0000 #0000ff", "red to blue", "255,0,0 0,0,255"} {
		out := apply(t, "adjust.replace", row, Args{"colors": arg})
		adjTWant(t, "match "+arg, px(out, 0, 0), [4]float64{0, 0, 1, 1}, 0.02)
		adjTWant(t, "far "+arg, px(out, 1, 0), [4]float64{0, 1, 0, 1}, 0.001)
		if p := px(out, 2, 0); p[2] < 0.5 || p[0] > 0.3 {
			t.Fatalf("near red not turned blue (%s): %v", arg, p)
		}
		adjTWant(t, "alpha "+arg, px(out, 3, 0), [4]float64{0, 0, 1, 0.5}, 0.02)
	}
	// Shading survives: the darker red stays darker than the pure blue's replacement.
	out := apply(t, "adjust.replace", row, Args{})
	Lm, _, _ := adjTLab(px(out, 0, 0))
	Ld, _, _ := adjTLab(px(out, 4, 0))
	if Ld >= Lm {
		t.Fatalf("lightness variation lost: %.3f vs %.3f", Ld, Lm)
	}
	// Grey source: translate instead of rotating hue.
	g := apply(t, "adjust.replace", adjTRow([4]float64{0.5, 0.5, 0.5, 1}), Args{"colors": "#808080 #a08060"})
	adjTWant(t, "grey source", px(g, 0, 0), [4]float64{0xa0 / 255.0, 0x80 / 255.0, 0x60 / 255.0, 1}, 0.02)
	adjTAlphaKept(t, "adjust.replace", Args{})
	adjTErr(t, "adjust.replace", Args{"colors": "red"})
	adjTErr(t, "adjust.replace", Args{"colors": "foo bar"})
}

func TestOpacity(t *testing.T) {
	row := adjTRow([4]float64{0.2, 0.4, 0.6, 1}, [4]float64{0.8, 0.1, 0.3, 0.5})
	for arg, k := range map[string]float64{"": 0.7, "30%": 0.3, "30": 0.3, "0": 0} {
		out := apply(t, "adjust.opacity", row, Args{"amount": arg})
		adjTWant(t, "opaque "+arg, px(out, 0, 0), [4]float64{0.2, 0.4, 0.6, k}, 0.0005)
		adjTWant(t, "translucent "+arg, px(out, 1, 0), [4]float64{0.8, 0.1, 0.3, 0.5 * k}, 0.0005)
	}
	if apply(t, "adjust.opacity", row, Args{"amount": "100%"}) != row {
		t.Fatal("100% should be a no-op")
	}
	adjTErr(t, "adjust.opacity", Args{"amount": "150"})
	adjTErr(t, "adjust.opacity", Args{"amount": "see-through"})
}

func TestAdjustVideoFilters(t *testing.T) {
	want := map[string]string{
		"adjust.contrast":  "eq=contrast=1.250",
		"adjust.gamma":     "eq=gamma=1.200",
		"adjust.hue":       "hue=h=30.000",
		"adjust.invert":    "negate",
		"adjust.lightness": "hue=s=0",
	}
	for name, f := range want {
		h, _ := Lookup(name)
		if h.Video == nil {
			t.Fatalf("%s has no Video", name)
		}
		got, err := h.Video(Args{})
		if err != nil || got != f {
			t.Fatalf("%s video: %q %v, want %q", name, got, err, f)
		}
	}
	h, _ := Lookup("adjust.swap")
	if got, _ := h.Video(Args{}); got != "colorchannelmixer=rr=0:rg=0:rb=1:gr=0:gg=1:gb=0:br=1:bg=0:bb=0" {
		t.Fatalf("swap video: %q", got)
	}
	h, _ = Lookup("adjust.channel")
	if got, _ := h.Video(Args{"channel": "green"}); got != "colorchannelmixer=rr=0:rg=1:rb=0:gr=0:gg=1:gb=0:br=0:bg=1:bb=0" {
		t.Fatalf("channel video: %q", got)
	}
	h, _ = Lookup("adjust.sepia")
	if got, _ := h.Video(Args{"amount": "0"}); !strings.Contains(got, "rr=1.0000:rg=0.0000") {
		t.Fatalf("sepia 0 video: %q", got)
	}
	if got, _ := h.Video(Args{}); !strings.Contains(got, "rr=0.3930:rg=0.7690:rb=0.1890") {
		t.Fatalf("sepia video: %q", got)
	}
}

// TestAdjustSkillFiles loads every skill file in skills/core/adjust and
// checks it against the registry, then runs the bare command on a picture.
func TestAdjustSkillFiles(t *testing.T) {
	files, _ := filepath.Glob("../../skills/core/adjust/*.json")
	if len(files) < 27 {
		t.Fatalf("expected the adjust skills, found %d files", len(files))
	}
	// Bare command may legitimately leave these alone on the full-range scene.
	stays := map[string]bool{"adjust.autolevels": true, "adjust.autocontrast": true}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var sk struct {
			ID         string   `json:"id"`
			Category   string   `json:"category"`
			InputTypes []string `json:"inputTypes"`
			Params     []struct {
				Name string `json:"name"`
			} `json:"params"`
			Executor struct{ Handler string } `json:"executor"`
		}
		if err := json.Unmarshal(raw, &sk); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if sk.ID+".json" != filepath.Base(f) {
			t.Fatalf("%s: id %q does not match the file name", f, sk.ID)
		}
		h, ok := Lookup(sk.Executor.Handler)
		if !ok || h.Pixel == nil {
			t.Fatalf("%s: handler %q is not a registered pixel handler", f, sk.Executor.Handler)
		}
		if len(sk.Params) > 1 {
			t.Fatalf("%s: more than one parameter", f)
		}
		hasVideo := false
		for _, in := range sk.InputTypes {
			hasVideo = hasVideo || in == "@video"
		}
		if hasVideo != (h.Video != nil) {
			t.Fatalf("%s: @video input (%v) does not match Handler.Video (%v)", f, hasVideo, h.Video != nil)
		}
		if stays[sk.Executor.Handler] || sk.Executor.Handler == "adjust.replace" {
			continue
		}
		s := scene()
		out := apply(t, sk.Executor.Handler, s, Args{})
		diff := false
		for i := range s.Pix {
			if s.Pix[i] != out.Pix[i] {
				diff = true
				break
			}
		}
		if !diff {
			t.Fatalf("%s: bare command did not change the picture", sk.ID)
		}
	}
}
