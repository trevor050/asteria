// Package addons installs optional engines (ffmpeg, Pandoc, LibreOffice...)
// on request, per user, without administrator rights.
//
// Everything about what can be downloaded lives in one reviewable manifest
// (addons/manifest.json at the repository root, embedded in the app): for
// each engine and platform, a pinned version, an HTTPS URL on the project's
// official release channel, the size, and the SHA-256 the project publishes.
// The app downloads only when the user clicks, verifies the checksum before
// anything is unpacked or run, unpacks into its own data folder and never
// runs an installer that asks for elevation. Where no such route exists on
// a platform the manifest says so ("manual") and the app points to the
// official download page instead.
package addons

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
)

// Manifest is the embedded catalogue of engines.
type Manifest struct {
	Schema  int      `json:"schema"`
	Engines []Engine `json:"engines"`
}

// Engine is one optional tool.
type Engine struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Unlocks     string `json:"unlocks"`
	Publisher   string `json:"publisher"`
	License     string `json:"license"`
	LicenseURL  string `json:"license_url"`
	Homepage    string `json:"homepage"`
	LicenseNote string `json:"license_note,omitempty"`
	// Tools are the command names skills ask for ("soffice", "ffprobe").
	Tools     []string            `json:"tools"`
	Platforms map[string]Artifact `json:"platforms"`
}

// Artifact is one downloadable build, or a manual route.
type Artifact struct {
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	// SHA1Published is checked too when a project publishes only SHA-1
	// (the SHA-256 above was then pinned after the SHA-1 matched).
	SHA1Published  string            `json:"sha1_published,omitempty"`
	ChecksumSource string            `json:"checksum_source,omitempty"`
	Signature      *Signature        `json:"signature,omitempty"`
	Format         string            `json:"format,omitempty"`
	Bins           map[string]string `json:"bins,omitempty"`
	UnpackedMB     int64             `json:"unpacked_mb,omitempty"`
	Notes          string            `json:"notes,omitempty"`
	Manual         *Manual           `json:"manual,omitempty"`
}

// Signature records an upstream signature checked when the manifest is
// updated (scripts/addons); at run time the pinned SHA-256 is the anchor.
type Signature struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

// Manual is shown when an engine can't be installed with one click.
type Manual struct {
	Page string `json:"page"`
	Why  string `json:"why"`
}

// Platform is the manifest key for this machine, e.g. "darwin-arm64".
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// ParseManifest reads and validates a manifest.
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("addons manifest: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range m.Engines {
		if e.ID == "" || seen[e.ID] {
			return nil, fmt.Errorf("addons manifest: missing or duplicate id %q", e.ID)
		}
		seen[e.ID] = true
		for plat, a := range e.Platforms {
			if a.Manual != nil {
				continue
			}
			if !strings.HasPrefix(a.URL, "https://") {
				return nil, fmt.Errorf("addons manifest: %s %s: URL must be https", e.ID, plat)
			}
			if len(a.SHA256) != 64 {
				return nil, fmt.Errorf("addons manifest: %s %s: needs a published sha256", e.ID, plat)
			}
			if len(a.Bins) == 0 || a.Size <= 0 {
				return nil, fmt.Errorf("addons manifest: %s %s: needs bins and size", e.ID, plat)
			}
		}
	}
	return &m, nil
}

// Engine returns the engine with the given id.
func (m *Manifest) Engine(id string) (Engine, bool) {
	for _, e := range m.Engines {
		if e.ID == id {
			return e, true
		}
	}
	return Engine{}, false
}

// ForTool returns the engine that provides a command, e.g. "ffprobe".
func (m *Manifest) ForTool(tool string) (Engine, bool) {
	tool = strings.TrimSuffix(strings.ToLower(tool), ".exe")
	for _, e := range m.Engines {
		for _, t := range e.Tools {
			if strings.EqualFold(t, tool) {
				return e, true
			}
		}
	}
	return Engine{}, false
}

// Artifact returns this platform's build, if the manifest has one.
func (e Engine) Artifact() (Artifact, bool) {
	a, ok := e.Platforms[Platform()]
	return a, ok
}
