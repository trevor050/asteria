package inspect

import (
	"bytes"
	"context"
	"image"
	"path/filepath"
	"strings"
	"testing"

	"asteria/internal/tools"
)

const fixtures = "../../testdata/fixtures"

func TestDescribe(t *testing.T) {
	finder := tools.NewFinder()
	_, haveFFprobe := finder.Lookup("ffprobe")
	cases := []struct {
		file, typ, kind, summary string
		needsFFprobe             bool
	}{
		{"sample.heic", ".heic", "image", "120×80", false},
		{"sample.webp", ".webp", "image", "120×80", false},
		{"three-pages.pdf", ".pdf", "pdf", "3 pages", false},
		{"bundle.zip", ".zip", "archive", "2 files", false},
		{"clip.mp4", ".mp4", "video", "0:02 · 160×120", true},
		{"tone.wav", ".wav", "audio", "0:02", true},
	}
	for _, c := range cases {
		if c.needsFFprobe && !haveFFprobe {
			continue
		}
		info := Describe(context.Background(), finder, filepath.Join(fixtures, c.file), c.typ)
		if info.Kind != c.kind || info.Summary != c.summary {
			t.Errorf("%s: kind=%s summary=%q; want %s %q", c.file, info.Kind, info.Summary, c.kind, c.summary)
		}
	}
	csv := Describe(context.Background(), finder, filepath.Join(fixtures, "people.csv"), ".csv")
	if csv.Kind != "text" || !strings.Contains(csv.Snippet, "Zoë") {
		t.Errorf("csv snippet = %q", csv.Snippet)
	}
	folder := Describe(context.Background(), finder, fixtures, "folder")
	if folder.Kind != "folder" || folder.Count < 10 || len(folder.Entries) == 0 {
		t.Errorf("folder = %+v", folder)
	}
}

func TestThumbnail(t *testing.T) {
	for _, name := range []string{"sample.heic", "sample.png", "sample.jpg"} {
		b, mime, err := Thumbnail(filepath.Join(fixtures, name), 60)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s thumbnail is not an image: %v", name, err)
		}
		if img.Bounds().Dx() != 60 || img.Bounds().Dy() != 40 {
			t.Errorf("%s thumbnail is %v", name, img.Bounds().Size())
		}
		if name == "sample.png" && mime != "image/png" {
			t.Errorf("transparent thumbnail should be PNG, got %s", mime)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 KB", 26_000_000: "26.0 MB", 150_000_000: "150 MB"} {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %s, want %s", n, got, want)
		}
	}
}
