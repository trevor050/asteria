package ops

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExtractZipAndTarGz(t *testing.T) {
	// The single top-level "bundle" folder is unwrapped.
	want := map[string]string{"hello.txt": "hello\n", "nested/über file.txt": "deep\n"}
	for _, name := range []string{"bundle.zip", "bundle.tar.gz"} {
		h, _ := Lookup("archive.extract")
		out := filepath.Join(t.TempDir(), "out")
		job := Prepare(Job{Inputs: []string{fixture(name)}, Names: []string{name}, Output: out})
		if err := h.Run(t.Context(), job); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := readTree(t, out)
		if len(got) != len(want) {
			t.Fatalf("%s extracted %v", name, got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, got[k], v)
			}
		}
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.zip")
	f, _ := os.Create(evil)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("../escaped.txt")
	_, _ = w.Write([]byte("nope"))
	zw.Close()
	f.Close()

	out := filepath.Join(dir, "sandbox", "out")
	h, _ := Lookup("archive.extract")
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{evil}, Names: []string{"evil.zip"}, Output: out}))
	assertUserError(t, err)
	if _, err := os.Stat(filepath.Join(dir, "sandbox", "escaped.txt")); err == nil {
		t.Fatal("zip slip wrote outside the output folder")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("failed extraction left a partial folder")
	}
}

func TestZipUsesDisplayNamesAndDedupes(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "step-2")
	if err := os.MkdirAll(filepath.Join(folder, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(folder, "sub", "a.txt"), []byte("A"), 0o644)

	h, _ := Lookup("archive.zip")
	out := filepath.Join(t.TempDir(), "archive.zip")
	job := Prepare(Job{
		Inputs: []string{fixture("sample.jpg"), fixture("sample.jpg"), folder},
		Names:  []string{"photo.jpg", "photo.jpg", "pages"},
		Output: out,
	})
	if err := h.Run(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"pages/", "pages/sub/", "pages/sub/a.txt", "photo (2).jpg", "photo.jpg"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("zip entries = %v, want %v", names, want)
	}
	rc, _ := zr.File[0].Open()
	defer rc.Close()
}

func TestSafeJoin(t *testing.T) {
	root := filepath.FromSlash("/out")
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd", `..\x`} {
		if _, err := safeJoin(root, bad); err == nil {
			t.Errorf("safeJoin(%q) should fail", bad)
		}
	}
	got, err := safeJoin(root, "a/b.txt")
	if err != nil || got != filepath.Join(root, "a", "b.txt") {
		t.Fatalf("safeJoin = %s, %v", got, err)
	}
}

func TestZipKeepsFilesWhoseNamesCollideWithGeneratedSuffix(t *testing.T) {
	h, _ := Lookup("archive.zip")
	out := filepath.Join(t.TempDir(), "archive.zip")
	job := Prepare(Job{
		Inputs: []string{fixture("notes.md"), fixture("people.csv"), fixture("sample.jpg")},
		Names:  []string{"report.txt", "report (2).txt", "report.txt"},
		Output: out,
	})
	if err := h.Run(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	seen := map[string]bool{}
	for _, entry := range zr.File {
		key := strings.ToLower(entry.Name)
		if seen[key] {
			t.Fatalf("duplicate zip entry %q would overwrite another file on extraction", entry.Name)
		}
		seen[key] = true
	}
	if len(seen) != len(job.Inputs) {
		t.Fatalf("zip has %d unique files, want %d", len(seen), len(job.Inputs))
	}
}
