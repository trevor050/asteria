package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asteria/internal/ops"
)

func coreRegistry(t *testing.T, community string) *Registry {
	t.Helper()
	r := NewRegistry(RegistryOptions{
		EmbeddedFS:    os.DirFS("../../skills"),
		EmbeddedRoot:  "core",
		CommunityRoot: community,
	})
	if p := r.Problems(); len(p) > 0 {
		t.Fatalf("skills failed to load: %v", p)
	}
	return r
}

func TestAllCoreSkillsLoadAndUseRealHandlers(t *testing.T) {
	r := coreRegistry(t, "")
	list := r.List()
	if len(list) < 30 {
		t.Fatalf("only %d core skills loaded", len(list))
	}
	for _, s := range list {
		if s.Source != SkillSourceCoreEmbedded {
			t.Errorf("%s source = %s", s.ID, s.Source)
		}
		switch s.Executor.Type {
		case "native":
			if _, ok := ops.Lookup(s.Executor.Handler); !ok {
				t.Errorf("%s uses unknown handler %s", s.ID, s.Executor.Handler)
			}
		case "meta":
		default:
			t.Errorf("core skill %s uses %s; built-ins must be native or meta", s.ID, s.Executor.Type)
		}
		for _, in := range s.InputTypes {
			if strings.HasPrefix(in, "@") {
				t.Errorf("%s: group %s was not expanded", s.ID, in)
			}
		}
	}
	merge, _ := r.GetByID("merge_pdfs")
	if !merge.Combine {
		t.Error("merge_pdfs should be inferred as a combine skill")
	}
	mp3, _ := r.GetByID("convert_to_mp3")
	if len(mp3.Requires) != 1 || mp3.Requires[0] != "ffmpeg" {
		t.Errorf("convert_to_mp3 requires = %v", mp3.Requires)
	}
	if !mp3.Accepts(".mov") || !mp3.Accepts(".wav") || mp3.Accepts(".pdf") {
		t.Error("@media expansion is wrong")
	}
}

func TestExampleSkillLoadsAsCommunity(t *testing.T) {
	r := coreRegistry(t, "../../skills/examples")
	s, ok := r.GetByID("web_thumbnail")
	if !ok {
		t.Fatal("example skill not loaded")
	}
	if s.Source != SkillSourceCommunity || s.Executor.Type != "pipeline" || s.OutputType != ".webp" {
		t.Fatalf("example skill = %+v", s)
	}
	for _, step := range s.Executor.Steps {
		if _, ok := r.GetByID(step.SkillID); !ok {
			t.Errorf("example references missing skill %s", step.SkillID)
		}
	}
}

func writeSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCommunitySkillsAreReportedNotLoaded(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "meta.json", `{"id":"sneaky","name":"Sneaky","version":"1","executor":{"type":"meta"}}`)
	writeSkill(t, dir, "two.json", `{"id":"two","name":"Two","version":"1","inputTypes":["@image"],"params":[{"name":"a"},{"name":"b"}],"executor":{"type":"native","handler":"image.resize"}}`)
	writeSkill(t, dir, "nohandler.json", `{"id":"nope","name":"Nope","version":"1","inputTypes":["*"],"executor":{"type":"native","handler":"image.teleport"}}`)
	writeSkill(t, dir, "broken.json", `{"id": `)
	writeSkill(t, dir, "ok.json", `{"id":"jpeg_small","name":"JPEG Small","version":"1","inputTypes":["@image"],"outputType":"jpg","executor":{"type":"native","handler":"image.convert","with":{"quality":60}}}`)
	r := NewRegistry(RegistryOptions{EmbeddedFS: os.DirFS("../../skills"), EmbeddedRoot: "core", CommunityRoot: dir})
	problems := strings.Join(r.Problems(), "\n")
	for _, want := range []string{"meta skills are built in", "at most one parameter", "unknown native handler", "parse"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems missing %q:\n%s", want, problems)
		}
	}
	for _, id := range []string{"sneaky", "two", "nope"} {
		if _, ok := r.GetByID(id); ok {
			t.Errorf("invalid skill %s was loaded", id)
		}
	}
	s, ok := r.GetByID("jpeg_small")
	if !ok || s.OutputType != ".jpg" || s.Executor.With["quality"] != float64(60) {
		t.Fatalf("valid community skill not loaded correctly: %+v", s)
	}
}

func top(t *testing.T, r *Registry, q string, types ...string) Match {
	t.Helper()
	m := r.Search(q, Context{InputTypes: types})
	if len(m) == 0 {
		t.Fatalf("no results for %q", q)
	}
	return m[0]
}

func TestSearchFindsWhatPeopleType(t *testing.T) {
	r := coreRegistry(t, "")
	cases := []struct {
		q, types, want, value string
	}{
		{"jpg", ".heic", "convert_to_jpeg", ""},
		{"resize 50%", ".png", "resize", "50%"},
		{"rs50", ".png", "resize", "50"},
		{"jpg80", ".png", "convert_to_jpeg", "80"},
		{"cvjpg", ".png", "convert_to_jpeg", ""},
		{"conv jp", ".png", "convert_to_jpeg", ""},
		{"merge", ".pdf", "merge_pdfs", ""},
		{"extract pages 1-3, 5", ".pdf", "extract_pages", "1-3, 5"},
		{"trim 0:05-0:30", ".mp4", "trim", "0:05-0:30"},
		{"mp3", ".mov", "convert_to_mp3", ""},
		{"extract audio", ".mov", "convert_to_mp3", ""},
		{"unzip", ".zip", "extract_archive", ""},
		{"comprss", ".jpg", "compress", ""},
		{"export", ".jpg", "export", ""},
	}
	for _, c := range cases {
		m := top(t, r, c.q, c.types)
		if m.Skill.ID != c.want || m.Value != c.value {
			t.Errorf("%q on %s -> %s (value %q); want %s (value %q)", c.q, c.types, m.Skill.ID, m.Value, c.want, c.value)
		}
	}
}

func TestSearchPrefersSkillsThatFitTheFiles(t *testing.T) {
	r := coreRegistry(t, "")
	// "compress" on a PDF should offer Compress PDF before image/video compress.
	if m := top(t, r, "compress", ".pdf"); m.Skill.ID != "compress_pdf" {
		t.Errorf("compress on pdf -> %s", m.Skill.ID)
	}
	if m := top(t, r, "compress", ".mov"); m.Skill.ID != "compress_video" {
		t.Errorf("compress on mov -> %s", m.Skill.ID)
	}
	// Empty query only lists skills that apply (plus meta skills).
	for _, m := range r.Search("", Context{InputTypes: []string{".csv"}}) {
		if !m.Skill.IsMeta && !m.Skill.Accepts(".csv") {
			t.Errorf("empty query on csv listed %s", m.Skill.ID)
		}
	}
}

func TestConvertToSameFormatDropsLower(t *testing.T) {
	r := coreRegistry(t, "")
	list := r.Search("", Context{InputTypes: []string{".png"}})
	pos := map[string]int{}
	for i, m := range list {
		pos[m.Skill.ID] = i
	}
	if pos["convert_to_png"] < pos["convert_to_jpeg"] {
		t.Error("Convert to PNG should rank below Convert to JPEG for PNG files")
	}
}

func TestFrecencyBreaksTies(t *testing.T) {
	r := coreRegistry(t, "")
	usage := map[string]UsageStats{"convert_to_m4a": {Count: 5, LastUsed: time.Now()}}
	m := r.Search("convert to", Context{InputTypes: []string{".wav"}, Usage: usage})
	if m[0].Skill.ID != "convert_to_m4a" {
		t.Errorf("frequent skill not first: %s", m[0].Skill.ID)
	}
}

func TestSplitInline(t *testing.T) {
	cases := map[string][2]string{
		"resize 50%":           {"resize", "50%"},
		"resize":               {"resize", ""},
		"rotate 180":           {"rotate", "180"},
		"extract pages 1-3, 5": {"extract pages", "1-3, 5"},
		"mp3":                  {"mp3", ""},
		"50%":                  {"50%", ""},
	}
	for in, want := range cases {
		a, b := SplitInline(in)
		if a != want[0] || b != want[1] {
			t.Errorf("SplitInline(%q) = %q,%q; want %q,%q", in, a, b, want[0], want[1])
		}
	}
}

func TestFileType(t *testing.T) {
	if FileType("Backup.TAR.GZ", false) != ".tar.gz" || FileType("a.JPG", false) != ".jpg" || FileType("x", true) != "folder" {
		t.Fatal("FileType")
	}
}

func TestStrongMatchesHideFuzzyNoise(t *testing.T) {
	r := coreRegistry(t, "")
	for _, m := range r.Search("comp", Context{InputTypes: []string{".jpg", ".mov"}}) {
		if strings.HasPrefix(m.Skill.Name, "Convert to") {
			t.Errorf("%q matched %s", "comp", m.Skill.Name)
		}
	}
}

func TestHotReloadPicksUpMovedSkillDirectory(t *testing.T) {
	base := t.TempDir()
	community := filepath.Join(base, "community")
	pack := filepath.Join(base, "ready-pack")
	for _, dir := range []string{community, pack} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(t, pack, "small.json", `{"id":"pack_resize","name":"Pack Resize","version":"1","inputTypes":["@image"],"executor":{"type":"native","handler":"image.resize","with":{"size":"50%"}}}`)
	r := coreRegistry(t, community)
	if err := r.StartHotReload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pack, filepath.Join(community, "new-pack")); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if _, ok := r.GetByID("pack_resize"); ok {
			return
		}
		select {
		case <-r.Changes():
		case <-deadline.C:
			t.Fatal("moving a folder of JSON skills into the watched folder did not load its skills")
		}
	}
}

// With a hundred-odd image skills, the obvious one must still come first.
func TestSearchKeepsTheObviousOnTop(t *testing.T) {
	r := coreRegistry(t, "")
	cases := []struct{ q, types, want, value string }{
		{"blur", ".jpg", "blur", ""},
		{"crop", ".jpg", "crop", ""},
		{"crop 16:9", ".jpg", "crop", "16:9"},
		{"rotate", ".jpg", "rotate", ""},
		{"rotate 45", ".jpg", "rotate", "45"},
		{"saturate", ".jpg", "saturate", ""},
		{"saturate 150%", ".jpg", "saturate", "150%"},
		{"saturate -30", ".jpg", "saturate", "-30"},
		{"desaturate", ".jpg", "saturate", ""},
		{"sharpen", ".jpg", "sharpen", ""},
		{"sharp", ".jpg", "sharpen", ""},
		{"brightness", ".jpg", "brightness", ""},
		{"brighter", ".jpg", "brightness", ""},
		{"contrast", ".jpg", "contrast", ""},
		{"black and white", ".jpg", "black_and_white", ""},
		{"bw", ".jpg", "black_and_white", ""},
		{"grayscale", ".jpg", "grayscale", ""},
		{"invert", ".jpg", "invert", ""},
		{"sepia", ".jpg", "sepia", ""},
		{"vignette", ".jpg", "vignette", ""},
		{"pixelate", ".jpg", "pixelate", ""},
		{"flip", ".jpg", "flip", ""},
		{"mirror", ".jpg", "mirror", ""},
		{"warm", ".jpg", "temperature", ""},
		{"cool", ".jpg", "cool", ""},
		{"round corners", ".png", "round_corners", ""},
		{"border", ".jpg", "border", ""},
		{"resize 1920", ".jpg", "resize", "1920"},
		{"jpg", ".png", "convert_to_jpeg", ""},
		{"compress", ".jpg", "compress", ""},
		{"saturate", ".mp4", "saturate", ""},
		{"dither gameboy", ".png", "dither", "gameboy"},
		{"watermark Draft copy", ".jpg", "watermark", "Draft copy"},
		{"replace color red blue", ".jpg", "replace_color", "red blue"},
		{"duotone navy gold", ".jpg", "duotone", "navy gold"},
		{"swap channels grb", ".jpg", "swap_channels", "grb"},
	}
	for _, c := range cases {
		m := top(t, r, c.q, c.types)
		if m.Skill.ID != c.want || m.Value != c.value {
			t.Errorf("%q on %s -> %s (value %q); want %s (value %q)", c.q, c.types, m.Skill.ID, m.Value, c.want, c.value)
		}
	}
}

// Before anything is typed, each family is represented by its everyday skill.
func TestEmptyQueryRepresentatives(t *testing.T) {
	r := coreRegistry(t, "")
	first := map[string]string{}
	for _, m := range r.Search("", Context{InputTypes: []string{".jpg"}}) {
		if _, ok := first[m.Skill.Category]; !ok {
			first[m.Skill.Category] = m.Skill.ID
		}
	}
	want := map[string]string{"transform": "resize", "adjust": "enhance", "filter": "sharpen", "compress": "compress"}
	for cat, id := range want {
		if first[cat] != id {
			t.Errorf("first %s skill for a JPEG is %s, want %s", cat, first[cat], id)
		}
	}
}
