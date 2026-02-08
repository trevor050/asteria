package drivers

import (
	"context"

	"asteria/internal/skills"
)

type ProgressFunc func(value float64)

type Driver interface {
	ID() string
	Supports(skill skills.Skill) bool
	Execute(ctx context.Context, inputPath string, outputPath string, skill skills.Skill, params map[string]any, progress ProgressFunc) error
}
