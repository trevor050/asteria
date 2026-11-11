package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"asteria/internal/addons"
	"asteria/internal/engine"
	"asteria/internal/inspect"
	"asteria/internal/ops"
	"asteria/internal/session"
	"asteria/internal/skills"
	"asteria/internal/storage"
	"asteria/internal/tools"
)

const (
	eventProgress      = "asteria:progress"
	eventFilesAdded    = "asteria:files-added"
	eventSkillsChanged = "asteria:skills-changed"
	maxFolderFiles     = 5000
)

// App is the service the frontend talks to.
type App struct {
	app    *application.App
	window application.Window

	registry *skills.Registry
	store    *session.Store
	engine   *engine.Engine
	tools    *tools.Finder
	settings *storage.SettingsStore
	usage    *storage.UsageStore
	trust    *storage.TrustStore

	skillsDir    string
	sessionsRoot string
	addons       *addons.Manager
	exportMu     sync.Mutex
}

// NewApp wires the backend together. It never touches the network.
func NewApp(embedded fs.FS) (*App, error) {
	settings, err := storage.NewSettingsStore()
	if err != nil {
		return nil, err
	}
	usage, err := storage.NewUsageStore()
	if err != nil {
		return nil, err
	}
	trust, err := storage.NewTrustStore()
	if err != nil {
		return nil, err
	}
	skillsDir, err := storage.SkillsDir()
	if err != nil {
		return nil, err
	}
	sessionsRoot, err := session.SessionsRoot()
	if err != nil {
		return nil, err
	}
	ws, err := session.NewWorkspace(sessionsRoot)
	if err != nil {
		return nil, err
	}

	// In a source checkout, edits to skills/core hot-reload without a rebuild.
	diskCore := ""
	if info, err := os.Stat(filepath.Join("skills", "core")); err == nil && info.IsDir() {
		diskCore = filepath.Join("skills", "core")
	}
	registry := skills.NewRegistry(skills.RegistryOptions{
		EmbeddedFS:    embedded,
		EmbeddedRoot:  "skills/core",
		DiskCoreRoot:  diskCore,
		CommunityRoot: skillsDir,
	})
	finder := tools.NewFinder()
	addonMgr, err := newAddons()
	if err != nil {
		return nil, err
	}
	finder.Prefer(addonMgr.BinPath)
	store := session.NewStore(ws)
	a := &App{
		registry:  registry,
		store:     store,
		tools:     finder,
		settings:  settings,
		usage:     usage,
		trust:     trust,
		skillsDir: skillsDir,
		addons:    addonMgr,
	}
	a.sessionsRoot = sessionsRoot
	a.engine = engine.New(engine.Options{
		Registry: registry,
		Store:    store,
		Tools:    finder,
		IsTrusted: func(id string) bool {
			ok, _ := trust.IsTrusted(id)
			return ok
		},
		OnUsed: func(id string) { _ = usage.Increment(id) },
	})
	return a, nil
}

// attach runs after application.New, i.e. after the single-instance check:
// a second launch has already handed its files over and exited by now.
func (a *App) attach(app *application.App, window application.Window) {
	a.app = app
	a.window = window
	session.CleanupStale(a.sessionsRoot, a.store.Workspace().Root)
	_ = a.registry.StartHotReload(app.Context())
	go func() {
		for range a.registry.Changes() {
			app.Event.Emit(eventSkillsChanged, a.registry.Problems())
		}
	}()
}

func (a *App) shutdown() {
	a.engine.Cancel()
	_ = a.store.Workspace().Close()
}

func (a *App) emit(name string, data any) {
	if a.app != nil {
		a.app.Event.Emit(name, data)
	}
}

// ---- state ----------------------------------------------------------------

// ToolStatus says whether an optional external tool was found.
type ToolStatus struct {
	Name  string `json:"name"`
	Found bool   `json:"found"`
	Path  string `json:"path"`
	Hint  string `json:"hint"`
}

// State is everything the UI needs on start.
type State struct {
	Files     []session.View   `json:"files"`
	Settings  storage.Settings `json:"settings"`
	SkillsDir string           `json:"skillsDir"`
	Problems  []string         `json:"problems"`
	Tools     []ToolStatus     `json:"tools"`
	Platform  string           `json:"platform"`
	Version   string           `json:"version"`
}

// GetState returns the tray, settings and environment.
func (a *App) GetState() State {
	settings, _ := a.settings.Load()
	applyWindowAppearance(a.window, settings.Appearance)
	a.tools.Reset()
	statuses := []ToolStatus{}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		p, ok := a.tools.Lookup(name)
		statuses = append(statuses, ToolStatus{Name: name, Found: ok, Path: p, Hint: tools.InstallHint(name)})
	}
	return State{
		Files:       a.store.Views(),
		Settings:    settings,
		SkillsDir:   a.skillsDir,
		Problems:    a.registry.Problems(),
		Tools:       statuses,
		Platform:    runtime.GOOS,
		Version:     Version,
	}
}

// ---- skills ---------------------------------------------------------------

// Suggestion is one command-bar result.
type Suggestion struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	IsMeta      bool              `json:"isMeta"`
	Combine     bool              `json:"combine"`
	OutputType  string            `json:"outputType"`
	Params      []skills.ParamDef `json:"params"`
	Value       string            `json:"value"`
	Applies     int               `json:"applies"`
	Missing     []string          `json:"missing"`
	// Addon names the add-on that provides the first missing tool.
	Addon     string `json:"addon"`
	Community bool   `json:"community"`
	// Live is true when the skill can preview on the fly while its value is
	// being adjusted (PreviewSkill).
	Live bool `json:"live"`
	// Icon is the skill's icon name (empty: the family default).
	Icon string `json:"icon"`
	// Matched is the alias the query matched, when the name did not.
	Matched string `json:"matched"`
	// Generic is true for skills that take any file (zip, checksum, file
	// info); the quick row prefers skills made for the file's type.
	Generic bool `json:"generic"`
}

// Search ranks skills for the query against the files skills would apply to.
func (a *App) Search(query string, fileIDs []string) []Suggestion {
	types := []string{}
	seen := map[string]bool{}
	for _, id := range fileIDs {
		if f, ok := a.store.Get(id); ok {
			t := f.Current().Type
			if !seen[t] {
				seen[t] = true
				types = append(types, t)
			}
		}
	}
	matches := a.registry.Search(query, skills.Context{InputTypes: types, Usage: a.usage.All()})
	out := make([]Suggestion, 0, len(matches))
	for _, m := range matches {
		s := m.Skill
		sug := Suggestion{
			ID: s.ID, Name: s.Name, Description: s.Description, Category: s.Category,
			IsMeta: s.IsMeta, Combine: s.Combine, OutputType: s.OutputType, Params: s.Params,
			Value: m.Value, Community: s.Source == skills.SkillSourceCommunity,
			Icon: s.Icon, Matched: m.Alias, Generic: slices.Contains(s.InputTypes, "*"),
		}
		if s.Executor.Type == "native" {
			if h, ok := ops.Lookup(s.Executor.Handler); ok {
				sug.Live = h.Live
			}
		}
		if !s.IsMeta {
			for _, id := range fileIDs {
				if f, ok := a.store.Get(id); ok && s.Accepts(f.Current().Type) {
					sug.Applies++
				}
			}
		}
		for _, tool := range s.Requires {
			if _, ok := a.tools.Lookup(tool); !ok {
				name := tool
				if e, ok := a.addons.Manifest.ForTool(tool); ok {
					name = e.Name
					if sug.Addon == "" {
						sug.Addon = e.ID
					}
				}
				if !slices.Contains(sug.Missing, name) {
					sug.Missing = append(sug.Missing, name)
				}
			}
		}
		out = append(out, sug)
	}
	return out
}

// ---- files ----------------------------------------------------------------

// AddResult reports what AddPaths did.
type AddResult struct {
	Added      []session.View `json:"added"`
	Duplicates int            `json:"duplicates"`
	Errors     []string       `json:"errors"`
	Truncated  bool           `json:"truncated"`
}

func hiddenName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "__MACOSX" || name == "Thumbs.db" || name == "desktop.ini"
}

// expand turns dropped folders into the files inside them.
func expand(paths []string) ([]string, bool, []string) {
	out := []string{}
	errs := []string{}
	truncated := false
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", filepath.Base(p), friendlyFSError(err)))
			continue
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if q != p && hiddenName(d.Name()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				if len(out) >= maxFolderFiles {
					truncated = true
					return fs.SkipAll
				}
				out = append(out, q)
			}
			return nil
		})
	}
	return out, truncated, errs
}

func friendlyFSError(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "file not found"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	return err.Error()
}

// AddPaths adds files (folders are expanded) to the tray.
func (a *App) AddPaths(paths []string) AddResult {
	files, truncated, errs := expand(paths)
	res := AddResult{Errors: errs, Truncated: truncated, Added: []session.View{}}
	todo := []string{}
	seen := map[string]bool{}
	for _, p := range files {
		if a.store.HasPath(p) || seen[p] {
			res.Duplicates++
			continue
		}
		seen[p] = true
		todo = append(todo, p)
	}
	// Describing files (dimensions, page counts, durations) is I/O bound;
	// do it in parallel but add to the tray in the order given.
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	infos := make([]inspect.Info, len(todo))
	for i, p := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			infos[i] = inspect.Describe(context.Background(), a.tools, p, skills.FileType(p, false))
		}(i, p)
	}
	wg.Wait()
	for i, p := range todo {
		// Earlier entries from this drop may refer to the same file with
		// different casing on Windows or a case-insensitive macOS volume.
		if a.store.HasPath(p) {
			res.Duplicates++
			continue
		}
		info := infos[i]
		f, err := a.store.Add(p, func(string, string) inspect.Info { return info })
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %s", filepath.Base(p), friendlyFSError(err)))
			continue
		}
		res.Added = append(res.Added, f.View())
	}
	return res
}

// PickFiles opens the native file picker and adds the selection.
func (a *App) PickFiles() (AddResult, error) {
	if a.app == nil {
		return AddResult{}, errors.New("file picker unavailable")
	}
	// Windows switches to a folder-only dialog when directories are enabled.
	// Keep its file picker usable; folders can still be dropped into the tray.
	paths, err := a.app.Dialog.OpenFile().
		SetTitle("Add files to Asteria").
		CanChooseFiles(true).
		CanChooseDirectories(runtime.GOOS != "windows").
		AttachToWindow(a.window).
		PromptForMultipleSelection()
	if err != nil {
		// Closing the dialog is not an error (Windows reports it as one).
		if strings.Contains(strings.ToLower(err.Error()), "cancel") {
			return AddResult{Added: []session.View{}}, nil
		}
		return AddResult{}, err
	}
	if len(paths) == 0 {
		return AddResult{Added: []session.View{}}, nil
	}
	return a.AddPaths(paths), nil
}

// RemoveFiles drops files from the tray. Originals are untouched.
func (a *App) RemoveFiles(ids []string) {
	a.store.Remove(ids)
}

// ---- running skills -------------------------------------------------------

// Result wraps an engine outcome plus a structured error for the UI.
type Result struct {
	Outcome   engine.Outcome `json:"outcome"`
	Error     string         `json:"error"`
	ErrorKind string         `json:"errorKind"` // user, missing-tool, trust, busy, internal
	Tool      *ToolStatus    `json:"tool,omitempty"`
	// Addon is the one-click add-on that provides a missing tool.
	Addon    *AddonView             `json:"addon,omitempty"`
	Trust    *TrustRequest          `json:"trust,omitempty"`
	Settings *storage.Settings      `json:"settings,omitempty"`
	Exported []session.ExportResult `json:"exported,omitempty"`
}

// TrustRequest asks the user to allow a community skill's permissions.
type TrustRequest struct {
	SkillID     string   `json:"skillId"`
	SkillName   string   `json:"skillName"`
	Permissions []string `json:"permissions"`
	Command     string   `json:"command"`
}

func (a *App) wrap(out engine.Outcome, err error) Result {
	r := Result{Outcome: out}
	if err == nil {
		return r
	}
	r.Error = engine.Readable(err)
	var mt *ops.MissingToolError
	var tr *engine.TrustRequiredError
	var ue *ops.UserError
	switch {
	case errors.As(err, &mt):
		r.ErrorKind = "missing-tool"
		r.Tool = &ToolStatus{Name: mt.Tool, Hint: tools.InstallHint(mt.Tool)}
		r.Addon = a.AddonForTool(mt.Tool)
		if r.Addon != nil {
			r.Error = r.Addon.Name + " is needed for this"
		}
	case errors.As(err, &tr):
		r.ErrorKind = "trust"
		r.Trust = &TrustRequest{SkillID: tr.SkillID, SkillName: tr.SkillName, Permissions: tr.Permissions, Command: tr.Command}
	case errors.Is(err, engine.ErrBusy):
		r.ErrorKind = "busy"
	case errors.As(err, &ue):
		r.ErrorKind = "user"
	default:
		r.ErrorKind = "internal"
	}
	return r
}

func (a *App) progress(p engine.Progress) { a.emit(eventProgress, p) }

// Apply runs a skill. Meta skills (export, settings, undo...) are handled here too.
func (a *App) Apply(fileIDs []string, skillID string, params map[string]any) Result {
	skill, ok := a.registry.GetByID(skillID)
	if !ok {
		return Result{Error: "That skill no longer exists", ErrorKind: "user"}
	}
	if skill.IsMeta {
		return a.runMeta(skill, fileIDs, params)
	}
	out, err := a.engine.Apply(a.ctx(), fileIDs, skillID, params, a.progress)
	return a.wrap(out, err)
}

func (a *App) ctx() context.Context {
	if a.app != nil {
		return a.app.Context()
	}
	return context.Background()
}

// Cancel stops the running operation.
func (a *App) Cancel() { a.engine.Cancel() }

// RemoveStep removes one applied skill and replays the rest.
func (a *App) RemoveStep(fileID string, index int) Result {
	out, err := a.engine.RemoveStep(a.ctx(), fileID, index, a.progress)
	return a.wrap(out, err)
}

// Undo reverts the last applied skill.
func (a *App) Undo() Result {
	out, err := a.engine.Undo(a.ctx())
	return a.wrap(out, err)
}

// TrustSkill records that the user allowed a community skill.
func (a *App) TrustSkill(skillID string) error {
	return a.trust.SetTrusted(skillID, true)
}

// ---- export ---------------------------------------------------------------

// Export writes the current output of each file. Files with no applied
// skills are skipped (there is nothing new to save).
func (a *App) Export(fileIDs []string) Result {
	a.exportMu.Lock()
	defer a.exportMu.Unlock()
	settings, _ := a.settings.Load()
	if len(fileIDs) == 0 {
		for _, f := range a.store.Files() {
			fileIDs = append(fileIDs, f.ID())
		}
	}
	res := Result{Exported: []session.ExportResult{}}
	todo := []*session.File{}
	for _, id := range fileIDs {
		f, ok := a.store.Get(id)
		if !ok {
			continue
		}
		if len(f.Steps()) == 0 && !f.Combined() {
			res.Outcome.Skipped = append(res.Outcome.Skipped, engine.Problem{FileID: id, Name: f.DisplayName(), Reason: "no changes to export"})
			continue
		}
		todo = append(todo, f)
	}
	for i, f := range todo {
		a.progress(engine.Progress{Label: "Exporting", Done: i, Total: len(todo), Fraction: float64(i) / float64(len(todo)), Current: f.DisplayName()})
		p, err := session.ExportFile(a.ctx(), f, settings.OutputFolder, settings.NamingPattern)
		if err != nil {
			res.Outcome.Failed = append(res.Outcome.Failed, engine.Problem{FileID: f.ID(), Name: f.DisplayName(), Reason: friendlyFSError(err)})
			continue
		}
		res.Exported = append(res.Exported, session.ExportResult{FileID: f.ID(), Name: filepath.Base(p), OutputPath: p})
	}
	a.progress(engine.Progress{Label: "Exporting", Done: len(todo), Total: len(todo), Fraction: 1})
	switch {
	case len(res.Exported) == 0 && len(res.Outcome.Failed) == 0:
		res.Outcome.Message = "Nothing to export yet: apply a skill first"
	default:
		parts := []string{fmt.Sprintf("Exported %d", len(res.Exported))}
		if len(res.Outcome.Failed) > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", len(res.Outcome.Failed)))
		}
		if len(res.Outcome.Skipped) > 0 {
			parts = append(parts, fmt.Sprintf("%d unchanged", len(res.Outcome.Skipped)))
		}
		res.Outcome.Message = strings.Join(parts, " · ")
	}
	return res
}

// PreviewSkill applies an image skill to a reduced copy of the file's current
// result and returns it as a data: URL, without changing the tray. It powers
// live adjustment while a value is dragged. maxSide bounds the long side.
func (a *App) PreviewSkill(fileID, skillID string, params map[string]any, maxSide int) (string, error) {
	b, mime, err := a.engine.Preview(a.ctx(), fileID, skillID, params, maxSide)
	if err != nil {
		return "", errors.New(engine.Readable(err))
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// links are the only pages the app ever opens, in the user's browser.
var links = map[string]string{
	"repository": "https://github.com/trevor050/asteria",
	"skills":     "https://github.com/trevor050/asteria/blob/master/skills/README.md",
}

// OpenLink opens one of the app's fixed links ("repository" or "skills").
func (a *App) OpenLink(name string) error {
	u, ok := links[name]
	if !ok {
		return fmt.Errorf("unknown link %q", name)
	}
	if a.app == nil {
		return errors.New("unavailable")
	}
	return a.app.Browser.OpenURL(u)
}

// Reveal shows a path in Finder / Explorer / the file manager.
func (a *App) Reveal(path string) error {
	if a.app == nil {
		return errors.New("unavailable")
	}
	return a.app.Env.OpenFileManager(path, true)
}

// ---- settings and meta skills ---------------------------------------------

// PickOutputFolder asks for an export folder and saves it.
func (a *App) PickOutputFolder() Result {
	if a.app == nil {
		return Result{Error: "folder picker unavailable", ErrorKind: "internal"}
	}
	folder, err := a.app.Dialog.OpenFile().
		SetTitle("Export to…").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		AttachToWindow(a.window).
		PromptForSingleSelection()
	if err != nil {
		return Result{Error: err.Error(), ErrorKind: "internal"}
	}
	if folder == "" {
		return Result{}
	}
	s, err := a.settings.Update(func(s *storage.Settings) { s.OutputFolder = folder })
	if err != nil {
		return Result{Error: err.Error(), ErrorKind: "internal"}
	}
	return Result{Settings: &s, Outcome: engine.Outcome{Message: "Exports go to " + folder}}
}

// UpdateSettings changes one setting: mode, namingPattern, accentColor,
// outputFolder, appearance ("system", "light", "dark"), reduceMotion or
// reduceTransparency ("true" or "false").
func (a *App) UpdateSettings(key, value string) Result {
	var msg string
	s, err := a.settings.Update(func(s *storage.Settings) {
		switch key {
		case "appearance":
			s.Appearance = value
			applyWindowAppearance(a.window, value)
		case "reduceMotion":
			s.ReduceMotion = value == "true"
		case "reduceTransparency":
			s.ReduceTransparency = value == "true"
		case "mode":
			s.Mode = value
		case "namingPattern":
			if strings.TrimSpace(value) != "" {
				s.NamingPattern = strings.TrimSpace(value)
				msg = "Exports will be named " + session.ExportName(s.NamingPattern, "photo", ".jpg", "resize")
			}
		case "accentColor":
			s.AccentColor = value
		case "outputFolder":
			s.OutputFolder = value
		}
	})
	if err != nil {
		return Result{Error: err.Error(), ErrorKind: "internal"}
	}
	return Result{Settings: &s, Outcome: engine.Outcome{Message: msg}}
}

func (a *App) runMeta(skill skills.Skill, fileIDs []string, params map[string]any) Result {
	param := func(name string) string {
		if v, ok := params[name]; ok && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return ""
	}
	switch skill.ID {
	case "export":
		return a.Export(fileIDs)
	case "undo":
		return a.Undo()
	case "clear_all":
		if a.engine.Busy() {
			return Result{Error: "Wait for the current operation to finish", ErrorKind: "busy"}
		}
		ids := []string{}
		for _, f := range a.store.Files() {
			ids = append(ids, f.ID())
		}
		a.store.Clear()
		return Result{Outcome: engine.Outcome{Removed: ids, Message: "Cleared the tray (originals are untouched)"}}
	case "switch_to_batch":
		r := a.UpdateSettings("mode", "batch")
		r.Outcome.Message = "Skills apply to the selection, or every file"
		return r
	case "switch_to_per_file":
		r := a.UpdateSettings("mode", "per_file")
		r.Outcome.Message = "Skills apply to the focused file only"
		return r
	case "set_output_folder":
		return a.PickOutputFolder()
	case "reset_output_folder":
		r := a.UpdateSettings("outputFolder", "")
		r.Outcome.Message = "Exports go next to each original"
		return r
	case "set_naming_pattern":
		return a.UpdateSettings("namingPattern", param("pattern"))
	case "set_accent_color":
		return a.UpdateSettings("accentColor", param("color"))
	case "open_skills_folder":
		if a.app == nil {
			return Result{Error: "unavailable", ErrorKind: "internal"}
		}
		if err := a.app.Env.OpenFileManager(a.skillsDir, false); err != nil {
			return Result{Error: err.Error(), ErrorKind: "internal"}
		}
		return Result{Outcome: engine.Outcome{Message: "Drop skill .json files here; they load instantly"}}
	}
	return Result{Error: "Unknown meta skill " + skill.ID, ErrorKind: "internal"}
}

// ListSkills returns every loaded skill (for the help sheet).
func (a *App) ListSkills() []Suggestion {
	all := a.registry.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	out := make([]Suggestion, 0, len(all))
	for _, s := range all {
		out = append(out, Suggestion{ID: s.ID, Name: s.Name, Description: s.Description, Category: s.Category, IsMeta: s.IsMeta, Combine: s.Combine, OutputType: s.OutputType, Params: s.Params, Community: s.Source == skills.SkillSourceCommunity, Icon: s.Icon})
	}
	return out
}

func (a *App) addFromArgs(args []string) {
	paths := []string{}
	for _, arg := range args {
		if len(arg) > 0 && arg[0] == '-' {
			continue
		}
		if _, err := os.Stat(arg); err == nil {
			paths = append(paths, arg)
		}
	}
	if len(paths) > 0 {
		a.emit(eventFilesAdded, a.AddPaths(paths))
	}
}
