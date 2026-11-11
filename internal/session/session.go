// Package session tracks the files in the tray and the chain of skills
// applied to each one.
//
// Originals are only ever read. Every applied step writes a new, immutable
// output inside the session workspace, so removing a step can replay the
// rest of the chain from the previous output and nothing is ever written
// next to the user's files until they export.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"asteria/internal/inspect"
	"asteria/internal/skills"
)

// Step is one output in a file's chain. Index -1 is the original.
type Step struct {
	ID      string         `json:"id"`
	SkillID string         `json:"skillId"`
	Label   string         `json:"label"`
	Params  map[string]any `json:"params,omitempty"`
	Path    string         `json:"-"`
	Type    string         `json:"type"` // ".jpg", "folder", ...
	Size    int64          `json:"size"`
	Info    inspect.Info   `json:"info"`
}

// IsDir reports whether the step produced a folder.
func (s Step) IsDir() bool { return s.Type == skills.OutputFolder }

// File is one item in the tray.
type File struct {
	mu sync.Mutex
	id string
	// name is the display name without extension.
	name     string
	original Step
	// hasOriginal is false for files created by combine skills.
	hasOriginal bool
	originPath  string
	sources     []string
	sourceDir   string
	steps       []Step
	dir         string
	seq         int
}

// View is the JSON shape the UI renders.
type View struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	DisplayName  string       `json:"displayName"`
	Type         string       `json:"type"`
	Kind         string       `json:"kind"`
	Origin       string       `json:"origin"`
	Sources      []string     `json:"sources,omitempty"`
	OriginalPath string       `json:"originalPath"`
	Size         int64        `json:"size"`
	OriginalSize int64        `json:"originalSize"`
	Info         inspect.Info `json:"info"`
	OriginalInfo inspect.Info `json:"originalInfo"`
	Steps        []Step       `json:"steps"`
	Rev          string       `json:"rev"`
}

// ID returns the file's id.
func (f *File) ID() string { return f.id }

// Current returns the newest output (or the original).
func (f *File) Current() Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.currentLocked()
}

func (f *File) currentLocked() Step {
	if len(f.steps) > 0 {
		return f.steps[len(f.steps)-1]
	}
	return f.original
}

// DisplayName is the name the current output would be exported under.
func (f *File) DisplayName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return displayName(f.name, f.currentLocked().Type)
}

func displayName(name, typ string) string {
	if typ == skills.OutputFolder || typ == "" {
		return name
	}
	return name + typ
}

// Steps returns a copy of the applied steps.
func (f *File) Steps() []Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Step{}, f.steps...)
}

// Combined reports whether the file was produced by a combine skill.
func (f *File) Combined() bool { return !f.hasOriginal }

// Original returns the original step (the source file).
func (f *File) Original() Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.original
}

// NewOutputPath reserves a unique path in the file's workspace for a step
// output of the given type.
func (f *File) NewOutputPath(skillID, typ string) string {
	f.mu.Lock()
	f.seq++
	n := f.seq
	f.mu.Unlock()
	base := fmt.Sprintf("%03d-%s", n, safeToken(skillID))
	if typ == skills.OutputFolder {
		return filepath.Join(f.dir, base)
	}
	return filepath.Join(f.dir, base+typ)
}

// Dir is the file's private workspace directory.
func (f *File) Dir() string { return f.dir }

// Append adds a completed step.
func (f *File) Append(s Step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, s)
}

// ReplaceSteps swaps the chain and deletes outputs no longer referenced.
func (f *File) ReplaceSteps(steps []Step) {
	f.mu.Lock()
	old := f.steps
	f.steps = append([]Step{}, steps...)
	keep := map[string]bool{}
	for _, s := range f.steps {
		keep[s.Path] = true
	}
	f.mu.Unlock()
	for _, s := range old {
		if !keep[s.Path] {
			_ = os.RemoveAll(s.Path)
		}
	}
}

// PopStep removes the last step if its id matches.
func (f *File) PopStep(stepID string) bool {
	f.mu.Lock()
	if len(f.steps) == 0 || f.steps[len(f.steps)-1].ID != stepID {
		f.mu.Unlock()
		return false
	}
	last := f.steps[len(f.steps)-1]
	f.steps = f.steps[:len(f.steps)-1]
	f.mu.Unlock()
	_ = os.RemoveAll(last.Path)
	return true
}

// View renders the file for the UI.
func (f *File) View() View {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.currentLocked()
	v := View{
		ID:           f.id,
		Name:         f.name,
		DisplayName:  displayName(f.name, cur.Type),
		Type:         cur.Type,
		Kind:         inspect.Kind(cur.Type),
		Origin:       "added",
		Sources:      f.sources,
		OriginalPath: f.originPath,
		Size:         cur.Size,
		OriginalSize: f.original.Size,
		Info:         cur.Info,
		OriginalInfo: f.original.Info,
		Steps:        append([]Step{}, f.steps...),
		Rev:          cur.ID,
	}
	if !f.hasOriginal {
		v.Origin = "combined"
		if len(f.steps) > 0 {
			v.OriginalSize = 0
			v.OriginalInfo = inspect.Info{}
		}
	}
	return v
}

// Store holds the tray.
type Store struct {
	mu    sync.RWMutex
	files map[string]*File
	order []string
	ws    *Workspace
}

// NewStore creates a store backed by the workspace.
func NewStore(ws *Workspace) *Store {
	return &Store{files: map[string]*File{}, ws: ws}
}

// Workspace returns the store's workspace.
func (s *Store) Workspace() *Workspace { return s.ws }

func newStepID() string { return uuid.NewString()[:8] }

// Add registers an existing file or folder on disk. It is never copied:
// the original is only read when a skill runs.
func (s *Store) Add(path string, describe func(path, typ string) inspect.Info) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	typ := skills.FileType(abs, info.IsDir())
	base := filepath.Base(abs)
	name := base
	if typ != skills.OutputFolder {
		name = base[:len(base)-len(typ)]
		if name == "" {
			name = base
		}
	}
	id := uuid.NewString()
	dir, err := s.ws.FileDir(id)
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if info.IsDir() {
		size = 0
	}
	f := &File{
		id:          id,
		name:        name,
		hasOriginal: true,
		originPath:  abs,
		sourceDir:   filepath.Dir(abs),
		dir:         dir,
		original:    Step{ID: "original", Path: abs, Type: typ, Size: size},
	}
	if describe != nil {
		f.original.Info = describe(abs, typ)
	}
	s.mu.Lock()
	s.files[id] = f
	s.order = append(s.order, id)
	s.mu.Unlock()
	return f, nil
}

// HasPath reports whether a file with this original path is already in the tray.
func (s *Store) HasPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.files {
		if f.hasOriginal && f.originPath == abs {
			return true
		}
	}
	return false
}

// NewCombined creates an empty file that a combine skill will fill. It is
// not visible until Publish is called.
func (s *Store) NewCombined(name string, sources []*File) (*File, error) {
	id := uuid.NewString()
	dir, err := s.ws.FileDir(id)
	if err != nil {
		return nil, err
	}
	f := &File{id: id, name: name, dir: dir}
	for _, src := range sources {
		f.sources = append(f.sources, src.DisplayName())
		if f.sourceDir == "" {
			f.sourceDir = src.sourceDir
		}
	}
	return f, nil
}

// Publish adds a combined file to the tray after the given file id (or at
// the end).
func (s *Store) Publish(f *File, after string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[f.id] = f
	pos := len(s.order)
	for i, id := range s.order {
		if id == after {
			pos = i + 1
		}
	}
	s.order = append(s.order[:pos], append([]string{f.id}, s.order[pos:]...)...)
}

// Discard deletes an unpublished file's workspace.
func (s *Store) Discard(f *File) { _ = os.RemoveAll(f.dir) }

// Get returns a file by id.
func (s *Store) Get(id string) (*File, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[id]
	return f, ok
}

// Files returns the tray in order.
func (s *Store) Files() []*File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*File, 0, len(s.order))
	for _, id := range s.order {
		if f, ok := s.files[id]; ok {
			out = append(out, f)
		}
	}
	return out
}

// Views renders every file.
func (s *Store) Views() []View {
	files := s.Files()
	out := make([]View, len(files))
	for i, f := range files {
		out[i] = f.View()
	}
	return out
}

// Remove drops files from the tray and deletes their workspace data.
func (s *Store) Remove(ids []string) {
	s.mu.Lock()
	drop := map[string]bool{}
	dirs := []string{}
	for _, id := range ids {
		if f, ok := s.files[id]; ok {
			drop[id] = true
			dirs = append(dirs, f.dir)
			delete(s.files, id)
		}
	}
	order := s.order[:0]
	for _, id := range s.order {
		if !drop[id] {
			order = append(order, id)
		}
	}
	s.order = order
	s.mu.Unlock()
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
}

// Clear empties the tray.
func (s *Store) Clear() {
	s.mu.Lock()
	ids := append([]string{}, s.order...)
	s.mu.Unlock()
	s.Remove(ids)
}

// NewStep builds a step record for a finished output.
func NewStep(skill skills.Skill, params map[string]any, path, typ string, info inspect.Info) Step {
	size := int64(0)
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() {
			size = dirSize(path)
		} else {
			size = st.Size()
		}
	}
	return Step{
		ID:      newStepID(),
		SkillID: skill.ID,
		Label:   Label(skill, params),
		Params:  params,
		Path:    path,
		Type:    typ,
		Size:    size,
		Info:    info,
	}
}

// Label is the chip text, e.g. "Resize 50%".
func Label(skill skills.Skill, params map[string]any) string {
	label := skill.Name
	if len(skill.Params) == 1 {
		p := skill.Params[0]
		v, ok := params[p.Name]
		if !ok || v == nil || fmt.Sprint(v) == "" {
			v = p.Default
		}
		if v != nil && fmt.Sprint(v) != "" {
			val := fmt.Sprint(v)
			if p.Unit != "" && !strings.HasSuffix(val, p.Unit) {
				val += p.Unit
			}
			label += " " + val
		}
	}
	return label
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func safeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "step"
	}
	return b.String()
}

// Now is overridable in tests.
var Now = time.Now
