package session

type Mode string

const (
	ModeBatch   Mode = "batch"
	ModePerFile Mode = "per_file"
)

type AppliedSkill struct {
	SkillID   string         `json:"skillId"`
	Params    map[string]any `json:"params"`
	AppliedAt string         `json:"appliedAt"`
}

type WorkingFile struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Extension        string         `json:"extension"`
	CurrentExtension string         `json:"currentExtension"`
	OriginalPath     string         `json:"originalPath"`
	WorkingPath      string         `json:"workingPath"`
	Size             int64          `json:"size"`
	PreviewDataURL   string         `json:"previewDataUrl"`
	AppliedSkills    []AppliedSkill `json:"appliedSkills"`
}

type SessionSnapshot struct {
	Mode          Mode   `json:"mode"`
	OutputFolder  string `json:"outputFolder"`
	NamingPattern string `json:"namingPattern"`
	AccentColor   string `json:"accentColor"`
}

type ExportResult struct {
	FileID     string `json:"fileId"`
	OutputPath string `json:"outputPath"`
}
