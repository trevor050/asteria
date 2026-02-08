package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"asteria/internal/executor"
	"asteria/internal/preview"
	"asteria/internal/session"
	"asteria/internal/skills"
	"asteria/internal/storage"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// App struct
type App struct {
	ctx           context.Context
	app           *application.App
	window        *application.WebviewWindow
	registry      *skills.Registry
	session       *session.State
	executor      *executor.Executor
	settingsStore *storage.SettingsStore
	usageStore    *storage.UsageStore
	trustStore    *storage.TrustStore
}

// NewApp creates a new App application struct
func NewApp() *App {
	settingsStore, _ := storage.NewSettingsStore()
	usageStore, _ := storage.NewUsageStore()
	trustStore, _ := storage.NewTrustStore()
	settings, _ := settingsStore.Load()
	sessionState, _ := session.NewState(session.SessionSnapshot{
		Mode:          session.ModeBatch,
		OutputFolder:  settings.OutputFolder,
		NamingPattern: settings.NamingPattern,
		AccentColor:   settings.AccentColor,
	})
	skillsDir, _ := storage.SkillsDir()
	registry := skills.NewRegistry(skills.RegistryOptions{
		EmbeddedFS:    assets,
		EmbeddedRoot:  "skills/core",
		DiskCoreRoot:  "skills/core",
		CommunityRoot: skillsDir,
	})
	exec := executor.NewExecutor(registry, sessionState, usageStore)
	return &App{
		registry:      registry,
		session:       sessionState,
		executor:      exec,
		settingsStore: settingsStore,
		usageStore:    usageStore,
		trustStore:    trustStore,
	}
}

// initWithApp is called after the app is created
func (a *App) initWithApp(app *application.App, window *application.WebviewWindow) {
	a.app = app
	a.window = window
	a.ctx = app.Context()

	// Hot reload skills (community + dev core).
	if a.registry != nil {
		_ = a.registry.StartHotReload(a.ctx)
		go func() {
			for range a.registry.Changes() {
				if a.window != nil {
					a.window.EmitEvent("asteria:skills-updated", map[string]any{"ok": true})
				}
			}
		}()
	}

	// Handle file drops via window events
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		// Files are passed in the event context - emit to frontend
		window.EmitEvent("asteria:file-drop", event.Context())
	})
}

func (a *App) GetSkillTrust(skillID string) (bool, error) {
	if a.trustStore == nil {
		return false, nil
	}
	return a.trustStore.IsTrusted(skillID)
}

func (a *App) SetSkillTrust(skillID string, trusted bool) error {
	if a.trustStore == nil {
		return nil
	}
	return a.trustStore.SetTrusted(skillID, trusted)
}

// startup is called when the app starts (for v2 compatibility, but we use initWithApp now)
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) GetSession() session.SessionSnapshot {
	return a.session.Snapshot()
}

func (a *App) GetSkills(query string, inputTypes []string) ([]skills.Skill, error) {
	usage := a.usageStore.All()
	return a.registry.Search(query, inputTypes, usage), nil
}

func (a *App) OpenFilesDialog() ([]string, error) {
	if a.app == nil {
		return nil, fmt.Errorf("app not initialized")
	}
	result, err := a.app.Dialog.OpenFile().
		CanChooseFiles(true).
		PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) AddFiles(paths []string) ([]session.WorkingFile, error) {
	added := make([]session.WorkingFile, 0, len(paths))
	for _, path := range paths {
		file, err := a.session.AddFile(path)
		if err != nil {
			continue
		}
		if fileState, ok := a.session.GetFile(file.ID); ok {
			if previewURL, err := preview.ImagePreview(file.WorkingPath, 520); err == nil {
				fileState.SetPreview(previewURL)
			}
			added = append(added, fileState.Data())
		} else {
			added = append(added, file)
		}
	}
	return added, nil
}

func (a *App) ExecuteSkill(fileIDs []string, skillID string, params map[string]any) (executor.SkillResult, error) {
	skill, ok := a.registry.GetByID(skillID)
	if !ok {
		return executor.SkillResult{}, fmt.Errorf("unknown skill")
	}

	// Chrome-like trust model: base permissions are allowed; elevated permissions
	// require an explicit user trust decision for community skills.
	if skill.Source == skills.SkillSourceCommunity && skill.RequiresTrust() {
		trusted := false
		if a.trustStore != nil {
			if v, err := a.trustStore.IsTrusted(skillID); err == nil {
				trusted = v
			}
		}
		if !trusted {
			elevated := skills.ElevatedPermissions(skill.Permissions)
			return executor.SkillResult{}, fmt.Errorf("skill requires trust: %s", strings.Join(elevated, ", "))
		}
	}

	if skill.IsMeta {
		return a.executeMetaSkill(skillID, params, fileIDs)
	}

	if len(fileIDs) == 0 {
		return executor.SkillResult{Session: a.session.Snapshot()}, nil
	}
	updated, err := a.executor.ApplySkill(a.ctx, fileIDs, skillID, params)
	if err != nil {
		return executor.SkillResult{}, err
	}
	return executor.SkillResult{
		UpdatedFiles: updated,
		Session:      a.session.Snapshot(),
	}, nil
}

func (a *App) RemoveSkill(fileID string, index int) (session.WorkingFile, error) {
	return a.executor.RemoveSkill(a.ctx, fileID, index)
}

func (a *App) SetMode(mode string) (session.SessionSnapshot, error) {
	switch mode {
	case string(session.ModeBatch):
		a.session.SetMode(session.ModeBatch)
	case string(session.ModePerFile):
		a.session.SetMode(session.ModePerFile)
	default:
		return a.session.Snapshot(), fmt.Errorf("invalid mode")
	}
	return a.session.Snapshot(), nil
}

func (a *App) ExportFiles(fileIDs []string) ([]session.ExportResult, error) {
	if len(fileIDs) == 0 {
		files := a.session.ListFiles()
		for _, file := range files {
			fileIDs = append(fileIDs, file.ID)
		}
	}
	results := make([]session.ExportResult, 0, len(fileIDs))
	for _, id := range fileIDs {
		fileState, ok := a.session.GetFile(id)
		if !ok {
			continue
		}
		data := fileState.Data()
		outputFolder := a.session.OutputFolder()
		if outputFolder == "" {
			outputFolder = filepath.Dir(data.OriginalPath)
		}
		skillName := "asteria"
		if len(data.AppliedSkills) > 0 {
			last := data.AppliedSkills[len(data.AppliedSkills)-1]
			skillName = last.SkillID
		}
		baseName := session.ExportName(a.session.NamingPattern(), data.Name, data.CurrentExtension, skillName)
		outputPath := resolveOutputPath(outputFolder, baseName)
		if err := session.CopyFile(data.WorkingPath, outputPath); err != nil {
			return results, err
		}
		results = append(results, session.ExportResult{FileID: id, OutputPath: outputPath})
	}
	return results, nil
}

func (a *App) ClearAll() error {
	return a.session.Clear()
}

func (a *App) executeMetaSkill(skillID string, params map[string]any, fileIDs []string) (executor.SkillResult, error) {
	switch skillID {
	case "switch_to_batch":
		a.session.SetMode(session.ModeBatch)
	case "switch_to_per_file":
		a.session.SetMode(session.ModePerFile)
	case "set_output_folder":
		if a.app == nil {
			return executor.SkillResult{}, fmt.Errorf("app not initialized")
		}
		folder, err := a.app.Dialog.OpenFile().
			CanChooseDirectories(true).
			CanChooseFiles(false).
			PromptForSingleSelection()
		if err != nil {
			return executor.SkillResult{}, err
		}
		if folder != "" {
			a.session.SetOutputFolder(folder)
			if a.settingsStore != nil {
				_ = a.settingsStore.Save(storage.Settings{
					OutputFolder:  folder,
					NamingPattern: a.session.NamingPattern(),
					AccentColor:   a.session.AccentColor(),
				})
			}
		}
	case "set_naming_pattern":
		if value, ok := params["pattern"]; ok {
			pattern, _ := value.(string)
			if strings.TrimSpace(pattern) != "" {
				a.session.SetNamingPattern(pattern)
				if a.settingsStore != nil {
					_ = a.settingsStore.Save(storage.Settings{
						OutputFolder:  a.session.OutputFolder(),
						NamingPattern: pattern,
						AccentColor:   a.session.AccentColor(),
					})
				}
			}
		}
	case "set_accent_color":
		if value, ok := params["color"]; ok {
			color, _ := value.(string)
			if strings.TrimSpace(color) != "" {
				a.session.SetAccentColor(color)
				if a.settingsStore != nil {
					_ = a.settingsStore.Save(storage.Settings{
						OutputFolder:  a.session.OutputFolder(),
						NamingPattern: a.session.NamingPattern(),
						AccentColor:   color,
					})
				}
				return executor.SkillResult{Session: a.session.Snapshot(), Message: "Accent updated"}, nil
			}
		}
	case "export":
		outputs, err := a.ExportFiles(fileIDs)
		if err != nil {
			return executor.SkillResult{}, err
		}
		return executor.SkillResult{Session: a.session.Snapshot(), Message: fmt.Sprintf("Exported %d files", len(outputs))}, nil
	case "clear_all":
		if err := a.session.Clear(); err != nil {
			return executor.SkillResult{}, err
		}
		return executor.SkillResult{Session: a.session.Snapshot(), Message: "Cleared all files"}, nil
	default:
		return executor.SkillResult{}, fmt.Errorf("unknown meta skill")
	}
	return executor.SkillResult{Session: a.session.Snapshot()}, nil
}

func resolveOutputPath(folder string, filename string) string {
	outputPath := filepath.Join(folder, filename)
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return outputPath
	}
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		full := filepath.Join(folder, candidate)
		if _, err := os.Stat(full); os.IsNotExist(err) {
			return full
		}
	}
	return outputPath
}
