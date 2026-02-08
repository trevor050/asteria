Skills live in `skills/`.

Core skills
- Location: `skills/core/`
- Shipped with the app and embedded into the binary.
- During development, the app also loads from disk `skills/core/` (so edits hot-reload).

Community skills
- Location (runtime, per-user): `AppConfigDir()/skills/` (see `internal/storage/skills.go`)
- Users/orgs can drop JSON skill definitions here (or pack folders later).

POC: add a community skill (hot reload)
1. Find your skills directory on disk (macOS example): `~/Library/Application Support/asteria/skills`
2. Drop a JSON file anywhere under that folder (subfolders are fine)
3. The app hot-reloads and the skill appears in search

Example (CLI skill)
This example runs `ffmpeg` and therefore uses the base `tools.exec` permission.

```json
{
  "id": "audio_mp3_to_wav",
  "name": "MP3 to WAV",
  "version": "0.1.0",
  "author": "community",
  "aliases": ["mp3 wav", "convert mp3"],
  "category": "convert",
  "description": "Convert MP3 to WAV using ffmpeg",
  "inputTypes": [".mp3"],
  "outputType": ".wav",
  "params": [],
  "driver": "cli",
  "isMeta": false,
  "executor": {
    "type": "cli",
    "command": "ffmpeg",
    "args": ["-y", "-i", "{{input}}", "{{output}}"],
    "timeoutMs": 600000
  },
  "permissions": ["files.read", "files.write", "files.temp", "tools.exec"],
  "dangerLevel": 0
}
```

Skill definition format (JSON)
Each `.json` file describes exactly one skill.

Required fields
- `id` (string)
- `name` (string)
- `version` (string)

Security model (Chrome-like)
- Base permissions are allowed by default.
- Elevated permissions require an explicit trust decision for community skills.
- Core skills are implicitly trusted.

Permissions
- Base: `files.read`, `files.write`, `files.temp`, `tools.exec`
- Elevated: `files.anywhere`, `network`, `tools.exec.any`, `system`

Notes
- The POC currently executes core image skills via the existing Go driver (by `id`).
- CLI skills are executed by `internal/drivers/cli.go`.
- Multi-step (pipeline) skills are supported by `executor.type: "pipeline"` and run a list of other skills in order.
- The JSON format already includes an `executor` block so we can add Lua later without changing the format.

Pipeline example
This lets you do things like: HEIC -> PNG -> Grayscale -> HEIC.

```json
{
  "id": "heic_grayscale",
  "name": "HEIC Grayscale",
  "version": "1.0.0",
  "inputTypes": [".heic", ".heif"],
  "outputType": ".heic",
  "driver": "pipeline",
  "executor": {
    "type": "pipeline",
    "steps": [
      {"skillId": "convert_heic_to_png"},
      {"skillId": "grayscale"},
      {"skillId": "convert_png_to_heic"}
    ]
  }
}
```
