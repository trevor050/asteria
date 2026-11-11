package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"asteria/internal/addons"
)

// The add-on catalogue: pinned versions, official URLs and checksums.
// Reviewed in the repository at addons/manifest.json.
//
//go:embed addons/manifest.json
var addonsManifestJSON []byte

const eventAddonProgress = "asteria:addon-progress"

// addonsRoot is where add-ons live: a per-user folder that needs no
// administrator rights and is not synced (LocalAppData on Windows).
func addonsRoot() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base, err := os.UserCacheDir() // %LocalAppData%
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "asteria", "addons"), nil
	case "darwin":
		base, err := os.UserConfigDir() // ~/Library/Application Support
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "asteria", "addons"), nil
	default:
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return filepath.Join(v, "asteria", "addons"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", "asteria", "addons"), nil
	}
}

func newAddons() (*addons.Manager, error) {
	m, err := addons.ParseManifest(addonsManifestJSON)
	if err != nil {
		return nil, err
	}
	root := os.Getenv("ASTERIA_ADDONS_DIR")
	if root == "" {
		if root, err = addonsRoot(); err != nil {
			return nil, err
		}
	}
	return addons.NewManager(root, m), nil
}

// AddonView is one add-on as Settings and install prompts show it.
type AddonView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Unlocks     string `json:"unlocks"`
	Publisher   string `json:"publisher"`
	License     string `json:"license"`
	LicenseURL  string `json:"licenseUrl"`
	Homepage    string `json:"homepage"`
	LicenseNote string `json:"licenseNote"`
	// State is installed (by Asteria), system (already on this computer),
	// available (one click), manual (official page only) or unavailable.
	State         string `json:"state"`
	Version       string `json:"version"`
	DownloadBytes int64  `json:"downloadBytes"`
	DiskBytes     int64  `json:"diskBytes"`
	Path          string `json:"path"`
	ManualPage    string `json:"manualPage"`
	ManualWhy     string `json:"manualWhy"`
}

func (a *App) addonView(e addons.Engine) AddonView {
	v := AddonView{ID: e.ID, Name: e.Name, Unlocks: e.Unlocks, Publisher: e.Publisher, License: e.License,
		LicenseURL: e.LicenseURL, Homepage: e.Homepage, LicenseNote: e.LicenseNote, State: "unavailable"}
	art, hasArt := e.Artifact()
	if rec, ok := a.addons.Installed(e.ID); ok {
		v.State, v.Version = "installed", rec.Version
		v.DiskBytes = a.addons.EngineBytes(e.ID)
		if len(e.Tools) > 0 {
			v.Path, _ = a.addons.BinPath(e.Tools[0])
		}
		return v
	}
	for _, t := range e.Tools {
		if p, ok := a.tools.Lookup(t); ok {
			v.State, v.Path = "system", p
			return v
		}
	}
	switch {
	case hasArt && art.Manual != nil:
		v.State, v.ManualPage, v.ManualWhy = "manual", art.Manual.Page, art.Manual.Why
	case hasArt:
		v.State, v.Version = "available", art.Version
		v.DownloadBytes = art.Size
		v.DiskBytes = art.UnpackedMB * 1_000_000
	default:
		v.ManualPage = e.Homepage
	}
	return v
}

// Addons lists every optional engine and its state on this computer.
func (a *App) Addons() []AddonView {
	a.tools.Reset()
	out := make([]AddonView, 0, len(a.addons.Manifest.Engines))
	for _, e := range a.addons.Manifest.Engines {
		out = append(out, a.addonView(e))
	}
	return out
}

// AddonForTool returns the add-on that provides a command, if any.
func (a *App) AddonForTool(tool string) *AddonView {
	e, ok := a.addons.Manifest.ForTool(tool)
	if !ok {
		return nil
	}
	v := a.addonView(e)
	return &v
}

// InstallAddon downloads and installs an add-on the user asked for. It is
// the only thing in Asteria that uses the network, and it sends nothing
// about the user's files. Progress arrives on asteria:addon-progress.
func (a *App) InstallAddon(id string) Result {
	err := a.addons.Install(a.ctx(), id, func(p addons.Progress) { a.emit(eventAddonProgress, p) })
	a.tools.Reset()
	if err != nil {
		if errors.Is(err, addons.ErrCancelled) {
			return Result{Error: "Cancelled", ErrorKind: "cancelled"}
		}
		var ue *addons.UserError
		if errors.As(err, &ue) {
			return Result{Error: ue.Msg, ErrorKind: "user"}
		}
		return Result{Error: err.Error(), ErrorKind: "internal"}
	}
	e, _ := a.addons.Manifest.Engine(id)
	r := Result{}
	r.Outcome.Message = fmt.Sprintf("%s is ready", e.Name)
	return r
}

// CancelAddon stops an install in progress.
func (a *App) CancelAddon(id string) { a.addons.Cancel(id) }

// RemoveAddon deletes an add-on Asteria installed and frees its space.
func (a *App) RemoveAddon(id string) Result {
	freed, err := a.addons.Remove(id)
	a.tools.Reset()
	if err != nil {
		return Result{Error: err.Error(), ErrorKind: "internal"}
	}
	e, _ := a.addons.Manifest.Engine(id)
	r := Result{}
	r.Outcome.Message = fmt.Sprintf("Removed %s and freed %s", e.Name, humanBytes(freed))
	return r
}

// OpenAddonPage opens an add-on's official page in the browser: what =
// "download" (the manual route), "license" or "home". Only URLs from the
// manifest can be opened.
func (a *App) OpenAddonPage(id, what string) error {
	e, ok := a.addons.Manifest.Engine(id)
	if !ok {
		return fmt.Errorf("unknown add-on %q", id)
	}
	u := e.Homepage
	switch what {
	case "license":
		u = e.LicenseURL
	case "download":
		if art, ok := e.Artifact(); ok && art.Manual != nil {
			u = art.Manual.Page
		}
	}
	if a.app == nil || u == "" {
		return errors.New("unavailable")
	}
	return a.app.Browser.OpenURL(u)
}

// AddonsUsedBytes is the disk space taken by add-ons Asteria installed.
func (a *App) AddonsUsedBytes() int64 { return a.addons.UsedBytes() }

func humanBytes(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%d MB", (n+500_000)/1_000_000)
	default:
		return fmt.Sprintf("%d KB", (n+500)/1000)
	}
}
