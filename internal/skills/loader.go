package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type LoaderOptions struct {
	// EmbeddedFS should contain EmbeddedRoot (for shipped core skills).
	EmbeddedFS   fs.FS
	EmbeddedRoot string

	// DiskCoreRoot is optional and is used during development to allow hot reload
	// of core skills without rebuilding the binary.
	DiskCoreRoot string

	// CommunityRoot is an on-disk directory that users can add skills/packs to.
	CommunityRoot string
}

type Loader struct {
	opts LoaderOptions

	mu      sync.RWMutex
	skills  map[string]Skill
	lastErr error

	watchMu sync.Mutex
	watcher *fsnotify.Watcher

	changed chan struct{}
}

func NewLoader(opts LoaderOptions) *Loader {
	return &Loader{
		opts:    opts,
		skills:  make(map[string]Skill),
		changed: make(chan struct{}, 1),
	}
}

func (l *Loader) Changes() <-chan struct{} {
	return l.changed
}

func (l *Loader) LastError() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lastErr
}

func (l *Loader) List() []Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Skill, 0, len(l.skills))
	for _, s := range l.skills {
		out = append(out, s)
	}
	return out
}

func (l *Loader) GetByID(id string) (Skill, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.skills[id]
	return s, ok
}

// LoadAll loads skills from embedded core, disk core (optional), and community.
// Precedence: embedded < disk core < community.
func (l *Loader) LoadAll() error {
	merged := make(map[string]Skill)
	var errOut error

	if l.opts.EmbeddedFS != nil && strings.TrimSpace(l.opts.EmbeddedRoot) != "" {
		sub, err := fs.Sub(l.opts.EmbeddedFS, filepath.ToSlash(l.opts.EmbeddedRoot))
		if err != nil {
			errOut = joinErr(errOut, fmt.Errorf("skills: invalid embedded root %q: %w", l.opts.EmbeddedRoot, err))
		} else {
			skills, err := l.collectFromFS(sub, SkillSourceCoreEmbedded)
			if err != nil {
				errOut = joinErr(errOut, err)
			}
			l.mergeInto(merged, skills)
		}
	}

	// Disk core (dev override)
	if root := strings.TrimSpace(l.opts.DiskCoreRoot); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			skills, err := l.collectFromDisk(root, SkillSourceCoreDisk)
			if err != nil {
				errOut = joinErr(errOut, err)
			}
			l.mergeInto(merged, skills)
		}
	}

	// Community
	if root := strings.TrimSpace(l.opts.CommunityRoot); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			skills, err := l.collectFromDisk(root, SkillSourceCommunity)
			if err != nil {
				errOut = joinErr(errOut, err)
			}
			l.mergeInto(merged, skills)
		}
	}

	// Normalize and basic validation.
	for id, s := range merged {
		s.Permissions = NormalizePermissions(s.Permissions)
		merged[id] = s
	}

	l.mu.Lock()
	l.skills = merged
	l.lastErr = errOut
	l.mu.Unlock()

	select {
	case l.changed <- struct{}{}:
	default:
	}

	return errOut
}

func (l *Loader) mergeInto(dst map[string]Skill, src map[string]Skill) {
	for k, v := range src {
		dst[k] = v
	}
}

// Watch starts hot reloading for on-disk directories (disk core + community).
func (l *Loader) Watch(ctx context.Context) error {
	l.watchMu.Lock()
	defer l.watchMu.Unlock()
	if l.watcher != nil {
		return nil
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	l.watcher = w

	addRoots := []string{}
	if root := strings.TrimSpace(l.opts.DiskCoreRoot); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			addRoots = append(addRoots, root)
		}
	}
	if root := strings.TrimSpace(l.opts.CommunityRoot); root != "" {
		if err := os.MkdirAll(root, 0o755); err == nil {
			addRoots = append(addRoots, root)
		}
	}

	for _, root := range addRoots {
		_ = watchRecursive(w, root)
	}

	// Debounce reloads.
	var (
		debounceMu sync.Mutex
		debounce   *time.Timer
	)
	scheduleReload := func() {
		debounceMu.Lock()
		defer debounceMu.Unlock()
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(150*time.Millisecond, func() {
			_ = l.LoadAll()
		})
	}

	go func() {
		defer func() {
			l.watchMu.Lock()
			_ = l.watcher.Close()
			l.watcher = nil
			l.watchMu.Unlock()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-w.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Create) != 0 {
					// If a new directory is created, watch it.
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = watchRecursive(w, event.Name)
					}
				}
				if isWatchableJSONFile(event.Name) {
					scheduleReload()
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				// Ignore watcher errors for now; they will surface as missing hot reload.
			}
		}
	}()

	return nil
}

func watchRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		_ = w.Add(path)
		return nil
	})
}

func isWatchableJSONFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

func isSkillJSONFilename(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".json") {
		return false
	}
	if strings.HasPrefix(lower, ".") {
		return false
	}
	// Reserved files for future pack infrastructure.
	if lower == "manifest.json" || lower == "pack.json" {
		return false
	}
	// Convention: files starting with '_' are metadata, not skills.
	if strings.HasPrefix(lower, "_") {
		return false
	}
	return true
}

func (l *Loader) collectFromFS(fsys fs.FS, source SkillSource) (map[string]Skill, error) {
	loaded := make(map[string]Skill)
	var errOut error
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errOut = joinErr(errOut, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isSkillJSONFilename(d.Name()) {
			return nil
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			errOut = joinErr(errOut, err)
			return nil
		}
		var s Skill
		if err := json.Unmarshal(b, &s); err != nil {
			errOut = joinErr(errOut, fmt.Errorf("skills: parse %s: %w", path, err))
			return nil
		}
		s.Source = source
		s.DefinitionPath = path
		norm, err := normalizeSkill(s)
		if err == nil {
			loaded[norm.ID] = norm
		} else {
			errOut = joinErr(errOut, fmt.Errorf("skills: invalid %s: %w", path, err))
		}
		return nil
	})
	return loaded, errOut
}

func (l *Loader) collectFromDisk(root string, source SkillSource) (map[string]Skill, error) {
	loaded := make(map[string]Skill)
	var errOut error
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errOut = joinErr(errOut, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isSkillJSONFilename(d.Name()) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			errOut = joinErr(errOut, err)
			return nil
		}
		var s Skill
		if err := json.Unmarshal(b, &s); err != nil {
			errOut = joinErr(errOut, fmt.Errorf("skills: parse %s: %w", path, err))
			return nil
		}
		s.Source = source
		s.DefinitionPath = path
		norm, err := normalizeSkill(s)
		if err != nil {
			errOut = joinErr(errOut, fmt.Errorf("skills: invalid %s: %w", path, err))
			return nil
		}
		loaded[norm.ID] = norm
		return nil
	})
	return loaded, errOut
}

func normalizeSkill(s Skill) (Skill, error) {
	if strings.TrimSpace(s.ID) == "" {
		return Skill{}, fmt.Errorf("missing id")
	}
	if strings.TrimSpace(s.Name) == "" {
		return Skill{}, fmt.Errorf("missing name")
	}
	if strings.TrimSpace(s.Version) == "" {
		return Skill{}, fmt.Errorf("missing version")
	}
	// Keep defaults safe.
	if strings.TrimSpace(s.Executor.Type) != "" {
		// Infer driver when omitted.
		switch strings.ToLower(strings.TrimSpace(s.Executor.Type)) {
		case "cli":
			if s.Driver == "" {
				s.Driver = "cli"
			}
		case "pipeline":
			if s.Driver == "" {
				s.Driver = "pipeline"
			}
		case "meta":
			if s.Driver == "" {
				s.Driver = "meta"
			}
		}
	}
	if s.Driver == "" {
		s.Driver = "meta"
	}
	if s.Params == nil {
		s.Params = nil
	}
	s.Permissions = NormalizePermissions(s.Permissions)
	return s, nil
}

func joinErr(existing error, next error) error {
	if next == nil {
		return existing
	}
	if existing == nil {
		return next
	}
	return fmt.Errorf("%v; %w", existing, next)
}
