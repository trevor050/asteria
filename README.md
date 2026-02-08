# Asteria

Local, live file actions with a command-bar workflow. Drop files, type a skill, watch the preview update, and export when ready.

## Core Behavior

- Every action is a skill (convert, resize, compress, switch modes, export).
- Skills apply instantly and stack as a live pipeline.
- Removing a skill replays the remaining chain for a seamless update.
- The command bar is the universal entry point for actions and settings.

## Development

Run the app in dev mode:

```bash
wails dev
```

If you want a browser preview with bindings available:

```bash
http://localhost:34115
```

## Build

```bash
wails build
```

## Notes

- Image processing is Go-native (via `disintegration/imaging`).
- Skill ranking uses frecency + fuzzy matching + input-type awareness.
- Settings and usage stats are stored in JSON under the user config directory.
