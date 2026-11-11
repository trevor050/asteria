package ops

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Limits that protect against archive bombs.
const (
	maxExtractBytes   = 20 << 30 // 20 GiB
	maxExtractEntries = 200_000
)

// ArchiveExts lists the archive formats archive.extract understands.
var ArchiveExts = []string{".zip", ".tar", ".tgz", ".tar.gz", ".gz"}

func init() {
	register(Handler{Name: "archive.zip", Run: archiveZip, Combine: true})
	register(Handler{Name: "archive.extract", Run: archiveExtract})
}

func uniqueName(seen map[string]int, name string) string {
	key := strings.ToLower(name)
	n := seen[key]
	seen[key] = n + 1
	if n == 0 {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := fmt.Sprintf("%s (%d)%s", base, n+1, ext)
	seen[strings.ToLower(candidate)]++
	return candidate
}

func archiveZip(ctx context.Context, job Job) (err error) {
	f, err := os.OpenFile(job.Output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	defer func() {
		if cerr := zw.Close(); err == nil {
			err = cerr
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(job.Output)
		}
	}()
	seen := map[string]int{}
	for i, in := range job.Inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		root := uniqueName(seen, job.Name(i))
		info, err := os.Stat(in)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if err := zipFile(zw, in, root, info); err != nil {
				return err
			}
		} else {
			err = filepath.WalkDir(in, func(p string, d fs.DirEntry, werr error) error {
				if werr != nil {
					return werr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				rel, err := filepath.Rel(in, p)
				if err != nil {
					return err
				}
				name := path.Join(root, filepath.ToSlash(rel))
				if d.IsDir() {
					if rel == "." {
						name = root
					}
					_, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/", Method: zip.Store, Modified: time.Now()})
					return err
				}
				if !d.Type().IsRegular() {
					return nil
				}
				fi, err := d.Info()
				if err != nil {
					return err
				}
				return zipFile(zw, p, name, fi)
			})
			if err != nil {
				return err
			}
		}
		job.Progress(float64(i+1) / float64(len(job.Inputs)))
	}
	return nil
}

func zipFile(zw *zip.Writer, src, name string, info fs.FileInfo) error {
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(w, in)
	return err
}

// safeJoin resolves an archive entry name under root, rejecting absolute
// paths and ".." traversal (zip slip).
func safeJoin(root, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	clean := path.Clean("/" + name)
	if clean == "/" {
		return "", errors.New("empty entry name")
	}
	if strings.Contains(name, "..") {
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return "", fmt.Errorf("unsafe path %q in archive", name)
			}
		}
	}
	if filepath.VolumeName(name) != "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe path %q in archive", name)
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}

// isMacJunk reports AppleDouble ("._name") and "__MACOSX/" entries that macOS
// adds to archives; they are metadata, not user files.
func isMacJunk(name string) bool {
	name = strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(name, "/") {
		if part == "__MACOSX" || strings.HasPrefix(part, "._") {
			return true
		}
	}
	return false
}

type extractBudget struct {
	bytes   int64
	entries int
}

func (b *extractBudget) take(n int64) error {
	b.entries++
	b.bytes += n
	if b.entries > maxExtractEntries {
		return Userf("archive has more than %d entries; refusing to extract", maxExtractEntries)
	}
	if b.bytes > maxExtractBytes {
		return Userf("archive expands to more than 20 GB; refusing to extract")
	}
	return nil
}

func writeEntry(dst string, r io.Reader, mode fs.FileMode, budget *extractBudget) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	perm := mode.Perm() | 0o600
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	remaining := int64(maxExtractBytes) - budget.bytes
	n, err := io.Copy(f, io.LimitReader(r, remaining+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return budget.take(n)
}

func archiveExtract(ctx context.Context, job Job) error {
	in := job.Input()
	lower := strings.ToLower(job.Name(0))
	if lower == "" {
		lower = strings.ToLower(in)
	}
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	var err error
	switch {
	case strings.HasSuffix(lower, ".zip"):
		err = extractZip(ctx, in, job)
	case strings.HasSuffix(lower, ".tar"):
		err = extractTarFile(ctx, in, job, false)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		err = extractTarFile(ctx, in, job, true)
	case strings.HasSuffix(lower, ".gz"):
		err = extractGzip(in, job)
	default:
		return Userf("%s is not a supported archive (zip, tar, tar.gz)", filepath.Base(lower))
	}
	if err != nil {
		_ = os.RemoveAll(job.Output)
	}
	return err
}

func extractZip(ctx context.Context, in string, job Job) error {
	zr, err := zip.OpenReader(in)
	if err != nil {
		return Userf("not a readable zip file: %v", err)
	}
	defer zr.Close()
	budget := &extractBudget{}
	for i, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if isMacJunk(f.Name) {
			continue
		}
		dst, err := safeJoin(job.Output, f.Name)
		if err != nil {
			return Userf("%v", err)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() && f.Mode()&fs.ModeType != 0 {
			continue // skip symlinks and devices
		}
		if f.Flags&0x1 != 0 {
			return Userf("this zip is password-protected")
		}
		rc, err := f.Open()
		if err != nil {
			return Userf("could not read %s from the zip: %v", f.Name, err)
		}
		err = writeEntry(dst, rc, f.Mode(), budget)
		rc.Close()
		if err != nil {
			return err
		}
		if i%32 == 0 {
			job.Progress(float64(i+1) / float64(len(zr.File)))
		}
	}
	return nil
}

func extractTarFile(ctx context.Context, in string, job Job, gz bool) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if gz {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return Userf("not a readable gzip file: %v", err)
		}
		defer gr.Close()
		r = gr
	}
	tr := tar.NewReader(r)
	budget := &extractBudget{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return Userf("not a readable tar archive: %v", err)
		}
		if hdr.Name == "pax_global_header" || isMacJunk(hdr.Name) {
			continue
		}
		dst, err := safeJoin(job.Output, hdr.Name)
		if err != nil {
			return Userf("%v", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeEntry(dst, tr, fs.FileMode(hdr.Mode), budget); err != nil {
				return err
			}
		}
	}
}

func extractGzip(in string, job Job) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return Userf("not a readable gzip file: %v", err)
	}
	defer gr.Close()
	name := gr.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(job.Name(0)), filepath.Ext(job.Name(0)))
	}
	dst, err := safeJoin(job.Output, filepath.Base(name))
	if err != nil {
		return Userf("%v", err)
	}
	return writeEntry(dst, gr, 0o644, &extractBudget{})
}
