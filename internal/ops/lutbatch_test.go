package ops

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A 2-point LUT that swaps red and blue turns a red photo blue, keeps each
// photo's name, and the LUT itself is not treated as a photo.
func TestApplyLUTBatchSwapsChannels(t *testing.T) {
	dir := t.TempDir()
	var cube strings.Builder
	cube.WriteString("TITLE \"swap\"\nLUT_3D_SIZE 2\n")
	for b := 0; b < 2; b++ {
		for g := 0; g < 2; g++ {
			for r := 0; r < 2; r++ { // red varies fastest
				cube.WriteString(string(rune('0'+b)) + " " + string(rune('0'+g)) + " " + string(rune('0'+r)) + "\n")
			}
		}
	}
	lut := filepath.Join(dir, "swap.cube")
	os.WriteFile(lut, []byte(cube.String()), 0o644)
	photo := filepath.Join(dir, "red.png")
	m := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	m.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	m.Set(1, 0, color.NRGBA{0, 0, 255, 128})
	f, _ := os.Create(photo)
	png.Encode(f, m)
	f.Close()

	out := filepath.Join(dir, "graded")
	h, _ := Lookup("lut.applyBatch")
	job := Prepare(Job{Inputs: []string{photo, lut}, Names: []string{"red.png", "swap.cube"}, Output: out})
	if err := h.Run(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 || entries[0].Name() != "red.png" {
		t.Fatalf("outputs: %v", entries)
	}
	g, err := DecodeImage(filepath.Join(out, "red.png"))
	if err != nil {
		t.Fatal(err)
	}
	if c := color.NRGBAModel.Convert(g.At(0, 0)).(color.NRGBA); c != (color.NRGBA{0, 0, 255, 255}) {
		t.Fatalf("pixel 0 = %v", c)
	}
	if c := color.NRGBAModel.Convert(g.At(1, 0)).(color.NRGBA); c.R != 255 || c.B != 0 || c.A != 128 {
		t.Fatalf("pixel 1 = %v (alpha must be kept)", c)
	}

	// Without a LUT the user is told what to add.
	err = h.Run(t.Context(), Prepare(Job{Inputs: []string{photo}, Names: []string{"red.png"}, Output: filepath.Join(dir, "x")}))
	if err == nil || !strings.Contains(err.Error(), ".cube") {
		t.Fatalf("err = %v", err)
	}
}
