// Package inspect describes files for previews: dimensions, page counts,
// durations, folder listings and text snippets, plus image thumbnails.
package inspect

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/disintegration/imaging"

	"asteria/internal/ops"
	"asteria/internal/tools"
)

// Entry is one item in a folder listing.
type Entry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir"`
}

// Info is a human summary of a file or folder.
type Info struct {
	Kind     string  `json:"kind"`
	Summary  string  `json:"summary"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Pages    int     `json:"pages,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Count    int     `json:"count,omitempty"`
	Entries  []Entry `json:"entries,omitempty"`
	Snippet  string  `json:"snippet,omitempty"`
}

var textExts = map[string]bool{
	".csv": true, ".tsv": true, ".json": true, ".yaml": true, ".yml": true, ".md": true,
	".markdown": true, ".html": true, ".htm": true, ".txt": true, ".xml": true, ".log": true,
}

func in(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Kind classifies a file type token (".jpg", "folder", ...).
func Kind(fileType string) string {
	t := strings.ToLower(fileType)
	switch {
	case t == "folder":
		return "folder"
	case in(ops.ImageInputExts, t):
		return "image"
	case t == ".pdf":
		return "pdf"
	case in(ops.VideoExts, t):
		return "video"
	case in(ops.AudioExts, t):
		return "audio"
	case in(ops.ArchiveExts, t):
		return "archive"
	case textExts[t]:
		return "text"
	}
	return "other"
}

const (
	maxEntries   = 200
	maxWalk      = 20000
	snippetBytes = 6 << 10
)

// Describe inspects path. It never fails: unknown or unreadable files just
// get a smaller summary.
func Describe(ctx context.Context, finder *tools.Finder, path, fileType string) Info {
	info := Info{Kind: Kind(fileType)}
	switch info.Kind {
	case "image":
		if cfg, err := imageConfig(path); err == nil {
			info.Width, info.Height = cfg.Width, cfg.Height
			if o := ops.JPEGOrientation(path); o >= 5 && o <= 8 {
				info.Width, info.Height = info.Height, info.Width
			}
			info.Summary = fmt.Sprintf("%d×%d", info.Width, info.Height)
		}
	case "pdf":
		if n, err := ops.PDFPageCount(ctx, path); err == nil {
			info.Pages = n
			info.Summary = plural(n, "page")
		}
	case "video", "audio":
		if finder != nil {
			if m, err := ops.ProbeMedia(ctx, finder, path); err == nil {
				info.Duration = m.Duration
				parts := []string{ops.FormatDuration(m.Duration)}
				if m.HasVideo {
					info.Width, info.Height = m.Width, m.Height
					parts = append(parts, fmt.Sprintf("%d×%d", m.Width, m.Height))
				} else if info.Kind == "video" {
					parts = append(parts, "audio only")
				}
				info.Summary = strings.Join(parts, " · ")
			}
		}
	case "folder":
		describeFolder(path, &info)
	case "archive":
		if strings.HasSuffix(strings.ToLower(path), ".zip") {
			if zr, err := zip.OpenReader(path); err == nil {
				n := 0
				for _, f := range zr.File {
					if !f.FileInfo().IsDir() {
						n++
					}
				}
				zr.Close()
				info.Count = n
				info.Summary = plural(n, "file")
			}
		}
	case "text":
		info.Snippet = snippet(path)
	}
	return info
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func imageConfig(path string) (image.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	return cfg, err
}

func describeFolder(root string, info *Info) {
	var total int64
	count := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if count >= maxWalk {
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		e := Entry{Path: filepath.ToSlash(rel), Dir: d.IsDir()}
		if !d.IsDir() {
			count++
			if fi, err := d.Info(); err == nil {
				e.Size = fi.Size()
				total += e.Size
			}
		}
		if len(info.Entries) < maxEntries {
			info.Entries = append(info.Entries, e)
		}
		return nil
	})
	sort.SliceStable(info.Entries, func(i, j int) bool { return info.Entries[i].Path < info.Entries[j].Path })
	info.Count = count
	info.Summary = plural(count, "file") + " · " + HumanSize(total)
}

func snippet(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, snippetBytes)
	n, _ := io.ReadFull(f, buf)
	b := bytes.TrimPrefix(buf[:n], []byte("\xef\xbb\xbf"))
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return ""
	}
	return string(b)
}

// HumanSize formats bytes as B, KB, MB or GB (base 1000, like Finder).
func HumanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f %cB", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGTPE"[exp])
}

// Thumbnail renders an image file to a JPEG (or PNG when it has
// transparency) no larger than max pixels on its longest side.
func Thumbnail(path string, max int) ([]byte, string, error) {
	img, err := ops.DecodeImage(path)
	if err != nil {
		return nil, "", err
	}
	b := img.Bounds()
	if b.Dx() > max || b.Dy() > max {
		img = imaging.Fit(img, max, max, imaging.Linear)
	}
	var buf bytes.Buffer
	if opaque, ok := img.(interface{ Opaque() bool }); ok && !opaque.Opaque() {
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}
