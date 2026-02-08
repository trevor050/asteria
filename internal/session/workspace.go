package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Workspace struct {
	Root string
}

func NewWorkspace() (*Workspace, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "asteria", "workspace", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{Root: root}, nil
}

func (w *Workspace) EnsureFileDir(fileID string) (string, error) {
	dir := filepath.Join(w.Root, fileID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (w *Workspace) SnapshotPath(fileID string, index int, ext string) string {
	filename := "snapshot-" + fmtIndex(index) + ext
	return filepath.Join(w.Root, fileID, filename)
}

func (w *Workspace) Reset() error {
	return os.RemoveAll(w.Root)
}

func CopyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func fmtIndex(index int) string {
	return fmt.Sprintf("%03d", index)
}
