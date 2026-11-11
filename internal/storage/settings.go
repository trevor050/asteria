package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	defaultNaming = "{name}.{ext}"
	legacyNaming  = "{name}_{skill}.{ext}"
	defaultAccent = "112,128,150"
)

var legacyAccents = map[string]string{
	"99,102,241": "112,128,150", // indigo -> slate
	"14,165,233": "104,146,176", // sky -> sky
	"16,185,129": "110,148,122", // emerald -> sage
	"249,115,22": "176,148,104", // orange -> sand
	"239,68,68":  "178,116,128", // red -> rose
	"168,85,247": "130,120,180", // violet -> iris
}

// Settings are the user's persistent preferences.
type Settings struct {
	// OutputFolder is where exports go; empty means next to each original.
	OutputFolder  string `json:"outputFolder"`
	NamingPattern string `json:"namingPattern"`
	AccentColor   string `json:"accentColor"`
	// Mode is "batch" (skills apply to the selection, or every file) or
	// "per_file" (skills apply to the focused file only).
	Mode string `json:"mode"`
	// Appearance is "system", "light" or "dark".
	Appearance string `json:"appearance"`
	// ReduceMotion and ReduceTransparency turn the reduced variants on even
	// when the operating system does not ask for them.
	ReduceMotion       bool `json:"reduceMotion"`
	ReduceTransparency bool `json:"reduceTransparency"`
}

func (s *Settings) normalize() {
	if s.NamingPattern == "" || s.NamingPattern == legacyNaming {
		s.NamingPattern = defaultNaming
	}
	if s.AccentColor == "" {
		s.AccentColor = defaultAccent
	}
	// The first palette was saturated; each old preset maps to its muted
	// counterpart (indigo, the old default, becomes the new default).
	if v, ok := legacyAccents[strings.ReplaceAll(s.AccentColor, " ", "")]; ok {
		s.AccentColor = v
	}
	if s.Mode != "per_file" {
		s.Mode = "batch"
	}
	if s.Appearance != "light" && s.Appearance != "dark" {
		s.Appearance = "system"
	}
}

// SettingsStore persists Settings as JSON in the config directory.
type SettingsStore struct {
	mu   sync.Mutex
	path string
}

func NewSettingsStore() (*SettingsStore, error) {
	dir, err := AppConfigDir()
	if err != nil {
		return nil, err
	}
	return &SettingsStore{path: filepath.Join(dir, "settings.json")}, nil
}

// NewSettingsStoreAt stores settings at an explicit path (tests).
func NewSettingsStoreAt(path string) *SettingsStore { return &SettingsStore{path: path} }

func (s *SettingsStore) Load() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var settings Settings
	data, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return Settings{}, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			settings = Settings{}
		}
	}
	settings.normalize()
	return settings, nil
}

func (s *SettingsStore) Save(settings Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings.normalize()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Update loads, mutates and saves settings atomically.
func (s *SettingsStore) Update(fn func(*Settings)) (Settings, error) {
	cur, err := s.Load()
	if err != nil {
		return cur, err
	}
	fn(&cur)
	cur.normalize()
	return cur, s.Save(cur)
}
