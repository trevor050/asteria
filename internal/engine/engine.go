// Package engine runs skills on tray files: per-file chains, combine skills,
// pipelines and CLI skills, with bounded concurrency, progress, cancellation
// and undo.
package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"asteria/internal/inspect"
	"asteria/internal/ops"
	"asteria/internal/session"
	"asteria/internal/skills"
	"asteria/internal/tools"
)

// Progress is reported while a batch runs.
type Progress struct {
	Label    string  `json:"label"`
	Done     int     `json:"done"`
	Total    int     `json:"total"`
	Fraction float64 `json:"fraction"`
	Current  string  `json:"current"`
}

// Problem is a per-file failure or skip.
type Problem struct {
	FileID string `json:"fileId"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Outcome describes what an operation changed.
type Outcome struct {
	Updated   []session.View `json:"updated"`
	Added     []session.View `json:"added"`
	Removed   []string       `json:"removed"`
	Failed    []Problem      `json:"failed"`
	Skipped   []Problem      `json:"skipped"`
	Cancelled bool           `json:"cancelled"`
	Message   string         `json:"message"`
}

// ErrBusy is returned when another operation is running.
var ErrBusy = errors.New("another operation is still running")

// TrustRequiredError asks the user to trust a community skill.
type TrustRequiredError struct {
	SkillID     string
	SkillName   string
	Permissions []string
	Command     string
}

func (e *TrustRequiredError) Error() string {
	return fmt.Sprintf("%s needs permission: %s", e.SkillName, strings.Join(e.Permissions, ", "))
}

// Options wires the engine to the rest of the app.
type Options struct {
	Registry *skills.Registry
	Store    *session.Store
	Tools    *tools.Finder
	// IsTrusted reports whether the user trusted a community skill.
	IsTrusted func(skillID string) bool
	// OnUsed records a successful use for ranking.
	OnUsed func(skillID string)
}

// Engine executes skills. Only one operation runs at a time.
type Engine struct {
	opts Options

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	undo    []undoEntry

	pix *pixCache
}

type undoEntry struct {
	label string
	steps map[string]string // fileID -> step id that was appended
	added string            // combined file id
}

const maxPipelineDepth = 6

// safeCommands may be run by community CLI skills with the base tools.exec
// permission; anything else needs tools.exec.any (and the user's trust).
var safeCommands = map[string]bool{"ffmpeg": true, "ffprobe": true, "magick": true, "pandoc": true, "cwebp": true, "gs": true, "qpdf": true,
	"soffice": true, "ebook-convert": true, "resvg": true, "7zz": true, "7za": true, "7z": true, "tesseract": true, "potrace": true}

func New(opts Options) *Engine {
	if opts.Tools == nil {
		opts.Tools = tools.NewFinder()
	}
	if opts.IsTrusted == nil {
		opts.IsTrusted = func(string) bool { return false }
	}
	if opts.OnUsed == nil {
		opts.OnUsed = func(string) {}
	}
	// Image work allocates large short-lived buffers; a soft memory limit
	// makes the collector return them promptly instead of letting the heap
	// grow to several times the working set on an 8 GB machine. An explicit
	// GOMEMLIMIT wins.
	if debug.SetMemoryLimit(-1) == math.MaxInt64 {
		debug.SetMemoryLimit(1280 << 20)
	}
	return &Engine{opts: opts, pix: newPixCache()}
}

func (e *Engine) begin(parent context.Context) (context.Context, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return nil, nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(parent)
	e.running = true
	e.cancel = cancel
	return ctx, func() {
		cancel()
		e.mu.Lock()
		e.running = false
		e.cancel = nil
		e.mu.Unlock()
	}, nil
}

// Cancel stops the running operation, if any.
func (e *Engine) Cancel() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
}

// Busy reports whether an operation is running.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// CheckRunnable verifies required tools and trust before anything runs.
func (e *Engine) CheckRunnable(skill skills.Skill) error {
	for _, tool := range e.requires(skill, 0) {
		if _, ok := e.opts.Tools.Lookup(tool); !ok {
			return &ops.MissingToolError{Tool: tool}
		}
	}
	if skill.Source == skills.SkillSourceCommunity {
		needs := skills.ElevatedPermissions(skill.Permissions)
		if skill.Executor.Type == "cli" {
			base := strings.ToLower(strings.TrimSuffix(filepath.Base(skill.Executor.Command), ".exe"))
			if !hasPerm(skill.Permissions, skills.PermToolsExec) {
				return ops.Userf("%s runs %s but does not declare the %q permission", skill.Name, base, skills.PermToolsExec)
			}
			if !safeCommands[base] && !hasPerm(skill.Permissions, skills.PermToolsExecAny) {
				return ops.Userf("%s runs %s, which needs the %q permission", skill.Name, base, skills.PermToolsExecAny)
			}
		}
		if len(needs) > 0 && !e.opts.IsTrusted(skill.ID) {
			return &TrustRequiredError{SkillID: skill.ID, SkillName: skill.Name, Permissions: needs, Command: skill.Executor.Command}
		}
	}
	return nil
}

func hasPerm(perms []string, p string) bool {
	for _, x := range perms {
		if x == p {
			return true
		}
	}
	return false
}

func (e *Engine) requires(skill skills.Skill, depth int) []string {
	out := append([]string{}, skill.Requires...)
	if skill.Executor.Type == "pipeline" && depth < maxPipelineDepth {
		for _, st := range skill.Executor.Steps {
			if sub, ok := e.opts.Registry.GetByID(st.SkillID); ok {
				out = append(out, e.requires(sub, depth+1)...)
			}
		}
	}
	return out
}

func (e *Engine) concurrency(skill skills.Skill) int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	switch skill.Executor.Type {
	case "native":
		if h, ok := ops.Lookup(skill.Executor.Handler); ok && h.Concurrency > 0 && h.Concurrency < n {
			n = h.Concurrency
		}
	case "cli":
		n = 1
	case "pipeline":
		for _, st := range skill.Executor.Steps {
			if sub, ok := e.opts.Registry.GetByID(st.SkillID); ok {
				if c := e.concurrency(sub); c < n {
					n = c
				}
			}
		}
	}
	return n
}

// Apply runs a skill on the given files.
func (e *Engine) Apply(parent context.Context, fileIDs []string, skillID string, params map[string]any, report func(Progress)) (Outcome, error) {
	skill, ok := e.opts.Registry.GetByID(skillID)
	if !ok {
		return Outcome{}, fmt.Errorf("unknown skill %q", skillID)
	}
	if skill.IsMeta {
		return Outcome{}, fmt.Errorf("%s is not a file skill", skill.Name)
	}
	if err := e.CheckRunnable(skill); err != nil {
		return Outcome{}, err
	}
	if report == nil {
		report = func(Progress) {}
	}
	targets, skipped := e.partition(skill, fileIDs)
	if len(targets) == 0 {
		if len(skipped) > 0 {
			return Outcome{}, ops.Userf("%s doesn't work on %s", skill.Name, describeTypes(skipped))
		}
		return Outcome{}, ops.Userf("add some files first")
	}
	ctx, done, err := e.begin(parent)
	if err != nil {
		return Outcome{}, err
	}
	defer done()

	var out Outcome
	if skill.Combine {
		out, err = e.applyCombine(ctx, skill, params, targets, report)
	} else {
		out, err = e.applyEach(ctx, skill, params, targets, report)
	}
	if err != nil {
		return out, err
	}
	for _, f := range skipped {
		out.Skipped = append(out.Skipped, Problem{FileID: f.ID(), Name: f.DisplayName(), Reason: "not a supported type"})
	}
	out.Message = summarize(skill, out)
	return out, nil
}

func describeTypes(files []*session.File) string {
	seen := map[string]bool{}
	types := []string{}
	for _, f := range files {
		t := f.Current().Type
		if t == skills.OutputFolder {
			t = "folders"
		} else {
			t = strings.TrimPrefix(t, ".")
		}
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	return strings.Join(types, ", ") + " files"
}

func (e *Engine) partition(skill skills.Skill, ids []string) (targets, skipped []*session.File) {
	for _, id := range ids {
		f, ok := e.opts.Store.Get(id)
		if !ok {
			continue
		}
		if skill.Accepts(f.Current().Type) {
			targets = append(targets, f)
		} else {
			skipped = append(skipped, f)
		}
	}
	return targets, skipped
}

func (e *Engine) applyEach(ctx context.Context, skill skills.Skill, params map[string]any, targets []*session.File, report func(Progress)) (Outcome, error) {
	type result struct {
		file *session.File
		step session.Step
		err  error
	}
	total := len(targets)
	results := make([]result, total)
	fractions := make([]float64, total)
	var pmu sync.Mutex
	finished := 0
	lastEmit := time.Time{}
	emit := func(force bool, current string) {
		// caller holds pmu
		if !force && time.Since(lastEmit) < 80*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		sum := 0.0
		for _, f := range fractions {
			sum += f
		}
		report(Progress{Label: skill.Name, Done: finished, Total: total, Fraction: sum / float64(total), Current: current})
	}
	pmu.Lock()
	emit(true, "")
	pmu.Unlock()

	sem := make(chan struct{}, e.concurrency(skill))
	var wg sync.WaitGroup
	for i, f := range targets {
		if ctx.Err() != nil {
			results[i] = result{file: f, err: ctx.Err()}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f *session.File) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				results[i] = result{file: f, err: ctx.Err()}
				return
			}
			name := f.DisplayName()
			progress := func(v float64) {
				pmu.Lock()
				fractions[i] = v
				emit(false, name)
				pmu.Unlock()
			}
			step, err := e.runOnFile(ctx, f, skill, params, progress)
			results[i] = result{file: f, step: step, err: err}
			pmu.Lock()
			fractions[i] = 1
			finished++
			emit(true, name)
			pmu.Unlock()
		}(i, f)
	}
	wg.Wait()

	out := Outcome{}
	entry := undoEntry{label: skill.Name, steps: map[string]string{}}
	for _, r := range results {
		switch {
		case r.err == nil:
			r.file.Append(r.step)
			entry.steps[r.file.ID()] = r.step.ID
			out.Updated = append(out.Updated, r.file.View())
		case errors.Is(r.err, context.Canceled):
			out.Cancelled = true
		default:
			out.Failed = append(out.Failed, Problem{FileID: r.file.ID(), Name: r.file.DisplayName(), Reason: readable(r.err)})
		}
	}
	if len(entry.steps) > 0 {
		e.pushUndo(entry)
		e.opts.OnUsed(skill.ID)
	}
	return out, nil
}

func (e *Engine) applyCombine(ctx context.Context, skill skills.Skill, params map[string]any, targets []*session.File, report func(Progress)) (Outcome, error) {
	// One input keeps its name ("report.pdf" -> "report.zip"); several get
	// the skill's outputName ("merged.pdf").
	name := skill.OutputName
	if len(targets) == 1 || name == "" {
		first := targets[0]
		name = strings.TrimSuffix(first.DisplayName(), first.Current().Type)
		if first.Current().IsDir() {
			name = first.DisplayName()
		}
	}
	nf, err := e.opts.Store.NewCombined(name, targets)
	if err != nil {
		return Outcome{}, err
	}
	report(Progress{Label: skill.Name, Total: 1})
	inputs := make([]string, len(targets))
	names := make([]string, len(targets))
	for i, f := range targets {
		inputs[i] = f.Current().Path
		names[i] = f.DisplayName()
	}
	outType := skill.OutputType
	outPath := nf.NewOutputPath(skill.ID, outType)
	h, _ := ops.Lookup(skill.Executor.Handler)
	job := ops.Prepare(ops.Job{
		Inputs: inputs, Names: names, Output: outPath,
		Args:     ops.Merge(fixedArgs(skill), params),
		Tools:    e.opts.Tools,
		Progress: func(v float64) { report(Progress{Label: skill.Name, Total: 1, Fraction: v}) },
	})
	var runErr error
	if skill.Executor.Type == "native" {
		runErr = h.Run(ctx, job)
	} else {
		runErr = ops.RunCLI(ctx, ops.CLISpec{Command: skill.Executor.Command, Args: skill.Executor.Args, TimeoutMs: skill.Executor.TimeoutMs}, job)
	}
	if runErr != nil {
		e.opts.Store.Discard(nf)
		if errors.Is(runErr, context.Canceled) {
			return Outcome{Cancelled: true}, nil
		}
		return Outcome{}, runErr
	}
	info := inspect.Describe(ctx, e.opts.Tools, outPath, outType)
	step := session.NewStep(skill, params, outPath, outType, info)
	nf.Append(step)
	e.opts.Store.Publish(nf, targets[len(targets)-1].ID())
	e.pushUndo(undoEntry{label: skill.Name, added: nf.ID()})
	e.opts.OnUsed(skill.ID)
	report(Progress{Label: skill.Name, Done: 1, Total: 1, Fraction: 1})
	return Outcome{Added: []session.View{nf.View()}}, nil
}

// runOnFile runs a per-file skill on f's current output and returns the new
// step without attaching it.
func (e *Engine) runOnFile(ctx context.Context, f *session.File, skill skills.Skill, params map[string]any, progress func(float64)) (session.Step, error) {
	cur := f.Current()
	path, typ, err := e.execute(ctx, f, skill, params, cur.Path, cur.Type, f.DisplayName(), progress, 0)
	if err != nil {
		return session.Step{}, err
	}
	info := inspect.Describe(ctx, e.opts.Tools, path, typ)
	return session.NewStep(skill, params, path, typ, info), nil
}

// execute runs one skill (recursing into pipelines) and returns the output
// path and type.
func (e *Engine) execute(ctx context.Context, f *session.File, skill skills.Skill, params map[string]any, inPath, inType, displayName string, progress func(float64), depth int) (string, string, error) {
	if depth > maxPipelineDepth {
		return "", "", ops.Userf("pipeline is nested too deeply")
	}
	if !skill.Accepts(inType) {
		return "", "", ops.Userf("%s doesn't accept %s files", skill.Name, strings.TrimPrefix(inType, "."))
	}
	switch skill.Executor.Type {
	case "pipeline":
		steps := skill.Executor.Steps
		curPath, curType := inPath, inType
		var temps []string
		defer func() {
			for _, t := range temps {
				_ = os.RemoveAll(t)
			}
		}()
		for i, st := range steps {
			sub, ok := e.opts.Registry.GetByID(st.SkillID)
			if !ok {
				return "", "", ops.Userf("pipeline step %q does not exist", st.SkillID)
			}
			if sub.IsMeta || sub.Combine {
				return "", "", ops.Userf("pipeline step %s can't run inside a pipeline", sub.Name)
			}
			stepProgress := func(v float64) { progress((float64(i) + v) / float64(len(steps))) }
			name := displayNameFor(displayName, curType)
			p, t, err := e.execute(ctx, f, sub, ops.Merge(params, st.Params), curPath, curType, name, stepProgress, depth+1)
			if err != nil {
				return "", "", fmt.Errorf("%s: %w", sub.Name, err)
			}
			if i < len(steps)-1 {
				temps = append(temps, p)
			}
			curPath, curType = p, t
		}
		return curPath, curType, nil
	case "native", "cli":
	default:
		return "", "", fmt.Errorf("%s cannot run on files", skill.Name)
	}

	outType := skill.OutputType
	if outType == "" {
		outType = inType
	}
	var handler ops.Handler
	if skill.Executor.Type == "native" {
		h, ok := ops.Lookup(skill.Executor.Handler)
		if !ok {
			return "", "", fmt.Errorf("unknown handler %s", skill.Executor.Handler)
		}
		handler = h
		if h.OutputExt != nil && outType != skills.OutputFolder {
			outType = h.OutputExt(outType, inType)
		}
	}
	outPath := f.NewOutputPath(skill.ID, outType)
	job := ops.Prepare(ops.Job{
		Inputs:   []string{inPath},
		Names:    []string{displayName},
		Output:   outPath,
		Args:     ops.Merge(fixedArgs(skill), params),
		Tools:    e.opts.Tools,
		Progress: progress,
	})
	var err error
	if skill.Executor.Type == "native" && handler.Pixel != nil && ops.IsImageType(inType) && ops.IsImageType(outType) {
		err = e.runPixel(ctx, f, handler, job.Args, inPath, outPath, progress)
	} else if skill.Executor.Type == "native" {
		err = handler.Run(ctx, job)
	} else {
		err = ops.RunCLI(ctx, ops.CLISpec{Command: skill.Executor.Command, Args: skill.Executor.Args, TimeoutMs: skill.Executor.TimeoutMs}, job)
	}
	if err != nil {
		_ = os.RemoveAll(outPath)
		return "", "", err
	}
	if _, statErr := os.Stat(outPath); statErr != nil {
		return "", "", fmt.Errorf("%s produced no output", skill.Name)
	}
	return outPath, outType, nil
}

func displayNameFor(name, typ string) string {
	ext := skills.FileType(name, false)
	if len(ext) > len(name) || !strings.EqualFold(name[len(name)-len(ext):], ext) {
		ext = filepath.Ext(name)
	}
	stem := name[:len(name)-len(ext)]
	if typ == skills.OutputFolder {
		return stem
	}
	return stem + typ
}

func readable(err error) string {
	var ue *ops.UserError
	if errors.As(err, &ue) {
		return ue.Msg
	}
	var mt *ops.MissingToolError
	if errors.As(err, &mt) {
		return mt.Error()
	}
	return err.Error()
}

// Readable turns an error into a message for the UI.
func Readable(err error) string { return readable(err) }

func summarize(skill skills.Skill, out Outcome) string {
	ok := len(out.Updated) + len(out.Added)
	parts := []string{}
	switch {
	case len(out.Added) > 0:
		parts = append(parts, fmt.Sprintf("%s: created %s", skill.Name, out.Added[0].DisplayName))
	case ok > 0:
		parts = append(parts, fmt.Sprintf("%s: %s", skill.Name, plural(ok, "file")))
	}
	if len(out.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", len(out.Failed)))
	}
	if len(out.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d left as is", len(out.Skipped)))
	}
	if out.Cancelled {
		parts = append(parts, "cancelled")
	}
	if len(parts) == 0 {
		return skill.Name + ": nothing changed"
	}
	return strings.Join(parts, " · ")
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

func (e *Engine) pushUndo(u undoEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.undo = append(e.undo, u)
	if len(e.undo) > 50 {
		e.undo = e.undo[len(e.undo)-50:]
	}
}

// Undo reverts the most recent Apply that can still be reverted.
func (e *Engine) Undo(parent context.Context) (Outcome, error) {
	_, done, err := e.begin(parent)
	if err != nil {
		return Outcome{}, err
	}
	defer done()
	e.mu.Lock()
	if len(e.undo) == 0 {
		e.mu.Unlock()
		return Outcome{Message: "Nothing to undo"}, nil
	}
	u := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.mu.Unlock()

	out := Outcome{}
	if u.added != "" {
		if _, ok := e.opts.Store.Get(u.added); ok {
			e.opts.Store.Remove([]string{u.added})
			out.Removed = append(out.Removed, u.added)
		}
	}
	for fileID, stepID := range u.steps {
		f, ok := e.opts.Store.Get(fileID)
		if !ok {
			continue
		}
		if f.PopStep(stepID) {
			out.Updated = append(out.Updated, f.View())
		}
	}
	if len(out.Updated) == 0 && len(out.Removed) == 0 {
		out.Message = "Nothing to undo for " + u.label
	} else {
		out.Message = "Undid " + u.label
	}
	return out, nil
}

// RemoveStep deletes one step from a file's chain and replays the steps
// after it from the previous output. Removing the first step of a combined
// file removes the file.
func (e *Engine) RemoveStep(parent context.Context, fileID string, index int, report func(Progress)) (Outcome, error) {
	f, ok := e.opts.Store.Get(fileID)
	if !ok {
		return Outcome{}, errors.New("file not found")
	}
	steps := f.Steps()
	if index < 0 || index >= len(steps) {
		return Outcome{}, errors.New("invalid step")
	}
	if f.Combined() && index == 0 {
		e.opts.Store.Remove([]string{fileID})
		return Outcome{Removed: []string{fileID}, Message: "Removed " + steps[0].Label}, nil
	}
	ctx, done, err := e.begin(parent)
	if err != nil {
		return Outcome{}, err
	}
	defer done()
	if report == nil {
		report = func(Progress) {}
	}

	kept := append([]session.Step{}, steps[:index]...)
	replay := steps[index+1:]
	base := f.Original()
	if len(kept) > 0 {
		base = kept[len(kept)-1]
	}
	curPath, curType := base.Path, base.Type
	out := Outcome{}
	for i, st := range replay {
		skill, ok := e.opts.Registry.GetByID(st.SkillID)
		if !ok {
			out.Message = fmt.Sprintf("Removed %s, but %s no longer exists; later steps were dropped", steps[index].Label, st.SkillID)
			break
		}
		label := "Replaying " + skill.Name
		progress := func(v float64) {
			report(Progress{Label: label, Done: i, Total: len(replay), Fraction: (float64(i) + v) / float64(len(replay))})
		}
		name := displayNameFor(f.DisplayName(), curType)
		p, t, err := e.execute(ctx, f, skill, st.Params, curPath, curType, name, progress, 0)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// Leave the chain untouched.
				for _, s := range kept[index:] {
					_ = os.RemoveAll(s.Path)
				}
				return Outcome{Cancelled: true, Message: "Cancelled; nothing changed"}, nil
			}
			out.Failed = append(out.Failed, Problem{FileID: fileID, Name: f.DisplayName(), Reason: readable(err)})
			out.Message = fmt.Sprintf("Removed %s, but %s failed on replay: %s. Later steps were dropped.", steps[index].Label, skill.Name, readable(err))
			break
		}
		info := inspect.Describe(ctx, e.opts.Tools, p, t)
		ns := session.NewStep(skill, st.Params, p, t, info)
		kept = append(kept, ns)
		curPath, curType = p, t
	}
	f.ReplaceSteps(kept)
	if out.Message == "" {
		out.Message = "Removed " + steps[index].Label
	}
	out.Updated = []session.View{f.View()}
	return out, nil
}

// fixedArgs are a skill's fixed "with" arguments. CLI skills also get each
// parameter's default, since their command templates ({{zoom}}) have no
// fallback of their own; native handlers apply their own defaults.
func fixedArgs(skill skills.Skill) map[string]any {
	if skill.Executor.Type == "native" {
		return skill.Executor.With
	}
	out := map[string]any{}
	for _, p := range skill.Params {
		if p.Default != nil {
			out[p.Name] = p.Default
		}
	}
	for k, v := range skill.Executor.With {
		out[k] = v
	}
	return out
}
