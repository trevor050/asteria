package skills

// Skill is one JSON skill definition. See skills/README.md for the format.
type Skill struct {
	Version     string     `json:"version,omitempty"`
	Author      string     `json:"author,omitempty"`
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Aliases     []string   `json:"aliases"`
	Category    string     `json:"category"`
	// Priority nudges ranking within a category (0 by default, up to 20):
	// it picks which skill represents its family in the suggestions shown
	// before anything is typed, and breaks ties between equal text matches.
	Priority float64 `json:"priority,omitempty"`
	Description string     `json:"description"`
	InputTypes  []string   `json:"inputTypes"`
	OutputType  string     `json:"outputType"`
	Params      []ParamDef `json:"params"`
	Driver      string     `json:"driver,omitempty"`
	IsMeta      bool       `json:"isMeta"`
	Executor    Executor   `json:"executor"`
	Permissions []string   `json:"permissions,omitempty"`
	DangerLevel int        `json:"dangerLevel"`

	// Combine marks skills that turn several files into one output (merge
	// PDFs, zip). It is inferred from the native handler when omitted.
	Combine bool `json:"combine,omitempty"`
	// OutputName is the base name for combined outputs, e.g. "merged".
	OutputName string `json:"outputName,omitempty"`
	// Requires lists external programs the skill needs, e.g. ["ffmpeg"].
	// Native handlers and CLI commands add theirs automatically.
	Requires []string `json:"requires,omitempty"`

	// Source is runtime metadata (not part of the JSON schema).
	Source SkillSource `json:"source"`
	// DefinitionPath is where the definition was loaded from.
	DefinitionPath string `json:"-"`
}

type SkillSource string

const (
	SkillSourceCoreEmbedded SkillSource = "core:embedded"
	SkillSourceCoreDisk     SkillSource = "core:disk"
	SkillSourceCommunity    SkillSource = "community"
)

// IsCore reports whether the skill ships with the app.
func (s Skill) IsCore() bool {
	return s.Source == SkillSourceCoreEmbedded || s.Source == SkillSourceCoreDisk
}

// Executor says how a skill runs.
type Executor struct {
	// Type is one of native, cli, pipeline or meta.
	Type string `json:"type"`

	// Native: a registered Go operation plus fixed arguments.
	Handler string         `json:"handler,omitempty"`
	With    map[string]any `json:"with,omitempty"`

	// CLI: an external command. Arguments are templated, never shell-parsed.
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	TimeoutMs int      `json:"timeoutMs,omitempty"`
	// OutputExtension is a legacy alias for the skill's outputType.
	OutputExtension string `json:"outputExtension,omitempty"`

	// Pipeline: other skills run in order on the same file.
	Steps []PipelineStep `json:"steps,omitempty"`
}

type PipelineStep struct {
	SkillID string         `json:"skillId"`
	Params  map[string]any `json:"params,omitempty"`
}

// ParamDef describes the skill's single user parameter.
type ParamDef struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Default any      `json:"default"`
	Presets []any    `json:"presets,omitempty"`
	Options []string `json:"options,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

// OutputFolder is the outputType value for skills that produce a folder.
const OutputFolder = "folder"
