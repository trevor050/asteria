package ops

import (
	"asteria/internal/tools"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CLISpec describes a skill that runs an external command.
type CLISpec struct {
	Command   string
	Args      []string
	TimeoutMs int
}

// RunCLI executes a declarative CLI skill. Templates {{input}}, {{output}},
// {{inputs}} (combine skills, expands to one argument per input) and
// {{<param>}} are substituted per argument; nothing goes through a shell.
func RunCLI(ctx context.Context, spec CLISpec, job Job) error {
	job = Prepare(job)
	name := strings.TrimSpace(spec.Command)
	if name == "" {
		return fmt.Errorf("cli skill has no command")
	}
	bin := name
	if !filepath.IsAbs(name) {
		p, ok := job.Tools.Lookup(name)
		if !ok {
			return &MissingToolError{Tool: name}
		}
		bin = p
	}
	if spec.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	args := make([]string, 0, len(spec.Args))
	for _, a := range spec.Args {
		if strings.TrimSpace(a) == "{{inputs}}" {
			args = append(args, job.Inputs...)
			continue
		}
		args = append(args, RenderTemplate(a, job))
	}
	cmd := tools.Command(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	job.Progress(0.1)
	err := cmd.Run()
	if err != nil {
		_ = os.RemoveAll(job.Output)
		if ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return Userf("%s timed out", name)
			}
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 400 {
			msg = "…" + msg[len(msg)-400:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return Userf("%s failed: %s", name, msg)
	}
	if _, err := os.Stat(job.Output); err != nil {
		return Userf("%s finished but did not write {{output}}", name)
	}
	return nil
}

// RenderTemplate substitutes {{input}}, {{output}} and parameter values.
func RenderTemplate(s string, job Job) string {
	out := strings.ReplaceAll(s, "{{input}}", job.Input())
	out = strings.ReplaceAll(out, "{{output}}", job.Output)
	for k, v := range job.Args {
		out = strings.ReplaceAll(out, "{{"+k+"}}", fmt.Sprint(v))
	}
	return out
}
