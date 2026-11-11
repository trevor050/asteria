package ops

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func runText(t *testing.T, in string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.txt")
	h, _ := Lookup("doc.text")
	if err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{filepath.Base(in)}, Output: out})); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDocxTextParagraphsTablesAndTrackedChanges(t *testing.T) {
	doc := `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t xml:space="preserve">wörld</w:t></w:r></w:p>
<w:p><w:r><w:t>a</w:t><w:tab/><w:t>b</w:t><w:br/><w:t>c</w:t></w:r><w:del><w:r><w:delText>gone</w:delText></w:r></w:del></w:p>
<w:p><w:r><w:fldChar/><w:instrText>PAGE</w:instrText><w:t>7</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>name</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>value</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
</w:body></w:document>`
	p := filepath.Join(t.TempDir(), "s.docx")
	writeZip(t, p, map[string]string{"word/document.xml": doc, "[Content_Types].xml": "<Types/>"})
	got := runText(t, p)
	want := "Hello wörld\na\tb\nc\n7\nname\tvalue\na\t1\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestODTTextSpacesSpansAndNotes(t *testing.T) {
	content := `<?xml version="1.0"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"><office:body><office:text>
<text:h>Title</text:h>
<text:p>one<text:s text:c="3"/>two <text:span>bold</text:span><text:tab/>x<office:annotation><text:p>comment</text:p></office:annotation></text:p>
</office:text></office:body></office:document-content>`
	p := filepath.Join(t.TempDir(), "s.odt")
	writeZip(t, p, map[string]string{"content.xml": content, "mimetype": "application/vnd.oasis.opendocument.text"})
	got := runText(t, p)
	want := "Title\none   two bold\tx\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestOfficeTextRealFixture(t *testing.T) {
	for _, ext := range []string{"docx", "odt"} {
		p := filepath.Join("..", "..", "testdata", "formats", "sample."+ext)
		if _, err := os.Stat(p); err != nil {
			t.Skip("fixture missing")
		}
		got := runText(t, p)
		if !strings.Contains(got, "Asteria matrix check") || !strings.Contains(got, "name\tvalue") {
			t.Fatalf("%s: %q", ext, got)
		}
	}
}
