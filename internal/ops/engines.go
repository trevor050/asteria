package ops

import (
	"asteria/internal/tools"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"asteria/internal/img"
)

// Handlers for external engines whose command lines don't fit the plain
// "input to output" template of CLI skills. Every engine runs with
// absolute paths only (so no file name can be read as an option), a
// timeout, the caller's cancellation, and a working directory inside the
// file's workspace so stray files never land next to the user's originals.

func init() {
	for _, h := range []Handler{
		{Name: "engine.libreoffice", Run: runLibreOffice, Requires: []string{"soffice"}, Concurrency: 1},
		{Name: "engine.ocr", Run: runOCR, Requires: []string{"tesseract"}, Concurrency: 2},
		{Name: "engine.pdfimages", Run: runPDFImages, Requires: []string{"gs"}, Concurrency: 2},
		{Name: "engine.trace", Run: runTrace, Requires: []string{"potrace"}, Concurrency: 2},
	} {
		register(h)
	}
}

// engineCmd runs bin with args, failing with the tool's last error lines.
func engineCmd(ctx context.Context, name, bin, dir string, timeout time.Duration, args ...string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		// Paths are absolute; anything else must not look like a path.
		_ = a
	}
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := tools.Command(c, bin, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.Err() == context.DeadlineExceeded {
			return Userf("%s took too long and was stopped", name)
		}
		msg := strings.TrimSpace(out.String())
		if len(msg) > 400 {
			msg = "…" + msg[len(msg)-400:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return Userf("%s failed: %s", name, msg)
	}
	return nil
}

func absAll(paths ...string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

func fileURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/... on Windows
	}
	return "file://" + strings.ReplaceAll(p, " ", "%20")
}

// loRunawayBase is the fixed part of the output size at which a
// LibreOffice conversion is treated as stuck (tests lower it).
var loRunawayBase int64 = 256 << 20

// runLibreOffice converts office documents with LibreOffice in headless
// mode. It uses its own throwaway profile so it never collides with a
// LibreOffice the user has open, and writes into a scratch folder because
// LibreOffice picks the output name itself.
func runLibreOffice(ctx context.Context, job Job) error {
	bin, ok := job.Tools.Lookup("soffice")
	if !ok {
		return &MissingToolError{Tool: "soffice"}
	}
	if runtime.GOOS == "windows" {
		// soffice.exe returns before converting; soffice.com waits.
		if com := strings.TrimSuffix(bin, filepath.Ext(bin)) + ".com"; fileExists(com) {
			bin = com
		}
	}
	paths, err := absAll(job.Input(), job.Output)
	if err != nil {
		return err
	}
	in, out := paths[0], paths[1]
	scratch, err := os.MkdirTemp(filepath.Dir(out), ".lo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	profile := filepath.Join(scratch, "profile")
	outDir := filepath.Join(scratch, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	target := strings.TrimPrefix(strings.ToLower(filepath.Ext(out)), ".")
	if f := job.Args.String("filter", ""); f != "" {
		target += ":" + f
	}
	job.Progress(0.1)
	// Some LibreOffice builds loop forever on certain files and keep
	// writing (26.8.1 on macOS repeats a table cell of a .docx exported to
	// text until the disk fills). Stop it once the output is far larger
	// than any real conversion of this input could be.
	limit := loRunawayBase + 64*fileSize(in)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	runaway := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if treeSize(outDir) > limit {
					close(runaway)
					stop()
					return
				}
			}
		}
	}()
	err = engineCmd(runCtx, "LibreOffice", bin, scratch, 5*time.Minute,
		"-env:UserInstallation="+fileURI(profile), "--headless", "--norestore", "--nolockcheck", "--nodefault",
		"--convert-to", target, "--outdir", outDir, in)
	select {
	case <-runaway:
		return Userf("LibreOffice got stuck converting this file and was stopped")
	default:
	}
	if err != nil {
		return err
	}
	produced, _ := filepath.Glob(filepath.Join(outDir, "*"))
	if len(produced) != 1 {
		return Userf("LibreOffice couldn't convert this file to %s", strings.ToUpper(strings.SplitN(target, ":", 2)[0]))
	}
	job.Progress(0.95)
	return os.Rename(produced[0], out)
}

// runOCR reads text from an image with Tesseract: a searchable PDF (the
// image with an invisible text layer) or plain text, by output extension.
func runOCR(ctx context.Context, job Job) error {
	bin, ok := job.Tools.Lookup("tesseract")
	if !ok {
		return &MissingToolError{Tool: "tesseract"}
	}
	paths, err := absAll(job.Input(), job.Output)
	if err != nil {
		return err
	}
	in, out := paths[0], paths[1]
	ext := strings.ToLower(filepath.Ext(out))
	base := strings.TrimSuffix(out, filepath.Ext(out)) + ".ocr"
	kind := "txt"
	if ext == ".pdf" {
		kind = "pdf"
	}
	lang := job.Args.String("language", "eng")
	if !validLang(lang) {
		return Userf("%q is not a Tesseract language code; try eng, deu or fra+eng", lang)
	}
	// Tesseract reads only some formats; hand it a PNG made by Asteria's
	// own decoder so HEIC, WebP and the rest work too.
	m, err := DecodeWorking(in)
	if err != nil {
		return err
	}
	png := base + ".src.png"
	defer os.Remove(png)
	if err := EncodeWorking(m, png); err != nil {
		return err
	}
	job.Progress(0.2)
	if err := engineCmd(ctx, "Tesseract", bin, filepath.Dir(out), 10*time.Minute, png, base, "-l", lang, kind); err != nil {
		return err
	}
	if kind == "txt" {
		if b, _ := os.ReadFile(base + ".txt"); len(strings.TrimSpace(string(b))) == 0 {
			_ = os.Remove(base + ".txt")
			return Userf("No text found in this picture")
		}
	}
	return os.Rename(base+"."+kind, out)
}

func validLang(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '+' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// runPDFImages renders every page of a PDF to PNG with Ghostscript into a
// folder (page-001.png ...), at a chosen resolution.
func runPDFImages(ctx context.Context, job Job) error {
	bin, ok := job.Tools.Lookup("gs")
	if !ok {
		return &MissingToolError{Tool: "gs"}
	}
	dpi, err := job.Args.IntInRange("dpi", 150, 36, 600)
	if err != nil {
		return err
	}
	paths, err := absAll(job.Input(), job.Output)
	if err != nil {
		return err
	}
	in, out := paths[0], paths[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	job.Progress(0.1)
	err = engineCmd(ctx, "Ghostscript", bin, out, 10*time.Minute,
		"-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=png16m", fmt.Sprintf("-r%d", dpi),
		"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-sOutputFile="+filepath.Join(out, "page-%03d.png"), in)
	if err != nil {
		_ = os.RemoveAll(out)
		return err
	}
	if pages, _ := filepath.Glob(filepath.Join(out, "page-*.png")); len(pages) == 0 {
		_ = os.RemoveAll(out)
		return Userf("Ghostscript didn't produce any pages from this PDF")
	}
	return nil
}

// runTrace turns a bitmap into an SVG with Potrace: the image is reduced to
// black and white at a threshold (in linear light) and traced.
func runTrace(ctx context.Context, job Job) error {
	bin, ok := job.Tools.Lookup("potrace")
	if !ok {
		return &MissingToolError{Tool: "potrace"}
	}
	thr, err := job.Args.Signed("threshold", 50, 1, 99)
	if err != nil {
		return err
	}
	m, err := DecodeWorking(job.Input())
	if err != nil {
		return err
	}
	out, err := filepath.Abs(job.Output)
	if err != nil {
		return err
	}
	bmp := strings.TrimSuffix(out, filepath.Ext(out)) + ".trace.pbm"
	defer os.Remove(bmp)
	if err := writePBM(m, float32(thr/100), bmp); err != nil {
		return err
	}
	job.Progress(0.4)
	return engineCmd(ctx, "Potrace", bin, filepath.Dir(out), 5*time.Minute, "--svg", "--output", out, bmp)
}

// writePBM writes a 1-bit portable bitmap: dark pixels (and opaque ones
// only) are ink.
func writePBM(m *img.Image, threshold float32, path string) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "P4\n%d %d\n", m.W, m.H)
	row := make([]byte, (m.W+7)/8)
	for y := 0; y < m.H; y++ {
		for i := range row {
			row[i] = 0
		}
		for x := 0; x < m.W; x++ {
			r, g, bb, a := m.At(x, y)
			lum := img.Luma(img.Lin(r), img.Lin(g), img.Lin(bb))
			if a > 0x8000 && img.LinearToSRGB(float64(lum)) < float64(threshold) {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		b.Write(row)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// treeSize is the total size of the files under dir.
func treeSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// keep imports used in all build configurations
var (
	_ = image.Rect
	_ = color.Black
	_ = sort.Strings
)
