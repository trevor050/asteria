package ops

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"asteria/internal/img"
)

func lutTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func lutTestNear(t *testing.T, what string, got, want, tol float32) {
	t.Helper()
	if d := float32(math.Abs(float64(got - want))); d > tol {
		t.Errorf("%s = %.6f, want %.6f (tol %.6f)", what, got, want, tol)
	}
}

// lutTestMaxDiff is the largest per-channel difference (16-bit units) in RGB;
// alpha must match exactly.
func lutTestMaxDiff(t *testing.T, a, b *img.Image) int {
	t.Helper()
	if a.W != b.W || a.H != b.H {
		t.Fatalf("size mismatch")
	}
	worst := 0
	for i := 0; i < len(a.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			d := int(a.Pix[i+c]) - int(b.Pix[i+c])
			if d < 0 {
				d = -d
			}
			worst = max(worst, d)
		}
		if a.Pix[i+3] != b.Pix[i+3] {
			t.Fatalf("alpha changed at %d: %d -> %d", i/4, a.Pix[i+3], b.Pix[i+3])
		}
	}
	return worst
}

func TestLUTIdentityLeavesPixels(t *testing.T) {
	src := scene()
	for name, l := range map[string]*lutData{
		"3d-17": lutIdentity3D(17),
		"1d": {Dim: 1, Size: 4, Max: [3]float32{1, 1, 1},
			Data: []float32{0, 0, 0, 1.0 / 3, 1.0 / 3, 1.0 / 3, 2.0 / 3, 2.0 / 3, 2.0 / 3, 1, 1, 1}},
	} {
		p := lutTestFile(t, "id.cube", lutWriteCube(l))
		out := apply(t, "lut.apply", src, Args{"lut": p})
		if d := lutTestMaxDiff(t, src, out); d > 2 {
			t.Errorf("%s: identity moved a channel by %d/65535", name, d)
		}
	}
}

func TestLUTSwapRedBlue(t *testing.T) {
	l := lutIdentity3D(3)
	for i := 0; i < len(l.Data); i += 3 {
		l.Data[i], l.Data[i+2] = l.Data[i+2], l.Data[i]
	}
	p := lutTestFile(t, "swap.cube", lutWriteCube(l))

	// On lattice points the channels are exactly swapped.
	grid := []uint16{0, 32768, 65535}
	m := img.New(27, 1)
	k := 0
	for _, r := range grid {
		for _, g := range grid {
			for _, b := range grid {
				m.Set(k, 0, r, g, b, 0xffff)
				k++
			}
		}
	}
	out := apply(t, "lut.apply", m, Args{"lut": p})
	for x := 0; x < 27; x++ {
		r, g, b, _ := m.At(x, 0)
		or, og, ob, _ := out.At(x, 0)
		for _, pair := range [][2]uint16{{or, b}, {og, g}, {ob, r}} {
			if d := int(pair[0]) - int(pair[1]); d < -1 || d > 1 {
				t.Fatalf("grid pixel %d: got (%d,%d,%d) from (%d,%d,%d)", x, or, og, ob, r, g, b)
			}
		}
	}

	// A swap is a linear map, which tetrahedral interpolation reproduces
	// exactly between lattice points too.
	src := scene()
	got := apply(t, "lut.apply", src, Args{"lut": p})
	want := src.NewLike()
	for i := 0; i < len(src.Pix); i += 4 {
		want.Pix[i], want.Pix[i+1], want.Pix[i+2], want.Pix[i+3] = src.Pix[i+2], src.Pix[i+1], src.Pix[i], src.Pix[i+3]
	}
	if d := lutTestMaxDiff(t, want, got); d > 3 {
		t.Errorf("off-grid swap differs by %d/65535", d)
	}
}

// A 2-point cube whose only non-zero entry is red at the (1,1,1) corner.
func lutCornerLUT(corner int) *lutData {
	l := &lutData{Dim: 3, Size: 2, Max: [3]float32{1, 1, 1}, Data: make([]float32, 24)}
	l.Data[3*corner] = 1 // red channel of that corner
	return l
}

func TestLUTTetrahedralByHand(t *testing.T) {
	// Input (0.5, 0.25, 0.75): b > r >= g, so the path is
	// c000 -> c001 (b) -> c101 (r) -> c111 (g) and the red output is
	// g * (c111 - c101) = 0.25. Trilinear would give r*g*b = 0.09375.
	l := lutCornerLUT(7)
	p := lutTestFile(t, "corner.cube", lutWriteCube(l))
	m := img.New(1, 1)
	m.Set(0, 0, 32768, 16384, 49152, 0xffff)
	out := apply(t, "lut.apply", m, Args{"lut": p})
	r, g, b, _ := out.At(0, 0)
	if d := int(r) - 16384; d < -2 || d > 2 || g != 0 || b != 0 {
		t.Errorf("got (%d,%d,%d), want (16384,0,0)", r, g, b)
	}

	// Over all six orderings of the fractions: the c111-only LUT returns the
	// smallest fraction (last step of the walk) and the c000-only LUT returns
	// 1 - the largest.
	vals := []float32{0.2, 0.5, 0.8}
	perms := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, pm := range perms {
		r, g, b := vals[pm[0]], vals[pm[1]], vals[pm[2]]
		lo, _, _ := lutCornerLUT(7).eval(r, g, b)
		lutTestNear(t, "c111 corner", lo, 0.2, 1e-6)
		hi, _, _ := lutCornerLUT(0).eval(r, g, b)
		lutTestNear(t, "c000 corner", hi, 0.2, 1e-6) // 1 - 0.8
	}
}

const lutTestCube = "# made by hand\r\nTITLE \"My Look\"\r\nLUT_3D_SIZE 2\r\nDOMAIN_MIN 0 0 0\r\nDOMAIN_MAX 1 1 1\r\n" +
	"0 0 0 # black\r\n1 0 0\r\n0 1 0\r\n1 1 0\r\n\r\n0 0 1\r\n1 0 1\r\n0 1 1\r\n1 1 1\r\n"

func TestLUTParseCube(t *testing.T) {
	l, err := lutParseCube([]byte(lutTestCube))
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "My Look" || l.Dim != 3 || l.Size != 2 {
		t.Fatalf("parsed %+v", l)
	}
	r, g, b := l.eval(0.2, 0.5, 0.9)
	lutTestNear(t, "r", r, 0.2, 1e-6)
	lutTestNear(t, "g", g, 0.5, 1e-6)
	lutTestNear(t, "b", b, 0.9, 1e-6)

	// DOMAIN_MIN/MAX: input -1..1 maps onto the table, so image value 0.5 is
	// table position 0.75.
	d, err := lutParseCube([]byte(strings.Replace(strings.Replace(lutTestCube, "DOMAIN_MIN 0 0 0", "DOMAIN_MIN -1 -1 -1", 1), "DOMAIN_MAX 1 1 1", "DOMAIN_MAX 1 1 1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ = d.eval(0.5, 0.5, 0.5)
	lutTestNear(t, "domain r", r, 0.75, 1e-6)

	// 1D, inverting.
	one, err := lutParseCube([]byte("LUT_1D_SIZE 2\n1 1 1\n0 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b = one.eval(0.25, 0.5, 1)
	lutTestNear(t, "1d r", r, 0.75, 1e-6)
	lutTestNear(t, "1d g", g, 0.5, 1e-6)
	lutTestNear(t, "1d b", b, 0, 1e-6)

	for name, bad := range map[string]string{
		"short":  "LUT_3D_SIZE 2\n0 0 0\n1 1 1\n",
		"both":   "LUT_1D_SIZE 2\nLUT_3D_SIZE 2\n0 0 0\n1 1 1\n",
		"huge":   "LUT_3D_SIZE 129\n0 0 0\n",
		"nosize": "0 0 0\n",
		"junk":   "LUT_3D_SIZE 2\n0 0 x\n",
		"extra":  "LUT_1D_SIZE 2\n0 0 0\n1 1 1\n2 2 2\n",
	} {
		if _, err := lutParseCube([]byte(bad)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLUTParse3DLOrder(t *testing.T) {
	// Blue varies fastest in .3dl; an identity table therefore lists
	// (0,0,0) (0,0,1) (0,1,0) (0,1,1) (1,0,0) ... with 12-bit output.
	txt := "3DMESH\nMesh 1 12\n0 1023\n" +
		"0 0 0\n0 0 4095\n0 4095 0\n0 4095 4095\n4095 0 0\n4095 0 4095\n4095 4095 0\n4095 4095 4095\n"
	l, err := lutParse3DL([]byte(txt))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b := l.eval(0.2, 0.5, 0.9)
	lutTestNear(t, "r", r, 0.2, 1e-6)
	lutTestNear(t, "g", g, 0.5, 1e-6)
	lutTestNear(t, "b", b, 0.9, 1e-6)

	// 16-bit data is recognised by its range.
	big := strings.ReplaceAll(txt, "4095", "65535")
	l, err = lutParse3DL([]byte(big))
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ = l.eval(1, 1, 1)
	lutTestNear(t, "16-bit white", r, 1, 1e-6)

	if _, err := lutParse3DL([]byte("0 1023\n0 0 0\n1 1 1\n")); err == nil {
		t.Error("expected an error for a table with the wrong entry count")
	}
}

// lutTestLook is a non-linear 5-point look whose values reach 1.
func lutTestLook() *lutData {
	n := 5
	l := lutIdentity3D(n)
	l.Title = "look"
	for i := 0; i < len(l.Data); i += 3 {
		r, g, b := l.Data[i], l.Data[i+1], l.Data[i+2]
		l.Data[i] = float32(math.Pow(float64(r), 0.8))
		l.Data[i+1] = g*g*0.5 + 0.5*g
		l.Data[i+2] = (b + r*g) / 2
		if r == 1 && g == 1 && b == 1 {
			l.Data[i], l.Data[i+1], l.Data[i+2] = 1, 1, 1
		}
	}
	return l
}

func lutTestSameCube(t *testing.T, a, b *lutData, tol float32) {
	t.Helper()
	if a.Dim != b.Dim || a.Size != b.Size || len(a.Data) != len(b.Data) {
		t.Fatalf("shape differs: %d/%d vs %d/%d", a.Dim, a.Size, b.Dim, b.Size)
	}
	for i := range a.Data {
		if d := float32(math.Abs(float64(a.Data[i] - b.Data[i]))); d > tol {
			t.Fatalf("entry %d: %.6f vs %.6f", i, a.Data[i], b.Data[i])
		}
	}
}

func TestLUTCubeTo3DLToCube(t *testing.T) {
	look := lutTestLook()
	in := lutTestFile(t, "look.cube", lutWriteCube(look))
	dl := mustRun(t, "lut.to3dl", []string{in}, "look.3dl", Args{})
	back := mustRun(t, "lut.toCube", []string{dl}, "back.cube", Args{})
	got, err := lutParseFile(back)
	if err != nil {
		t.Fatal(err)
	}
	// 12-bit output quantisation: half a step of 1/4095.
	lutTestSameCube(t, look, got, 0.5/4095+1e-6)

	// The cube file itself round-trips to the 6 decimals it writes.
	rt, err := lutParseCube(lutWriteCube(look))
	if err != nil {
		t.Fatal(err)
	}
	lutTestSameCube(t, look, rt, 1e-6)
	if rt.Title != "look" {
		t.Errorf("title = %q", rt.Title)
	}
}

func TestLUT1DTo3DL(t *testing.T) {
	inv := &lutData{Dim: 1, Size: 2, Max: [3]float32{1, 1, 1}, Data: []float32{1, 1, 1, 0, 0, 0}}
	in := lutTestFile(t, "inv.cube", lutWriteCube(inv))
	dl := mustRun(t, "lut.to3dl", []string{in}, "inv.3dl", Args{})
	got, err := lutParseFile(dl)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != 33 {
		t.Errorf("1D tables become 33-point cubes, got %d", got.Size)
	}
	r, g, b := got.eval(0.25, 0.5, 1)
	lutTestNear(t, "r", r, 0.75, 2e-4)
	lutTestNear(t, "g", g, 0.5, 2e-4)
	lutTestNear(t, "b", b, 0, 2e-4)
}

func TestLUTHaldLevel8Identity(t *testing.T) {
	id := lutIdentity3D(64)
	h := lutToHaldImage(id, 8)
	if h.W != 512 || h.H != 512 {
		t.Fatalf("level 8 should be 512x512, got %dx%d", h.W, h.H)
	}
	back, err := lutFromHald(h)
	if err != nil {
		t.Fatal(err)
	}
	lutTestSameCube(t, id, back, 1.0/65535)

	if _, err := lutFromHald(img.New(10, 10)); err == nil {
		t.Error("a 10x10 image is not a Hald CLUT")
	}
	if _, err := lutFromHald(img.New(512, 500)); err == nil {
		t.Error("a non-square image is not a Hald CLUT")
	}
}

func TestLUTHaldFile(t *testing.T) {
	look := lutTestLook()
	in := lutTestFile(t, "look.cube", lutWriteCube(look))
	png := mustRun(t, "lut.toHald", []string{in}, "look.png", Args{"level": 4})
	l, err := lutParseFile(png)
	if err != nil {
		t.Fatal(err)
	}
	if l.Size != 16 {
		t.Fatalf("level 4 should give a 16-point cube, got %d", l.Size)
	}
	// Compare on the Hald's own grid: evaluating the original look there,
	// within 8-bit PNG quantisation.
	want := look.resample3D(16)
	lutTestSameCube(t, want, l, 0.5/255+1e-4)

	// Applying the Hald PNG gives (nearly) the same picture as the cube.
	src := scene()
	viaCube := apply(t, "lut.apply", src, Args{"lut": in})
	viaHald := apply(t, "lut.apply", src, Args{"lut": png})
	if d := lutTestMaxDiff(t, viaCube, viaHald); d > 4000 { // ~6% of full scale: 5-point vs 16-point resampling of a curved look
		t.Errorf("hald and cube results differ by %d/65535", d)
	}
}

func TestLUTResolveAndErrors(t *testing.T) {
	p := lutTestFile(t, "quoted.cube", lutWriteCube(lutIdentity3D(2)))
	if _, err := lutResolve(`"` + p + `"`); err != nil {
		t.Errorf("quoted path: %v", err)
	}
	if _, err := lutResolve(strings.TrimSuffix(p, ".cube")); err != nil {
		t.Errorf("path without extension: %v", err)
	}
	if _, err := lutResolve("definitely-not-a-real-lut-name"); err == nil {
		t.Error("expected an error for a missing LUT")
	}
	h, ok := Lookup("lut.apply")
	if !ok {
		t.Fatal("lut.apply not registered")
	}
	if _, err := h.Pixel(context.Background(), scene(), Args{}); err == nil {
		t.Error("expected an error when no LUT is given")
	}
	txt := lutTestFile(t, "x.txt", []byte("hi"))
	if _, err := lutParseFile(txt); err == nil {
		t.Error("a .txt file is not a LUT")
	}
}
