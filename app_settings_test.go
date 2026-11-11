package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"asteria/internal/session"
)

// isolate points every per-user directory at a temp dir so NewApp never
// touches the real settings, cache or skills folder.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "localappdata"))
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	app, err := NewApp(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.store.Workspace().Close() })
	return app
}

func TestSettingsPersistAcrossRestart(t *testing.T) {
	isolate(t)
	app := newTestApp(t)
	st := app.GetState().Settings
	if st.Appearance != "system" || st.ReduceMotion || st.ReduceTransparency {
		t.Fatalf("defaults: %+v", st)
	}
	for _, kv := range [][2]string{
		{"appearance", "light"},
		{"reduceMotion", "true"},
		{"reduceTransparency", "true"},
		{"namingPattern", "{name}_{date}.{ext}"},
		{"outputFolder", "/tmp/exports"},
	} {
		if r := app.UpdateSettings(kv[0], kv[1]); r.Error != "" || r.Settings == nil {
			t.Fatalf("UpdateSettings(%s): %+v", kv[0], r)
		}
	}
	// A second App reads the same settings file, like a relaunch.
	again := newTestApp(t).GetState()
	got := again.Settings
	if got.Appearance != "light" || !got.ReduceMotion || !got.ReduceTransparency ||
		got.NamingPattern != "{name}_{date}.{ext}" || got.OutputFolder != "/tmp/exports" {
		t.Fatalf("after restart: %+v", got)
	}
	if again.Version != Version || Version == "" {
		t.Fatalf("version %q", again.Version)
	}
	if r := app.UpdateSettings("reduceMotion", "false"); r.Settings.ReduceMotion {
		t.Fatal("reduceMotion did not turn off")
	}
}

func TestAppearanceRejectsUnknownValues(t *testing.T) {
	isolate(t)
	app := newTestApp(t)
	if r := app.UpdateSettings("appearance", "dark"); r.Settings.Appearance != "dark" {
		t.Fatalf("dark: %+v", r.Settings)
	}
	if r := app.UpdateSettings("appearance", "sepia"); r.Settings.Appearance != "system" {
		t.Fatalf("unknown value should fall back to system: %+v", r.Settings)
	}
}

func TestNamingSettingNamesExportsAndNeverOverwrites(t *testing.T) {
	isolate(t)
	app := newTestApp(t)
	src := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(src, []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	app.UpdateSettings("outputFolder", out)
	app.UpdateSettings("namingPattern", "{name}-{skill}.{ext}")
	added := app.AddPaths([]string{src})
	if len(added.Added) != 1 {
		t.Fatalf("add: %+v", added)
	}
	id := added.Added[0].ID
	if r := app.Apply([]string{id}, "markdown_to_html", nil); r.Error != "" {
		t.Fatalf("apply: %+v", r)
	}
	var names []string
	for i := 0; i < 2; i++ {
		r := app.Export([]string{id})
		if len(r.Exported) != 1 {
			t.Fatalf("export %d: %+v", i, r)
		}
		names = append(names, r.Exported[0].Name)
	}
	if names[0] != "notes-markdown_to_html.html" || names[1] != "notes-markdown_to_html (1).html" {
		t.Fatalf("names %v", names)
	}
	app.UpdateSettings("namingPattern", session.DefaultNamingPattern)
	r := app.Export([]string{id})
	if len(r.Exported) != 1 || r.Exported[0].Name != "notes.html" {
		t.Fatalf("default pattern: %+v", r.Exported)
	}
	if !strings.HasPrefix(r.Exported[0].OutputPath, out) {
		t.Fatalf("exported outside the chosen folder: %s", r.Exported[0].OutputPath)
	}
}

func TestOpenLinkOnlyOpensKnownLinks(t *testing.T) {
	isolate(t)
	app := newTestApp(t)
	if err := app.OpenLink("https://example.com"); err == nil || !strings.Contains(err.Error(), "unknown link") {
		t.Fatalf("arbitrary URL accepted: %v", err)
	}
}
