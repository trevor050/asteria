package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindsWinGetFFmpegWithoutTraversingItsLinks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("PATH", "")
	t.Setenv("ASTERIA_FFMPEG", "")
	t.Setenv("ASTERIA_FFPROBE", "")
	bin := filepath.Join(root, "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg_Microsoft.Winget.Source_test", "ffmpeg-9.0.2-full_build", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		path := filepath.Join(bin, name+".exe")
		if err := os.WriteFile(path, []byte("test executable"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := NewFinder().Lookup(name)
		if !ok || got != path {
			t.Errorf("%s: got %q, %v; want real WinGet package path %q", name, got, ok, path)
		}
	}
}
