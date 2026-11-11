package ops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const formatsDir = "../../testdata/formats"

type tstEntry struct {
	name, body string
	typ        byte
	link       string
}

func tstTar(t *testing.T, es []tstEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tstXZ(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tstZstd(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tstUnpackFile runs archive.unpack on a file whose display name is name.
func tstUnpackFile(t *testing.T, in, name string) (string, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	h, ok := Lookup("archive.unpack")
	if !ok {
		t.Fatal("archive.unpack not registered")
	}
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{name}, Output: out}))
	return out, err
}

func tstUnpackBytes(t *testing.T, data []byte, name string) (string, error) {
	t.Helper()
	// The on-disk name is meaningless on purpose: detection uses the content.
	in := filepath.Join(t.TempDir(), "input.bin")
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return tstUnpackFile(t, in, name)
}

func tstTree(t *testing.T, root string) map[string]string {
	t.Helper()
	return readTree(t, root)
}

func TestUnpackTarXZAndZstd(t *testing.T) {
	tarData := tstTar(t, []tstEntry{
		{name: "bundle/", typ: tar.TypeDir},
		{name: "bundle/hello.txt", body: "hello\n"},
		{name: "bundle/sub/deep über.txt", body: "deep\n"},
	})
	want := map[string]string{"hello.txt": "hello\n", "sub/deep über.txt": "deep\n"}
	cases := map[string][]byte{
		"bundle.tar.xz":  tstXZ(t, tarData),
		"bundle.txz":     tstXZ(t, tarData),
		"bundle.tar.zst": tstZstd(t, tarData),
	}
	for name, data := range cases {
		out, err := tstUnpackBytes(t, data, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := tstTree(t, out)
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, got[k], v)
			}
		}
	}
}

func TestUnpackFormatFixtures(t *testing.T) {
	const marker = "Asteria matrix check"
	for _, name := range []string{"sample.tar.xz", "sample.tar.zst", "sample.tar.bz2"} {
		out, err := tstUnpackFile(t, filepath.Join(formatsDir, name), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := tstTree(t, out)
		if got["note.txt"] != marker+"\n" || len(got) != 1 {
			t.Errorf("%s: tree = %v", name, got)
		}
	}
	// Single-file compression: the inner name comes from the display name.
	for _, c := range []struct{ file, display string }{
		{"sample.gz", "notes.txt.gz"},
		{"sample.xz", "notes.txt.xz"},
		{"sample.zst", "notes.txt.zst"},
		{"sample.bz2", "notes.txt.bz2"},
	} {
		out, err := tstUnpackFile(t, filepath.Join(formatsDir, c.file), c.display)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		got := tstTree(t, out)
		if got["notes.txt"] != marker+"\n" || len(got) != 1 {
			t.Errorf("%s: tree = %v", c.file, got)
		}
	}
}

func TestUnpack7z(t *testing.T) {
	out, err := tstUnpackFile(t, filepath.Join(formatsDir, "sample.7z"), "sample.7z")
	if err != nil {
		t.Fatal(err)
	}
	got := tstTree(t, out)
	if !strings.Contains(got["note.txt"], "Asteria matrix check") {
		t.Errorf("7z tree = %v", got)
	}
}

func TestUnpackRejectsTraversalAndSkipsLinks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry tstEntry
	}{
		{"parent", tstEntry{name: "../evil.txt", body: "x"}},
		{"nested parent", tstEntry{name: "a/../../evil.txt", body: "x"}},
		{"absolute", tstEntry{name: "/tmp/asteria-evil.txt", body: "x"}},
	} {
		data := tstXZ(t, tstTar(t, []tstEntry{{name: "ok.txt", body: "fine"}, tc.entry}))
		out, err := tstUnpackBytes(t, data, "evil.tar.xz")
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
		if _, statErr := os.Stat(out); statErr == nil {
			t.Errorf("%s: output folder should be removed after a failure", tc.name)
		}
		if _, statErr := os.Stat(filepath.Join(filepath.Dir(out), "evil.txt")); statErr == nil {
			t.Errorf("%s: wrote outside the output folder", tc.name)
		}
	}

	// Symlinks and hard links are never created.
	data := tstZstd(t, tstTar(t, []tstEntry{
		{name: "real.txt", body: "data"},
		{name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		{name: "hard", typ: tar.TypeLink, link: "real.txt"},
	}))
	out, err := tstUnpackBytes(t, data, "links.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	got := tstTree(t, out)
	if len(got) != 1 || got["real.txt"] != "data" {
		t.Errorf("tree = %v", got)
	}
	if _, err := os.Lstat(filepath.Join(out, "link")); err == nil {
		t.Error("symlink was created")
	}
}

func TestUnpackRejectsGarbage(t *testing.T) {
	_, err := tstUnpackBytes(t, []byte("just some text, not an archive"), "nope.xz")
	if err == nil || !strings.Contains(err.Error(), "not a supported archive") {
		t.Errorf("err = %v", err)
	}
	_, err = tstUnpackBytes(t, append([]byte{0xfd, '7', 'z', 'X', 'Z', 0}, bytes.Repeat([]byte{1}, 40)...), "bad.xz")
	if err == nil {
		t.Error("corrupt xz should fail")
	}
}

func runCompress(t *testing.T, handler, in, name, outName string) string {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok {
		t.Fatalf("%s not registered", handler)
	}
	out := filepath.Join(t.TempDir(), outName)
	if err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{name}, Output: out})); err != nil {
		t.Fatalf("%s: %v", handler, err)
	}
	return out
}

func TestSingleFileRoundTrips(t *testing.T) {
	content := "héllo wörld\nsecond line\n"
	src := filepath.Join(t.TempDir(), "disk-name.dat")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ handler, ext string }{
		{"archive.gzip", ".gz"}, {"archive.xz", ".xz"}, {"archive.zstd", ".zst"},
	} {
		packed := runCompress(t, c.handler, src, "notes.txt", "out"+c.ext)
		raw, _ := os.ReadFile(packed)
		if len(raw) == 0 || string(raw) == content {
			t.Fatalf("%s: output is not compressed", c.handler)
		}
		out, err := tstUnpackFile(t, packed, "notes.txt"+c.ext)
		if err != nil {
			t.Fatalf("%s: %v", c.handler, err)
		}
		got := tstTree(t, out)
		if len(got) != 1 || got["notes.txt"] != content {
			t.Errorf("%s: tree = %v", c.handler, got)
		}
	}

	// gzip stores the original name; it wins over a renamed .gz.
	packed := runCompress(t, "archive.gzip", src, "notes.txt", "out.gz")
	f, _ := os.Open(packed)
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if gr.Name != "notes.txt" {
		t.Errorf("gzip header name = %q", gr.Name)
	}
	f.Close()
	out, err := tstUnpackFile(t, packed, "renamed.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got := tstTree(t, out); got["notes.txt"] != content || len(got) != 1 {
		t.Errorf("tree = %v", got)
	}
}

func TestCompressRejectsFolderForSingleFile(t *testing.T) {
	h, _ := Lookup("archive.gzip")
	dir := t.TempDir()
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{dir}, Names: []string{"folder"}, Output: filepath.Join(t.TempDir(), "x.gz")}))
	if err == nil {
		t.Fatal("expected an error for a folder")
	}
}

func TestTarCompressRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "photos")
	want := map[string]string{"a.txt": "alpha\n", "sub/b.txt": "beta\n", "sub/empty.txt": ""}
	for name, body := range want {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink must not be packed.
	_ = os.Symlink("/etc/passwd", filepath.Join(src, "link"))

	for _, c := range []struct{ handler, ext string }{{"archive.tarxz", ".tar.xz"}, {"archive.tarzst", ".tar.zst"}} {
		packed := runCompress(t, c.handler, src, "photos", "out"+c.ext)
		raw, _ := os.ReadFile(packed)

		// Verify with an independent read of the container.
		tr := tar.NewReader(func() *bytes.Reader {
			if c.ext == ".tar.xz" {
				r, err := xz.NewReader(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				var b bytes.Buffer
				if _, err := b.ReadFrom(r); err != nil {
					t.Fatal(err)
				}
				return bytes.NewReader(b.Bytes())
			}
			dec, err := zstd.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()
			var b bytes.Buffer
			if _, err := b.ReadFrom(dec); err != nil {
				t.Fatal(err)
			}
			return bytes.NewReader(b.Bytes())
		}())
		var names []string
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			if hdr.Uid != 0 || hdr.Uname != "" {
				t.Errorf("%s: %s leaks owner info", c.handler, hdr.Name)
			}
			if !strings.HasPrefix(hdr.Name, "photos/") && hdr.Name != "photos/" {
				t.Errorf("%s: unexpected entry %q", c.handler, hdr.Name)
			}
			names = append(names, hdr.Name)
		}
		sort.Strings(names)
		if joined := strings.Join(names, ","); strings.Contains(joined, "link") || !strings.Contains(joined, "photos/sub/b.txt") {
			t.Errorf("%s: entries = %v", c.handler, names)
		}

		out, err := tstUnpackFile(t, packed, "photos"+c.ext)
		if err != nil {
			t.Fatalf("%s: %v", c.handler, err)
		}
		got := tstTree(t, out)
		if len(got) != len(want) {
			t.Fatalf("%s: tree = %v", c.handler, got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", c.handler, k, got[k], v)
			}
		}
	}

	// A single file packs too.
	one := filepath.Join(t.TempDir(), "one.dat")
	_ = os.WriteFile(one, []byte("solo"), 0o644)
	packed := runCompress(t, "archive.tarxz", one, "solo.txt", "solo.tar.xz")
	out, err := tstUnpackFile(t, packed, "solo.tar.xz")
	if err != nil {
		t.Fatal(err)
	}
	if got := tstTree(t, out); got["solo.txt"] != "solo" || len(got) != 1 {
		t.Errorf("tree = %v", got)
	}
}

func TestListArchiveContents(t *testing.T) {
	list := func(in, name string) string {
		t.Helper()
		h, _ := Lookup("archive.list")
		out := filepath.Join(t.TempDir(), "list.txt")
		if err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{name}, Output: out})); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := os.ReadFile(out)
		return string(b)
	}

	tarData := tstTar(t, []tstEntry{
		{name: "bundle/", typ: tar.TypeDir},
		{name: "bundle/hello.txt", body: "hello\n"},
	})
	p := filepath.Join(t.TempDir(), "x.bin")
	_ = os.WriteFile(p, tstXZ(t, tarData), 0o644)
	got := list(p, "bundle.tar.xz")
	for _, want := range []string{"bundle.tar.xz: 1 files, 6 bytes", "bundle/hello.txt", "bundle/\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("tar.xz listing missing %q:\n%s", want, got)
		}
	}

	got = list(filepath.Join(formatsDir, "sample.7z"), "sample.7z")
	if !strings.Contains(got, "note.txt") || !strings.Contains(got, "1 files, 21 bytes") {
		t.Errorf("7z listing:\n%s", got)
	}

	got = list(filepath.Join(formatsDir, "sample.gz"), "notes.txt.gz")
	if !strings.Contains(got, "notes.txt") || !strings.Contains(got, "21") {
		t.Errorf("gz listing:\n%s", got)
	}

	got = list(fixture("bundle.zip"), "bundle.zip")
	if !strings.Contains(got, "hello.txt") {
		t.Errorf("zip listing:\n%s", got)
	}
}
