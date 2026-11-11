package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"asteria/internal/img"
)

// lut.applyBatch grades photos with a LUT dropped alongside them: the
// inputs are one .cube or .3dl file (or a Hald CLUT PNG whose name says so)
// plus any number of images, and the output is a folder holding a graded
// copy of each image under its own name.

func init() {
	register(Handler{Name: "lut.applyBatch", Run: runApplyLUTBatch, Combine: true})
}

func isLUTName(name string) bool {
	lower := strings.ToLower(name)
	switch filepath.Ext(lower) {
	case ".cube", ".3dl":
		return true
	case ".png":
		return strings.Contains(lower, "hald") || strings.Contains(lower, "clut")
	}
	return false
}

func runApplyLUTBatch(ctx context.Context, job Job) error {
	lutIdx := -1
	for i := range job.Inputs {
		if isLUTName(job.Name(i)) {
			if lutIdx >= 0 {
				return Userf("Add just one LUT: %s and %s are both LUTs", job.Name(lutIdx), job.Name(i))
			}
			lutIdx = i
		}
	}
	if lutIdx < 0 {
		return Userf("Add a .cube or .3dl LUT file together with the photos")
	}
	if len(job.Inputs) < 2 {
		return Userf("Add the photos to grade together with %s", job.Name(lutIdx))
	}
	l, err := lutParseFile(job.Inputs[lutIdx])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	used := map[string]int{}
	total := len(job.Inputs) - 1
	done := 0
	for i, in := range job.Inputs {
		if i == lutIdx {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m, err := DecodeWorking(in)
		if err != nil {
			return Userf("%s: %v", job.Name(i), err)
		}
		graded := img.MapColor(m, img.Encoded, l.eval)
		graded.Quality = m.Quality
		name := job.Name(i)
		ext := strings.ToLower(filepath.Ext(name))
		if !canEncode(ext) {
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ".jpg"
		}
		name = uniqueName(used, name)
		if err := EncodeWorking(graded, filepath.Join(job.Output, name)); err != nil {
			return err
		}
		done++
		job.Progress(float64(done) / float64(total))
	}
	return nil
}

// canEncode reports whether the pixel core can write this extension.
func canEncode(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp", ".heic", ".heif":
		return true
	}
	return false
}
