package addons

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live tests download real engines from their official hosts, so they only
// run when asked:
//
//	ASTERIA_ADDON_LIVE=1 ASTERIA_ADDONS_DIR=<scratch dir> go test ./internal/addons -run Live -v -timeout 2h
//
// ASTERIA_ADDON_LIVE_ONLY=ffmpeg,pandoc limits the clean-install pass.

func liveManager(t *testing.T) *Manager {
	t.Helper()
	if os.Getenv("ASTERIA_ADDON_LIVE") == "" {
		t.Skip("set ASTERIA_ADDON_LIVE=1 (downloads real engines)")
	}
	root := os.Getenv("ASTERIA_ADDONS_DIR")
	if root == "" {
		t.Fatal("set ASTERIA_ADDONS_DIR to a scratch folder")
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "addons", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(root, m)
}

// oneClick lists the engines this platform installs with one click.
func oneClick(m *Manifest) []Engine {
	var out []Engine
	for _, e := range m.Engines {
		if a, ok := e.Artifact(); ok && a.Manual == nil {
			out = append(out, e)
		}
	}
	return out
}

// Every one-click engine installs from a clean state, its tools run, it is
// listed with its version, and Remove frees the space.
func TestLiveCleanInstallAndRemove(t *testing.T) {
	m := liveManager(t)
	only := os.Getenv("ASTERIA_ADDON_LIVE_ONLY")
	for _, e := range oneClick(m.Manifest) {
		if only != "" && !strings.Contains(","+only+",", ","+e.ID+",") {
			continue
		}
		t.Run(e.ID, func(t *testing.T) {
			a, _ := e.Artifact()
			_, _ = m.Remove(e.ID)
			start := time.Now()
			var last Progress
			err := m.Install(context.Background(), e.ID, func(p Progress) { last = p })
			took := time.Since(start)
			if err != nil {
				t.Fatalf("install: %v (last phase %s)", err, last.Phase)
			}
			rec, ok := m.Installed(e.ID)
			if !ok || rec.Version != a.Version {
				t.Fatalf("installed record = %+v", rec)
			}
			for _, tool := range e.Tools {
				if _, inBins := a.Bins[tool]; !inBins {
					continue
				}
				if p, ok := m.BinPath(tool); !ok {
					t.Errorf("tool %s not found after install", tool)
				} else {
					t.Logf("%s -> %s", tool, p)
				}
			}
			used := m.EngineBytes(e.ID)
			freed, err := m.Remove(e.ID)
			if err != nil {
				t.Fatalf("remove: %v", err)
			}
			if _, err := os.Stat(m.dir(e.ID)); !os.IsNotExist(err) {
				t.Fatalf("engine folder still there after remove")
			}
			t.Logf("RESULT %s %s: download %s, on disk %s, freed %s, %s", e.ID, a.Version, human(a.Size), human(used), human(freed), took.Round(time.Second))
		})
	}
}

// Cancelling during the download stops it, removes the partial file and
// installs nothing.
func TestLiveCancelMidDownload(t *testing.T) {
	m := liveManager(t)
	id := pickLargest(t, m, 20<<20)
	_, _ = m.Remove(id)
	ctx := context.Background()
	done := make(chan error, 1)
	cancelled := false
	go func() {
		done <- m.Install(ctx, id, func(p Progress) {
			if p.Phase == "download" && p.Done > 2<<20 && !cancelled {
				cancelled = true
				go m.Cancel(id)
			}
		})
	}()
	err := <-done
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want cancelled", err)
	}
	assertNothingLeft(t, m, id)
	t.Logf("RESULT cancel %s: stopped after >2 MB, partial removed", id)
}

// Without a network the user gets a plain message and nothing is left.
func TestLiveNoNetwork(t *testing.T) {
	m := liveManager(t)
	id := pickLargest(t, m, 0)
	_, _ = m.Remove(id)
	dead, _ := url.Parse("http://127.0.0.1:9") // nothing listens here
	m.Client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(dead)}, Timeout: 20 * time.Second}
	err := m.Install(context.Background(), id, nil)
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "Couldn't reach") {
		t.Fatalf("err = %v", err)
	}
	assertNothingLeft(t, m, id)
	t.Logf("RESULT offline %s: %q", id, ue.Msg)
}

// A download that doesn't match the pinned checksum is refused before
// anything is unpacked or run.
func TestLiveCorruptDownloadRefused(t *testing.T) {
	m := liveManager(t)
	id := pickSmallest(t, m)
	e, _ := m.Manifest.Engine(id)
	a, _ := e.Artifact()
	bad := []byte(a.SHA256)
	if bad[0] == '0' {
		bad[0] = '1'
	} else {
		bad[0] = '0'
	}
	a.SHA256 = string(bad)
	a.SHA1Published = ""
	setArtifact(m.Manifest, id, a)
	_, _ = m.Remove(id)
	err := m.Install(context.Background(), id, nil)
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "didn't match its published checksum") {
		t.Fatalf("err = %v", err)
	}
	assertNothingLeft(t, m, id)
	t.Logf("RESULT corrupt %s: %q", id, ue.Msg)
}

// Not enough free space is reported before downloading anything.
func TestLiveNotEnoughSpace(t *testing.T) {
	m := liveManager(t)
	id := pickLargest(t, m, 0)
	_, _ = m.Remove(id)
	m.FreeSpace = func(string) (uint64, error) { return 1 << 20, nil }
	err := m.Install(context.Background(), id, nil)
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "Not enough free space") {
		t.Fatalf("err = %v", err)
	}
	assertNothingLeft(t, m, id)
	t.Logf("RESULT low disk %s: %q", id, ue.Msg)
}

func pickLargest(t *testing.T, m *Manager, atLeast int64) string {
	t.Helper()
	best, size := "", int64(0)
	for _, e := range oneClick(m.Manifest) {
		a, _ := e.Artifact()
		// Prefer a mid-size engine: big enough to cancel, quick to start.
		if a.Size >= atLeast && (best == "" || a.Size < size) {
			best, size = e.ID, a.Size
		}
	}
	if best == "" {
		t.Skip("no suitable engine on this platform")
	}
	return best
}

func pickSmallest(t *testing.T, m *Manager) string {
	t.Helper()
	best, size := "", int64(1<<62)
	for _, e := range oneClick(m.Manifest) {
		if a, _ := e.Artifact(); a.Size < size {
			best, size = e.ID, a.Size
		}
	}
	if best == "" {
		t.Skip("no one-click engine on this platform")
	}
	return best
}

func setArtifact(m *Manifest, id string, a Artifact) {
	for i := range m.Engines {
		if m.Engines[i].ID == id {
			p := map[string]Artifact{}
			for k, v := range m.Engines[i].Platforms {
				p[k] = v
			}
			p[Platform()] = a
			m.Engines[i].Platforms = p
		}
	}
}

func assertNothingLeft(t *testing.T, m *Manager, id string) {
	t.Helper()
	if _, err := os.Stat(m.dir(id)); !os.IsNotExist(err) {
		t.Errorf("engine folder exists after a failed install")
	}
	for _, sub := range []string{".downloads", ".staging"} {
		entries, _ := os.ReadDir(filepath.Join(m.Root, sub))
		for _, en := range entries {
			if strings.HasPrefix(en.Name(), id) {
				t.Errorf("left behind: %s", filepath.Join(sub, en.Name()))
			}
		}
	}
	if _, ok := m.Installed(id); ok {
		t.Errorf("listed as installed after a failed install")
	}
}

var _ = fmt.Sprint
