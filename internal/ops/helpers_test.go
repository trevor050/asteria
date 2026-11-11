package ops

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"asteria/internal/tools"
)

const fixtures = "../../testdata/fixtures"

func fixture(name string) string { return filepath.Join(fixtures, name) }

// run invokes a registered handler on inputs and returns the output path.
func run(t *testing.T, handler string, inputs []string, outName string, args Args) (string, error) {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok {
		t.Fatalf("handler %s not registered", handler)
	}
	out := filepath.Join(t.TempDir(), outName)
	job := Prepare(Job{Inputs: inputs, Output: out, Args: args, Tools: tools.NewFinder()})
	err := h.Run(context.Background(), job)
	return out, err
}

func mustRun(t *testing.T, handler string, inputs []string, outName string, args Args) string {
	t.Helper()
	out, err := run(t, handler, inputs, outName, args)
	if err != nil {
		t.Fatalf("%s failed: %v", handler, err)
	}
	return out
}

func decode(t *testing.T, path string) (image.Image, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return img, format
}

func rgb(c color.Color) (int, int, int, int) {
	r, g, b, a := c.RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8), int(a >> 8)
}

func near(a, b, tol int) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

func assertColor(t *testing.T, img image.Image, x, y int, want [3]int, tol int) {
	t.Helper()
	r, g, b, _ := rgb(img.At(x, y))
	if !near(r, want[0], tol) || !near(g, want[1], tol) || !near(b, want[2], tol) {
		t.Errorf("pixel (%d,%d) = %d,%d,%d; want ~%v (±%d)", x, y, r, g, b, want, tol)
	}
}

func assertUserError(t *testing.T, err error) {
	t.Helper()
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %T: %v", err, err)
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
