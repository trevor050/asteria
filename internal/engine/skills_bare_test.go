package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"

	"asteria/internal/img"
	"asteria/internal/ops"
	"asteria/internal/skills"
)

// Every image skill on the pixel core: the bare command runs and visibly
// changes a test picture, every preset parses and runs, the input is never
// modified, and running on a preview proxy works too.
func TestEveryImageSkillBareAndPresets(t *testing.T) {
	e := newEnv(t, "")
	// Skills whose bare command legitimately leaves this picture unchanged.
	unchanged := map[string]bool{
		"convert_to_jpeg": true, "convert_to_png": true, "convert_to_webp": true, "convert_to_heic": true,
		"trim_borders": true, "trim_transparent": true, "compress": true,
		// The scene already spans the full range in every channel.
		"auto_levels": true,
		// Profiles are converted at decode; this skill only re-encodes as sRGB.
		"convert_to_srgb": true,
	}
	// Skills that change only a small region (corners, a caption).
	local := map[string]bool{"round_corners": true, "watermark": true}
	pic := bareScene()
	sum := hashImg(pic)
	n := 0
	for _, sk := range e.reg.List() {
		if sk.Executor.Type != "native" || !sk.Accepts(".png") {
			continue
		}
		h, ok := ops.Lookup(sk.Executor.Handler)
		if !ok || h.Pixel == nil {
			continue
		}
		n++
		args := ops.Merge(sk.Executor.With, nil)
		if len(sk.Params) == 1 && sk.Params[0].Default != nil {
			args[sk.Params[0].Name] = sk.Params[0].Default
		}
		out, err := ops.RunPixel(context.Background(), h, pic, args)
		if err != nil {
			t.Errorf("%s (bare): %v", sk.ID, err)
			continue
		}
		if hashImg(pic) != sum {
			t.Fatalf("%s modified its input", sk.ID)
		}
		if !unchanged[sk.ID] && out.W == pic.W && out.H == pic.H {
			if d := diff(out, pic); (local[sk.ID] && d == 0) || (!local[sk.ID] && d < 0.002) {
				t.Errorf("%s (bare) barely changed the picture (mean diff %.4f)", sk.ID, d)
			}
		}
		if len(sk.Params) == 1 {
			for _, p := range sk.Params[0].Presets {
				v := presetValue(p)
				a := ops.Merge(sk.Executor.With, map[string]any{sk.Params[0].Name: v})
				if _, err := ops.RunPixel(context.Background(), h, pic, a); err != nil {
					t.Errorf("%s preset %q: %v", sk.ID, v, err)
				}
			}
		}
		proxy := pic.Clone()
		proxy.Scale = 0.5
		if _, err := ops.RunPixel(context.Background(), h, proxy, args); err != nil {
			t.Errorf("%s on a proxy: %v", sk.ID, err)
		}
	}
	if n < 6 {
		t.Fatalf("only %d pixel skills found", n)
	}
	t.Logf("%d image skills checked", n)
}

// Every skill's JSON is complete enough for the command bar.
func TestImageSkillMetadata(t *testing.T) {
	e := newEnv(t, "")
	for _, sk := range e.reg.List() {
		if sk.IsMeta || sk.Source == skills.SkillSourceCommunity {
			continue
		}
		if strings.TrimSpace(sk.Description) == "" || strings.TrimSpace(sk.Category) == "" {
			t.Errorf("%s: missing description or category", sk.ID)
		}
		for _, p := range sk.Params {
			if p.Default == nil {
				t.Errorf("%s: parameter %s has no default", sk.ID, p.Name)
			}
		}
	}
}

func presetValue(p any) string {
	if m, ok := p.(map[string]any); ok {
		return fmt.Sprint(m["value"])
	}
	return fmt.Sprint(p)
}

func bareScene() *img.Image {
	m := img.New(64, 48)
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			r := img.Q(float32(x) / 63)
			g := img.Q(float32(y) / 47)
			b := img.Q(float32((x+y)%32) / 31)
			a := uint16(0xffff)
			if x > 56 && y > 40 {
				a = 0x8000
			}
			m.Set(x, y, r, g, b, a)
		}
	}
	// a hard-edged patch for spatial effects
	for y := 16; y < 32; y++ {
		for x := 20; x < 40; x++ {
			m.Set(x, y, 0xf000, 0x2000, 0x2000, 0xffff)
		}
	}
	return m
}

func hashImg(m *img.Image) uint64 {
	h := fnv.New64a()
	b := make([]byte, 2)
	for _, v := range m.Pix {
		b[0], b[1] = byte(v), byte(v>>8)
		h.Write(b)
	}
	return h.Sum64()
}

func diff(a, b *img.Image) float64 {
	var s float64
	for i := range a.Pix {
		d := float64(a.Pix[i]) - float64(b.Pix[i])
		if d < 0 {
			d = -d
		}
		s += d
	}
	return s / float64(len(a.Pix)) / 65535
}
