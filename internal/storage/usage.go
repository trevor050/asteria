package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"asteria/internal/skills"
)

type UsageStore struct {
	mu   sync.Mutex
	path string
	data map[string]skills.UsageStats
}

func NewUsageStore() (*UsageStore, error) {
	dir, err := AppConfigDir()
	if err != nil {
		return nil, err
	}
	store := &UsageStore{
		path: filepath.Join(dir, "usage_stats.json"),
		data: make(map[string]skills.UsageStats),
	}
	_ = store.load()
	return store, nil
}

func (u *UsageStore) load() error {
	data, err := os.ReadFile(u.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stats map[string]skills.UsageStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return err
	}
	u.data = stats
	return nil
}

func (u *UsageStore) snapshot() map[string]skills.UsageStats {
	u.mu.Lock()
	defer u.mu.Unlock()
	copied := make(map[string]skills.UsageStats, len(u.data))
	for k, v := range u.data {
		copied[k] = v
	}
	return copied
}

func (u *UsageStore) All() map[string]skills.UsageStats {
	return u.snapshot()
}

func (u *UsageStore) Increment(skillID string) error {
	u.mu.Lock()
	stat := u.data[skillID]
	stat.Count++
	stat.LastUsed = time.Now()
	u.data[skillID] = stat
	data, err := json.MarshalIndent(u.data, "", "  ")
	u.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(u.path, data, 0o644)
}
