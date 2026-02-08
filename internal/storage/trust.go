package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// TrustStore persists user trust decisions for community skills.
// Core (embedded) skills are implicitly trusted.
type TrustStore struct {
	path string
	mu   sync.Mutex
}

type TrustState struct {
	TrustedSkills map[string]bool `json:"trustedSkills"`
}

func NewTrustStore() (*TrustStore, error) {
	dir, err := AppConfigDir()
	if err != nil {
		return nil, err
	}
	return &TrustStore{path: filepath.Join(dir, "trust.json")}, nil
}

func (t *TrustStore) Load() (TrustState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return TrustState{TrustedSkills: map[string]bool{}}, nil
		}
		return TrustState{}, err
	}
	var state TrustState
	if err := json.Unmarshal(data, &state); err != nil {
		return TrustState{}, err
	}
	if state.TrustedSkills == nil {
		state.TrustedSkills = map[string]bool{}
	}
	return state, nil
}

func (t *TrustStore) Save(state TrustState) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if state.TrustedSkills == nil {
		state.TrustedSkills = map[string]bool{}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.path, data, 0o644)
}

func (t *TrustStore) IsTrusted(skillID string) (bool, error) {
	state, err := t.Load()
	if err != nil {
		return false, err
	}
	return state.TrustedSkills[skillID], nil
}

func (t *TrustStore) SetTrusted(skillID string, trusted bool) error {
	state, err := t.Load()
	if err != nil {
		return err
	}
	if state.TrustedSkills == nil {
		state.TrustedSkills = map[string]bool{}
	}
	if trusted {
		state.TrustedSkills[skillID] = true
	} else {
		delete(state.TrustedSkills, skillID)
	}
	return t.Save(state)
}
