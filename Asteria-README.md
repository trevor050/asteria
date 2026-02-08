# Asteria

## Idea + Philosophy (in your words)
Asteria is an **open‑source, cross‑platform “anything → anything” file workflow app** with a **spotlight‑style command box**. It’s **Raycast/Spotlight UX + a deterministic local pipeline engine**. You drop files, type a skill, watch the preview update, and export when ready. It’s not a chatbot — it’s a **tool router with taste**.

Core philosophy:
- **Everything is a skill.** Convert, resize, compress, switch modes, export — and even settings live in the same command bar.
- **The command bar is the universal entry point** for actions and settings.
- **Skills apply instantly and stack as a live pipeline.**
- **Removing a skill replays the remaining chain** for a seamless update.
- Runs **entirely local**, with AI tool‑calling planned later.

This is not “just conversions.” It’s **workflow composition** with a command‑bar UX.

---

## Connection to MoonCow (Vibe DNA)
Asteria inherits its **command‑bar‑to‑everything** DNA from my earlier project **MoonCow** — a fast, glassy, Zen/Arc‑style command palette built for Firefox. MoonCow taught the core vibe: **lightweight command bar, smart ranking, and a UI that feels smooth instead of heavy.** Asteria takes that same philosophy and applies it to **local file workflows** instead of browser actions.



## The Feel (Visual + Motion Spec)
You wanted a **modern mac app** feel that still works cross‑platform — not clunky, not Electron‑heavy. The vibe target is:

**Look**
- **Glassy / translucent** layers with soft blur and depth.
- **Edge‑to‑edge** layout, minimal chrome, lots of breathing room.
- **Frameless‑feeling window** (custom title bar + drag region).
- **Expressive type** (Space Grotesk) with clean hierarchy.
- Subtle gradients, muted contrast, and soft shadows.

**Motion**
- **Staggered entrance** for files and skill chips.
- **Springy transitions** on command bar focus and selection.
- **Micro‑interactions** on hover, selection, and skill removal.
- A calm **onboarding moment** (“drop files to begin”).

**Native feel rules**
- Use OS dialogs (file pickers, folder pickers).
- Respect OS shortcuts (Cmd+K, Cmd+O, Esc, arrows).
- Keep focus discipline — the command bar always feels ready.

---

## User Flow (Visual, Step‑by‑Step)

```
┌─────────────────────────────────────────────────────────────┐
│ Asteria • File actions, chained live                         │
│ [Batch] [Per‑file]                                   [Add]   │
├─────────────────────────────────────────────────────────────┤
│  ⌘  Type a skill…                                            │
│  Convert to JPEG     Resize 50%     Compress 85%             │
├─────────────────────────────────────────────────────────────┤
│  [ File Card ]   [ File Card ]   [ File Card ]               │
│  preview           preview         preview                   │
│  Chips: JPEG ×  Resize ×  Compress ×                         │
└─────────────────────────────────────────────────────────────┘
```

**Flow**
1. **Drop files** into the tray (or Cmd+O).
2. **Command bar auto‑focuses** with ranked skills.
3. Type “jp” → “Convert to JPEG” → Enter.
4. If a skill has params, the input **switches to param mode** (Tab cycles presets).
5. Preview updates immediately and a **skill chip** appears.
6. Remove a middle chip → **Asteria replays the chain** so the rest stays intact.
7. **Cmd+Enter to Export All** (smart naming, no overwrites).

---

## Interaction Model (Exact Behavior)
- **Live execution**: each skill runs immediately on the working copy.
- **Skill tags**: applied steps are visible as chips on each file.
- **Seamless removal**: deleting a chip rebuilds from the nearest snapshot and re‑applies remaining steps.
- **Ranking updates**: after “Convert to JPEG,” convert‑type skills drop lower.

---

## Tech Stack (What We’re Building With + Why)
**Shell: Wails v2 (Go + OS WebView)**
- Native window behavior + dialogs → OS‑native feel.
- Fast builds on M1 Air (Go > Rust for dev speed).
- Small runtime footprint, avoids Electron heaviness.

**Frontend: Svelte + Vite + TypeScript**
- Fast dev loop, minimal UI overhead.
- Ideal for a keyboard‑first command‑bar UI.

**Backend: Go**
- Efficient local file pipelines, deterministic behavior.

**Processing strategy**
- **Go‑native image ops** via `disintegration/imaging`.
- **FFmpeg later** for video/audio (PATH‑detected, not bundled initially).

**Storage**
- JSON under user config for settings + usage stats.

---

## Architecture (Deep Dive)

```
Wails App
├── Frontend (Svelte + Vite)
│   ├── Command Bar (search + params)
│   ├── File Tray (drag/drop + preview)
│   └── Pipeline Preview (steps + outputs)
└── Backend (Go)
    ├── SkillRegistry + Ranker
    ├── SessionState + Workspace
    ├── Executor (replay + undo)
    └── Drivers (image, meta; video later)
```

### Skill System (Backbone)
A **Skill** defines:
- `id`, `name`, `aliases`, `category`
- `description`
- `inputTypes`, `outputType`
- `params` (with presets for Tab‑cycling)
- `driver`, `isMeta`, `dangerLevel`

### Ranker (Command Bar Brain)
Ranking =
- Base category priority
- Input‑type match boost
- Alias + fuzzy match
- Frecency (recent + frequent)
- Adaptive learning (usage stats)

### Session + Workspace
- Base copy + current working copy.
- Snapshots before each skill to support replay.
- Applied skill list with params.

### Executor (Live Pipeline)
- Execute skill immediately.
- Snapshot before each step.
- Remove a middle step → restore snapshot → re‑apply remainder.

### Drivers
- ImageDriver (Go‑native imaging)
- MetaDriver (settings/mode changes)
- Video/AudioDriver (FFmpeg later)

---

## MVP Skills
**Convert**
- Convert to JPEG
- Convert to PNG

**Transform**
- Resize (percent presets)

**Compress**
- Compress (quality presets)

**Filter**
- Grayscale
- Blur

**Meta**
- Switch to batch / per‑file
- Set output folder
- Set naming pattern
- Export
- Clear all

---

## AI‑Readiness (Later)
The AI layer is a **planner**, not the executor. The pipeline JSON stays stable so an LLM can emit tool calls later without changing the core engine.

---

## Build / Dev
```bash
wails dev
```
Browser preview:
```bash
http://localhost:34115
```
Build:
```bash
wails build
```

---

## Examples (3 Levels)

### 1) Quick one‑step conversion (autofill)
**Goal:** convert a single HEIC to JPEG fast.
- Drop `IMG_1024.HEIC`
- Type `jpeg` → **autofills** to “Convert to JPEG” → Enter
- Preview updates instantly → **Cmd+Enter to Export**

### 2) Two easy actions, mixed files (shorthand)
**Goal:** resize and compress a batch of mixed PNG/JPGs.
- Drop 20 mixed images
- Type `RS50` → **autofills** to “Resize 50%” → Enter
- Type `CMP85` → **autofills** to “Compress 85%” → Enter
- **Cmd+Enter** to export all (same naming pattern)

### 3) Power user, 12+ skills, delete mid‑chain
**Goal:** build a rich pipeline, then remove a middle step.

Skills applied (example chain):
1. convert to png
2. resize 75%
3. crop 1:1
4. rotate 90°
5. sharpen
6. denoise
7. adjust brightness +10
8. adjust contrast +8
9. apply vignette
10. compress 80%
11. add watermark
12. set output folder
13. set naming pattern
14. export

Now the user deletes **skill #6 (denoise)**.
- Asteria restores the snapshot from just before denoise.
- Replays skills 7–14 automatically.
- The rest of the chain stays intact and the preview updates seamlessly.

---

## Road to Agentic (Later)
In agentic mode, the command bar becomes a **batch planner**. You describe the outcome once, press `Cmd+Shift+Enter`, and Asteria does the rest using the same skills under the hood.

**Example command (bulk + multi‑file intent):**
> “For all screenshots: convert to PNG, resize to 1200px wide, add subtle shadow, and export to /Exports/Screens. For photos: convert to JPEG, compress to 80%, keep EXIF, and export to /Exports/Photos. For logos: keep transparent PNG, upscale 2x, and export to /Exports/Logos.”

**What the agent does:**
- Parses the intent and **selects skills** (Convert, Resize, Shadow, Compress, Preserve EXIF, Upscale, Export).
- Builds **separate pipelines per file type** and validates inputs.
- Runs each skill, **checks previews/results**, and retries if a step fails.
- If something looks off (wrong size/format), it adjusts and re‑runs that step.
- Finishes with a clean export report.

**Outcome:**
You can walk away. When you come back, the agent has executed the full workflow using Asteria’s skills, and the results are waiting in the right folders.

Once the agentic layer exists, the same 12‑step workflow becomes one input.

**User types:**
> “Convert to PNG, resize 75%, crop 1:1, rotate 90°, sharpen, denoise, brighten +10, contrast +8, add vignette, compress 80%, watermark bottom‑right, set output folder to /Exports, naming pattern {name}_v3, export.”

**User presses:** `Cmd+Shift+Enter`

**Result:**
- The AI planner converts the request into a pipeline JSON.
- Asteria validates it and executes each step using the existing tool drivers.
- The live preview updates exactly as if the user added each skill manually.
