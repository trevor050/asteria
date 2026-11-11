// Package ops holds Asteria's native operations. Skills reference them by
// handler name ("image.convert", "pdf.merge", ...) from their JSON definition,
// so new skills can reuse an operation with different fixed arguments without
// recompiling the app.
package ops

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"asteria/internal/img"
	"asteria/internal/tools"
)

// Job is a single invocation of a handler.
type Job struct {
	// Inputs holds one path for per-file handlers and one or more paths for
	// combine handlers. Inputs are never modified.
	Inputs []string
	// Names are the user-facing file names (with extension) for Inputs, used
	// where names end up inside the output (archive entries).
	Names []string
	// Output is the path the handler must create. For folder outputs it is a
	// directory path that does not exist yet.
	Output string
	// Args are the skill's fixed "with" arguments merged with the user's
	// parameter values (user values win).
	Args Args
	// Progress reports completion of this job in the range [0,1]. Never nil.
	Progress func(float64)
	// Tools resolves optional external programs.
	Tools *tools.Finder
}

// Input returns the first input path.
func (j Job) Input() string {
	if len(j.Inputs) == 0 {
		return ""
	}
	return j.Inputs[0]
}

// Name returns the display name for input i, falling back to its base name.
func (j Job) Name(i int) string {
	if i < len(j.Names) && j.Names[i] != "" {
		return j.Names[i]
	}
	if i < len(j.Inputs) {
		return filepath.Base(j.Inputs[i])
	}
	return ""
}

// Handler is a named native operation.
type Handler struct {
	Name string
	// Run performs the operation.
	Run func(ctx context.Context, job Job) error
	// Combine is true when the handler accepts several inputs at once.
	Combine bool
	// OutputExt optionally adjusts the output extension chosen by the skill
	// (for example, image handlers cannot encode HEIC, so they fall back to
	// JPEG). It receives the planned extension and the input extension.
	OutputExt func(planned, input string) string
	// Requires lists external tools the handler needs, e.g. "ffmpeg".
	Requires []string
	// Concurrency caps how many jobs of this handler run in parallel.
	// Zero means the engine default.
	Concurrency int

	// Pixel, when set, makes this an image operation on the shared pixel
	// core: it receives the lossless working image and returns a new one
	// (never modifying its input). The engine supplies the input from its
	// cache or by replaying the chain from the last real file, encodes the
	// result once, and can run it on a small proxy for live previews. Run
	// is filled in automatically for Pixel handlers.
	Pixel PixelFunc
	// Live marks Pixel operations fast enough to preview while a value is
	// being dragged (on a ~1 megapixel proxy).
	Live bool
	// Video optionally maps the same arguments to an ffmpeg -vf filter so
	// the skill also works on video files.
	Video func(args Args) (string, error)
}

// PixelFunc is an image operation on the shared pixel core.
type PixelFunc func(ctx context.Context, m *img.Image, args Args) (*img.Image, error)

// ErrUnsupported marks input the handler cannot process. The engine reports it
// as a skipped file instead of a failure.
var ErrUnsupported = errors.New("unsupported input")

// UserError is an error whose message is safe and useful to show verbatim.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// Userf builds a UserError.
func Userf(format string, args ...any) error {
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

var registry = map[string]Handler{}

func register(h Handler) {
	if _, exists := registry[h.Name]; exists {
		panic("ops: duplicate handler " + h.Name)
	}
	if h.Pixel != nil {
		if h.Run == nil {
			h.Run = standalonePixel(h)
		}
		if h.OutputExt == nil {
			h.OutputExt = pixelOutputExt
		}
		if h.Concurrency == 0 {
			h.Concurrency = 3
		}
	}
	registry[h.Name] = h
}

// Lookup returns the handler with the given name.
func Lookup(name string) (Handler, bool) {
	h, ok := registry[strings.TrimSpace(name)]
	return h, ok
}

// Names returns all registered handler names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func noProgress(float64) {}

// Prepare fills in defaults on a job before running it.
func Prepare(job Job) Job {
	if job.Progress == nil {
		job.Progress = noProgress
	}
	if job.Args == nil {
		job.Args = Args{}
	}
	if job.Tools == nil {
		job.Tools = tools.NewFinder()
	}
	return job
}
