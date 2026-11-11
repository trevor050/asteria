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
	// preferred is asked first: engines the user added from Asteria.
	preferred func(name string) (string, bool)
}

// Prefer makes the finder ask fn before PATH and the usual install
// locations (Asteria's own add-ons win over system copies).
func (f *Finder) Prefer(fn func(name string) (string, bool)) {
	f.mu.Lock()
	f.preferred = fn
	f.cache = map[string]string{}
	f.mu.Unlock()
}

// FromAddon reports whether a found tool came from Asteria's add-ons.
func (f *Finder) FromAddon(name string) bool {
	f.mu.Lock()
	fn := f.preferred
	f.mu.Unlock()
	if fn == nil {
		return false
	}
	_, ok := fn(strings.ToLower(strings.TrimSpace(name)))
	return ok
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

// aliases are other names the same program ships under: 7-Zip's console
// build is 7zz on macOS/Linux and 7za or 7z on Windows; Ghostscript is
// gswin64c on Windows.
var aliases = map[string][]string{
	"7zz": {"7z", "7za"},
	"gs":  {"gswin64c"},
}

func (f *Finder) resolve(name string) string {
	for _, n := range append([]string{name}, aliases[name]...) {
		if p := f.resolveOne(n); p != "" {
			return p
		}
	}
	return ""
}

func (f *Finder) resolveOne(name string) string {
	f.mu.Lock()
	pref := f.preferred
	f.mu.Unlock()
	if pref != nil {
		if p, ok := pref(name); ok {
			return p
		}
	}
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
		dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", filepath.Join(home, "bin")}
		// Apps installed the usual way keep their command-line tools inside.
		for _, app := range []string{"LibreOffice.app/Contents/MacOS", "calibre.app/Contents/MacOS", "Inkscape.app/Contents/MacOS"} {
			dirs = append(dirs, filepath.Join("/Applications", app), filepath.Join(home, "Applications", app))
		}
		return dirs
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
		for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA") + `\Programs`} {
			if pf == "" {
				continue
			}
			dirs = append(dirs,
				filepath.Join(pf, "ffmpeg", "bin"),
				filepath.Join(pf, "LibreOffice", "program"),
				filepath.Join(pf, "Calibre2"),
				filepath.Join(pf, "Pandoc"),
				filepath.Join(pf, "7-Zip"),
				filepath.Join(pf, "Tesseract-OCR"),
				filepath.Join(pf, "Inkscape", "bin"))
			for _, pattern := range []string{`gs\gs*\bin`, `ImageMagick-*`} {
				found, _ := filepath.Glob(filepath.Join(pf, pattern))
				dirs = append(dirs, found...)
			}
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
