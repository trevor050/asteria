package addons

import (
	"asteria/internal/tools"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// UserError is a message meant to be shown as is.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userf(format string, a ...any) error { return &UserError{Msg: fmt.Sprintf(format, a...)} }

// Progress reports an install: phase is "download", "verify", "unpack" or
// "check"; done and total are bytes for the download, otherwise 0.
type Progress struct {
	Engine string  `json:"engine"`
	Phase  string  `json:"phase"`
	Done   int64   `json:"done"`
	Total  int64   `json:"total"`
	Frac   float64 `json:"fraction"`
}

// Installed is written next to an engine once it is ready.
type Installed struct {
	Version     string    `json:"version"`
	Platform    string    `json:"platform"`
	SHA256      string    `json:"sha256"`
	InstalledAt time.Time `json:"installedAt"`
	Bytes       int64     `json:"bytes"`
}

// Manager installs and removes engines under Root.
type Manager struct {
	Root     string
	Manifest *Manifest
	Client   *http.Client
	// FreeSpace reports free bytes at a path; replaced in tests.
	FreeSpace func(path string) (uint64, error)
	// CheckRuns proves the unpacked tools start; replaced in tests.
	CheckRuns func(ctx context.Context, dir string, a Artifact) error

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// NewManager returns a manager rooted at dir (created on first install).
func NewManager(root string, m *Manifest) *Manager {
	return &Manager{
		Root:      root,
		Manifest:  m,
		Client:    &http.Client{Timeout: 0, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 20 * time.Second, ResponseHeaderTimeout: 30 * time.Second}},
		FreeSpace: freeSpace,
		CheckRuns: checkRuns,
		running:   map[string]context.CancelFunc{},
	}
}

func (m *Manager) dir(id string) string { return filepath.Join(m.Root, id) }

// Installed returns the record of an installed engine.
func (m *Manager) Installed(id string) (Installed, bool) {
	b, err := os.ReadFile(filepath.Join(m.dir(id), "installed.json"))
	if err != nil {
		return Installed{}, false
	}
	var in Installed
	if json.Unmarshal(b, &in) != nil {
		return Installed{}, false
	}
	return in, true
}

// BinPath returns the installed path of a tool from an installed engine.
func (m *Manager) BinPath(tool string) (string, bool) {
	e, ok := m.Manifest.ForTool(tool)
	if !ok {
		return "", false
	}
	if _, ok := m.Installed(e.ID); !ok {
		return "", false
	}
	a, ok := e.Artifact()
	if !ok || a.Manual != nil {
		return "", false
	}
	rel, ok := a.Bins[strings.TrimSuffix(strings.ToLower(tool), ".exe")]
	if !ok {
		return "", false
	}
	p := filepath.Join(m.dir(e.ID), filepath.FromSlash(rel))
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

// Cancel stops an install in progress; the partial download is removed.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	cancel := m.running[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ErrCancelled is returned when the user cancels an install.
var ErrCancelled = errors.New("cancelled")

// Install downloads, verifies and unpacks an engine. It is safe to call
// again after a failure: an interrupted download resumes.
func (m *Manager) Install(parent context.Context, id string, report func(Progress)) (err error) {
	e, ok := m.Manifest.Engine(id)
	if !ok {
		return userf("Unknown add-on %q", id)
	}
	a, ok := e.Artifact()
	if !ok {
		return userf("%s isn't available for this computer (%s)", e.Name, Platform())
	}
	if a.Manual != nil {
		return userf("%s can't be added automatically on this computer. %s", e.Name, a.Manual.Why)
	}
	if !strings.HasPrefix(a.URL, "https://") {
		return userf("refusing to download %s over an insecure connection", e.Name)
	}
	if report == nil {
		report = func(Progress) {}
	}
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	if m.running[id] != nil {
		m.mu.Unlock()
		cancel()
		return userf("%s is already being added", e.Name)
	}
	m.running[id] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
		cancel()
	}()

	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return err
	}
	need := uint64(a.Size) + uint64(a.UnpackedMB)*1_100_000
	if free, ferr := m.FreeSpace(m.Root); ferr == nil && free < need {
		return userf("Not enough free space for %s: it needs about %s and %s is free.", e.Name, human(int64(need)), human(int64(free)))
	}

	dl := filepath.Join(m.Root, ".downloads")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return err
	}
	part := filepath.Join(dl, fmt.Sprintf("%s-%s-%s.part", e.ID, a.Version, Platform()))
	if err := m.download(ctx, a, part, func(done int64) {
		report(Progress{Engine: id, Phase: "download", Done: done, Total: a.Size, Frac: float64(done) / float64(a.Size)})
	}); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_ = os.Remove(part)
			return ErrCancelled
		}
		return err // the partial file is kept so a retry resumes
	}

	report(Progress{Engine: id, Phase: "verify", Frac: 1})
	sum, err := fileSHA256(part)
	if err != nil {
		return err
	}
	if a.SHA1Published != "" {
		if s1, err := fileSHA1(part); err != nil || !strings.EqualFold(s1, a.SHA1Published) {
			sum = "mismatch"
		}
	}
	if !strings.EqualFold(sum, a.SHA256) {
		_ = os.Remove(part)
		return userf("The %s download didn't match its published checksum, so it was deleted and nothing was installed. Please try again later.", e.Name)
	}

	report(Progress{Engine: id, Phase: "unpack"})
	staging := filepath.Join(m.Root, ".staging", e.ID+"-"+randHex())
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := unpack(ctx, a, part, staging); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ErrCancelled
		}
		return fmt.Errorf("unpacking %s: %w", e.Name, err)
	}
	for tool, rel := range a.Bins {
		p := filepath.Join(staging, filepath.FromSlash(rel))
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return userf("%s was downloaded but %s is missing from it; nothing was installed.", e.Name, tool)
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(p, st.Mode()|0o111)
		}
	}

	report(Progress{Engine: id, Phase: "check"})
	if err := m.CheckRuns(ctx, staging, a); err != nil {
		return userf("%s was downloaded but doesn't run on this computer (%v); nothing was installed.", e.Name, err)
	}
	size, _ := dirSize(staging)
	rec := Installed{Version: a.Version, Platform: Platform(), SHA256: a.SHA256, InstalledAt: time.Now().UTC(), Bytes: size}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(staging, "installed.json"), b, 0o644); err != nil {
		return err
	}
	final := m.dir(id)
	old := final + ".old-" + randHex()
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return err
		}
		defer os.RemoveAll(old)
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.Rename(old, final)
		return err
	}
	_ = os.Remove(part)
	return nil
}

// Remove deletes an installed engine and returns the bytes freed.
func (m *Manager) Remove(id string) (int64, error) {
	if _, ok := m.Manifest.Engine(id); !ok {
		return 0, userf("Unknown add-on %q", id)
	}
	d := m.dir(id)
	size, _ := dirSize(d)
	if err := os.RemoveAll(d); err != nil {
		return 0, err
	}
	parts, _ := filepath.Glob(filepath.Join(m.Root, ".downloads", id+"-*.part"))
	for _, p := range parts {
		_ = os.Remove(p)
	}
	return size, nil
}

// EngineBytes is the disk space one installed engine takes.
func (m *Manager) EngineBytes(id string) int64 {
	n, _ := dirSize(m.dir(id))
	return n
}

// UsedBytes is the disk space taken by installed engines.
func (m *Manager) UsedBytes() int64 {
	var total int64
	for _, e := range m.Manifest.Engines {
		if _, ok := m.Installed(e.ID); ok {
			n, _ := dirSize(m.dir(e.ID))
			total += n
		}
	}
	return total
}

// download fetches a.URL into part, resuming when part already holds the
// beginning of the file and the server supports ranges.
func (m *Manager) download(ctx context.Context, a Artifact, part string, progress func(int64)) error {
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
		if have > a.Size {
			_ = os.Remove(part)
			have = 0
		}
	}
	if have == a.Size {
		progress(have)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Asteria add-on installer")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return networkError(a.URL, err)
	}
	defer resp.Body.Close()
	// Redirects must stay on https (GitHub and SourceForge redirect to CDNs).
	if resp.Request != nil && resp.Request.URL.Scheme != "https" {
		return userf("refusing a download that was redirected to an insecure address")
	}
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		have = 0
		flags |= os.O_TRUNC
	case http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return userf("The download was interrupted in a way that can't be resumed; please try again.")
	default:
		return userf("The download server answered %s. Please try again later.", resp.Status)
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	limit := a.Size - have + 1 // never write more than the pinned size
	buf := make([]byte, 256<<10)
	done := have
	last := time.Time{}
	r := io.LimitReader(resp.Body, limit)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if done > a.Size {
				_ = f.Close()
				_ = os.Remove(part)
				return userf("The download was larger than expected, so it was deleted.")
			}
			if time.Since(last) > 100*time.Millisecond {
				progress(done)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return networkError(a.URL, rerr)
		}
	}
	progress(done)
	if done != a.Size {
		return userf("The download stopped early (%s of %s). Try again to resume it.", human(done), human(a.Size))
	}
	return nil
}

func networkError(raw string, err error) error {
	host := raw
	if u, perr := url.Parse(raw); perr == nil {
		host = u.Host
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) || strings.Contains(err.Error(), "no such host") {
		return userf("Couldn't reach %s. Check your internet connection and try again.", host)
	}
	return userf("The download from %s failed (%v). Try again to resume it.", host, err)
}

func fileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkRuns runs each tool with a version flag to prove it works here
// (missing libraries, wrong architecture).
func checkRuns(ctx context.Context, dir string, a Artifact) error {
	for tool, rel := range a.Bins {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		flag := "--version"
		switch tool {
		case "ffmpeg", "ffprobe":
			flag = "-version"
		case "7zz", "7za", "7z":
			flag = "i"
		case "gs", "gswin64c":
			flag = "--version"
		}
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		cmd := tools.Command(c, p, flag)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil && len(out) == 0 {
			return fmt.Errorf("%s: %v", tool, err)
		}
	}
	return nil
}

func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n, err
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", (n+(1<<19))>>20)
	default:
		return fmt.Sprintf("%d KB", (n+512)>>10)
	}
}
