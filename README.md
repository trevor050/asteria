# Asteria

Asteria is a local-first desktop app for chaining file actions from a command bar.

Drop files, type a skill, preview changes immediately, and export when ready.
It is built as an open-source alternative to Filestar-style batch file workflows.

## Why This Exists

- Keyboard-first workflow for repetitive file operations.
- Deterministic local pipeline, no cloud round-trips required.
- Fast iteration with native-feeling desktop UX.
- Open source and hackable, so you can extend skills instead of being locked to a closed tool.

## Core Concepts

- Everything is a skill: convert, resize, compress, filters, mode switches, export.
- Skills execute immediately and stack as a live pipeline.
- Removing a skill replays the remaining chain to keep output coherent.
- Ranking combines fuzzy match + frecency + input-type awareness.

## Tech Stack

- Backend: Go
- Desktop shell: Wails v3 alpha
- Frontend: Svelte + TypeScript + Vite
- Image processing: `github.com/disintegration/imaging`

## Project Layout

```text
.
├── app.go                # Backend methods exposed to frontend
├── main.go               # Wails app bootstrap
├── internal/             # Pipeline/session/storage/skills logic
├── frontend/             # Svelte app
│   ├── src/
│   └── wailsjs/          # Generated bridge bindings
├── skills/               # Skill definitions (JSON)
└── wails.json            # Wails config
```

## Quick Start

### Prerequisites

- Go (matching `go.mod`)
- Node.js + npm
- Wails CLI

Install Wails CLI:

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
```

### Install Dependencies

```bash
cd frontend
npm install
cd ..
```

### Run Dev Mode

```bash
wails dev
```

Wails also exposes a browser dev URL during development (typically `http://localhost:34115`).

### Build

```bash
wails build
```

## Current Skill Categories

- Image transforms (resize, blur, grayscale)
- Format conversion (JPEG/PNG/HEIC workflows)
- Compression presets
- Meta controls (batch/per-file mode, export behavior, naming/output options)

## Local Data

Asteria stores settings/trust/usage metadata in JSON under your user config directory.

## Migration Note

This repo originally started as a Tauri app. It has been fully migrated to Wails for faster iteration and a tighter Go-native workflow.
