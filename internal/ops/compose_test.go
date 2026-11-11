package ops

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/webp"

	"asteria/internal/img"
)

// Helpers (prefixed cmpT to stay clear of other test files).

func cmpTWrite(t *testing.T, dir, name string, m *img.Image) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := EncodeWorking(m, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// cmpTRun runs a Run handler and decodes a single-file output.
func cmpTRun(t *testing.T, handler string, inputs, names []string, out string, args Args) error {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok || h.Run == nil {
		t.Fatalf("no handler %s", handler)
	}
	return h.Run(t.Context(), Prepare(Job{Inputs: inputs, Names: names, Output: out, Args: args}))
}

func cmpTMust(t *testing.T, handler string, inputs, names []string, out string, args Args) *img.Image {
	t.Helper()
	if err := cmpTRun(t, handler, inputs, names, out, args); err != nil {
		t.Fatalf("%s: %v", handler, err)
	}
	m, err := DecodeWorking(out)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func cmpTPx(t *testing.T, m *img.Image, x, y int, want [4]float64, tol float64) {
	t.Helper()
	if x >= m.W || y >= m.H {
		t.Fatalf("pixel (%d,%d) outside %dx%d", x, y, m.W, m.H)
	}
	if got := px(m, x, y); !closePx(got, want, tol) {
		t.Errorf("pixel (%d,%d) = %.3f, want %.3f", x, y, got, want)
	}
}

// cmpTOKLab is a reference linear sRGB -> OKLab.
func cmpTOKLab(r, g, b float64) (float64, float64, float64) {
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s
}

var (
	cmpRed     = [4]float64{1, 0, 0, 1}
	cmpGreen   = [4]float64{0, 1, 0, 1}
	cmpBlue    = [4]float64{0, 0, 1, 1}
	cmpWhitePx = [4]float64{1, 1, 1, 1}
)

func TestComposeFillBackground(t *testing.T) {
	m := solid(4, 4, 1, 0, 0, 0.5)
	a := float64(img.Q(0.5)) / 65535
	out := apply(t, "compose.background", m, Args{})
	// Linear light over white: R = a + (1-a) = 1, G = B = 1-a.
	want := [4]float64{1, linToSRGB(1 - a), linToSRGB(1 - a), 1}
	cmpTPx(t, out, 1, 1, want, 0.002)
	// Over black: R = a, G = B = 0.
	out = apply(t, "compose.background", m, Args{"color": "#000000"})
	cmpTPx(t, out, 2, 2, [4]float64{linToSRGB(a), 0, 0, 1}, 0.002)
	// Input is untouched.
	if r, _, _, al := m.At(0, 0); r != 0xffff || al != img.Q(0.5) {
		t.Fatal("input modified")
	}
	// Opaque input is a no-op, and bad colours are errors.
	op := solid(2, 2, 0.2, 0.4, 0.6, 1)
	if apply(t, "compose.background", op, Args{}) != op {
		t.Error("opaque image should come back unchanged")
	}
	h, _ := Lookup("compose.background")
	if _, err := RunPixel(t.Context(), h, m, Args{"color": "banana"}); err == nil {
		t.Error("bad colour accepted")
	}
	if _, err := RunPixel(t.Context(), h, m, Args{"color": "transparent"}); err == nil {
		t.Error("transparent background accepted")
	}
}

func TestComposeRemoveBackground(t *testing.T) {
	// White page with a blue square; auto picks white from the corners.
	m := solid(20, 20, 1, 1, 1, 1)
	for y := 6; y < 14; y++ {
		for x := 6; x < 14; x++ {
			m.Set(x, y, 0x1000, 0x2000, 0xe000, 0xffff)
		}
	}
	out := apply(t, "compose.removebg", m, Args{})
	cmpTPx(t, out, 0, 0, [4]float64{0, 0, 0, 0}, 0.001)
	cmpTPx(t, out, 19, 19, [4]float64{0, 0, 0, 0}, 0.001)
	want := px(m, 10, 10)
	cmpTPx(t, out, 10, 10, want, 0.0001)
	// An explicit key colour works, case-insensitively.
	out = apply(t, "compose.removebg", m, Args{"color": "WHITE"})
	cmpTPx(t, out, 3, 3, [4]float64{0, 0, 0, 0}, 0.001)
	// Input untouched.
	if _, _, _, a := m.At(0, 0); a != 0xffff {
		t.Fatal("input modified")
	}

	// Soft edge + de-fringe. A pixel that is 30% red, 70% white key in
	// linear light.
	e := solid(1, 1, linToSRGB(1), linToSRGB(0.7), linToSRGB(0.7), 1)
	out = apply(t, "compose.removebg", e, Args{"color": "white"})
	cr, cg, cb := 1.0, 0.7, 0.7
	kL, kA, kB := cmpTOKLab(1, 1, 1)
	L, A, B := cmpTOKLab(cr, cg, cb)
	d := math.Sqrt((L-kL)*(L-kL) + (A-kA)*(A-kA) + (B-kB)*(B-kB))
	tt := math.Max(0, math.Min(1, (d-0.06)/0.10))
	if tt <= 0 || tt >= 1 {
		t.Fatalf("test pixel not on the ramp (t=%g)", tt)
	}
	f := func(c, k float64) float64 { return math.Max(0, math.Min(1, (c-(1-tt)*k)/tt)) }
	got := px(out, 0, 0)
	if math.Abs(got[3]-tt) > 0.002 {
		t.Errorf("edge alpha %.4f, want %.4f", got[3], tt)
	}
	for i, c := range []float64{cr, cg, cb} {
		if w := linToSRGB(f(c, 1)); math.Abs(got[i]-w) > 0.005 {
			t.Errorf("de-fringed channel %d = %.4f, want %.4f", i, got[i], w)
		}
	}
	// The edge colour moved away from white towards red.
	if got[1] > 0.6 {
		t.Errorf("white fringe left in colour: %v", got)
	}
}

func TestComposeDropShadow(t *testing.T) {
	m := solid(40, 40, 1, 0, 0, 1)
	out := apply(t, "compose.shadow", m, Args{})
	// s=40: sigma 0.8, offset (0, 1), pad = ceil(2.4)+1 = 4.
	if out.W != 48 || out.H != 48 {
		t.Fatalf("size %dx%d, want 48x48", out.W, out.H)
	}
	cmpTPx(t, out, 28, 28, cmpRed, 0.001)
	cmpTPx(t, out, 0, 0, [4]float64{0, 0, 0, 0}, 0.001)
	// One pixel below the picture: black, partly opaque, never more than 45%.
	p := px(out, 24, 44)
	if p[3] < 0.1 || p[3] > 0.451 || p[0] != 0 || p[1] != 0 || p[2] != 0 {
		t.Errorf("shadow pixel %v", p)
	}
	// Stronger setting gives a darker shadow.
	p2 := px(apply(t, "compose.shadow", m, Args{"strength": "200%"}), 24, 44)
	if p2[3] <= p[3] {
		t.Errorf("strength 200%% not stronger: %v vs %v", p2[3], p[3])
	}
	// Transparent areas cast no shadow.
	hole := solid(40, 40, 0, 0, 0, 0)
	if v := px(apply(t, "compose.shadow", hole, Args{}), 24, 24)[3]; v != 0 {
		t.Errorf("transparent image cast shadow %v", v)
	}
}

func TestComposeWatermarkText(t *testing.T) {
	m := solid(400, 300, 0.5, 0.5, 0.5, 1)
	out := apply(t, "compose.text", m, Args{"text": "AB Studio"})
	if out.W != m.W || out.H != m.H {
		t.Fatal("size changed")
	}
	var changed, lighter, darker int
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			a, b := px(m, x, y), px(out, x, y)
			if a == b {
				continue
			}
			changed++
			if x < m.W/2 || y < m.H/2 {
				t.Fatalf("pixel (%d,%d) outside the bottom-right quadrant changed", x, y)
			}
			if b[0] > a[0]+0.05 {
				lighter++
			}
			if b[0] < a[0]-0.05 {
				darker++
			}
		}
	}
	if changed == 0 || lighter == 0 || darker == 0 {
		t.Fatalf("expected white text and dark shadow, changed=%d lighter=%d darker=%d", changed, lighter, darker)
	}
	// Bare command stamps the copyright line.
	if bare := apply(t, "compose.text", m, Args{}); bytes.Equal(u16Bytes(bare.Pix), u16Bytes(m.Pix)) {
		t.Error("default watermark changed nothing")
	}
	if _, _, _, a := m.At(399, 299); a != 0xffff {
		t.Fatal("input modified")
	}
	// A very long text shrinks to fit instead of running off the picture.
	long := apply(t, "compose.text", solid(120, 80, 0.5, 0.5, 0.5, 1), Args{"text": "A very long copyright notice that cannot fit"})
	if long.W != 120 {
		t.Fatal("size changed")
	}
}

func u16Bytes(p []uint16) []byte {
	b := make([]byte, 2*len(p))
	for i, v := range p {
		binary.LittleEndian.PutUint16(b[2*i:], v)
	}
	return b
}

func TestComposeGrid(t *testing.T) {
	dir := t.TempDir()
	cols := [][4]float64{cmpRed, cmpGreen, cmpBlue}
	var in []string
	for i, c := range cols {
		in = append(in, cmpTWrite(t, dir, fmt.Sprintf("%d.png", i), solid(100, 100, c[0], c[1], c[2], 1)))
	}
	// 3 images -> 2 columns x 2 rows, cells 100, gap round(2%) = 2.
	out := cmpTMust(t, "compose.grid", in, nil, filepath.Join(dir, "grid.png"), Args{})
	if out.W != 2*100+3*2 || out.H != 2*100+3*2 {
		t.Fatalf("grid size %dx%d, want 206x206", out.W, out.H)
	}
	cmpTPx(t, out, 2+50, 2+50, cmpRed, 0.01)
	cmpTPx(t, out, 104+50, 2+50, cmpGreen, 0.01) // cell 2
	cmpTPx(t, out, 2+50, 104+50, cmpBlue, 0.01)
	cmpTPx(t, out, 104+50, 104+50, cmpWhitePx, 0.01) // empty fourth cell
	cmpTPx(t, out, 0, 0, cmpWhitePx, 0.01)

	out = cmpTMust(t, "compose.grid", in, nil, filepath.Join(dir, "g3.png"), Args{"columns": "3"})
	if out.W != 3*100+4*2 || out.H != 100+2*2 {
		t.Fatalf("3-column size %dx%d", out.W, out.H)
	}
	out = cmpTMust(t, "compose.grid", in, nil, filepath.Join(dir, "g32.png"), Args{"columns": "3x2"})
	if out.W != 308 || out.H != 104 {
		t.Fatalf("3x2 size %dx%d", out.W, out.H)
	}
	if err := cmpTRun(t, "compose.grid", in, nil, filepath.Join(dir, "bad.png"), Args{"columns": "2x1"}); err == nil {
		t.Error("2x1 for 3 images accepted")
	}
	if err := cmpTRun(t, "compose.grid", in, nil, filepath.Join(dir, "bad2.png"), Args{"columns": "lots"}); err == nil {
		t.Error("junk layout accepted")
	}

	// Pictures of different shape are fitted and centred in the cell.
	wide := cmpTWrite(t, dir, "wide.png", solid(100, 50, 1, 0, 0, 1))
	tall := cmpTWrite(t, dir, "tall.png", solid(50, 100, 0, 1, 0, 1))
	out = cmpTMust(t, "compose.grid", []string{wide, tall}, nil, filepath.Join(dir, "mixed.png"), Args{})
	cmpTPx(t, out, 2+50, 2+50, cmpRed, 0.01)
	cmpTPx(t, out, 2+50, 2+10, cmpWhitePx, 0.01) // above the letterboxed wide picture
	cmpTPx(t, out, 104+50, 2+50, cmpGreen, 0.01)
}

func TestComposeContactSheet(t *testing.T) {
	dir := t.TempDir()
	var in, names []string
	for i, c := range [][4]float64{cmpRed, cmpGreen, cmpBlue} {
		in = append(in, cmpTWrite(t, dir, fmt.Sprintf("%d.png", i), solid(100, 100, c[0], c[1], c[2], 1)))
		names = append(names, fmt.Sprintf("photo %d.png", i+1))
	}
	out := cmpTMust(t, "compose.contactsheet", in, names, filepath.Join(dir, "sheet.png"), Args{})
	// fs = max(9, round(5)) = 9, label = round(16.2) = 16, gap 2.
	if out.W != 206 || out.H != 2*(100+16)+3*2 {
		t.Fatalf("sheet size %dx%d, want 206x238", out.W, out.H)
	}
	cmpTPx(t, out, 104+50, 2+50, cmpGreen, 0.01)
	// The label band under cell 1 holds grey text on white.
	dark := 0
	for y := 2 + 100; y < 2+100+16; y++ {
		for x := 2; x < 102; x++ {
			p := px(out, x, y)
			if p[0] < 0.8 {
				dark++
				if math.Abs(p[0]-p[1]) > 0.01 || math.Abs(p[1]-p[2]) > 0.01 {
					t.Fatalf("label pixel not grey: %v", p)
				}
			}
		}
	}
	if dark < 10 {
		t.Errorf("only %d text pixels under the first cell", dark)
	}
	// Nothing but white in the gap between the label and the next row.
	cmpTPx(t, out, 50, 2+100+16+1, cmpWhitePx, 0.001)
}

func TestComposeStrip(t *testing.T) {
	dir := t.TempDir()
	a := cmpTWrite(t, dir, "a.png", solid(20, 10, 1, 0, 0, 1))
	b := cmpTWrite(t, dir, "b.png", solid(10, 10, 0, 1, 0, 1))
	c := cmpTWrite(t, dir, "c.png", solid(30, 15, 0, 0, 1, 1))
	in := []string{a, b, c}
	out := cmpTMust(t, "compose.strip", in, nil, filepath.Join(dir, "h.png"), Args{})
	// Common height 10: widths 20 + 10 + 20.
	if out.W != 50 || out.H != 10 {
		t.Fatalf("strip %dx%d, want 50x10", out.W, out.H)
	}
	cmpTPx(t, out, 5, 5, cmpRed, 0.01)
	cmpTPx(t, out, 25, 5, cmpGreen, 0.01)
	cmpTPx(t, out, 40, 5, cmpBlue, 0.01)
	out = cmpTMust(t, "compose.strip", in, nil, filepath.Join(dir, "v.png"), Args{"direction": "vertical"})
	// Common width 10: heights 5 + 10 + 5.
	if out.W != 10 || out.H != 20 {
		t.Fatalf("vertical strip %dx%d, want 10x20", out.W, out.H)
	}
	cmpTPx(t, out, 5, 2, cmpRed, 0.01)
	cmpTPx(t, out, 5, 10, cmpGreen, 0.01)
	cmpTPx(t, out, 5, 17, cmpBlue, 0.01)
}

func TestComposeOverlayAndWatermarkImage(t *testing.T) {
	dir := t.TempDir()
	base := cmpTWrite(t, dir, "base.png", solid(41, 31, 0, 0, 1, 1))
	top := solid(11, 11, 0, 1, 0, 1)
	top.Set(5, 5, 0xffff, 0, 0xffff, 0xffff) // magenta centre
	topP := cmpTWrite(t, dir, "top.png", top)
	out := cmpTMust(t, "compose.overlay", []string{base, topP}, nil, filepath.Join(dir, "o.png"), Args{})
	if out.W != 41 || out.H != 31 {
		t.Fatalf("overlay size %dx%d", out.W, out.H)
	}
	cmpTPx(t, out, 20, 15, [4]float64{1, 0, 1, 1}, 0.001) // centre of base
	cmpTPx(t, out, 16, 15, cmpGreen, 0.001)
	cmpTPx(t, out, 2, 2, cmpBlue, 0.001)

	// A picture larger than the base is shrunk to fit inside it.
	big := cmpTWrite(t, dir, "big.png", solid(100, 20, 1, 0, 0, 1))
	out = cmpTMust(t, "compose.overlay", []string{base, big}, nil, filepath.Join(dir, "o2.png"), Args{})
	if out.W != 41 || out.H != 31 {
		t.Fatalf("size %dx%d", out.W, out.H)
	}
	cmpTPx(t, out, 1, 15, cmpRed, 0.01)
	cmpTPx(t, out, 20, 2, cmpBlue, 0.01)

	// Exactly two inputs.
	for _, in := range [][]string{{base}, {base, topP, big}} {
		err := cmpTRun(t, "compose.overlay", in, nil, filepath.Join(dir, "x.png"), Args{})
		assertUserError(t, err)
		err = cmpTRun(t, "compose.watermarkimage", in, nil, filepath.Join(dir, "y.png"), Args{})
		assertUserError(t, err)
	}

	// Logo: 20% of 100 = 20 wide, 80% opaque, margin round(3% of 80) = 2.
	b2 := cmpTWrite(t, dir, "b2.png", solid(100, 80, 0, 0, 1, 1))
	logo := cmpTWrite(t, dir, "logo.png", solid(10, 10, 1, 0, 0, 1))
	out = cmpTMust(t, "compose.watermarkimage", []string{b2, logo}, nil, filepath.Join(dir, "w.png"), Args{})
	if out.W != 100 || out.H != 80 {
		t.Fatalf("size %dx%d", out.W, out.H)
	}
	// Linear light: R = 0.8, B = 0.2.
	cmpTPx(t, out, 88, 68, [4]float64{linToSRGB(0.8), 0, linToSRGB(0.2), 1}, 0.005)
	cmpTPx(t, out, 10, 10, cmpBlue, 0.001)
	cmpTPx(t, out, 99, 79, cmpBlue, 0.001) // inside the margin
}

func TestComposeAnimatedGIF(t *testing.T) {
	dir := t.TempDir()
	in := []string{
		cmpTWrite(t, dir, "1.png", solid(40, 30, 1, 0, 0, 1)),
		cmpTWrite(t, dir, "2.png", solid(20, 20, 0, 1, 0, 1)), // smaller: letterboxed on white
		cmpTWrite(t, dir, "3.png", solid(40, 30, 0, 0, 1, 1)),
	}
	out := filepath.Join(dir, "a.gif")
	if err := cmpTRun(t, "compose.gif", in, nil, out, Args{"delay": "0.25"}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(out)
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 3 || g.LoopCount != 0 {
		t.Fatalf("frames=%d loop=%d", len(g.Image), g.LoopCount)
	}
	for i, d := range g.Delay {
		if d != 25 {
			t.Errorf("frame %d delay %d, want 25", i, d)
		}
	}
	at := func(i, x, y int) [3]int {
		r, gg, b, _ := g.Image[i].At(x, y).RGBA()
		return [3]int{int(r >> 8), int(gg >> 8), int(b >> 8)}
	}
	for i, want := range [][3]int{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}} {
		got := at(i, 20, 15)
		for k := range got {
			if !near(got[k], want[k], 6) {
				t.Errorf("frame %d centre %v, want %v", i, got, want)
				break
			}
		}
	}
	if got := at(1, 1, 1); got[0] < 250 || got[1] < 250 || got[2] < 250 {
		t.Errorf("letterbox not white: %v", got)
	}
	if b := g.Image[0].Bounds(); b.Dx() != 40 || b.Dy() != 30 {
		t.Errorf("frame size %v", b)
	}
	// Default delay is half a second; one image is not an animation.
	if err := cmpTRun(t, "compose.gif", in, nil, filepath.Join(dir, "d.gif"), Args{}); err != nil {
		t.Fatal(err)
	}
	f2, _ := os.Open(filepath.Join(dir, "d.gif"))
	defer f2.Close()
	if g2, _ := gif.DecodeAll(f2); g2 == nil || g2.Delay[0] != 50 {
		t.Errorf("default delay: %+v", g2)
	}
	assertUserError(t, cmpTRun(t, "compose.gif", in[:1], nil, filepath.Join(dir, "e.gif"), Args{}))
	assertUserError(t, cmpTRun(t, "compose.gif", in, nil, filepath.Join(dir, "f.gif"), Args{"delay": "0"}))
}

func TestComposeQuantizeKeepsAverageColour(t *testing.T) {
	// 1024 distinct colours reduced to a 256-colour palette with dithering.
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x * 8), uint8(y * 8), uint8((x + y) * 4), 255})
		}
	}
	pal := cmpQuantize(src, 256)
	if len(pal) == 0 || len(pal) > 256 {
		t.Fatalf("palette size %d", len(pal))
	}
	dst := image.NewPaletted(src.Bounds(), pal)
	draw.FloydSteinberg.Draw(dst, src.Bounds(), src, image.Point{})
	var sum, sumRef [3]float64
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			r, g, b, _ := dst.At(x, y).RGBA()
			sum[0] += float64(r >> 8)
			sum[1] += float64(g >> 8)
			sum[2] += float64(b >> 8)
			c := src.NRGBAAt(x, y)
			sumRef[0] += float64(c.R)
			sumRef[1] += float64(c.G)
			sumRef[2] += float64(c.B)
		}
	}
	for i := range sum {
		if math.Abs(sum[i]-sumRef[i])/1024 > 4 {
			t.Errorf("channel %d mean off by %.2f", i, math.Abs(sum[i]-sumRef[i])/1024)
		}
	}
	// Two flat colours stay exact.
	flat := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.NRGBA{200, 10, 30, 255}), image.Point{}, draw.Src)
	flat.SetNRGBA(0, 0, color.NRGBA{9, 99, 199, 255})
	p := cmpQuantize(flat, 256)
	if len(p) != 2 {
		t.Fatalf("flat palette %v", p)
	}
}

func TestComposeAnimatedWebPAndFrames(t *testing.T) {
	dir := t.TempDir()
	in := []string{
		cmpTWrite(t, dir, "1.png", solid(32, 32, 1, 0, 0, 1)),
		cmpTWrite(t, dir, "2.png", solid(32, 32, 0, 0, 1, 1)),
	}
	out := filepath.Join(dir, "a.webp")
	if err := cmpTRun(t, "compose.webpanim", in, nil, out, Args{"delay": "0.3"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	a, err := webp.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Image) != 2 || a.LoopCount != 0 {
		t.Fatalf("frames=%d loop=%d", len(a.Image), a.LoopCount)
	}
	for i, d := range a.Delay {
		if d < 280 || d > 320 {
			t.Errorf("frame %d delay %dms", i, d)
		}
	}
	m := img.FromStd(a.Image[1])
	cmpTPx(t, m, 16, 16, cmpBlue, 0.08)

	// And the frames come back out.
	dst := filepath.Join(dir, "frames")
	if err := cmpTRun(t, "compose.frames", []string{out}, []string{"a.webp"}, dst, Args{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"frame_001.png", "frame_002.png"} {
		if _, err := os.Stat(filepath.Join(dst, n)); err != nil {
			t.Error(err)
		}
	}
}

func TestComposeSpriteSheet(t *testing.T) {
	dir := t.TempDir()
	var in []string
	for i := 0; i < 5; i++ {
		w, h := 10, 10
		if i == 1 {
			w, h = 16, 12
		}
		in = append(in, cmpTWrite(t, dir, fmt.Sprintf("%d.png", i), solid(w, h, float64(i)/4, 1-float64(i)/4, 0.5, 1)))
	}
	out := cmpTMust(t, "compose.spritesheet", in, nil, filepath.Join(dir, "s.png"), Args{})
	// 5 frames -> 3 columns x 2 rows of 16x12 cells, no gaps.
	if out.W != 48 || out.H != 24 {
		t.Fatalf("sheet %dx%d, want 48x24", out.W, out.H)
	}
	colour := func(i int) [4]float64 {
		return [4]float64{float64(i) / 4, 1 - float64(i)/4, 0.5, 1}
	}
	// Frame 0 (10x10) centred in cell (0,0): offset (3, 1).
	cmpTPx(t, out, 3+5, 1+5, colour(0), 0.01)
	cmpTPx(t, out, 2, 5, [4]float64{0, 0, 0, 0}, 0.001) // transparent around it
	// Frame 1 fills cell (1,0) exactly.
	cmpTPx(t, out, 16, 0, colour(1), 0.01)
	cmpTPx(t, out, 31, 11, colour(1), 0.01)
	// Frame 3 in row 2, column 1: cell origin (0,12).
	cmpTPx(t, out, 3+5, 12+1+5, colour(3), 0.01)
	// Frame 4: cell (1,1), origin (16,12).
	cmpTPx(t, out, 16+3+5, 12+1+5, colour(4), 0.01)
	// Unused sixth cell stays transparent.
	cmpTPx(t, out, 40, 18, [4]float64{0, 0, 0, 0}, 0.001)
}

func TestComposeSplitTiles(t *testing.T) {
	dir := t.TempDir()
	// A picture whose every pixel is distinct, so re-assembly is a real test.
	src := img.New(10, 7)
	for y := 0; y < 7; y++ {
		for x := 0; x < 10; x++ {
			src.Set(x, y, uint16(x*25*257), uint16(y*36*257), uint16((x+y)*10*257), 0xffff)
		}
	}
	p := cmpTWrite(t, dir, "src.png", src)
	ref, err := DecodeWorking(p)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "tiles")
	if err := cmpTRun(t, "compose.split", []string{p}, []string{"src.png"}, out, Args{"grid": "3x2"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 6 {
		t.Fatalf("%d tiles, want 6", len(entries))
	}
	// Columns: widths 3, 3, 4 (remainder last); rows: heights 3, 4.
	re := img.New(10, 7)
	widths, heights := []int{3, 3, 4}, []int{3, 4}
	y0 := 0
	for r := 0; r < 2; r++ {
		x0 := 0
		for c := 0; c < 3; c++ {
			tile, err := DecodeWorking(filepath.Join(out, fmt.Sprintf("tile_r%d_c%d.png", r+1, c+1)))
			if err != nil {
				t.Fatal(err)
			}
			if tile.W != widths[c] || tile.H != heights[r] {
				t.Errorf("tile r%d c%d is %dx%d, want %dx%d", r+1, c+1, tile.W, tile.H, widths[c], heights[r])
			}
			for y := 0; y < tile.H; y++ {
				for x := 0; x < tile.W; x++ {
					a, b, cc, d := tile.At(x, y)
					re.Set(x0+x, y0+y, a, b, cc, d)
				}
			}
			x0 += widths[c]
		}
		y0 += heights[r]
	}
	if !bytes.Equal(u16Bytes(re.Pix), u16Bytes(ref.Pix)) {
		t.Error("re-assembled tiles differ from the input")
	}
	// Default is 2x2; single number is square; oversize grids are errors.
	out2 := filepath.Join(dir, "tiles2")
	if err := cmpTRun(t, "compose.split", []string{p}, nil, out2, Args{}); err != nil {
		t.Fatal(err)
	}
	if e, _ := os.ReadDir(out2); len(e) != 4 {
		t.Errorf("default split wrote %d tiles", len(e))
	}
	out3 := filepath.Join(dir, "tiles3")
	if err := cmpTRun(t, "compose.split", []string{p}, nil, out3, Args{"grid": "3"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := os.ReadDir(out3); len(e) != 9 {
		t.Errorf("3 wrote %d tiles", len(e))
	}
	bad := filepath.Join(dir, "bad")
	assertUserError(t, cmpTRun(t, "compose.split", []string{p}, nil, bad, Args{"grid": "20x20"}))
	assertUserError(t, cmpTRun(t, "compose.split", []string{p}, nil, bad, Args{"grid": "wide"}))
	if _, err := os.Stat(bad); err == nil {
		t.Error("failed split left a folder behind")
	}
}

func TestComposeExtractGIFFrames(t *testing.T) {
	dir := t.TempDir()
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}, color.RGBA{255, 255, 0, 255}}
	frame := func(r image.Rectangle, idx uint8) *image.Paletted {
		p := image.NewPaletted(r, pal)
		for i := range p.Pix {
			p.Pix[i] = idx
		}
		return p
	}
	g := &gif.GIF{
		Config: image.Config{ColorModel: pal, Width: 6, Height: 4},
		Image: []*image.Paletted{
			frame(image.Rect(0, 0, 6, 4), 0), // red everywhere
			frame(image.Rect(2, 1, 4, 3), 2), // blue 2x2, then cleared
			frame(image.Rect(0, 0, 2, 2), 1), // green 2x2, then restored
			frame(image.Rect(5, 3, 6, 4), 3), // yellow pixel
		},
		Delay:    []int{5, 5, 5, 5},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious, gif.DisposalNone},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "anim.gif")
	if err := os.WriteFile(in, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "frames")
	if err := cmpTRun(t, "compose.frames", []string{in}, []string{"anim.gif"}, out, Args{}); err != nil {
		t.Fatal(err)
	}
	if e, _ := os.ReadDir(out); len(e) != 4 {
		t.Fatalf("%d frames written", len(e))
	}
	load := func(i int) *img.Image {
		m, err := DecodeWorking(filepath.Join(out, fmt.Sprintf("frame_%03d.png", i)))
		if err != nil {
			t.Fatal(err)
		}
		if m.W != 6 || m.H != 4 {
			t.Fatalf("frame %d is %dx%d", i, m.W, m.H)
		}
		return m
	}
	none := [4]float64{0, 0, 0, 0}
	yellow := [4]float64{1, 1, 0, 1}
	f1, f2, f3, f4 := load(1), load(2), load(3), load(4)
	cmpTPx(t, f1, 0, 0, cmpRed, 0.01)
	cmpTPx(t, f1, 2, 1, cmpRed, 0.01)
	cmpTPx(t, f2, 2, 1, cmpBlue, 0.01)
	cmpTPx(t, f2, 3, 2, cmpBlue, 0.01)
	cmpTPx(t, f2, 4, 1, cmpRed, 0.01)
	// Frame 2 was disposed to background: its rectangle is transparent.
	cmpTPx(t, f3, 2, 1, none, 0.01)
	cmpTPx(t, f3, 0, 0, cmpGreen, 0.01)
	cmpTPx(t, f3, 5, 0, cmpRed, 0.01)
	// Frame 3 was disposed to previous: green disappears, the hole stays.
	cmpTPx(t, f4, 0, 0, cmpRed, 0.01)
	cmpTPx(t, f4, 2, 1, none, 0.01)
	cmpTPx(t, f4, 5, 3, yellow, 0.01)

	// Not an animation: an error that says what to do.
	still := cmpTWrite(t, dir, "still.png", solid(4, 4, 1, 0, 0, 1))
	assertUserError(t, cmpTRun(t, "compose.frames", []string{still}, nil, filepath.Join(dir, "nf"), Args{}))
}

type cmpTICOEntry struct {
	w, h int
	png  image.Image
}

func cmpTReadICO(t *testing.T, path string) []cmpTICOEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		t.Fatalf("bad ICO header % x", b[:min(6, len(b))])
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	var out []cmpTICOEntry
	for i := 0; i < n; i++ {
		e := b[6+16*i:][:16]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := int(binary.LittleEndian.Uint32(e[8:]))
		off := int(binary.LittleEndian.Uint32(e[12:]))
		if off+size > len(b) {
			t.Fatalf("entry %d runs past the file", i)
		}
		im, err := png.Decode(bytes.NewReader(b[off : off+size]))
		if err != nil {
			t.Fatalf("entry %d is not a PNG: %v", i, err)
		}
		out = append(out, cmpTICOEntry{w, h, im})
	}
	return out
}

func TestComposeFaviconSet(t *testing.T) {
	dir := t.TempDir()
	// 40x20: left half red, right half transparent; centre-cropped to the
	// middle 20x20 (x 10..29).
	src := img.New(40, 20)
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			src.Set(x, y, 0xffff, 0, 0, 0xffff)
		}
	}
	p := cmpTWrite(t, dir, "logo.png", src)
	out := filepath.Join(dir, "fav")
	if err := cmpTRun(t, "compose.favicon", []string{p}, []string{"logo.png"}, out, Args{}); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"favicon-16.png": 16, "favicon-32.png": 32, "apple-touch-icon.png": 180, "icon-192.png": 192, "icon-512.png": 512} {
		f, err := os.Open(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		if err != nil || cfg.Width != size || cfg.Height != size {
			t.Errorf("%s: %v %dx%d, want %d", name, err, cfg.Width, cfg.Height, size)
		}
	}
	ico := cmpTReadICO(t, filepath.Join(out, "favicon.ico"))
	if len(ico) != 3 {
		t.Fatalf("%d ICO entries", len(ico))
	}
	for i, want := range []int{16, 32, 48} {
		b := ico[i].png.Bounds()
		if ico[i].w != want || ico[i].h != want || b.Dx() != want || b.Dy() != want {
			t.Errorf("entry %d is %dx%d (png %v), want %d", i, ico[i].w, ico[i].h, b, want)
		}
	}
	// Apple touch icon is opaque with white where the logo was transparent.
	at, err := DecodeWorking(filepath.Join(out, "apple-touch-icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !at.Opaque() {
		t.Error("apple-touch-icon.png is not opaque")
	}
	cmpTPx(t, at, 10, 90, cmpRed, 0.02)
	cmpTPx(t, at, 170, 90, cmpWhitePx, 0.02)
	// The 512 icon keeps the transparency.
	big, _ := DecodeWorking(filepath.Join(out, "icon-512.png"))
	cmpTPx(t, big, 500, 256, [4]float64{0, 0, 0, 0}, 0.01)
}

func TestComposeAppIconSet(t *testing.T) {
	dir := t.TempDir()
	p := cmpTWrite(t, dir, "icon.png", solid(48, 64, 0.2, 0.5, 0.9, 1))
	out := filepath.Join(dir, "app")
	if err := cmpTRun(t, "compose.appicon", []string{p}, nil, out, Args{}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []int{16, 32, 64, 128, 256, 512, 1024} {
		f, err := os.Open(filepath.Join(out, fmt.Sprintf("icon_%d.png", s)))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		if err != nil || cfg.Width != s || cfg.Height != s {
			t.Errorf("icon_%d.png: %v %dx%d", s, err, cfg.Width, cfg.Height)
		}
	}
	ico := cmpTReadICO(t, filepath.Join(out, "app.ico"))
	if len(ico) != 4 {
		t.Fatalf("%d ICO entries", len(ico))
	}
	for i, want := range []int{16, 32, 48, 256} {
		if ico[i].w != want || ico[i].png.Bounds().Dx() != want {
			t.Errorf("entry %d is %d (png %d), want %d", i, ico[i].w, ico[i].png.Bounds().Dx(), want)
		}
	}
	m, _ := DecodeWorking(filepath.Join(out, "icon_128.png"))
	cmpTPx(t, m, 64, 64, [4]float64{0.2, 0.5, 0.9, 1}, 0.01)
}
