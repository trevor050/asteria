package engine

import (
	"context"
	"testing"

	"asteria/internal/img"
	"asteria/internal/ops"
	"asteria/internal/tools"
)

// A chain of image steps encodes each step once but feeds the next step the
// lossless working image, so step 3 of a JPEG chain equals applying all
// three operations to the decoded original in memory, and a cold replay
// (cache dropped) reproduces it exactly.
func TestPixelChainIsLosslessAndReplays(t *testing.T) {
	e := newEnv(t, "")
	f := e.add("sample.jpg", "")
	steps := []struct {
		skill  string
		params map[string]any
	}{
		{"grayscale", nil},
		{"blur", map[string]any{"radius": 1.5}},
		{"rotate", map[string]any{"angle": 90}},
	}
	for _, s := range steps {
		out, err := e.eng.Apply(context.Background(), []string{f.ID()}, s.skill, s.params, nil)
		if err != nil || len(out.Updated) != 1 {
			t.Fatalf("%s: %v %+v", s.skill, err, out)
		}
	}
	cur := f.Current()
	viaCache, err := e.eng.working(context.Background(), f, cur.Path)
	if err != nil {
		t.Fatal(err)
	}

	// Direct, in memory, from the original.
	m, err := ops.DecodeWorking(f.Original().Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		sk, _ := e.reg.GetByID(s.skill)
		h, _ := ops.Lookup(sk.Executor.Handler)
		if m, err = ops.RunPixel(context.Background(), h, m, ops.Merge(sk.Executor.With, s.params)); err != nil {
			t.Fatal(err)
		}
	}
	same := func(a, b *img.Image) bool {
		if a.W != b.W || a.H != b.H {
			return false
		}
		for i := range a.Pix {
			if a.Pix[i] != b.Pix[i] {
				return false
			}
		}
		return true
	}
	if !same(viaCache, m) {
		t.Fatal("cached chain differs from a direct in-memory run (lossy intermediate?)")
	}

	// Cold: drop everything and replay from the original.
	e.eng.pix = newPixCache()
	cold, err := e.eng.working(context.Background(), f, cur.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !same(cold, m) {
		t.Fatal("cold replay differs from the direct run")
	}
	if cold.W != 80 || cold.H != 120 {
		t.Fatalf("rotated size %dx%d", cold.W, cold.H)
	}
}

func TestPreviewRunsOnAProxyWithoutTouchingTheTray(t *testing.T) {
	e := newEnv(t, "")
	f := e.add("sample.png", "")
	before := len(f.Steps())
	b, mime, err := e.eng.Preview(context.Background(), f.ID(), "grayscale", nil, 64)
	if err != nil || len(b) == 0 || mime == "" {
		t.Fatalf("preview: %v %d %s", err, len(b), mime)
	}
	if len(f.Steps()) != before {
		t.Fatal("preview added a step")
	}
	if _, _, err := e.eng.Preview(context.Background(), f.ID(), "compress", nil, 64); err == nil {
		t.Fatal("compress is not a pixel op and must not preview")
	}
}

// Pixel skills that also run on video keep the video's format through the
// engine (regression: the image output rule once turned .mp4 into .jpg).
func TestPixelSkillOnVideoKeepsVideoFormat(t *testing.T) {
	if _, ok := tools.NewFinder().Lookup("ffmpeg"); !ok {
		t.Skip("ffmpeg not installed")
	}
	e := newEnv(t, "")
	f := e.add("clip.mp4", "")
	for _, id := range []string{"saturate", "mirror", "grayscale"} {
		out, err := e.eng.Apply(context.Background(), []string{f.ID()}, id, nil, nil)
		if err != nil || len(out.Updated) != 1 {
			t.Fatalf("%s on video: %v %+v", id, err, out)
		}
		if typ := f.Current().Type; typ != ".mp4" {
			t.Fatalf("%s produced %s, want .mp4", id, typ)
		}
	}
}
