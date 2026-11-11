package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddPathsDeduplicatesCaseAliasesInOneDrop(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app, err := NewApp(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.store.Workspace().Close() })
	path := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := app.AddPaths([]string{path, strings.ToUpper(path)})
	if len(result.Errors) != 0 || len(result.Added) != 1 || result.Duplicates != 1 {
		t.Fatalf("same file dropped twice with different casing: %+v", result)
	}
}
