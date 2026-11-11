package ops

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func pages(t *testing.T, path string) int {
	t.Helper()
	n, err := PDFPageCount(context.Background(), path)
	if err != nil {
		t.Fatalf("page count %s: %v", path, err)
	}
	return n
}

func validatePDF(t *testing.T, path string) {
	t.Helper()
	if err := api.ValidateFile(context.Background(), path, pdfConfig(), nil); err != nil {
		t.Fatalf("%s is not a valid PDF: %v", path, err)
	}
}

func TestMergePDFs(t *testing.T) {
	out := mustRun(t, "pdf.merge", []string{fixture("three-pages.pdf"), fixture("three-pages.pdf")}, "merged.pdf", nil)
	validatePDF(t, out)
	if n := pages(t, out); n != 6 {
		t.Fatalf("merged pages = %d, want 6", n)
	}
	_, err := run(t, "pdf.merge", []string{fixture("three-pages.pdf")}, "one.pdf", nil)
	assertUserError(t, err)
}

func TestSplitPDFIntoNumberedPages(t *testing.T) {
	out := mustRun(t, "pdf.split", []string{fixture("three-pages.pdf")}, "pages", nil)
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
		validatePDF(t, filepath.Join(out, e.Name()))
		if n := pages(t, filepath.Join(out, e.Name())); n != 1 {
			t.Errorf("%s has %d pages, want 1", e.Name(), n)
		}
	}
	want := []string{"page-001.pdf", "page-002.pdf", "page-003.pdf"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("split files = %v, want %v", names, want)
	}
	// No temp directory left next to the output.
	siblings, _ := os.ReadDir(filepath.Dir(out))
	for _, s := range siblings {
		if strings.HasPrefix(s.Name(), ".split-") {
			t.Fatalf("temp dir %s left behind", s.Name())
		}
	}
}

func TestParsePageSelection(t *testing.T) {
	got, err := ParsePageSelection("3, 1-2, 2-", 3)
	if err != nil || !reflect.DeepEqual(got, []string{"3", "1-2", "2-3"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "0", "4", "2-1", "a-b", "1--3"} {
		if _, err := ParsePageSelection(bad, 3); err == nil {
			t.Errorf("ParsePageSelection(%q) should fail", bad)
		}
	}
}

func TestExtractPages(t *testing.T) {
	out := mustRun(t, "pdf.extract", []string{fixture("three-pages.pdf")}, "picked.pdf", Args{"pages": "3,1"})
	validatePDF(t, out)
	if n := pages(t, out); n != 2 {
		t.Fatalf("extracted pages = %d, want 2", n)
	}
	_, err := run(t, "pdf.extract", []string{fixture("three-pages.pdf")}, "bad.pdf", Args{"pages": "9"})
	assertUserError(t, err)
}

func TestCompressPDFIsValidAndNotBigger(t *testing.T) {
	out := mustRun(t, "pdf.compress", []string{fixture("three-pages.pdf")}, "small.pdf", nil)
	validatePDF(t, out)
	if pages(t, out) != 3 {
		t.Fatal("compress changed the page count")
	}
	if size(t, out) > size(t, fixture("three-pages.pdf")) {
		t.Fatalf("compressed PDF grew: %d > %d", size(t, out), size(t, fixture("three-pages.pdf")))
	}
}

func TestImagesToPDFPageSizesMatchImages(t *testing.T) {
	inputs := []string{fixture("sample.jpg"), fixture("sample.png"), fixture("sample.heic"), writeOrientedJPEG(t, 6)}
	out := mustRun(t, "pdf.from_images", inputs, "photos.pdf", nil)
	validatePDF(t, out)
	dims, err := api.PageDimsFile(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if len(dims) != 4 {
		t.Fatalf("pages = %d, want 4", len(dims))
	}
	for i := 0; i < 3; i++ {
		if int(dims[i].Width) != 120 || int(dims[i].Height) != 80 {
			t.Errorf("page %d is %vx%v, want 120x80", i+1, dims[i].Width, dims[i].Height)
		}
	}
	// The EXIF-rotated JPEG must produce an upright (portrait) page.
	if int(dims[3].Width) != 20 || int(dims[3].Height) != 40 {
		t.Errorf("rotated page is %vx%v, want 20x40", dims[3].Width, dims[3].Height)
	}
}

func TestNotAPDF(t *testing.T) {
	_, err := run(t, "pdf.compress", []string{fixture("people.csv")}, "x.pdf", nil)
	assertUserError(t, err)
}
