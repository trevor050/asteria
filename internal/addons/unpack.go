package addons

import (
	"archive/tar"
	"archive/zip"
	"asteria/internal/tools"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Limits that stop a hostile or broken archive from filling the disk.
const (
	maxEntries   = 200_000
	maxTotalSize = 8 << 30
)

// unpack extracts an archive of the given format into dst, refusing any
// entry that would land outside dst and any link that points outside it.
func unpack(ctx context.Context, a Artifact, src, dst string) error {
	switch strings.ToLower(a.Format) {
	case "zip":
		return unzip(ctx, src, dst)
	case "tar.gz", "tgz":
		return untarCompressed(ctx, src, dst, "gz")
	case "tar.xz", "txz":
		return untarCompressed(ctx, src, dst, "xz")
	case "tar.zst":
		return untarCompressed(ctx, src, dst, "zst")
	case "tar.bz2":
		return untarCompressed(ctx, src, dst, "bz2")
	case "tar":
		return untarCompressed(ctx, src, dst, "")
	case "7z":
		return un7z(ctx, src, dst)
	case "binary":
		// The download is the program itself; it goes where bins says.
		for _, rel := range a.Bins {
			p, err := safeJoin(dst, rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			return copyFile(src, p, 0o755)
		}
		return errors.New("binary add-on without a bins entry")
	case "dmg":
		return unpackDMG(ctx, src, dst)
	case "msi":
		return unpackMSI(ctx, src, dst)
	case "appimage":
		return unpackAppImage(ctx, src, dst)
	case "deb-bundle":
		return unpackDebBundle(ctx, src, dst)
	}
	return fmt.Errorf("unsupported archive format %q", a.Format)
}

// safeJoin returns dst/name, or an error when name escapes dst.
func safeJoin(dst, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(name) || strings.Contains(name, ":") && runtime.GOOS == "windows" {
		return "", fmt.Errorf("refusing archive entry %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing archive entry %q", name)
	}
	return filepath.Join(dst, clean), nil
}

// linkInside reports whether a symlink at path with target stays in dst.
func linkInside(dst, path, target string) bool {
	if filepath.IsAbs(target) || strings.HasPrefix(target, "/") {
		return false
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
	rel, err := filepath.Rel(dst, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func unzip(ctx context.Context, src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return errors.New("archive has too many entries")
	}
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := safeJoin(dst, f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, _ := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err := makeLink(dst, p, string(target)); err != nil {
				return err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxTotalSize {
			return errors.New("archive unpacks to more than 8 GB")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(p, rc, mode.Perm()|0o600, int64(f.UncompressedSize64))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func un7z(ctx context.Context, src, dst string) error {
	r, err := sevenzip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	if len(r.File) > maxEntries {
		return errors.New("archive has too many entries")
	}
	var total int64
	for _, f := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := safeJoin(dst, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue // Windows archives don't need links
		}
		total += int64(f.UncompressedSize)
		if total > maxTotalSize {
			return errors.New("archive unpacks to more than 8 GB")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(p, rc, f.Mode().Perm()|0o600, int64(f.UncompressedSize))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untarCompressed(ctx context.Context, src, dst, comp string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	r, closeFn, err := decompressor(bufio.NewReaderSize(f, 1<<20), comp)
	if err != nil {
		return err
	}
	defer closeFn()
	return untar(ctx, r, dst)
}

func decompressor(r io.Reader, comp string) (io.Reader, func(), error) {
	switch comp {
	case "gz":
		z, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return z, func() { z.Close() }, nil
	case "xz":
		z, err := xz.NewReader(r)
		return z, func() {}, err
	case "zst":
		z, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return z, z.Close, nil
	case "bz2":
		return bzip2.NewReader(r), func() {}, nil
	}
	return r, func() {}, nil
}

func untar(ctx context.Context, r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	var total int64
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if n++; n > maxEntries {
			return errors.New("archive has too many entries")
		}
		p, err := safeJoin(dst, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxTotalSize {
				return errors.New("archive unpacks to more than 8 GB")
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := writeFile(p, tr, os.FileMode(h.Mode).Perm()|0o600, h.Size); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := makeLink(dst, p, h.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			target, err := safeJoin(dst, h.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := copyFile(target, p, 0o755); err != nil {
				return err
			}
		default:
			// devices, fifos and the like are never needed
		}
	}
}

func makeLink(dst, p, target string) error {
	if !linkInside(dst, p, target) {
		return fmt.Errorf("refusing a link that points outside the add-on: %s -> %s", filepath.Base(p), target)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	_ = os.Remove(p)
	if runtime.GOOS == "windows" {
		// Symlinks need a privilege on Windows; copy the target instead
		// once it exists (archives list targets first in practice).
		resolved := filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
		if st, err := os.Stat(resolved); err == nil && !st.IsDir() {
			return copyFile(resolved, p, 0o755)
		}
		return nil
	}
	return os.Symlink(target, p)
}

func writeFile(p string, r io.Reader, mode os.FileMode, size int64) error {
	out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(r, size))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	return writeFile(dst, in, mode, st.Size())
}

// unpackDMG mounts a disk image read-only (no admin needed), copies its
// contents (typically one .app bundle) and detaches it.
func unpackDMG(ctx context.Context, src, dst string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("disk images only open on macOS")
	}
	mnt, err := os.MkdirTemp("", "asteria-dmg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mnt)
	// A .part name confuses hdiutil's format sniffing less than you'd
	// think, but give it the real extension anyway.
	img := strings.TrimSuffix(src, ".part") + ".dmg"
	if err := os.Rename(src, img); err != nil {
		return err
	}
	defer os.Rename(img, src)
	attach := tools.Command(ctx, "/usr/bin/hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, img)
	attach.Stdin = strings.NewReader("Y\n") // accept a license agreement if the image shows one
	if out, err := attach.CombinedOutput(); err != nil {
		return fmt.Errorf("hdiutil attach: %v: %s", err, bytes.TrimSpace(out))
	}
	defer exec.Command("/usr/bin/hdiutil", "detach", "-force", mnt).Run()
	entries, err := os.ReadDir(mnt)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || e.Type()&os.ModeSymlink != 0 {
			continue // skip the "Applications" shortcut and hidden files
		}
		// ditto keeps bundle structure, permissions and code signatures.
		cp := tools.Command(ctx, "/usr/bin/ditto", filepath.Join(mnt, e.Name()), filepath.Join(dst, e.Name()))
		if out, err := cp.CombinedOutput(); err != nil {
			return fmt.Errorf("copying %s: %v: %s", e.Name(), err, bytes.TrimSpace(out))
		}
	}
	return nil
}

// unpackMSI performs an administrative extraction (msiexec /a), which
// copies the package's files to a folder without installing, registering
// or elevating anything.
func unpackMSI(ctx context.Context, src, dst string) error {
	if runtime.GOOS != "windows" {
		return errors.New("MSI packages only open on Windows")
	}
	msi := strings.TrimSuffix(src, ".part") + ".msi"
	if err := os.Rename(src, msi); err != nil {
		return err
	}
	defer os.Rename(msi, src)
	abs, _ := filepath.Abs(dst)
	logf := filepath.Join(filepath.Dir(msi), "msiexec.log")
	cmd := tools.Command(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "msiexec.exe"), "/a", msi, "/qn", "/norestart", "TARGETDIR="+abs, "/l*v", logf)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("msiexec /a: %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// unpackAppImage extracts an AppImage's file system (no FUSE needed).
func unpackAppImage(ctx context.Context, src, dst string) error {
	if runtime.GOOS != "linux" {
		return errors.New("AppImages only run on Linux")
	}
	img := filepath.Join(dst, "app.AppImage")
	if err := copyFile(src, img, 0o755); err != nil {
		return err
	}
	cmd := tools.Command(ctx, img, "--appimage-extract")
	cmd.Dir = dst
	cmd.Env = append(os.Environ(), "APPIMAGE_EXTRACT_AND_RUN=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extracting AppImage: %v: %s", err, lastLines(out))
	}
	return os.Remove(img)
}

// unpackDebBundle extracts a tar.gz full of .deb packages (how LibreOffice
// ships for Linux) without root: each .deb's data archive goes into dst.
func unpackDebBundle(ctx context.Context, src, dst string) error {
	tmp := filepath.Join(dst, ".debs")
	if err := untarCompressed(ctx, src, tmp, "gz"); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	var debs []string
	_ = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".deb") {
			debs = append(debs, p)
		}
		return nil
	})
	if len(debs) == 0 {
		return errors.New("no .deb packages in the bundle")
	}
	for _, deb := range debs {
		if err := extractDeb(ctx, deb, dst); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(deb), err)
		}
	}
	return nil
}

// extractDeb reads the ar archive of a .deb and unpacks data.tar.*.
func extractDeb(ctx context.Context, deb, dst string) error {
	f, err := os.Open(deb)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != "!<arch>\n" {
		return errors.New("not a Debian package")
	}
	for {
		hdr := make([]byte, 60)
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF {
				return errors.New("no data archive in package")
			}
			return err
		}
		name := strings.TrimRight(strings.TrimSpace(string(hdr[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size < 0 {
			return errors.New("corrupt package")
		}
		body := io.LimitReader(br, size)
		if strings.HasPrefix(name, "data.tar") {
			comp := strings.TrimPrefix(strings.TrimPrefix(name, "data.tar"), ".")
			if comp == "gz" || comp == "xz" || comp == "zst" || comp == "bz2" || comp == "" {
				r, closeFn, err := decompressor(body, comp)
				if err != nil {
					return err
				}
				defer closeFn()
				return untar(ctx, r, dst)
			}
			return fmt.Errorf("unsupported %s", name)
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return err
		}
		if size%2 == 1 {
			_, _ = br.ReadByte()
		}
	}
}

func lastLines(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = "…" + s[len(s)-300:]
	}
	return s
}
