package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsNormalizeAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewSettingsStoreAt(path)
	s, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Appearance != "system" || s.Mode != "batch" || s.NamingPattern != defaultNaming {
		t.Fatalf("defaults: %+v", s)
	}
	s.Appearance = "dark"
	s.ReduceMotion = true
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	got, _ := NewSettingsStoreAt(path).Load()
	if got.Appearance != "dark" || !got.ReduceMotion || got.ReduceTransparency {
		t.Fatalf("round trip: %+v", got)
	}
	// Settings written by older versions have no appearance key.
	if err := os.WriteFile(path, []byte(`{"mode":"per_file","appearance":"neon"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old, _ := store.Load()
	if old.Appearance != "system" || old.Mode != "per_file" {
		t.Fatalf("old file: %+v", old)
	}
}

func TestAccentDefaultsToSlateAndOldPresetsMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewSettingsStoreAt(path)
	s, _ := store.Load()
	if s.AccentColor != "112,128,150" {
		t.Fatalf("default accent %q", s.AccentColor)
	}
	for old, want := range map[string]string{"99,102,241": "112,128,150", "16,185,129": "110,148,122", "200,10,10": "200,10,10"} {
		if err := os.WriteFile(path, []byte(`{"accentColor":"`+old+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := store.Load()
		if got.AccentColor != want {
			t.Errorf("%s -> %s, want %s", old, got.AccentColor, want)
		}
	}
}
