// Package tools locates optional external programs (currently ffmpeg and
// ffprobe) that a few skills depend on.
//
// GUI apps on macOS do not inherit the user's shell PATH, so a plain
// exec.LookPath misses Homebrew installs. Finder therefore also checks the
// usual package-manager locations for each platform.
package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Finder resolves executables by name and caches the result.
type Finder struct {
	mu    sync.Mutex
	cache map[string]string
	// extraDirs is searched after PATH. Overridable in tests.
	extraDirs []string
}

// NewFinder returns a Finder that searches PATH plus platform defaults.
func NewFinder() *Finder {
	return &Finder{cache: map[string]string{}, extraDirs: defaultDirs()}
}

// NewFinderWithDirs returns a Finder that searches PATH plus the given dirs.
func NewFinderWithDirs(dirs []string) *Finder {
	return &Finder{cache: map[string]string{}, extraDirs: dirs}
}

// Lookup returns the absolute path for the named tool and whether it exists.
// An environment variable ASTERIA_<NAME> (for example ASTERIA_FFMPEG) takes
// precedence when it points at an existing file.
func (f *Finder) Lookup(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	f.mu.Lock()
	if p, ok := f.cache[name]; ok {
		f.mu.Unlock()
		return p, p != ""
	}
	f.mu.Unlock()

	p := f.resolve(name)

	f.mu.Lock()
	f.cache[name] = p
	f.mu.Unlock()
	return p, p != ""
}

// Reset clears the cache so a tool installed while the app is running is
// picked up on the next lookup.
func (f *Finder) Reset() {
	f.mu.Lock()
	f.cache = map[string]string{}
	f.mu.Unlock()
}

func (f *Finder) resolve(name string) string {
	envKey := "ASTERIA_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" && isExecutable(v) {
		return v
	}
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	exe := name
	if runtime.GOOS == "windows" && !strings.HasSuffix(exe, ".exe") {
		exe += ".exe"
	}
	for _, dir := range f.extraDirs {
		candidate := filepath.Join(dir, exe)
		if isExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func defaultDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", filepath.Join(home, "bin")}
	case "windows":
		dirs := []string{}
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			dirs = append(dirs, filepath.Join(v, "Microsoft", "WinGet", "Links"))
			// Service logons can reject WinGet's symlinks as untrusted
			// mount points. The package's real binaries still work.
			bins, _ := filepath.Glob(filepath.Join(v, "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg*", "ffmpeg*", "bin"))
			dirs = append(dirs, bins...)
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "scoop", "shims"))
		}
		dirs = append(dirs, `C:\ProgramData\chocolatey\bin`)
		if v := os.Getenv("ProgramFiles"); v != "" {
			dirs = append(dirs, filepath.Join(v, "ffmpeg", "bin"))
		}
		return dirs
	default:
		return []string{"/usr/bin", "/usr/local/bin", "/snap/bin", filepath.Join(home, ".local", "bin")}
	}
}

// InstallHint returns a short, platform-specific install command for a tool.
func InstallHint(name string) string {
	switch strings.ToLower(name) {
	case "ffmpeg", "ffprobe":
		switch runtime.GOOS {
		case "darwin":
			return "brew install ffmpeg"
		case "windows":
			return "winget install Gyan.FFmpeg"
		default:
			return "sudo apt install ffmpeg (or your distro's package manager)"
		}
	}
	return "install " + name + " and make sure it is on your PATH"
}
