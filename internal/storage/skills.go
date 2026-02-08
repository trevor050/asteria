package storage

import (
	"os"
	"path/filepath"
)

// SkillsDir returns the on-disk directory used for community skills and packs.
// This is user-specific and portable across installs.
func SkillsDir() (string, error) {
	base, err := AppConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
