package drivers

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"asteria/internal/skills"
)

// CLIDriver executes declarative CLI skills.
//
// This is the POC execution engine that lets skills be authored in JSON without
// adding Go code, while still supporting the existing Driver interface.
type CLIDriver struct {
	// Allowlist for untrusted community skills (base permission: tools.exec).
	// Anything outside this list requires elevated permission tools.exec.any.
	AllowedCommands []string
}

func (d *CLIDriver) ID() string {
	return "cli"
}

func (d *CLIDriver) Supports(skill skills.Skill) bool {
	return skill.Driver == d.ID() || skill.Executor.Type == "cli"
}

func (d *CLIDriver) Execute(ctx context.Context, inputPath string, outputPath string, skill skills.Skill, params map[string]any, progress ProgressFunc) error {
	if skill.Executor.Type != "cli" {
		return fmt.Errorf("cli driver requires executor.type=cli")
	}
	if strings.TrimSpace(skill.Executor.Command) == "" {
		return fmt.Errorf("cli driver requires executor.command")
	}
	if !hasPermission(skill.Permissions, skills.PermToolsExec) {
		return fmt.Errorf("skill missing required permission: %s", skills.PermToolsExec)
	}

	cmdName := skill.Executor.Command
	base := strings.ToLower(filepath.Base(cmdName))
	allowAny := hasPermission(skill.Permissions, skills.PermToolsExecAny)
	if skill.Source == skills.SkillSourceCommunity && !allowAny {
		allowed := d.allowed(base)
		if !allowed {
			return fmt.Errorf("community skill requires %s to run %q", skills.PermToolsExecAny, base)
		}
	}

	ctxToUse := ctx
	var cancel context.CancelFunc
	if skill.Executor.TimeoutMs > 0 {
		ctxToUse, cancel = context.WithTimeout(ctx, time.Duration(skill.Executor.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	args := make([]string, 0, len(skill.Executor.Args))
	for _, a := range skill.Executor.Args {
		args = append(args, renderTemplate(a, inputPath, outputPath, params))
	}

	cmd := exec.CommandContext(ctxToUse, cmdName, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if progress != nil {
		progress(0.2)
	}
	err := cmd.Run()
	if progress != nil {
		progress(1.0)
	}
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			errText = strings.TrimSpace(stdout.String())
		}
		if errText != "" {
			return fmt.Errorf("cli skill failed: %s", errText)
		}
		return fmt.Errorf("cli skill failed: %w", err)
	}
	return nil
}

func (d *CLIDriver) allowed(cmdBase string) bool {
	if len(d.AllowedCommands) == 0 {
		// Conservative default allowlist.
		d.AllowedCommands = []string{"ffmpeg", "magick", "convert", "identify"}
	}
	for _, a := range d.AllowedCommands {
		if strings.EqualFold(a, cmdBase) {
			return true
		}
	}
	return false
}

func renderTemplate(s string, inputPath string, outputPath string, params map[string]any) string {
	out := strings.ReplaceAll(s, "{{input}}", inputPath)
	out = strings.ReplaceAll(out, "{{output}}", outputPath)
	if params == nil {
		return out
	}
	for k, v := range params {
		key := "{{" + k + "}}"
		out = strings.ReplaceAll(out, key, fmt.Sprint(v))
	}
	return out
}

func hasPermission(perms []string, perm string) bool {
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}
