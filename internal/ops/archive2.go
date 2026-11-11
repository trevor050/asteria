package ops

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
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

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	xzdec "github.com/therootcompany/xz"
	"github.com/ulikunitz/xz"
)

// Extra archive handlers: tar.xz / tar.zst / tar.bz2 / 7z / single-file
// .gz .xz .zst .bz2 extraction (archive.unpack), listings (archive.list) and
// compression (archive.gzip, archive.xz, archive.zstd, archive.tarxz,
// archive.tarzst).
//
// Formats are detected from the file's magic bytes, not its name, so a
// ".xz" that holds a tarball unpacks as a tarball whether or not the type
// token for ".tar.xz" exists. All extraction goes through the guards in
// archive.go (safeJoin, extractBudget, writeEntry); links are never created.

func init() {
	register(Handler{Name: "archive.unpack", Run: a2Unpack})
	register(Handler{Name: "archive.list", Run: a2List})
	register(Handler{Name: "archive.gzip", Run: a2CompressSingle("gz")})
	register(Handler{Name: "archive.xz", Run: a2CompressSingle("xz")})
	register(Handler{Name: "archive.zstd", Run: a2CompressSingle("zst")})
	register(Handler{Name: "archive.tarxz", Run: a2CompressTar("xz")})
	register(Handler{Name: "archive.tarzst", Run: a2CompressTar("zst")})
}

// a2Sniff identifies a format from the first bytes of a file.
func a2Sniff(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "gz"
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return "xz"
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return "zst"
	case bytes.HasPrefix(head, []byte("BZh")):
		return "bz2"
	case bytes.HasPrefix(head, []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}):
		return "7z"
	case bytes.HasPrefix(head, []byte("PK")):
		return "zip"
	case a2IsTar(head):
		return "tar"
	}
	return ""
}

func a2IsTar(head []byte) bool {
	return len(head) >= 262 && string(head[257:262]) == "ustar"
}

// a2SniffFile reads the magic of f and rewinds it.
func a2SniffFile(f *os.File) (string, error) {
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return a2Sniff(buf[:n]), nil
}

// a2Decompress wraps r in a decompressor. The returned name is the original
// file name stored in the stream (gzip only), or "".
func a2Decompress(r io.Reader, kind string) (io.Reader, func(), string, error) {
	switch kind {
	case "gz":
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, "", Userf("not a readable gzip file: %v", err)
		}
		return gr, func() { _ = gr.Close() }, gr.Name, nil
	case "xz":
		// XZ Embedded's decoder handles the BCJ filters some tools use.
		xr, err := xzdec.NewReader(r, 256<<20)
		if err != nil {
			return nil, nil, "", Userf("not a readable xz file: %v", err)
		}
		return xr, func() {}, "", nil
	case "zst":
		zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, nil, "", Userf("not a readable zstd file: %v", err)
		}
		return zr, zr.Close, "", nil
	case "bz2":
		return bzip2.NewReader(r), func() {}, "", nil
	}
	return nil, nil, "", Userf("unsupported compression %q", kind)
}

type a2CtxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c a2CtxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// a2Wrap turns low-level read errors into messages fit for the UI, leaving
// cancellations and user errors untouched.
func a2Wrap(err error, what string) error {
	if err == nil {
		return nil
	}
	var ue *UserError
	if errors.As(err, &ue) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return Userf("could not read %s: %v", what, err)
}

var a2Suffixes = map[string][]string{
	"gz":  {".gz"},
	"xz":  {".xz"},
	"zst": {".zst", ".zstd"},
	"bz2": {".bz2", ".bz"},
}

// a2InnerName picks the name of the decompressed file: the name stored in a
// gzip header, else the display name without its compression suffix.
func a2InnerName(display, kind, stored string) string {
	valid := func(n string) bool { return n != "" && n != "." && n != ".." && n != "/" }
	if stored != "" {
		n := path.Base(strings.ReplaceAll(stored, "\\", "/"))
		if valid(n) {
			return n
		}
	}
	base := path.Base(strings.ReplaceAll(display, "\\", "/"))
	if !valid(base) {
		return "file"
	}
	lower := strings.ToLower(base)
	for _, suf := range a2Suffixes[kind] {
		if strings.HasSuffix(lower, suf) && len(base) > len(suf) {
			return base[:len(base)-len(suf)]
		}
	}
	return base + ".out"
}

func a2Unpack(ctx context.Context, job Job) error {
	in := job.Input()
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	kind, err := a2SniffFile(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	switch kind {
	case "7z":
		err = a2Extract7z(ctx, in, job)
	case "gz", "xz", "zst", "bz2":
		err = a2ExtractCompressed(ctx, f, kind, job)
	case "tar":
		err = a2ExtractTar(ctx, f, job)
	case "zip":
		err = extractZip(ctx, in, job)
	default:
		err = Userf("%s is not a supported archive (tar.xz, tar.zst, tar.bz2, 7z, gz, xz, zst, bz2)", job.Name(0))
	}
	if err != nil {
		_ = os.RemoveAll(job.Output)
		return err
	}
	return flattenSingleRoot(job.Output)
}

func a2ExtractCompressed(ctx context.Context, f *os.File, kind string, job Job) error {
	r, closeFn, stored, err := a2Decompress(a2CtxReader{ctx, f}, kind)
	if err != nil {
		return err
	}
	defer closeFn()
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(512)
	if a2IsTar(head) {
		return a2ExtractTar(ctx, br, job)
	}
	name := a2InnerName(job.Name(0), kind, stored)
	dst, err := safeJoin(job.Output, name)
	if err != nil {
		return Userf("%v", err)
	}
	if err := writeEntry(dst, br, 0o644, &extractBudget{}); err != nil {
		return a2Wrap(err, job.Name(0))
	}
	return nil
}

// a2ExtractTar unpacks a tar stream. Only directories and regular files are
// created; symlinks, hard links and devices are skipped.
func a2ExtractTar(ctx context.Context, r io.Reader, job Job) error {
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
			return a2Wrap(err, "the tar archive")
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
				return a2Wrap(err, hdr.Name)
			}
		}
	}
}

func a2Extract7z(ctx context.Context, in string, job Job) error {
	zr, err := sevenzip.OpenReader(in)
	if err != nil {
		return Userf("not a readable 7z archive (password-protected archives are not supported): %v", err)
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
		fi := f.FileInfo()
		if fi.IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			continue
		}
		mode := fi.Mode()
		if !mode.IsRegular() && mode&fs.ModeType != 0 {
			continue // symlinks and devices
		}
		rc, err := f.Open()
		if err != nil {
			return Userf("could not read %s from the 7z archive: %v", f.Name, err)
		}
		err = writeEntry(dst, a2CtxReader{ctx, rc}, mode, budget)
		_ = rc.Close()
		if err != nil {
			return a2Wrap(err, f.Name)
		}
		if i%32 == 0 {
			job.Progress(float64(i+1) / float64(len(zr.File)))
		}
	}
	return nil
}

// ---- listing ----

type a2Entry struct {
	name string
	size int64
	mod  time.Time
	dir  bool
}

func a2List(ctx context.Context, job Job) error {
	in := job.Input()
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	kind, err := a2SniffFile(f)
	if err != nil {
		return err
	}
	var entries []a2Entry
	add := func(e a2Entry) error {
		if len(entries) >= maxExtractEntries {
			return Userf("archive has more than %d entries; refusing to list", maxExtractEntries)
		}
		entries = append(entries, e)
		return nil
	}
	switch kind {
	case "zip":
		zr, zerr := zip.OpenReader(in)
		if zerr != nil {
			return Userf("not a readable zip file: %v", zerr)
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if err = add(a2Entry{name: zf.Name, size: int64(zf.UncompressedSize64), mod: zf.Modified, dir: zf.FileInfo().IsDir()}); err != nil {
				return err
			}
		}
	case "7z":
		sr, serr := sevenzip.OpenReader(in)
		if serr != nil {
			return Userf("not a readable 7z archive: %v", serr)
		}
		defer sr.Close()
		for _, sf := range sr.File {
			if err = add(a2Entry{name: sf.Name, size: int64(sf.UncompressedSize), mod: sf.Modified, dir: sf.FileInfo().IsDir()}); err != nil {
				return err
			}
		}
	case "tar":
		err = a2ListTar(ctx, f, add)
	case "gz", "xz", "zst", "bz2":
		r, closeFn, stored, derr := a2Decompress(a2CtxReader{ctx, f}, kind)
		if derr != nil {
			return derr
		}
		defer closeFn()
		br := bufio.NewReaderSize(r, 64<<10)
		head, _ := br.Peek(512)
		if a2IsTar(head) {
			err = a2ListTar(ctx, br, add)
		} else {
			n, cerr := io.Copy(io.Discard, io.LimitReader(br, maxExtractBytes+1))
			if cerr != nil {
				return a2Wrap(cerr, job.Name(0))
			}
			err = add(a2Entry{name: a2InnerName(job.Name(0), kind, stored), size: n})
		}
	default:
		return Userf("%s is not a supported archive", job.Name(0))
	}
	if err != nil {
		return a2Wrap(err, job.Name(0))
	}

	var total int64
	files := 0
	for _, e := range entries {
		if !e.dir {
			total += e.size
			files++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d files, %d bytes uncompressed\n\n", job.Name(0), files, total)
	fmt.Fprintf(&b, "%12s  %-16s  %s\n", "Size", "Modified", "Path")
	for _, e := range entries {
		size := fmt.Sprint(e.size)
		if e.dir {
			size = "-"
		}
		date := "-"
		if !e.mod.IsZero() {
			date = e.mod.UTC().Format("2006-01-02 15:04")
		}
		name := e.name
		if e.dir && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		fmt.Fprintf(&b, "%12s  %-16s  %s\n", size, date, name)
	}
	return os.WriteFile(job.Output, []byte(b.String()), 0o644)
}

func a2ListTar(ctx context.Context, r io.Reader, add func(a2Entry) error) error {
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Name == "pax_global_header" {
			continue
		}
		e := a2Entry{name: hdr.Name, size: hdr.Size, mod: hdr.ModTime, dir: hdr.Typeflag == tar.TypeDir}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			e.name += " -> " + hdr.Linkname
			e.size = 0
		}
		if err := add(e); err != nil {
			return err
		}
	}
}

// ---- compression ----

type a2ProgressReader struct {
	r     io.Reader
	total int64
	n     int64
	fn    func(float64)
}

func (p *a2ProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.total > 0 {
		p.fn(float64(p.n) / float64(p.total))
	}
	return n, err
}

func a2Latin1(s string) bool {
	for _, r := range s {
		if r == 0 || r > 0xFF {
			return false
		}
	}
	return true
}

// a2NewCompressor returns a compressing writer on top of w.
func a2NewCompressor(kind string, w io.Writer, name string, mod time.Time) (io.WriteCloser, error) {
	switch kind {
	case "gz":
		gw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
		if err != nil {
			return nil, err
		}
		if name != "" && a2Latin1(name) {
			gw.Name = name
		}
		gw.ModTime = mod
		return gw, nil
	case "xz":
		return xz.NewWriter(w)
	case "zst":
		return zstd.NewWriter(w, zstd.WithEncoderConcurrency(2))
	}
	return nil, fmt.Errorf("unknown compressor %q", kind)
}

func a2CompressSingle(kind string) func(context.Context, Job) error {
	return func(ctx context.Context, job Job) (err error) {
		info, err := os.Stat(job.Input())
		if err != nil {
			return err
		}
		if info.IsDir() {
			return Userf("%s is a folder; use tar.xz, tar.zst or zip to compress folders", job.Name(0))
		}
		src, err := os.Open(job.Input())
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(job.Output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(job.Output)
			}
		}()
		w, err := a2NewCompressor(kind, out, filepath.Base(job.Name(0)), info.ModTime())
		if err != nil {
			return err
		}
		pr := &a2ProgressReader{r: a2CtxReader{ctx, src}, total: info.Size(), fn: job.Progress}
		if _, err = io.Copy(w, pr); err != nil {
			_ = w.Close()
			return err
		}
		return w.Close()
	}
}

func a2CompressTar(kind string) func(context.Context, Job) error {
	return func(ctx context.Context, job Job) (err error) {
		info, err := os.Stat(job.Input())
		if err != nil {
			return err
		}
		out, err := os.OpenFile(job.Output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(job.Output)
			}
		}()
		w, err := a2NewCompressor(kind, out, "", time.Time{})
		if err != nil {
			return err
		}
		tw := tar.NewWriter(w)
		root := strings.NewReplacer("/", "_", "\\", "_").Replace(job.Name(0))
		if root == "" || root == "." || root == ".." {
			root = "archive"
		}
		if err = a2AddTar(ctx, tw, job.Input(), root, info); err != nil {
			_ = w.Close()
			return err
		}
		if err = tw.Close(); err != nil {
			_ = w.Close()
			return err
		}
		return w.Close()
	}
}

func a2TarHeader(fi fs.FileInfo, name string) (*tar.Header, error) {
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return nil, err
	}
	hdr.Name = name
	hdr.Uid, hdr.Gid = 0, 0
	hdr.Uname, hdr.Gname = "", ""
	hdr.ModTime = hdr.ModTime.Truncate(time.Second)
	hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
	hdr.Format = tar.FormatUnknown
	return hdr, nil
}

func a2AddTarFile(tw *tar.Writer, src string, hdr *tar.Header) error {
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, io.LimitReader(f, hdr.Size))
	return err
}

func a2AddTar(ctx context.Context, tw *tar.Writer, in, root string, info fs.FileInfo) error {
	if !info.IsDir() {
		hdr, err := a2TarHeader(info, root)
		if err != nil {
			return err
		}
		return a2AddTarFile(tw, in, hdr)
	}
	return filepath.WalkDir(in, func(p string, d fs.DirEntry, werr error) error {
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
		name := root
		if rel != "." {
			name = path.Join(root, filepath.ToSlash(rel))
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			hdr, err := a2TarHeader(fi, name+"/")
			if err != nil {
				return err
			}
			return tw.WriteHeader(hdr)
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks, sockets, devices
		}
		hdr, err := a2TarHeader(fi, name)
		if err != nil {
			return err
		}
		return a2AddTarFile(tw, p, hdr)
	})
}
