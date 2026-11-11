package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Workspace is the per-run scratch directory that holds step outputs.
type Workspace struct {
	Root string
}

// SessionsRoot is where workspaces live: <user cache>/asteria/sessions.
func SessionsRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "asteria", "sessions"), nil
}

// NewWorkspace creates a fresh workspace under root.
func NewWorkspace(root string) (*Workspace, error) {
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Workspace{Root: dir}, nil
}

// FileDir creates the directory for one tray item.
func (w *Workspace) FileDir(id string) (string, error) {
	dir := filepath.Join(w.Root, id)
	return dir, os.MkdirAll(dir, 0o700)
}

// Close deletes the workspace.
func (w *Workspace) Close() error { return os.RemoveAll(w.Root) }

// CleanupStale removes workspaces left behind by earlier runs (for example
// after a crash) and the pre-0.1 "workspace" directory. Asteria runs as a
// single instance, so any other session directory is stale.
func CleanupStale(sessionsRoot string, keep string) {
	entries, err := os.ReadDir(sessionsRoot)
	if err == nil {
		for _, e := range entries {
			p := filepath.Join(sessionsRoot, e.Name())
			if p == keep || !e.IsDir() {
				continue
			}
			_ = os.RemoveAll(p)
		}
	}
	legacy := filepath.Join(filepath.Dir(sessionsRoot), "workspace")
	if strings.HasSuffix(filepath.Dir(sessionsRoot), "asteria") {
		_ = os.RemoveAll(legacy)
	}
}
