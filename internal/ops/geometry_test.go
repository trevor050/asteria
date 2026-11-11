package ops

import (
	"context"
	"math"
	"testing"

	"asteria/internal/img"
)

// geoMark builds a w x h opaque image where every pixel is unique, so a
// moved pixel can be identified exactly.
func geoMark(w, h int) *img.Image {
	m := img.New(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, uint16(1000+x*600), uint16(2000+y*900), 30000, 0xffff)
		}
	}
	return m
}

func geoSame(t *testing.T, what string, a *img.Image, ax, ay int, b *img.Image, bx, by int) {
	t.Helper()
	ar, ag, ab, aa := a.At(ax, ay)
	br, bg, bb, ba := b.At(bx, by)
	if ar != br || ag != bg || ab != bb || aa != ba {
		t.Fatalf("%s: out(%d,%d) = %v, want in(%d,%d) = %v", what, bx, by,
			[4]uint16{br, bg, bb, ba}, ax, ay, [4]uint16{ar, ag, ab, aa})
	}
}

func geoSize(t *testing.T, what string, m *img.Image, w, h int) {
	t.Helper()
	if m.W != w || m.H != h {
		t.Fatalf("%s: size %dx%d, want %dx%d", what, m.W, m.H, w, h)
	}
}

func geoUntouched(t *testing.T, handler string, args Args) {
	t.Helper()
	in := geoMark(24, 16)
	before := in.Clone()
	apply(t, handler, in, args)
	if in.W != before.W || in.H != before.H {
		t.Fatalf("%s changed the input size", handler)
	}
	for i := range in.Pix {
		if in.Pix[i] != before.Pix[i] {
			t.Fatalf("%s modified its input", handler)
		}
	}
}

func TestGeoInputsUntouched(t *testing.T) {
	for h, a := range map[string]Args{
		"geo.crop": {"size": "50%"}, "geo.trimborders": {}, "geo.trimtransparent": {},
		"geo.rotate": {"angle": "33"}, "geo.straighten": {}, "geo.flip": {}, "geo.pad": {},
		"geo.border": {}, "geo.roundcorners": {"radius": "5"}, "geo.circle": {}, "geo.tile": {},
		"geo.fit": {"size": "30x30"}, "geo.fill": {"size": "30x30"}, "geo.stretch": {"size": "30x30"},
		"geo.upscale": {},
	} {
		geoUntouched(t, h, a)
	}
}

func TestGeoCrop(t *testing.T) {
	in := geoMark(100, 60)
	// Bare command: largest centred square.
	out := apply(t, "geo.crop", in, Args{})
	geoSize(t, "1:1", out, 60, 60)
	geoSame(t, "1:1", in, 20, 0, out, 0, 0)
	// Ratio: 16:9 of 100x60 is 100x56, centred (y offset 2).
	out = apply(t, "geo.crop", in, Args{"size": "16:9"})
	geoSize(t, "16:9", out, 100, 56)
	geoSame(t, "16:9", in, 0, 2, out, 0, 0)
	// Portrait ratio and the word "square".
	geoSize(t, "4:5", apply(t, "geo.crop", in, Args{"size": "4:5"}), 48, 60)
	geoSize(t, "square", apply(t, "geo.crop", in, Args{"size": "square"}), 60, 60)
	// Exact size, centred.
	out = apply(t, "geo.crop", in, Args{"size": "40x30"})
	geoSize(t, "40x30", out, 40, 30)
	geoSame(t, "40x30", in, 30, 15, out, 0, 0)
	// Larger than the image clamps instead of failing.
	geoSize(t, "clamp", apply(t, "geo.crop", in, Args{"size": "800x600"}), 100, 60)
	// Percent keeps the centre.
	out = apply(t, "geo.crop", in, Args{"size": "50%"})
	geoSize(t, "50%", out, 50, 30)
	geoSame(t, "50%", in, 25, 15, out, 0, 0)
	// Proxy: sizes are full-resolution pixels, the result stays a proxy.
	p := geoMark(50, 30)
	p.Scale = 0.5
	out = apply(t, "geo.crop", p, Args{"size": "40x30"})
	geoSize(t, "proxy", out, 20, 15)
	if out.Scale != 0.5 {
		t.Fatalf("proxy scale lost: %v", out.Scale)
	}
	h, _ := Lookup("geo.crop")
	if _, err := RunPixel(context.Background(), h, in, Args{"size": "banana"}); err == nil {
		t.Fatal("accepted a nonsense crop")
	}
	if _, err := RunPixel(context.Background(), h, in, Args{"size": "0%"}); err == nil {
		t.Fatal("accepted 0%")
	}
}

func TestGeoFlipRotate90(t *testing.T) {
	in := geoMark(7, 4)
	v := apply(t, "geo.flip", in, Args{"axis": "vertical"})
	geoSame(t, "flip", in, 2, 0, v, 2, 3)
	hz := apply(t, "geo.flip", in, Args{"axis": "horizontal"})
	geoSame(t, "mirror", in, 2, 0, hz, 4, 0)

	r := apply(t, "geo.rotate", in, Args{})
	geoSize(t, "rotate 90", r, 4, 7)
	geoSame(t, "rotate 90", in, 2, 1, r, 4-1-1, 2) // (x,y) -> (H-1-y, x)
	r = apply(t, "geo.rotate", in, Args{"angle": "-90"})
	geoSize(t, "rotate -90", r, 4, 7)
	geoSame(t, "rotate -90", in, 2, 1, r, 1, 7-1-2) // (x,y) -> (y, W-1-x)
	r = apply(t, "geo.rotate", in, Args{"angle": "180"})
	geoSame(t, "rotate 180", in, 2, 1, r, 4, 2)
	if apply(t, "geo.rotate", in, Args{"angle": "360"}) != in {
		t.Fatal("360 should be a no-op")
	}
	geoSize(t, "quarter turn word", apply(t, "geo.rotate", in, Args{"angle": "0.25turn"}), 4, 7)
}

func TestGeoRotate45(t *testing.T) {
	in := solid(20, 20, 0.8, 0.3, 0.1, 1)
	out := apply(t, "geo.rotate", in, Args{"angle": "45"})
	want := int(math.Ceil(20 * math.Sqrt2))
	geoSize(t, "45", out, want, want)
	if c := px(out, want/2, want/2); !closePx(c, px(in, 0, 0), 0.002) {
		t.Fatalf("centre %v, want %v", c, px(in, 0, 0))
	}
	for _, p := range [][2]int{{0, 0}, {want - 1, 0}, {0, want - 1}, {want - 1, want - 1}} {
		if a := px(out, p[0], p[1])[3]; a != 0 {
			t.Fatalf("corner %v alpha %v, want 0", p, a)
		}
	}
	// Midpoints of the edges of the rotated square are inside it.
	if px(out, want/2, 3)[3] < 0.99 {
		t.Fatalf("expected opaque body near the top vertex, got %v", px(out, want/2, 3))
	}
	// A 30 degree turn of a wide image grows by the textbook amount.
	w := solid(30, 10, 1, 1, 1, 1)
	o := apply(t, "geo.rotate", w, Args{"angle": "30"})
	th := 30 * math.Pi / 180
	geoSize(t, "30", o, int(math.Ceil(30*math.Cos(th)+10*math.Sin(th)-1e-6)), int(math.Ceil(30*math.Sin(th)+10*math.Cos(th)-1e-6)))
}

func TestGeoRotateDirectionAndNoBleed(t *testing.T) {
	// A bright dot right of centre moves to below centre after a clockwise 90-ish turn.
	in := solid(21, 21, 0, 0, 0, 1)
	in.Set(18, 10, 0xffff, 0xffff, 0xffff, 0xffff)
	in.Set(17, 10, 0xffff, 0xffff, 0xffff, 0xffff)
	out := apply(t, "geo.rotate", in, Args{"angle": "80"})
	bestX, bestY, best := 0, 0, 0.0
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			if v := px(out, x, y)[0]; v > best {
				bestX, bestY, best = x, y, v
			}
		}
	}
	if bestY <= out.H/2+2 || math.Abs(float64(bestX-out.W/2)) > 4 {
		t.Fatalf("bright dot ended at (%d,%d) of %dx%d, want below centre", bestX, bestY, out.W, out.H)
	}

	// Opaque blue next to fully transparent red: no red may appear.
	b := img.New(20, 20)
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			if x < 10 {
				b.Set(x, y, 0, 0, 0xffff, 0xffff)
			} else {
				b.Set(x, y, 0xffff, 0, 0, 0)
			}
		}
	}
	o := apply(t, "geo.rotate", b, Args{"angle": "30"})
	seen := 0
	for y := 0; y < o.H; y++ {
		for x := 0; x < o.W; x++ {
			p := px(o, x, y)
			if p[3] > 0.01 {
				seen++
				if p[0] > 0.01 || p[2] < 0.99 {
					t.Fatalf("colour bled at (%d,%d): %v", x, y, p)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("nothing visible after rotation")
	}
}

func TestGeoStraighten(t *testing.T) {
	in := solid(60, 40, 0.2, 0.6, 0.9, 1)
	for _, ang := range []string{"", "-10", "20", "0.5"} {
		args := Args{}
		if ang != "" {
			args["angle"] = ang
		}
		out := apply(t, "geo.straighten", in, args)
		if out.W >= in.W || out.H >= in.H || out.W < 10 {
			t.Fatalf("angle %q: size %dx%d unexpected", ang, out.W, out.H)
		}
		if r := float64(out.W) / float64(out.H); math.Abs(r-1.5) > 0.08 {
			t.Fatalf("angle %q: aspect %.3f, want 1.5", ang, r)
		}
		for y := 0; y < out.H; y++ {
			for x := 0; x < out.W; x++ {
				if px(out, x, y)[3] < 0.9999 {
					t.Fatalf("angle %q: transparent pixel at (%d,%d): %v", ang, x, y, px(out, x, y))
				}
			}
		}
		if c := px(out, out.W/2, out.H/2); !closePx(c, px(in, 0, 0), 0.003) {
			t.Fatalf("angle %q: colour changed %v", ang, c)
		}
	}
	// Analytic size: a 3:2 rectangle turned 2 degrees.
	cw, ch := geoInscribed(60, 40, 2*math.Pi/180, 0)
	if cw < 56 || cw > 58 || ch < 37 || ch > 39 {
		t.Fatalf("inscribed %dx%d out of range", cw, ch)
	}
	h, _ := Lookup("geo.straighten")
	if _, err := RunPixel(context.Background(), h, in, Args{"angle": "60"}); err == nil {
		t.Fatal("accepted 60 degrees")
	}
}

func TestGeoPadBorder(t *testing.T) {
	in := geoMark(10, 10)
	out := apply(t, "geo.pad", in, Args{})
	geoSize(t, "pad 10%", out, 12, 12)
	geoSame(t, "pad", in, 3, 4, out, 4, 5)
	if px(out, 0, 0)[3] != 0 || px(out, 11, 11)[3] != 0 {
		t.Fatal("pad should be transparent")
	}
	geoSize(t, "pad 3px", apply(t, "geo.pad", in, Args{"amount": "3"}), 16, 16)
	geoSize(t, "pad 20%", apply(t, "geo.pad", in, Args{"amount": "20%"}), 14, 14)
	// Proxy: pixels are full-resolution, 4px at scale 0.5 is 2.
	p := solid(20, 20, 1, 0, 0, 1)
	p.Scale = 0.5
	o := apply(t, "geo.pad", p, Args{"amount": "4"})
	geoSize(t, "pad proxy", o, 24, 24)
	if o.Scale != 0.5 {
		t.Fatal("pad lost proxy scale")
	}

	big := solid(50, 50, 0.2, 0.4, 0.6, 1)
	b := apply(t, "geo.border", big, Args{})
	geoSize(t, "border 4%", b, 54, 54)
	if !closePx(px(b, 0, 0), [4]float64{1, 1, 1, 1}, 0.001) || !closePx(px(b, 53, 27), [4]float64{1, 1, 1, 1}, 0.001) {
		t.Fatal("default border should be white")
	}
	if !closePx(px(b, 2, 2), px(big, 0, 0), 0.001) || !closePx(px(b, 1, 30), [4]float64{1, 1, 1, 1}, 0.001) {
		t.Fatal("border covers the picture or is too thin")
	}
	k := apply(t, "geo.border", big, Args{"color": "black", "width": "5"})
	geoSize(t, "black border", k, 60, 60)
	if !closePx(px(k, 4, 4), [4]float64{0, 0, 0, 1}, 0.001) || !closePx(px(k, 5, 5), px(big, 0, 0), 0.001) {
		t.Fatal("black border wrong")
	}
	// Tiny images still get a visible border.
	geoSize(t, "tiny border", apply(t, "geo.border", in, Args{}), 12, 12)
}

func TestGeoTrim(t *testing.T) {
	in := solid(30, 24, 1, 1, 1, 1)
	for y := 5; y < 19; y++ {
		for x := 3; x < 25; x++ {
			in.Set(x, y, 0x3000, 0x6000, 0x9000, 0xffff)
		}
	}
	// Slightly off-white noise in the border must still count as border.
	in.Set(1, 1, 0xfd00, 0xfe00, 0xfe00, 0xffff)
	out := apply(t, "geo.trimborders", in, Args{})
	geoSize(t, "trim", out, 22, 14)
	geoSame(t, "trim", in, 3, 5, out, 0, 0)
	// Uniform images are left alone.
	u := solid(8, 8, 0.5, 0.5, 0.5, 1)
	if apply(t, "geo.trimborders", u, Args{}) != u {
		t.Fatal("uniform image should be returned unchanged")
	}
	// Transparent borders trim against a transparent corner.
	tr := img.New(20, 20)
	tr.Set(7, 9, 0xffff, 0, 0, 0xffff)
	geoSize(t, "trim transparent corner", apply(t, "geo.trimborders", tr, Args{}), 1, 1)

	x := img.New(20, 20)
	for y := 6; y <= 12; y++ {
		for xx := 4; xx <= 10; xx++ {
			x.Set(xx, y, 0xffff, 0, 0, 0x1000)
		}
	}
	o := apply(t, "geo.trimtransparent", x, Args{})
	geoSize(t, "trim transparent", o, 7, 7)
	geoSame(t, "trim transparent", x, 4, 6, o, 0, 0)
	h, _ := Lookup("geo.trimtransparent")
	if _, err := RunPixel(context.Background(), h, img.New(5, 5), Args{}); err == nil {
		t.Fatal("empty image should be an error")
	}
}

func TestGeoRoundCorners(t *testing.T) {
	in := solid(40, 40, 0.9, 0.2, 0.2, 1)
	out := apply(t, "geo.roundcorners", in, Args{"radius": "10"})
	geoSize(t, "round", out, 40, 40)
	for _, p := range [][2]int{{0, 0}, {39, 0}, {0, 39}, {39, 39}} {
		if a := px(out, p[0], p[1])[3]; a != 0 {
			t.Fatalf("corner %v alpha %v", p, a)
		}
	}
	for _, p := range [][2]int{{20, 20}, {20, 0}, {0, 20}, {39, 20}, {20, 39}, {10, 10}} {
		if a := px(out, p[0], p[1]); a[3] != 1 || !closePx(a, px(in, 0, 0), 0.0001) {
			t.Fatalf("pixel %v changed: %v", p, a)
		}
	}
	// Antialiasing: some pixels are partly covered, none outside [0,1].
	partial := 0
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if a := px(out, x, y)[3]; a > 0 && a < 1 {
				partial++
			}
		}
	}
	if partial < 5 {
		t.Fatalf("expected antialiased edge, %d partial pixels", partial)
	}
	// Bare command rounds (8% of 40 = 3.2).
	d := apply(t, "geo.roundcorners", in, Args{})
	if px(d, 0, 0)[3] != 0 || px(d, 20, 20)[3] != 1 {
		t.Fatal("default radius did nothing")
	}
	if c := apply(t, "geo.roundcorners", in, Args{"radius": "50%"}); px(c, 0, 0)[3] != 0 || px(c, 20, 20)[3] != 1 {
		t.Fatal("max radius should still round the corners")
	}
	if apply(t, "geo.roundcorners", in, Args{"radius": "0"}) != in {
		t.Fatal("radius 0 should be a no-op")
	}
}

func TestGeoCircle(t *testing.T) {
	in := solid(30, 20, 0.1, 0.7, 0.3, 1)
	out := apply(t, "geo.circle", in, Args{})
	geoSize(t, "circle", out, 20, 20)
	if px(out, 0, 0)[3] != 0 || px(out, 19, 19)[3] != 0 {
		t.Fatal("corners should be transparent")
	}
	if c := px(out, 10, 10); c[3] != 1 || !closePx(c, px(in, 0, 0), 0.0001) {
		t.Fatalf("centre %v", c)
	}
	edge := 0
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			d := math.Hypot(float64(x)+0.5-10, float64(y)+0.5-10)
			if math.Abs(d-10) < 0.1 {
				edge++
				if a := px(out, x, y)[3]; a < 0.35 || a > 0.65 {
					t.Fatalf("boundary pixel (%d,%d) d=%.3f alpha %.3f, want ~0.5", x, y, d, a)
				}
			}
		}
	}
	if edge == 0 {
		t.Fatal("test found no boundary pixels")
	}
}

func TestGeoTile(t *testing.T) {
	in := geoMark(4, 3)
	out := apply(t, "geo.tile", in, Args{"grid": "3x2"})
	geoSize(t, "tile 3x2", out, 12, 6)
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			geoSame(t, "tile", in, x%4, y%3, out, x, y)
		}
	}
	geoSize(t, "tile default", apply(t, "geo.tile", in, Args{}), 8, 6)
	geoSize(t, "tile 10x10", apply(t, "geo.tile", in, Args{"grid": "10x10"}), 40, 30)
	geoSize(t, "tile 3", apply(t, "geo.tile", in, Args{"grid": "3"}), 12, 9)
	h, _ := Lookup("geo.tile")
	for _, bad := range []string{"11x1", "0x2", "lots"} {
		if _, err := RunPixel(context.Background(), h, in, Args{"grid": bad}); err == nil {
			t.Fatalf("accepted grid %q", bad)
		}
	}
}

func TestGeoFitFillStretch(t *testing.T) {
	in := solid(40, 20, 0.2, 0.5, 0.8, 1)
	f := apply(t, "geo.fit", in, Args{"size": "30x30"})
	geoSize(t, "fit", f, 30, 30)
	if px(f, 15, 0)[3] != 0 || px(f, 15, 29)[3] != 0 {
		t.Fatal("letterbox bars should be transparent")
	}
	if c := px(f, 15, 15); !closePx(c, px(in, 0, 0), 0.003) {
		t.Fatalf("fit centre %v", c)
	}
	// 40x20 into 30x30 is 30x15, centred: rows 7..21.
	if px(f, 15, 6)[3] != 0 || px(f, 15, 7)[3] < 0.99 || px(f, 15, 21)[3] < 0.99 || px(f, 15, 22)[3] != 0 {
		t.Fatalf("fit bars misplaced: %v %v %v %v", px(f, 15, 6), px(f, 15, 7), px(f, 15, 21), px(f, 15, 22))
	}
	geoSize(t, "fit default", apply(t, "geo.fit", in, Args{}), 1080, 1080)

	// Fill: left half red, right half blue; a 10x10 centre crop of 40x20 keeps 10 of each.
	two := solid(40, 20, 1, 0, 0, 1)
	for y := 0; y < 20; y++ {
		for x := 20; x < 40; x++ {
			two.Set(x, y, 0, 0, 0xffff, 0xffff)
		}
	}
	fl := apply(t, "geo.fill", two, Args{"size": "10x10"})
	geoSize(t, "fill", fl, 10, 10)
	if !closePx(px(fl, 1, 5), [4]float64{1, 0, 0, 1}, 0.05) || !closePx(px(fl, 8, 5), [4]float64{0, 0, 1, 1}, 0.05) {
		t.Fatalf("fill not symmetric: %v %v", px(fl, 1, 5), px(fl, 8, 5))
	}
	geoSize(t, "fill tall", apply(t, "geo.fill", two, Args{"size": "10x30"}), 10, 30)
	geoSize(t, "fill up", apply(t, "geo.fill", two, Args{"size": "100x50"}), 100, 50)

	s := apply(t, "geo.stretch", in, Args{"size": "17x33"})
	geoSize(t, "stretch", s, 17, 33)
	if a := px(s, 0, 0)[3]; a < 0.999 {
		t.Fatalf("stretch edge alpha %v", a)
	}

	// Proxies: targets are full-resolution sizes.
	p := solid(20, 10, 0.5, 0.5, 0.5, 1)
	p.Scale = 0.5
	o := apply(t, "geo.fill", p, Args{"size": "40x40"})
	geoSize(t, "fill proxy", o, 20, 20)
	if o.Scale != 0.5 {
		t.Fatal("fill lost proxy scale")
	}
	geoSize(t, "stretch proxy", apply(t, "geo.stretch", p, Args{"size": "100x50"}), 50, 25)
	geoSize(t, "fit proxy", apply(t, "geo.fit", p, Args{"size": "40x40"}), 20, 20)
	// Single number is a square.
	geoSize(t, "square size", apply(t, "geo.stretch", in, Args{"size": "12"}), 12, 12)
	h, _ := Lookup("geo.fill")
	if _, err := RunPixel(context.Background(), h, in, Args{"size": "wide"}); err == nil {
		t.Fatal("accepted a nonsense size")
	}

	// Transparent letterbox must not bleed colour: opaque white scaled next to
	// the transparent bars keeps pure white.
	w := solid(40, 20, 1, 1, 1, 1)
	lb := apply(t, "geo.fit", w, Args{"size": "20x20"})
	for y := 0; y < 20; y++ {
		if p := px(lb, 10, y); p[3] > 0.01 && (p[0] < 0.995 || p[1] < 0.995 || p[2] < 0.995) {
			t.Fatalf("fit bled colour at row %d: %v", y, p)
		}
	}
}

func TestGeoUpscale(t *testing.T) {
	in := geoMark(3, 2)
	out := apply(t, "geo.upscale", in, Args{})
	geoSize(t, "upscale 4x", out, 12, 8)
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			geoSame(t, "upscale", in, x/4, y/4, out, x, y)
		}
	}
	for _, f := range []string{"3", "3x", "3X"} {
		o := apply(t, "geo.upscale", in, Args{"factor": f})
		geoSize(t, "upscale "+f, o, 9, 6)
		geoSame(t, "upscale "+f, in, 2, 1, o, 8, 5)
	}
	p := geoMark(3, 2)
	p.Scale = 0.5
	if o := apply(t, "geo.upscale", p, Args{"factor": "2"}); o.Scale != 0.5 || o.W != 6 {
		t.Fatalf("proxy upscale: %dx%d scale %v", o.W, o.H, o.Scale)
	}
	h, _ := Lookup("geo.upscale")
	for _, bad := range []string{"2.5", "0", "-2", "big", "99999"} {
		if _, err := RunPixel(context.Background(), h, in, Args{"factor": bad}); err == nil {
			t.Fatalf("accepted factor %q", bad)
		}
	}
	if apply(t, "geo.upscale", in, Args{"factor": "1"}) != in {
		t.Fatal("1x should be a no-op")
	}
}

func TestGeoVideoFilters(t *testing.T) {
	r, _ := Lookup("geo.rotate")
	for ang, want := range map[string]string{"90": "transpose=clock", "180": "hflip,vflip", "-90": "transpose=cclock"} {
		got, err := r.Video(Args{"angle": ang})
		if err != nil || got != want {
			t.Fatalf("rotate %s: %q %v, want %q", ang, got, err, want)
		}
	}
	if got, err := r.Video(Args{"angle": "15"}); err != nil || got == "" {
		t.Fatalf("rotate 15: %q %v", got, err)
	}
	f, _ := Lookup("geo.flip")
	if got, _ := f.Video(Args{"axis": "vertical"}); got != "vflip" {
		t.Fatalf("flip: %q", got)
	}
	if got, _ := f.Video(Args{"axis": "horizontal"}); got != "hflip" {
		t.Fatalf("mirror: %q", got)
	}
}
