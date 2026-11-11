package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func init() {
	register(Handler{Name: "pdf.merge", Run: pdfMerge, Combine: true})
	register(Handler{Name: "pdf.split", Run: pdfSplit})
	register(Handler{Name: "pdf.extract", Run: pdfExtract})
	register(Handler{Name: "pdf.compress", Run: pdfCompress})
	register(Handler{Name: "pdf.from_images", Run: pdfFromImages, Combine: true})
}

// pdfConfig returns a pdfcpu configuration that never touches the user's
// config directory on disk.
func pdfConfig() *model.Configuration {
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil || conf == nil {
		conf = model.NewDefaultConfiguration()
	}
	conf.ValidationMode = model.ValidationRelaxed
	return conf
}

func pdfError(path string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "encrypt") || strings.Contains(low, "password"):
		return Userf("%s is password-protected", filepath.Base(path))
	case strings.Contains(low, "not a pdf") || strings.Contains(low, "header") || strings.Contains(low, "xref"):
		return Userf("%s is not a readable PDF", filepath.Base(path))
	}
	return Userf("PDF error in %s: %s", filepath.Base(path), msg)
}

// PDFPageCount returns the number of pages in a PDF file.
func PDFPageCount(ctx context.Context, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := api.PageCount(ctx, f, pdfConfig())
	if err != nil {
		return 0, pdfError(path, err)
	}
	return n, nil
}

func pdfMerge(ctx context.Context, job Job) error {
	if len(job.Inputs) < 2 {
		return Userf("select at least two PDFs to merge")
	}
	err := api.MergeCreateFile(ctx, job.Inputs, job.Output, false, pdfConfig())
	if err != nil {
		_ = os.Remove(job.Output)
		return pdfError(job.Input(), err)
	}
	return nil
}

// pdfSplit writes one PDF per page into the output folder, named
// page-001.pdf, page-002.pdf and so on.
func pdfSplit(ctx context.Context, job Job) error {
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(job.Output), ".split-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := api.SplitFile(ctx, job.Input(), tmp, 1, pdfConfig()); err != nil {
		return pdfError(job.Input(), err)
	}
	job.Progress(0.8)
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	type page struct {
		n    int
		name string
	}
	pages := []page{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
			continue
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		idx := strings.LastIndex(base, "_")
		n, err := strconv.Atoi(base[idx+1:])
		if idx < 0 || err != nil {
			return fmt.Errorf("unexpected split output %q", name)
		}
		pages = append(pages, page{n: n, name: name})
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].n < pages[j].n })
	width := len(strconv.Itoa(len(pages)))
	if width < 3 {
		width = 3
	}
	for _, p := range pages {
		dst := filepath.Join(job.Output, fmt.Sprintf("page-%0*d.pdf", width, p.n))
		if err := os.Rename(filepath.Join(tmp, p.name), dst); err != nil {
			return err
		}
	}
	return nil
}

// ParsePageSelection validates a page list such as "1-3, 5, 8-" against the
// page count and returns pdfcpu selection strings.
func ParsePageSelection(spec string, pageCount int) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, Userf("enter pages such as 1-3, 5")
	}
	out := []string{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i >= 0 {
			lo, hi = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		a, b := 1, pageCount
		var err error
		if lo != "" {
			if a, err = strconv.Atoi(lo); err != nil {
				return nil, Userf("%q is not a page range", part)
			}
		}
		if hi != "" {
			if b, err = strconv.Atoi(hi); err != nil {
				return nil, Userf("%q is not a page range", part)
			}
		}
		if a < 1 || b < a {
			return nil, Userf("%q is not a page range", part)
		}
		if a > pageCount {
			return nil, Userf("page %d does not exist (the PDF has %d pages)", a, pageCount)
		}
		if b > pageCount {
			b = pageCount
		}
		if a == b {
			out = append(out, strconv.Itoa(a))
		} else {
			out = append(out, fmt.Sprintf("%d-%d", a, b))
		}
	}
	if len(out) == 0 {
		return nil, Userf("enter pages such as 1-3, 5")
	}
	return out, nil
}

func pdfExtract(ctx context.Context, job Job) error {
	n, err := PDFPageCount(ctx, job.Input())
	if err != nil {
		return err
	}
	sel, err := ParsePageSelection(job.Args.String("pages", ""), n)
	if err != nil {
		return err
	}
	// CollectFile keeps the order the user typed (TrimFile would sort).
	if err := api.CollectFile(ctx, job.Input(), job.Output, sel, pdfConfig()); err != nil {
		_ = os.Remove(job.Output)
		return pdfError(job.Input(), err)
	}
	return nil
}

func pdfCompress(ctx context.Context, job Job) error {
	if err := api.OptimizeFile(ctx, job.Input(), job.Output, pdfConfig(), nil); err != nil {
		_ = os.Remove(job.Output)
		return pdfError(job.Input(), err)
	}
	in, err1 := os.Stat(job.Input())
	out, err2 := os.Stat(job.Output)
	if err1 == nil && err2 == nil && out.Size() >= in.Size() {
		if err := os.Remove(job.Output); err != nil {
			return err
		}
		return CopyFile(job.Input(), job.Output)
	}
	return nil
}

// pdfFromImages puts each image on its own page sized to the image. Formats
// pdfcpu cannot embed directly (and JPEGs whose EXIF says "rotate me") are
// re-encoded first so pages come out upright.
func pdfFromImages(ctx context.Context, job Job) error {
	tmp, err := os.MkdirTemp(filepath.Dir(job.Output), ".img2pdf-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	prepared := make([]string, 0, len(job.Inputs))
	for i, in := range job.Inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		ext := strings.ToLower(filepath.Ext(in))
		direct := (ext == ".png") || ((ext == ".jpg" || ext == ".jpeg") && JPEGOrientation(in) <= 1)
		if direct {
			prepared = append(prepared, in)
		} else {
			img, err := DecodeImage(in)
			if err != nil {
				return err
			}
			outExt := ".jpg"
			if hasAlpha(img) || ext == ".png" || ext == ".gif" || ext == ".bmp" {
				outExt = ".png"
			}
			p := filepath.Join(tmp, fmt.Sprintf("%04d%s", i, outExt))
			if err := EncodeImage(img, p, 92, false); err != nil {
				return err
			}
			prepared = append(prepared, p)
		}
		job.Progress(float64(i+1) / float64(len(job.Inputs)) * 0.7)
	}
	imp := pdfcpu.DefaultImportConfig()
	imp.Pos = types.Full
	if err := api.ImportImagesFile(ctx, prepared, job.Output, imp, pdfConfig()); err != nil {
		_ = os.Remove(job.Output)
		return Userf("could not build PDF: %v", err)
	}
	return nil
}
