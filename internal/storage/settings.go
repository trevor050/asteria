package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Settings struct {
	OutputFolder  string `json:"outputFolder"`
	NamingPattern string `json:"namingPattern"`
	AccentColor   string `json:"accentColor"`
}

type SettingsStore struct {
	path string
}

func NewSettingsStore() (*SettingsStore, error) {
	dir, err := AppConfigDir()
	if err != nil {
		return nil, err
	}
	return &SettingsStore{path: filepath.Join(dir, "settings.json")}, nil
}

func (s *SettingsStore) Load() (Settings, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{
				OutputFolder:  "",
				NamingPattern: "{name}_{skill}.{ext}",
				AccentColor:   "99,102,241",
			}, nil
		}
		return Settings{}, err
	}
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, err
	}
	if settings.NamingPattern == "" {
		settings.NamingPattern = "{name}_{skill}.{ext}"
	}
	if settings.AccentColor == "" {
		settings.AccentColor = "99,102,241"
	}
	return settings, nil
}

func (s *SettingsStore) Save(settings Settings) error {
	if settings.NamingPattern == "" {
		settings.NamingPattern = "{name}_{skill}.{ext}"
	}
	if settings.AccentColor == "" {
		settings.AccentColor = "99,102,241"
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
