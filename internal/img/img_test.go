package img

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"testing"
)

func solid(w, h int, r, g, b, a uint16) *Image {
	m := New(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, r, g, b, a)
		}
	}
	return m
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestTransferRoundTripWithinOneStep(t *testing.T) {
	for v := 0; v <= 65535; v += 7 {
		if back := Enc(Lin(uint16(v))); math.Abs(float64(back)-float64(v)) > 1 {
			t.Fatalf("%d -> %v -> %d", v, Lin(uint16(v)), back)
		}
	}
	// Known value: sRGB 0.5 is 0.21404 linear.
	if !near(float64(Lin(Q(0.5))), 0.21404, 1e-4) {
		t.Fatalf("lin(0.5) = %v", Lin(Q(0.5)))
	}
}

func TestOKLabRoundTripAndWhite(t *testing.T) {
	L, A, B := LinearToOKLab(1, 1, 1)
	if !near(float64(L), 1, 1e-3) || !near(float64(A), 0, 1e-3) || !near(float64(B), 0, 1e-3) {
		t.Fatalf("white = %v %v %v", L, A, B)
	}
	r, g, b := OKLabToLinear(LinearToOKLab(0.2, 0.5, 0.8))
	if !near(float64(r), 0.2, 1e-3) || !near(float64(g), 0.5, 1e-3) || !near(float64(b), 0.8, 1e-3) {
		t.Fatalf("round trip %v %v %v", r, g, b)
	}
}

func TestFromStdKeepsDepthAndBounds(t *testing.T) {
	src := image.NewNRGBA64(image.Rect(10, 20, 13, 22)) // non-zero origin
	src.SetNRGBA64(12, 21, color.NRGBA64{R: 0x1234, G: 0x5678, B: 0x9abc, A: 0xffff})
	m := FromStd(src)
	if !m.Deep || m.W != 3 || m.H != 2 {
		t.Fatalf("deep=%v size=%dx%d", m.Deep, m.W, m.H)
	}
	if r, g, b, _ := m.At(2, 1); r != 0x1234 || g != 0x5678 || b != 0x9abc {
		t.Fatalf("pixel %x %x %x", r, g, b)
	}
	n := image.NewNRGBA(image.Rect(5, 5, 7, 7))
	n.SetNRGBA(6, 6, color.NRGBA{200, 100, 50, 128})
	m = FromStd(n)
	if r, g, b, a := m.At(1, 1); r != 200*0x101 || g != 100*0x101 || b != 50*0x101 || a != 128*0x101 {
		t.Fatalf("nrgba pixel %d %d %d %d", r, g, b, a)
	}
}

// Downscaling a hard edge between opaque blue and transparent red must not
// produce red or dark fringes: premultiplied mixing ignores the colour of
// transparent pixels.
func TestResizeIsPremultipliedLinear(t *testing.T) {
	m := New(64, 8)
	for y := 0; y < 8; y++ {
		for x := 0; x < 64; x++ {
			if x < 32 {
				m.Set(x, y, 0, 0, 0xffff, 0xffff)
			} else {
				m.Set(x, y, 0xffff, 0, 0, 0) // transparent red
			}
		}
	}
	out := Resize(m, 16, 2, Lanczos3)
	for x := 0; x < 16; x++ {
		r, _, b, a := out.At(x, 0)
		if a > 0x200 && r > 0x400 {
			t.Fatalf("red bled into the edge at x=%d: r=%d b=%d a=%d", x, r, b, a)
		}
		if a > 0x8000 && b < 0xf000 {
			t.Fatalf("edge darkened at x=%d: b=%d a=%d", x, b, a)
		}
	}
	// Linear-light averaging: a 1px black/white checker shrinks to linear
	// 0.5, which is sRGB 0.735 (gamma-space averaging would give 0.5).
	c := New(32, 32)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			v := uint16(0)
			if (x+y)%2 == 0 {
				v = 0xffff
			}
			c.Set(x, y, v, v, v, 0xffff)
		}
	}
	g := Resize(c, 4, 4, Lanczos3)
	if r, _, _, _ := g.At(2, 2); !near(float64(F(r)), 0.735, 0.02) {
		t.Fatalf("checker average = %v, want 0.735 (linear light)", F(r))
	}
}

func TestBlurKeepsFlatAreasAndAlpha(t *testing.T) {
	m := solid(40, 30, 0x4000, 0x8000, 0xc000, 0xffff)
	for _, s := range []float64{1.5, 12} { // exact kernel and box approximation
		out := Blur(m.ToLinear(), s, s).ToImage(m)
		for _, p := range [][2]int{{0, 0}, {20, 15}, {39, 29}} {
			r, g, b, a := out.At(p[0], p[1])
			if math.Abs(float64(r)-0x4000) > 2 || math.Abs(float64(g)-0x8000) > 2 || math.Abs(float64(b)-0xc000) > 2 || a != 0xffff {
				t.Fatalf("sigma %v at %v: %x %x %x %x", s, p, r, g, b, a)
			}
		}
	}
	// A single white dot spreads symmetrically and conserves energy.
	d := New(41, 41)
	d.Set(20, 20, 0xffff, 0xffff, 0xffff, 0xffff)
	l := Blur(d.ToLinear(), 3, 3)
	var sum float64
	for i := 3; i < len(l.Pix); i += 4 {
		sum += float64(l.Pix[i])
	}
	if !near(sum, 1, 0.01) {
		t.Fatalf("alpha energy %v, want 1", sum)
	}
	if !near(float64(l.Pix[4*(20*41+17)+3]), float64(l.Pix[4*(20*41+23)+3]), 1e-6) {
		t.Fatal("blur is not symmetric")
	}
}

func TestParseColor(t *testing.T) {
	cases := map[string][4]uint8{
		"#ff8800":          {255, 136, 0, 255},
		"f80":              {255, 136, 0, 255},
		"rgb(255, 136, 0)": {255, 136, 0, 255},
		"255,136,0":        {255, 136, 0, 255},
		"#00000080":        {0, 0, 0, 128},
		"White":            {255, 255, 255, 255},
		"transparent":      {0, 0, 0, 0},
	}
	for in, want := range cases {
		c, err := ParseColor(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		got := [4]uint8{To8(Q(c.R)), To8(Q(c.G)), To8(Q(c.B)), To8(Q(c.A))}
		if got != want {
			t.Fatalf("%q = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseColor("blurple"); err == nil {
		t.Fatal("accepted a bogus colour")
	}
}

// buildProfile writes a minimal matrix/TRC ICC profile with the given D50
// colorants and a pure gamma curve.
func buildProfile(cols [3][3]float64, gamma float64) []byte {
	tags := []struct {
		sig  string
		data []byte
	}{}
	xyz := func(v [3]float64) []byte {
		b := make([]byte, 20)
		copy(b, "XYZ ")
		for i, x := range v {
			binary.BigEndian.PutUint32(b[8+4*i:], uint32(int32(math.Round(x*65536))))
		}
		return b
	}
	curv := make([]byte, 14)
	copy(curv, "curv")
	binary.BigEndian.PutUint32(curv[8:], 1)
	binary.BigEndian.PutUint16(curv[12:], uint16(gamma*256))
	for c, sig := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		tags = append(tags, struct {
			sig  string
			data []byte
		}{sig, xyz([3]float64{cols[0][c], cols[1][c], cols[2][c]})})
	}
	for _, sig := range []string{"rTRC", "gTRC", "bTRC"} {
		tags = append(tags, struct {
			sig  string
			data []byte
		}{sig, curv})
	}
	off := 132 + 12*len(tags)
	b := make([]byte, off)
	copy(b[16:], "RGB ")
	copy(b[36:], "acsp")
	binary.BigEndian.PutUint32(b[128:], uint32(len(tags)))
	for i, tg := range tags {
		e := b[132+12*i:]
		copy(e, tg.sig)
		binary.BigEndian.PutUint32(e[4:], uint32(len(b)))
		binary.BigEndian.PutUint32(e[8:], uint32(len(tg.data)))
		b = append(b, tg.data...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}
	_ = off
	binary.BigEndian.PutUint32(b[0:], uint32(len(b)))
	return b
}

func TestDisplayP3ConvertsToSRGB(t *testing.T) {
	// Display P3 colorants adapted to D50 (as in Apple's profile), with a
	// gamma 2.2-ish curve standing in for the sRGB curve.
	p3 := [3][3]float64{
		{0.5151, 0.2920, 0.1571},
		{0.2412, 0.6922, 0.0666},
		{-0.0011, 0.0419, 0.7841},
	}
	prof := ParseProfile(buildProfile(p3, 1))
	if prof == nil || prof.IsSRGB() {
		t.Fatal("P3 profile not parsed or mistaken for sRGB")
	}
	// Linear P3 white stays white; P3 pure red is outside sRGB and clips to
	// sRGB red; a P3 mid grey stays grey.
	m := New(3, 1)
	m.Set(0, 0, 0xffff, 0xffff, 0xffff, 0xffff)
	m.Set(1, 0, 0xffff, 0, 0, 0xffff)
	m.Set(2, 0, Q(0.5), Q(0.5), Q(0.5), 0x8000)
	out := ToSRGB(m, prof)
	if r, g, b, _ := out.At(0, 0); r < 0xfe00 || g < 0xfe00 || b < 0xfe00 {
		t.Fatalf("white became %x %x %x", r, g, b)
	}
	if r, g, b, _ := out.At(1, 0); r != 0xffff || g > 0x400 || b > 0x400 {
		t.Fatalf("P3 red became %x %x %x", r, g, b)
	}
	if r, g, b, a := out.At(2, 0); math.Abs(float64(r)-float64(g)) > 0x200 || math.Abs(float64(g)-float64(b)) > 0x200 || a != 0x8000 {
		t.Fatalf("grey became %x %x %x alpha %x", r, g, b, a)
	}
	// An sRGB profile is detected and left alone.
	srgb := ParseProfile(buildProfile(srgbToXYZ, 2.2))
	if srgb == nil {
		t.Fatal("sRGB profile not parsed")
	}
	if got := ToSRGB(m, &Profile{toXYZ: srgbToXYZ, trc: [3]curve{{params: []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045, 0, 0}, kind: 3}, {params: []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045, 0, 0}, kind: 3}, {params: []float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045, 0, 0}, kind: 3}}}); got != m {
		t.Fatal("sRGB profile triggered a conversion")
	}
}

func TestReserveQueuesAndReleases(t *testing.T) {
	s := newSemaphore(100)
	r1, _ := s.acquire(t.Context(), 80)
	done := make(chan struct{})
	go func() {
		r2, _ := s.acquire(t.Context(), 50)
		r2()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("second reservation did not wait")
	default:
	}
	r1()
	<-done
}
