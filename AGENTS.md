# AGENTS.md

Asteria: local-first desktop file converter. Wails 3.1 (Go) + Svelte 5. Drop files, type a skill in a command bar, preview, export. No network for processing, no telemetry; keep it that way.

## Architecture
- `main.go` app/window/single-instance/drop handler; `app.go` the one bound service (`App`, methods = frontend API); `preview.go` asset middleware serving `/asteria/thumb|file/<fileId>/<stepId|original>` (tray files by id only).
- `internal/ops`: native handlers registered by name (`image.convert`, `pdf.merge`, `media.trim`...). Handler picks format from the OUTPUT EXTENSION. `ops.UserError` = message shown verbatim; `MissingToolError` for ffmpeg etc.
- `internal/skills`: JSON skill schema, loader (embedded `skills/core` < disk `skills/core` in a checkout < user dir), validation, ranking (inline values `resize 50%`, `rs50`). Max ONE param per skill (UI contract). Meta skills only in core; handled in `app.go runMeta`.
- `internal/engine`: Apply (per-file, bounded concurrency per handler, progress, cancel), combine skills -> new tray file, pipelines, RemoveStep replay, Undo stack. One op at a time (`ErrBusy`).
- `internal/session`: tray. Originals are read-only and never copied; each step writes a new immutable file `<workspace>/<fileId>/NNN-skill.ext`. Export uses O_EXCL + " (n)" suffix, never overwrites.
- `internal/inspect`: Info summaries (dims/pages/duration/folder listing/text snippet), thumbnails, video poster via ffmpeg.
- Frontend: `src/App.svelte` (state, keys, toasts, queue), `lib/CommandBar.svelte`, `lib/FileTile.svelte`, `lib/QuickLook.svelte`. Bindings in `frontend/bindings` are generated (gitignored).

## Commands
- Tools: `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0` (this checkout keeps one in `.tools/`, gitignored). Go 1.26 (pulled by deps), Node 22.
- Tests: `go test -tags nodynamic -race ./internal/...` (media tests skip without ffmpeg). UI: `cd frontend && npm run check`.
- Bindings: `wails3 generate bindings -ts -i -clean=true` (needs `frontend/dist` to exist for the embed; touch a file if fresh).
- Native: `wails3 build` / `wails3 task darwin:package:universal` + `wails3 task darwin:create:dmg` -> `bin/Asteria.app`, `bin/Asteria.dmg`.
- Browser testing (no native window needed): `go build -tags server,nodynamic -o bin/asteria-server . && HOME=<scratch> ./bin/asteria-server` -> http://127.0.0.1:34217. Page exposes `window.__asteria` (bindings) and `window.__asteriaAdd(paths)` (adds like a drop). Use an isolated HOME or you will read/write the real user settings.
- Release: push `v*` tag -> `.github/workflows/release.yml` -> draft release. `scripts/set-version.sh X.Y.Z` stamps versions.

## Hazards
- Build tag `nodynamic` (default via root Taskfile EXTRA_TAGS) keeps gen2brain/webp from loading a system libwebp.
- HEIC uses `gen2brain/h265/heic` (pure Go, SIMD). Must pass `heic.Options{AutoRotate: true}`; the image.RegisterFormat wrapper does NOT rotate. The old WASM decoder (gen2brain/heic) was ~10x slower; don't reintroduce.
- Wails exits a second instance inside `application.New`; workspace cleanup must stay after it (`attach`), or a second launch deletes the running app's work. `SecondInstanceData.Args[0]` is the executable.
- Go PNG encoder writes 16-bit for YCbCr sources; `EncodeImage` converts first.
- macOS GUI apps lack shell PATH; `internal/tools` also checks Homebrew/winget/scoop/choco dirs.
- Linux build needs GTK4 + WebKitGTK 6.0 (`libgtk-4-dev libwebkitgtk-6.0-dev`), Ubuntu 24.04+.
- pdfcpu must use `api.ConfigurationModeStateless` (otherwise it writes a config dir).
- Old app versions stored `namingPattern: "{name}_{skill}.{ext}"`; settings normalize it to `{name}.{ext}`.
- This machine: Screen Recording permission is not granted to agents, so native window screenshots fail; use server mode + Playwright.
