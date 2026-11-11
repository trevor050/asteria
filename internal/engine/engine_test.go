package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"asteria/internal/inspect"
	"asteria/internal/ops"
	"asteria/internal/session"
	"asteria/internal/skills"
	"asteria/internal/tools"
)

const fixtures = "../../testdata/fixtures"

type env struct {
	t       *testing.T
	eng     *Engine
	store   *session.Store
	reg     *skills.Registry
	src     string
	trusted map[string]bool
}

func newEnv(t *testing.T, community string) *env {
	t.Helper()
	reg := skills.NewRegistry(skills.RegistryOptions{
		EmbeddedFS: os.DirFS("../../skills"), EmbeddedRoot: "core", CommunityRoot: community,
	})
	if p := reg.Problems(); len(p) > 0 {
		t.Fatalf("skill problems: %v", p)
	}
	ws, err := session.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(ws)
	e := &env{t: t, store: store, reg: reg, src: t.TempDir(), trusted: map[string]bool{}}
	e.eng = New(Options{Registry: reg, Store: store, Tools: tools.NewFinder(), IsTrusted: func(id string) bool { return e.trusted[id] }})
	return e
}

// add copies a fixture into the source dir (optionally renamed) and adds it.
func (e *env) add(fixture, as string) *session.File {
	e.t.Helper()
	if as == "" {
		as = fixture
	}
	b, err := os.ReadFile(filepath.Join(fixtures, fixture))
	if err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(e.src, as)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	f, err := e.store.Add(p, func(path, typ string) inspect.Info { return inspect.Describe(context.Background(), nil, path, typ) })
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *env) apply(skillID string, params map[string]any, files ...*session.File) Outcome {
	e.t.Helper()
	ids := []string{}
	for _, f := range files {
		ids = append(ids, f.ID())
	}
	out, err := e.eng.Apply(context.Background(), ids, skillID, params, nil)
	if err != nil {
		e.t.Fatalf("apply %s: %v", skillID, err)
	}
	return out
}

func snapshot(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, en.Name()))
		out[en.Name()] = b
	}
	return out
}

func dims(t *testing.T, path string) (int, int, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return cfg.Width, cfg.Height, format
}

func TestBatchConvertSkipsWrongTypesAndNeverTouchesOriginals(t *testing.T) {
	e := newEnv(t, "")
	jpg, heic, csv := e.add("sample.jpg", ""), e.add("sample.heic", ""), e.add("people.csv", "")
	before := snapshot(t, e.src)

	out := e.apply("convert_to_png", nil, jpg, heic, csv)
	if len(out.Updated) != 2 || len(out.Skipped) != 1 || out.Skipped[0].Name != "people.csv" {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.Contains(out.Message, "2 files") || !strings.Contains(out.Message, "1 left as is") {
		t.Fatalf("message = %q", out.Message)
	}
	for _, f := range []*session.File{jpg, heic} {
		cur := f.Current()
		if cur.Type != ".png" || f.DisplayName() != "sample.png" {
			t.Fatalf("current = %+v", cur)
		}
		if w, h, format := dims(t, cur.Path); w != 120 || h != 80 || format != "png" {
			t.Fatalf("output %dx%d %s", w, h, format)
		}
		if !strings.HasPrefix(cur.Path, e.store.Workspace().Root) {
			t.Fatalf("output written outside the workspace: %s", cur.Path)
		}
	}
	after := snapshot(t, e.src)
	for name, b := range before {
		if !bytes.Equal(b, after[name]) {
			t.Fatalf("original %s was modified", name)
		}
	}
	if len(after) != len(before) {
		t.Fatalf("files appeared next to the originals: %v", after)
	}
}

func TestRemovingAMiddleStepReplaysTheRest(t *testing.T) {
	e := newEnv(t, "")
	f := e.add("sample.jpg", "")
	e.apply("resize", map[string]any{"size": "50%"}, f)
	e.apply("grayscale", nil, f)
	e.apply("convert_to_png", nil, f)
	if w, _, _ := dims(t, f.Current().Path); w != 60 {
		t.Fatalf("width before removal = %d", w)
	}
	firstGray := f.Steps()[1].Path

	out, err := e.eng.RemoveStep(context.Background(), f.ID(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := f.Steps()
	if len(steps) != 2 || steps[0].SkillID != "grayscale" || steps[1].SkillID != "convert_to_png" {
		t.Fatalf("steps after removal = %+v", steps)
	}
	w, h, format := dims(t, f.Current().Path)
	if w != 120 || h != 80 || format != "png" {
		t.Fatalf("replayed output %dx%d %s", w, h, format)
	}
	if _, err := os.Stat(firstGray); !os.IsNotExist(err) {
		t.Fatal("stale step output was not cleaned up")
	}
	if !strings.HasPrefix(out.Message, "Removed Resize 50%") {
		t.Fatalf("message = %q", out.Message)
	}
}

func TestUndoPopsTheLastApply(t *testing.T) {
	e := newEnv(t, "")
	a, b := e.add("sample.jpg", "a.jpg"), e.add("sample.png", "b.png")
	e.apply("grayscale", nil, a, b)
	e.apply("convert_to_webp", nil, a, b)
	out, err := e.eng.Undo(context.Background())
	if err != nil || len(out.Updated) != 2 {
		t.Fatalf("undo = %+v, %v", out, err)
	}
	if a.Current().Type != ".jpg" || len(a.Steps()) != 1 || b.Current().Type != ".png" {
		t.Fatalf("undo left %s / %s", a.Current().Type, b.Current().Type)
	}
}

func TestCombineCreatesANewFileAndUndoRemovesIt(t *testing.T) {
	e := newEnv(t, "")
	p1, p2 := e.add("three-pages.pdf", "a.pdf"), e.add("three-pages.pdf", "b.pdf")
	img := e.add("sample.jpg", "")
	out := e.apply("merge_pdfs", nil, p1, p2, img)
	if len(out.Added) != 1 || len(out.Skipped) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	merged := out.Added[0]
	if merged.DisplayName != "merged.pdf" || merged.Info.Pages != 6 || merged.Origin != "combined" {
		t.Fatalf("merged = %+v", merged)
	}
	if strings.Join(merged.Sources, ",") != "a.pdf,b.pdf" {
		t.Fatalf("sources = %v", merged.Sources)
	}
	views := e.store.Views()
	if len(views) != 4 || views[2].ID != merged.ID {
		t.Fatal("merged file should appear right after the last input")
	}
	if _, err := e.eng.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.store.Views()) != 3 {
		t.Fatal("undo did not remove the merged file")
	}
}

func TestSplitThenZipTheFolder(t *testing.T) {
	e := newEnv(t, "")
	pdf := e.add("three-pages.pdf", "report.pdf")
	e.apply("split_pdf", nil, pdf)
	cur := pdf.Current()
	if !cur.IsDir() || cur.Info.Count != 3 || pdf.DisplayName() != "report" {
		t.Fatalf("split result = %+v (%s)", cur, pdf.DisplayName())
	}
	out := e.apply("zip", nil, pdf)
	z := out.Added[0]
	if z.DisplayName != "report.zip" || z.Info.Count != 3 {
		t.Fatalf("zip = %+v", z)
	}
	zf, _ := e.store.Get(z.ID)
	zr, err := zip.OpenReader(zf.Current().Path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if names[1] != "report/page-001.pdf" {
		t.Fatalf("zip entries = %v", names)
	}
}

func TestFailuresAreReportedPerFile(t *testing.T) {
	e := newEnv(t, "")
	good := e.add("sample.jpg", "good.jpg")
	bad := e.add("people.csv", "broken.jpg") // not really a JPEG
	out := e.apply("resize", map[string]any{"size": "50%"}, good, bad)
	if len(out.Updated) != 1 || len(out.Failed) != 1 || out.Failed[0].Name != "broken.jpg" {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.Contains(out.Failed[0].Reason, "not an image") {
		t.Fatalf("reason = %q", out.Failed[0].Reason)
	}
	if len(bad.Steps()) != 0 {
		t.Fatal("failed file got a step")
	}
}

func TestBadParameterFailsCleanly(t *testing.T) {
	e := newEnv(t, "")
	f := e.add("sample.jpg", "")
	out := e.apply("resize", map[string]any{"size": "banana"}, f)
	if len(out.Failed) != 1 || len(f.Steps()) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	entries, _ := os.ReadDir(f.Dir())
	if len(entries) != 0 {
		t.Fatalf("failed step left files: %v", entries)
	}
}

func TestUnicodeAndOddFilenamesExport(t *testing.T) {
	e := newEnv(t, "")
	f := e.add("sample.jpg", "fötø 🌅 (1) #&.jpg")
	e.apply("convert_to_png", nil, f)
	dest := t.TempDir()
	p, err := session.ExportFile(context.Background(), f, dest, session.DefaultNamingPattern)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "fötø 🌅 (1) #&.png" {
		t.Fatalf("exported as %q", filepath.Base(p))
	}
	if _, _, format := dims(t, p); format != "png" {
		t.Fatal("exported file is not a PNG")
	}
}

func TestExportNeverOverwrites(t *testing.T) {
	e := newEnv(t, "")
	f := e.add("sample.jpg", "")
	e.apply("compress", map[string]any{"quality": 50}, f)
	original, _ := os.ReadFile(filepath.Join(e.src, "sample.jpg"))
	// Default folder is next to the original, which already has sample.jpg.
	p1, err := session.ExportFile(context.Background(), f, "", session.DefaultNamingPattern)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := session.ExportFile(context.Background(), f, "", session.DefaultNamingPattern)
	if filepath.Base(p1) != "sample (1).jpg" || filepath.Base(p2) != "sample (2).jpg" {
		t.Fatalf("exported %s, %s", filepath.Base(p1), filepath.Base(p2))
	}
	now, _ := os.ReadFile(filepath.Join(e.src, "sample.jpg"))
	if !bytes.Equal(original, now) {
		t.Fatal("export overwrote the original")
	}
}

func TestExampleCommunityPipelineSkillRuns(t *testing.T) {
	e := newEnv(t, "../../skills/examples")
	f := e.add("sample.heic", "")
	out := e.apply("web_thumbnail", nil, f)
	if len(out.Updated) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	w, h, format := dims(t, f.Current().Path)
	if format != "webp" || w != 120 || h != 80 {
		t.Fatalf("thumbnail = %dx%d %s", w, h, format)
	}
	if f.Steps()[0].Label != "Web Thumbnail" {
		t.Fatalf("label = %q", f.Steps()[0].Label)
	}
	// Intermediate pipeline outputs are cleaned up; only the result remains.
	entries, _ := os.ReadDir(f.Dir())
	if len(entries) != 1 {
		t.Fatalf("workspace has %d entries, want 1", len(entries))
	}
}

func TestCommunityCLISkillNeedsTrust(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses cp")
	}
	dir := t.TempDir()
	def := `{"id":"copy_it","name":"Copy It","version":"1","inputTypes":["*"],"outputType":".txt",
	  "executor":{"type":"cli","command":"cp","args":["{{input}}","{{output}}"]},
	  "permissions":["files.read","files.write","tools.exec","tools.exec.any"]}`
	if err := os.WriteFile(filepath.Join(dir, "copy.json"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, dir)
	f := e.add("notes.md", "")
	_, err := e.eng.Apply(context.Background(), []string{f.ID()}, "copy_it", nil, nil)
	var trust *TrustRequiredError
	if !errors.As(err, &trust) || trust.Permissions[0] != "tools.exec.any" {
		t.Fatalf("want TrustRequiredError, got %v", err)
	}
	e.trusted["copy_it"] = true
	e.apply("copy_it", nil, f)
	got, _ := os.ReadFile(f.Current().Path)
	want, _ := os.ReadFile(filepath.Join(fixtures, "notes.md"))
	if !bytes.Equal(got, want) {
		t.Fatal("cli skill output differs")
	}
}

func TestMissingToolStopsBeforeRunning(t *testing.T) {
	e := newEnv(t, "")
	t.Setenv("PATH", "")
	e.eng.opts.Tools = tools.NewFinderWithDirs(nil)
	f := e.add("tone.wav", "")
	_, err := e.eng.Apply(context.Background(), []string{f.ID()}, "convert_to_mp3", nil, nil)
	var missing *ops.MissingToolError
	if !errors.As(err, &missing) || missing.Tool != "ffmpeg" {
		t.Fatalf("want missing ffmpeg, got %v", err)
	}
}

func TestCancelMidBatchKeepsFilesUnchanged(t *testing.T) {
	if _, ok := tools.NewFinder().Lookup("ffmpeg"); !ok {
		t.Skip("ffmpeg not installed")
	}
	e := newEnv(t, "")
	files := []*session.File{}
	ids := []string{}
	for _, n := range []string{"a", "b", "c", "d"} {
		f := e.add("clip.mp4", n+".mp4")
		files = append(files, f)
		ids = append(ids, f.ID())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := false
	out, err := e.eng.Apply(ctx, ids, "convert_to_webm", nil, func(p Progress) {
		if !cancelled && p.Fraction > 0 {
			cancelled = true
			cancel()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Cancelled {
		t.Fatalf("outcome = %+v", out)
	}
	if len(out.Updated) == len(files) {
		t.Fatal("cancel did not stop the batch")
	}
	for _, f := range files {
		entries, _ := os.ReadDir(f.Dir())
		if len(entries) != len(f.Steps()) {
			t.Fatalf("%s has stray outputs after cancel", f.DisplayName())
		}
	}
}

func TestBusyRejectsConcurrentOperations(t *testing.T) {
	e := newEnv(t, "")
	_, done, err := e.eng.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	f := e.add("sample.jpg", "")
	if _, err := e.eng.Apply(context.Background(), []string{f.ID()}, "grayscale", nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}
