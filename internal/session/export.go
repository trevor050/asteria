package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// DefaultNamingPattern keeps the original name; collisions get " (1)".
const DefaultNamingPattern = "{name}.{ext}"

// LegacyNamingPattern was the default before 0.1 and is migrated.
const LegacyNamingPattern = "{name}_{skill}.{ext}"

// ExportResult reports where one file was written.
type ExportResult struct {
	FileID     string `json:"fileId"`
	Name       string `json:"name"`
	OutputPath string `json:"outputPath"`
}

// ExportName renders a naming pattern. Supported tokens: {name}, {ext},
// {skill} (the last applied skill id) and {date} (YYYY-MM-DD). The result
// is sanitized for every platform; ext is "" for folders.
func ExportName(pattern, name, ext, skill string) string {
	if strings.TrimSpace(pattern) == "" {
		pattern = DefaultNamingPattern
	}
	ext = strings.TrimPrefix(ext, ".")
	out := pattern
	out = strings.ReplaceAll(out, "{name}", name)
	out = strings.ReplaceAll(out, "{skill}", skill)
	out = strings.ReplaceAll(out, "{date}", Now().Format("2006-01-02"))
	if ext == "" {
		out = strings.ReplaceAll(out, ".{ext}", "")
		out = strings.ReplaceAll(out, "{ext}", "")
	} else {
		out = strings.ReplaceAll(out, "{ext}", ext)
		if !strings.HasSuffix(strings.ToLower(out), "."+strings.ToLower(ext)) {
			out += "." + ext
		}
	}
	return sanitizeFilename(out)
}

func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 32 || r == 127:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRightFunc(b.String(), func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	out = strings.TrimLeftFunc(out, unicode.IsSpace)
	if out == "" || out == "." || out == ".." {
		return "file"
	}
	// Windows device names stay reserved before any extension (NUL.tar.gz).
	stem, _, _ := strings.Cut(out, ".")
	stem = strings.ToUpper(stem)
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "COM¹", "COM²", "COM³",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "LPT¹", "LPT²", "LPT³":
		out = "_" + out
	}
	return out
}

// splitName separates "photo.tar.gz" style names into stem and extension.
func splitName(name string) (string, string) {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".tar.gz") {
		return name[:len(name)-7], name[len(name)-7:]
	}
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext), ext
}

// reserve creates dest exclusively, adding " (1)", " (2)"... when a file or
// folder with that name already exists. It never overwrites anything.
func reserve(dir, name string, isDir bool) (string, *os.File, error) {
	stem, ext := splitName(name)
	if isDir {
		stem, ext = name, ""
	}
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		p := filepath.Join(dir, candidate)
		if isDir {
			err := os.Mkdir(p, 0o755)
			if err == nil {
				return p, nil, nil
			}
			if !errors.Is(err, fs.ErrExist) {
				return "", nil, err
			}
			continue
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return p, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("could not find a free name for %s", name)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// ExportFile writes the file's current output into dir and returns the path.
func ExportFile(ctx context.Context, f *File, dir, pattern string) (string, error) {
	cur := f.Current()
	f.mu.Lock()
	name := f.name
	skill := "original"
	if len(f.steps) > 0 {
		skill = f.steps[len(f.steps)-1].SkillID
	}
	if dir == "" {
		dir = f.sourceDir
	}
	f.mu.Unlock()
	if dir == "" {
		return "", errors.New("no output folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := cur.Type
	if cur.IsDir() {
		ext = ""
	}
	target := ExportName(pattern, name, ext, skill)
	dest, out, err := reserve(dir, target, cur.IsDir())
	if err != nil {
		return "", err
	}
	if cur.IsDir() {
		if err := copyTree(ctx, cur.Path, dest); err != nil {
			_ = os.RemoveAll(dest)
			return "", err
		}
		return dest, nil
	}
	in, err := os.Open(cur.Path)
	if err != nil {
		out.Close()
		_ = os.Remove(dest)
		return "", err
	}
	defer in.Close()
	_, err = io.Copy(out, ctxReader{ctx, in})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	return dest, nil
}

func copyTree(ctx context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, ctxReader{ctx, in})
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	})
}
