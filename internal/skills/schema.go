package skills

type Skill struct {
	Version     string     `json:"version,omitempty"`
	Author      string     `json:"author,omitempty"`
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Aliases     []string   `json:"aliases"`
	Category    string     `json:"category"`
	Description string     `json:"description"`
	InputTypes  []string   `json:"inputTypes"`
	OutputType  string     `json:"outputType"`
	Params      []ParamDef `json:"params"`
	Driver      string     `json:"driver"`
	IsMeta      bool       `json:"isMeta"`
	Executor    Executor   `json:"executor,omitempty"`
	Permissions []string   `json:"permissions,omitempty"`
	DangerLevel int        `json:"dangerLevel"`

	// Source is runtime metadata (not part of the JSON schema).
	Source SkillSource `json:"-"`
	// DefinitionPath is the on-disk path (if loaded from disk).
	DefinitionPath string `json:"-"`
}

type SkillSource string

const (
	SkillSourceCoreEmbedded SkillSource = "core:embedded"
	SkillSourceCoreDisk     SkillSource = "core:disk"
	SkillSourceCommunity    SkillSource = "community"
)

type Executor struct {
	Type string `json:"type"` // native | cli | lua | meta

	// Native
	Handler string `json:"handler,omitempty"`

	// CLI
	Command         string   `json:"command,omitempty"`
	Args            []string `json:"args,omitempty"`
	OutputExtension string   `json:"outputExtension,omitempty"`
	TimeoutMs       int      `json:"timeoutMs,omitempty"`

	// Lua
	Script string `json:"script,omitempty"`

	// Pipeline
	Steps []PipelineStep `json:"steps,omitempty"`
}

type PipelineStep struct {
	SkillID string         `json:"skillId"`
	Params  map[string]any `json:"params,omitempty"`
}

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
}
