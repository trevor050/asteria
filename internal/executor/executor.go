package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"asteria/internal/drivers"
	"asteria/internal/preview"
	"asteria/internal/session"
	"asteria/internal/skills"
	"asteria/internal/storage"
)

type Executor struct {
	registry *skills.Registry
	session  *session.State
	drivers  map[string]drivers.Driver
	usage    *storage.UsageStore
}

const maxPipelineDepth = 6

func NewExecutor(registry *skills.Registry, sessionState *session.State, usage *storage.UsageStore) *Executor {
	return &Executor{
		registry: registry,
		session:  sessionState,
		drivers: map[string]drivers.Driver{
			"image": &drivers.ImageDriver{},
			"cli":   &drivers.CLIDriver{},
		},
		usage: usage,
	}
}

func (e *Executor) executeSkillToOutput(ctx context.Context, inputPath string, inputExt string, fileDir string, skill skills.Skill, params map[string]any, depth int) (string, string, error) {
	if skill.IsMeta {
		return "", "", fmt.Errorf("meta skills cannot be executed on files")
	}
	if strings.EqualFold(skill.Executor.Type, "pipeline") {
		if depth > maxPipelineDepth {
			return "", "", fmt.Errorf("pipeline depth exceeded")
		}
		currentPath := inputPath
		currentExt := inputExt
		for _, step := range skill.Executor.Steps {
			stepSkill, ok := e.registry.GetByID(step.SkillID)
			if !ok {
				return "", "", fmt.Errorf("unknown pipeline step skill: %s", step.SkillID)
			}
			if stepSkill.IsMeta {
				return "", "", fmt.Errorf("pipeline step cannot be meta: %s", step.SkillID)
			}
			mergedParams := mergeParams(params, step.Params)
			outPath, outExt, err := e.executeSkillToOutput(ctx, currentPath, currentExt, fileDir, stepSkill, mergedParams, depth+1)
			if err != nil {
				return "", "", err
			}
			currentPath = outPath
			currentExt = outExt
		}
		return currentPath, currentExt, nil
	}

	driverID := skill.Driver
	if strings.TrimSpace(driverID) == "" {
		if strings.EqualFold(skill.Executor.Type, "cli") {
			driverID = "cli"
		}
	}
	driver, ok := e.drivers[driverID]
	if !ok {
		return "", "", fmt.Errorf("missing driver: %s", driverID)
	}

	outputExt := effectiveOutputExt(skill, inputExt)
	outputPath := filepath.Join(fileDir, "current"+outputExt)
	if err := driver.Execute(ctx, inputPath, outputPath, skill, params, nil); err != nil {
		return "", "", err
	}
	return outputPath, outputExt, nil
}

func mergeParams(base map[string]any, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func effectiveOutputExt(skill skills.Skill, currentExt string) string {
	if skill.OutputType != "" && skill.OutputType != "none" {
		return skill.OutputType
	}
	if strings.EqualFold(skill.Executor.Type, "cli") && strings.TrimSpace(skill.Executor.OutputExtension) != "" {
		ext := strings.TrimSpace(skill.Executor.OutputExtension)
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		return strings.ToLower(ext)
	}
	return currentExt
}

func (e *Executor) ApplySkill(ctx context.Context, fileIDs []string, skillID string, params map[string]any) ([]session.WorkingFile, error) {
	skill, ok := e.registry.GetByID(skillID)
	if !ok {
		return nil, fmt.Errorf("unknown skill: %s", skillID)
	}
	var driver drivers.Driver
	if !strings.EqualFold(skill.Executor.Type, "pipeline") {
		var ok bool
		driver, ok = e.drivers[skill.Driver]
		if !ok {
			return nil, fmt.Errorf("missing driver: %s", skill.Driver)
		}
	}
	var wg sync.WaitGroup
	results := make([]session.WorkingFile, len(fileIDs))
	errs := make(chan error, len(fileIDs))
	for i, fileID := range fileIDs {
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			updated, err := e.applyToFile(ctx, id, skill, driver, params)
			if err != nil {
				errs <- err
				return
			}
			results[idx] = updated
		}(i, fileID)
	}
	wg.Wait()
	close(errs)
	if err := firstError(errs); err != nil {
		return nil, err
	}
	_ = e.usage.Increment(skillID)
	return results, nil
}

func (e *Executor) RemoveSkill(ctx context.Context, fileID string, index int) (session.WorkingFile, error) {
	fileState, ok := e.session.GetFile(fileID)
	if !ok {
		return session.WorkingFile{}, fmt.Errorf("file not found")
	}
	applied := fileState.AppliedSkills()
	if index < 0 || index >= len(applied) {
		return session.WorkingFile{}, fmt.Errorf("invalid skill index")
	}
	updated := append([]session.AppliedSkill{}, applied...)
	updated = append(updated[:index], updated[index+1:]...)
	fileState.ReplaceApplied(updated)

	if err := e.rebuildFrom(ctx, fileState, index); err != nil {
		return session.WorkingFile{}, err
	}
	return fileState.Data(), nil
}

func (e *Executor) applyToFile(ctx context.Context, fileID string, skill skills.Skill, driver drivers.Driver, params map[string]any) (session.WorkingFile, error) {
	fileState, ok := e.session.GetFile(fileID)
	if !ok {
		return session.WorkingFile{}, fmt.Errorf("file not found")
	}
	data := fileState.Data()
	currentPath := data.WorkingPath
	currentExt := data.CurrentExtension
	fileDir := filepath.Dir(fileState.BasePath())
	outputPath, outputExt, err := e.executeSkillToOutput(ctx, currentPath, currentExt, fileDir, skill, params, 0)
	if err != nil {
		return session.WorkingFile{}, err
	}
	size := data.Size
	if info, err := os.Stat(outputPath); err == nil {
		size = info.Size()
	}
	fileState.SetCurrentPath(outputPath, outputExt, size)

	snapshotIndex := len(data.AppliedSkills)
	snapshotPath := e.sessionSnapshotPath(fileState, snapshotIndex, outputExt)
	if err := session.CopyFile(outputPath, snapshotPath); err == nil {
		fileState.SetSnapshot(snapshotIndex, snapshotPath)
	}

	fileState.AppendApplied(session.NewAppliedSkill(skill.ID, params))
	if previewURL, err := preview.ImagePreview(outputPath, 520); err == nil {
		fileState.SetPreview(previewURL)
	}
	return fileState.Data(), nil
}

func (e *Executor) rebuildFrom(ctx context.Context, fileState *session.FileState, startIndex int) error {
	basePath := fileState.BasePath()
	applied := fileState.AppliedSkills()
	fileDir := filepath.Dir(basePath)

	startPath := basePath
	if startIndex > 0 {
		if snapshot, ok := fileState.SnapshotAt(startIndex - 1); ok {
			startPath = snapshot
		}
	}

	ext := filepath.Ext(startPath)
	currentPath := filepath.Join(fileDir, "current"+ext)
	if err := session.CopyFile(startPath, currentPath); err != nil {
		return err
	}

	size := int64(0)
	if info, err := os.Stat(currentPath); err == nil {
		size = info.Size()
	}
	fileState.SetCurrentPath(currentPath, ext, size)
	fileState.TrimSnapshots(startIndex)

	for i := startIndex; i < len(applied); i++ {
		step := applied[i]
		skill, ok := e.registry.GetByID(step.SkillID)
		if !ok {
			return fmt.Errorf("unknown skill: %s", step.SkillID)
		}

		data := fileState.Data()
		currentPath = data.WorkingPath
		currentExt := data.CurrentExtension
		outputPath, outputExt, err := e.executeSkillToOutput(ctx, currentPath, currentExt, fileDir, skill, step.Params, 0)
		if err != nil {
			return err
		}
		size := int64(0)
		if info, err := os.Stat(outputPath); err == nil {
			size = info.Size()
		}
		fileState.SetCurrentPath(outputPath, outputExt, size)
		snapshotPath := e.sessionSnapshotPath(fileState, i, outputExt)
		if err := session.CopyFile(outputPath, snapshotPath); err == nil {
			fileState.SetSnapshot(i, snapshotPath)
		}
	}
	finalPath := fileState.Data().WorkingPath
	if previewURL, err := preview.ImagePreview(finalPath, 520); err == nil {
		fileState.SetPreview(previewURL)
	}
	return nil
}

func (e *Executor) sessionSnapshotPath(fileState *session.FileState, index int, ext string) string {
	return fileState.SnapshotPath(e.session.Workspace(), index, ext)
}

func firstError(errs <-chan error) error {
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
