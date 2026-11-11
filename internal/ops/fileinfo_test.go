package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fileInfoOf(t *testing.T, name string, content []byte) fileInfo {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, name)
	if err := os.WriteFile(in, content, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "info.json")
	h, _ := Lookup("file.info")
	if err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{name}, Output: out})); err != nil {
		t.Fatal(err)
	}
	var fi fileInfo
	b, _ := os.ReadFile(out)
	if err := json.Unmarshal(b, &fi); err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestFileInfoSniffsContentNotName(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	fi := fileInfoOf(t, "holiday.dat", png)
	if fi.Kind != "PNG image" || fi.MIME != "image/png" || fi.NameMatches == nil || *fi.NameMatches {
		t.Fatalf("%+v", fi)
	}
	if fi.Size != int64(len(png)) || len(fi.SHA256) != 64 || fi.Text {
		t.Fatalf("%+v", fi)
	}
	docx := append([]byte("PK\x03\x04"), []byte("....[Content_Types].xml....word/document.xml")...)
	if fi := fileInfoOf(t, "report.docx", docx); fi.Kind != "Word document" || !*fi.NameMatches {
		t.Fatalf("%+v", fi)
	}
}

func TestFileInfoText(t *testing.T) {
	fi := fileInfoOf(t, "notes.weird", []byte("one\r\ntwo\r\nthree"))
	if !fi.Text || fi.Encoding != "ASCII" || fi.Lines != 3 || fi.LineEndings != "Windows (CRLF)" {
		t.Fatalf("%+v", fi)
	}
	fi = fileInfoOf(t, "u.txt", []byte("héllo\nwörld\n"))
	if fi.Encoding != "UTF-8" || fi.Lines != 2 || fi.LineEndings != "Unix (LF)" {
		t.Fatalf("%+v", fi)
	}
	fi = fileInfoOf(t, "blob.bin", []byte{0, 1, 2, 3, 0xfe, 0x00, 0x10})
	if fi.Text || fi.Kind != "Unrecognized binary data" {
		t.Fatalf("%+v", fi)
	}
	if fi := fileInfoOf(t, "empty", nil); fi.Kind != "Empty file" {
		t.Fatalf("%+v", fi)
	}
}
