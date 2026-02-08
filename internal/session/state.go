package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type FileState struct {
	mu        sync.Mutex
	data      WorkingFile
	basePath  string
	snapshots []string
}

type State struct {
	mu            sync.RWMutex
	files         map[string]*FileState
	order         []string
	workspace     *Workspace
	mode          Mode
	outputFolder  string
	namingPattern string
	accentColor   string
}

func NewState(snapshot SessionSnapshot) (*State, error) {
	workspace, err := NewWorkspace()
	if err != nil {
		return nil, err
	}
	mode := snapshot.Mode
	if mode == "" {
		mode = ModeBatch
	}
	naming := snapshot.NamingPattern
	if strings.TrimSpace(naming) == "" {
		naming = "{name}_{skill}.{ext}"
	}
	accent := snapshot.AccentColor
	if strings.TrimSpace(accent) == "" {
		accent = "99,102,241"
	}
	return &State{
		files:         make(map[string]*FileState),
		order:         []string{},
		workspace:     workspace,
		mode:          mode,
		outputFolder:  snapshot.OutputFolder,
		namingPattern: naming,
		accentColor:   accent,
	}, nil
}

func (s *State) AccentColor() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accentColor
}

func (s *State) SetAccentColor(color string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(color) == "" {
		return
	}
	s.accentColor = color
}

func (s *State) AddFile(path string) (WorkingFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return WorkingFile{}, err
	}
	id := uuid.NewString()
	ext := strings.ToLower(filepath.Ext(path))
	name := strings.TrimSuffix(filepath.Base(path), ext)
	fileDir, err := s.workspace.EnsureFileDir(id)
	if err != nil {
		return WorkingFile{}, err
	}
	basePath := filepath.Join(fileDir, "base"+ext)
	currentPath := filepath.Join(fileDir, "current"+ext)
	if err := CopyFile(path, basePath); err != nil {
		return WorkingFile{}, err
	}
	if err := CopyFile(path, currentPath); err != nil {
		return WorkingFile{}, err
	}
	wf := WorkingFile{
		ID:               id,
		Name:             name,
		Extension:        ext,
		CurrentExtension: ext,
		OriginalPath:     path,
		WorkingPath:      currentPath,
		Size:             info.Size(),
		PreviewDataURL:   "",
		AppliedSkills:    []AppliedSkill{},
	}
	state := &FileState{
		data:      wf,
		basePath:  basePath,
		snapshots: []string{},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[id] = state
	s.order = append(s.order, id)
	return wf, nil
}

func (s *State) GetFile(id string) (*FileState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, ok := s.files[id]
	return file, ok
}

func (s *State) ListFiles() []WorkingFile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	files := make([]WorkingFile, 0, len(s.order))
	for _, id := range s.order {
		if file, ok := s.files[id]; ok {
			file.mu.Lock()
			files = append(files, file.data)
			file.mu.Unlock()
		}
	}
	return files
}

func (s *State) Mode() Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *State) SetMode(mode Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
}

func (s *State) OutputFolder() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.outputFolder
}

func (s *State) SetOutputFolder(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputFolder = path
}

func (s *State) NamingPattern() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.namingPattern
}

func (s *State) SetNamingPattern(pattern string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(pattern) == "" {
		return
	}
	s.namingPattern = pattern
}

func (s *State) Snapshot() SessionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SessionSnapshot{
		Mode:          s.mode,
		OutputFolder:  s.outputFolder,
		NamingPattern: s.namingPattern,
		AccentColor:   s.accentColor,
	}
}

func (s *State) Workspace() *Workspace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspace
}

func (s *State) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = make(map[string]*FileState)
	s.order = []string{}
	s.mode = ModeBatch
	s.outputFolder = ""
	s.namingPattern = "{name}_{skill}.{ext}"
	return s.workspace.Reset()
}

func (f *FileState) BasePath() string {
	return f.basePath
}

func (f *FileState) CurrentPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data.WorkingPath
}

func (f *FileState) SetCurrentPath(path string, ext string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.WorkingPath = path
	f.data.CurrentExtension = ext
	f.data.Size = size
}

func (f *FileState) SetPreview(preview string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.PreviewDataURL = preview
}

func (f *FileState) AppendApplied(skill AppliedSkill) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.AppliedSkills = append(f.data.AppliedSkills, skill)
}

func (f *FileState) ReplaceApplied(skills []AppliedSkill) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data.AppliedSkills = skills
}

func (f *FileState) SnapshotPath(workspace *Workspace, index int, ext string) string {
	return workspace.SnapshotPath(f.data.ID, index, ext)
}

func (f *FileState) AddSnapshot(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, path)
}

func (f *FileState) TrimSnapshots(index int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index > len(f.snapshots) {
		f.snapshots = []string{}
		return
	}
	f.snapshots = append([]string{}, f.snapshots[:index]...)
}

func (f *FileState) SnapshotAt(index int) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.snapshots) {
		return "", false
	}
	return f.snapshots[index], true
}

func (f *FileState) SnapshotsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapshots)
}

func (f *FileState) SetSnapshot(index int, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 {
		return
	}
	if index < len(f.snapshots) {
		f.snapshots[index] = path
		return
	}
	for len(f.snapshots) < index {
		f.snapshots = append(f.snapshots, "")
	}
	f.snapshots = append(f.snapshots, path)
}

func (f *FileState) Data() WorkingFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *FileState) AppliedSkills() []AppliedSkill {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make([]AppliedSkill, len(f.data.AppliedSkills))
	copy(copied, f.data.AppliedSkills)
	return copied
}

func NewAppliedSkill(skillID string, params map[string]any) AppliedSkill {
	return AppliedSkill{
		SkillID:   skillID,
		Params:    params,
		AppliedAt: time.Now().Format(time.RFC3339),
	}
}

func ExportName(pattern string, name string, ext string, skill string) string {
	if strings.TrimSpace(pattern) == "" {
		pattern = "{name}_{skill}.{ext}"
	}
	sanitizedSkill := strings.ReplaceAll(skill, " ", "_")
	sanitizedSkill = strings.ReplaceAll(sanitizedSkill, "-", "_")
	out := strings.ReplaceAll(pattern, "{name}", name)
	out = strings.ReplaceAll(out, "{ext}", strings.TrimPrefix(ext, "."))
	out = strings.ReplaceAll(out, "{skill}", sanitizedSkill)
	if !strings.Contains(out, ".") {
		out = fmt.Sprintf("%s.%s", out, strings.TrimPrefix(ext, "."))
	}
	return out
}
