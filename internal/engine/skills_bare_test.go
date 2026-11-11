package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
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
		// Sizes in percent of the picture are a pixel or less on this 64x48
		// scene; TestBareCommandIsPerceptibleOnAPhoto covers those skills.
		relative := len(sk.Params) == 1 && strings.HasSuffix(fmt.Sprint(sk.Params[0].Default), "%") && sk.Params[0].Unit == "%" && !strings.HasPrefix(fmt.Sprint(sk.Params[0].Default), "+")
		if !unchanged[sk.ID] && !relative && out.W == pic.W && out.H == pic.H {
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

// The bare command of every color, tone and effect skill visibly changes an
// ordinary photo (testdata/fixtures/photo.jpg, NASA, public domain):
// mean OKLab difference of at least 1.5 (x100), or for saturation-type
// skills a mean chroma change of at least 20%.
func TestBareCommandIsPerceptibleOnAPhoto(t *testing.T) {
	e := newEnv(t, "")
	photo, err := ops.DecodeWorking("../../testdata/fixtures/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	// Depend on content: nothing to fix, no matching color, no noise, or
	// only alpha changes on an opaque photo.
	exempt := map[string]string{
		"auto_levels": "full-range photo", "auto_contrast": "full-range photo", "enhance": "full-range photo",
		"auto_white_balance": "neutral photo", "replace_color": "no red in the photo", "denoise": "no noise",
		"opacity": "changes alpha only",
	}
	for _, sk := range e.reg.List() {
		if (sk.Category != "adjust" && sk.Category != "filter") || sk.Executor.Type != "native" {
			continue
		}
		h, ok := ops.Lookup(sk.Executor.Handler)
		if !ok || h.Pixel == nil || exempt[sk.ID] != "" {
			continue
		}
		args := ops.Merge(sk.Executor.With, nil)
		if len(sk.Params) == 1 && sk.Params[0].Default != nil {
			args[sk.Params[0].Name] = sk.Params[0].Default
		}
		out, err := ops.RunPixel(context.Background(), h, photo, args)
		if err != nil {
			t.Errorf("%s: %v", sk.ID, err)
			continue
		}
		de, dc := perceptualChange(photo, out)
		if de < 1.5 && dc < 0.2 {
			t.Errorf("%s (bare) is too timid on an ordinary photo: mean dE %.2f, chroma change %.0f%%", sk.ID, de, dc*100)
		}
	}
}

func perceptualChange(a, b *img.Image) (de, chroma float64) {
	if a.W != b.W || a.H != b.H {
		return 100, 1
	}
	var sum, ca, cb float64
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		L1, A1, B1 := img.LinearToOKLab(img.Lin(a.Pix[i]), img.Lin(a.Pix[i+1]), img.Lin(a.Pix[i+2]))
		L2, A2, B2 := img.LinearToOKLab(img.Lin(b.Pix[i]), img.Lin(b.Pix[i+1]), img.Lin(b.Pix[i+2]))
		sum += math.Sqrt(float64((L1-L2)*(L1-L2) + (A1-A2)*(A1-A2) + (B1-B2)*(B1-B2)))
		ca += math.Hypot(float64(A1), float64(B1))
		cb += math.Hypot(float64(A2), float64(B2))
		n++
	}
	return 100 * sum / float64(n), math.Abs(cb-ca) / math.Max(ca, 1e-9)
}
