package ops

import (
	"math"
	"testing"
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
