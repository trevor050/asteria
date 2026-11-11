package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceIsLazyAndCleanupKeepsCurrent(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "20200101-000000-1")
	if err := os.MkdirAll(filepath.Join(stale, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Root); !os.IsNotExist(err) {
		t.Fatal("NewWorkspace must not create anything on disk")
	}
	if _, err := ws.FileDir("abc"); err != nil {
		t.Fatal(err)
	}
	CleanupStale(root, ws.Root)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale workspace was not removed")
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "abc")); err != nil {
		t.Fatal("current workspace was removed")
	}
}

func TestExportName(t *testing.T) {
	cases := []struct{ pattern, name, ext, skill, want string }{
		{DefaultNamingPattern, "photo", ".jpg", "resize", "photo.jpg"},
		{LegacyNamingPattern, "photo", ".jpg", "resize", "photo_resize.jpg"},
		{"{name}-small", "photo", ".jpg", "resize", "photo-small.jpg"},
		{DefaultNamingPattern, "pages", "", "split_pdf", "pages"},
		{DefaultNamingPattern, `a/b:c*?"<>|`, ".png", "x", "a-b-c------.png"},
		{DefaultNamingPattern, "CON", ".txt", "x", "_CON.txt"},
		{DefaultNamingPattern, "", ".txt", "x", ".txt"},
		{"{name}. ", "trailing", "", "x", "trailing"},
	}
	for _, c := range cases {
		if got := ExportName(c.pattern, c.name, c.ext, c.skill); got != c.want {
			t.Errorf("ExportName(%q, %q, %q) = %q, want %q", c.pattern, c.name, c.ext, got, c.want)
		}
	}
}

func TestExportNameEscapesEveryWindowsDeviceName(t *testing.T) {
	for _, stem := range []string{"COM4", "com9", "LPT4", "lpt9", "COM¹", "LPT²", "NUL.tar"} {
		got := ExportName(DefaultNamingPattern, stem, ".txt", "resize")
		want := "_" + stem + ".txt"
		if got != want {
			t.Errorf("reserved Windows name %q: got %q, want %q", stem, got, want)
		}
	}
}
