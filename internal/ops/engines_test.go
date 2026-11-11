//go:build !windows

package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asteria/internal/tools"
)

// A LibreOffice that never stops writing is stopped by the size guard with
// a plain message, and its scratch output is removed.
func TestLibreOfficeRunawayOutputIsStopped(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "soffice")
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = --outdir ] && od=$2; shift; done\nyes 1 > \"$od/stuck.txt\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "in.doc")
	os.WriteFile(in, []byte("x"), 0o644)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	old := loRunawayBase
	loRunawayBase = 1 << 20
	defer func() { loRunawayBase = old }()

	f := tools.NewFinderWithDirs(nil)
	f.Prefer(func(name string) (string, bool) { return fake, name == "soffice" })
	h, _ := Lookup("engine.libreoffice")
	start := time.Now()
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{"in.doc"}, Output: filepath.Join(work, "in.txt"), Tools: f}))
	if err == nil || !strings.Contains(err.Error(), "got stuck") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	left, _ := os.ReadDir(work)
	if len(left) != 0 {
		t.Fatalf("scratch left behind: %v", left)
	}
}
