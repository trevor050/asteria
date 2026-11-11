# Asteria

Your new favorite way to convert a file. Drop files in, type what you want, see the result, export. Nothing leaves your computer.

![Asteria enhancing a photo with one click, dragging saturation with a live preview, adding a vignette, making a collage of four photos, browsing every skill by family and exporting](docs/demo.gif)

Every "convert X to Y" task usually ends with uploading a file to some random website, waiting through ads, and hoping it gives you back what you sent. Asteria is the opposite: a small desktop app with a Spotlight-style command bar. Type `heic to jpg`, `resize 50%`, `saturate 150%`, `dither gameboy`, `merge pdfs` or `trim 0:05-0:30`, watch the preview update, and press <kbd>⌘</kbd><kbd>↵</kbd> to save. It's not a chatbot, it's a tool router with taste.

It's an open-source take on Filestar-style batch file tools, and it grew out of [MoonCow](https://github.com/trevor050/mooncow), my command palette for Firefox: same idea of a fast, keyboard-first bar that answers small tasks in place, applied to the files on your disk.

## What it does

Over a hundred skills, most of them for pictures: the fun part is stacking them (`enhance`, then `saturate +30`, `vignette`, `crop 4:5`) and watching it happen.

| Family | Skills |
| --- | --- |
| Convert (4) | Convert to HEIC · Convert to JPEG · Convert to PNG · Convert to WebP |
| Size and shape (19) | Black Border · Border · Circle Crop · Crop · Crop Square · Fill · Fit · Flip\* · Mirror\* · Pad · Pixel Art Upscale · Resize · Rotate\* · Round Corners · Straighten · Stretch · Tile · Trim Borders · Trim Transparent |
| Social and wallpaper sizes (11) | Instagram Portrait · Instagram Square · iPad Wallpaper · iPhone Wallpaper · LinkedIn Banner · OG Image · Story · Twitter Header · Wallpaper 1080p · Wallpaper 4K · YouTube Thumbnail |
| Compress (1) | Compress Image |
| Color and tone (29) | Auto Contrast · Auto Enhance · Auto Levels · Auto White Balance · Black & White\* · Brightness · Channel\* · Colorize · Contrast\* · Cooler · Duotone · Exposure · Gamma\* · Grayscale\* · Highlights · Hue Rotate\* · Invert\* · Levels · Opacity · Posterize · Replace Color · Saturate\* · Sepia\* · Shadows · Swap Channels\* · Temperature · Threshold · Tint (green-magenta) · Vibrance |
| Effects (20) | Add Noise\* · Blur\* · Box Blur\* · Chromatic Aberration · Denoise · Dither · Edge Detect\* · Emboss · Film Grain · Glitch · Glow · Halftone · Mosaic · Motion Blur · Pixelate\* · Scanlines · Sharpen\* · Vertical Motion Blur · Vignette\* · Zoom Blur |
| Compose and output (17) | Animated GIF · Animated WebP · App Icon Set · Contact Sheet · Drop Shadow · Extract Frames · Favicon Set · Fill Background · Image Grid · Image Strip · Overlay · Remove Background Color · Split into Tiles · Sprite Sheet · Vertical Strip · Watermark · Watermark Image |
| Info and cleanup (8) | Compare Images · Convert to sRGB · Count Colors · Extract Palette · Image Difference · Palette Swatches · Show Metadata · Strip Metadata |
| PDF (5) | Compress PDF · Extract Pages · Images to PDF · Merge PDFs · Split PDF |
| Audio and video (9) | Compress Video · Convert to FLAC · Convert to GIF · Convert to M4A · Convert to MP3 · Convert to MP4 · Convert to WAV · Convert to WebM · Trim |
| Archives (2) | Extract Archive · Zip |
| Data (6) | CSV to JSON · CSV to Markdown Table · JSON to CSV · JSON to YAML · Markdown to HTML · YAML to JSON |

131 skills. \* also works on video (needs ffmpeg).

Some things to type: `heic to jpg`, `resize 1920`, `crop 16:9`, `rotate -3`, `straighten 2`, `saturate 150%`, `temperature -20`, `duotone navy gold`, `pixelate 16`, `dither gameboy`, `glitch`, `round corners`, `watermark Draft`, `strip metadata`, `image grid`, `animated gif`, `favicon set`, `instagram story`, `merge pdfs`, `extract pages 1-3, 5`, `trim 0:05-0:30`, `csv to json`.

Images, PDFs, archives and data are handled by Go code built into the app, including HEIC (iPhone photos) and WebP. Image skills share one pixel core: 16-bit, color-managed (Display P3 photos are converted, not washed out), blurs and resizing in linear light, and a chain of edits is kept lossless in memory so a JPEG is only re-encoded once per step, never compounded. Audio and video use [ffmpeg](https://ffmpeg.org) if it's installed. If it isn't, those skills say so in the suggestions and tell you the one command to install it (`brew install ffmpeg`, `winget install Gyan.FFmpeg`, `sudo apt install ffmpeg`).

## How it works

- **Everything is a skill.** Converting, resizing, exporting, switching modes and picking an output folder all live in the same command bar.
- **One file gets the whole window.** Drop a single file and it's shown large and centered, uncropped, with its size, dimensions and steps right under it and the bar right under that. A PDF shows its first page, a CSV shows as a table. Add more and it settles into the grid; "One by one" brings the big view back for any file (<kbd>←</kbd> <kbd>→</kbd> to move, <kbd>Esc</kbd> for all of them).
- **Drag a value, see it live.** Pick a skill with a number (saturation, exposure, blur, vignette...) and a slider appears; the picture updates as you drag, on a quick preview copy, and nothing is applied until <kbd>↵</kbd>. Hovering a suggestion previews it too.
- **Everything that fits, by family.** "All N skills" lists every skill that applies to your files, grouped into color and tone, effects, size and shape, compose, info and so on.
- **It shows you what's next.** Drop files and the likely skills for those exact files sit right above the bar, one click away, next to a real count of everything that applies. Mixed tray? You get a spread: convert, resize, compress, merge.
- **Skills apply instantly and stack.** Each file shows the chain it went through (`Convert to JPEG 85%` → `Resize 1920`) with its size and dimensions before and after.
- **Removing a skill replays the rest.** Delete a step from the middle of a chain and Asteria rebuilds from the step before it. <kbd>⌘</kbd><kbd>Z</kbd> undoes the last skill you applied.
- **Your originals are never touched.** Skills work on copies in a private workspace. Export writes new files next to the originals (or to a folder you pick) and never overwrites anything: `photo.jpg` becomes `photo (1).jpg` if the name is taken.
- **Suggestions tell you what will happen before you press Enter**: how many of your files a skill applies to, what format comes out, and whether it needs a tool you don't have.

| Key | |
| --- | --- |
| type anything | Search skills. A value after the name is used directly: `resize 50%`, `jpg80`, `rs50` |
| <kbd>↵</kbd> | Apply the highlighted skill (or open its one setting) |
| <kbd>Tab</kbd> | Cycle presets while editing a setting |
| <kbd>Space</kbd> / <kbd>⌘</kbd><kbd>Y</kbd> | Quick Look the focused file; <kbd>O</kbd> toggles the original, <kbd>←</kbd><kbd>→</kbd> move between files |
| <kbd>⌘</kbd><kbd>↵</kbd> | Export changed files |
| <kbd>⌘</kbd><kbd>O</kbd> or the **+** in the bar | Add files (drop folders into the window on Windows) |
| <kbd>⌘</kbd><kbd>,</kbd> | Settings: theme (system, light, dark), accent, reduced motion and transparency, where exports go and how they're named, the skills folder, ffmpeg status |
| <kbd>⌘</kbd><kbd>Z</kbd> | Undo the last skill |
| <kbd>Esc</kbd> | Cancel what's running, close menus, clear the selection |
| <kbd>⌘</kbd>-click, <kbd>⇧</kbd>-click, <kbd>⌘</kbd><kbd>A</kbd> | Select files; skills then apply only to the selection |
| <kbd>⌫</kbd> | Remove the selected files from the tray (the files themselves stay put) |

Skills you type while another is running are queued and run in order.

<p>
  <img src="docs/screenshot-single-dark.png" width="49%" alt="One photo in dark mode: shown large and centered, its size before and after a resize, the step chip, and the likely next steps right above the command bar">
  <img src="docs/screenshot-live.png" width="49%" alt="Saturation being dragged on a slider with the photo previewing the change live">
</p>
<p>
  <img src="docs/screenshot-dark.png" width="49%" alt="Eight files in dark mode: four photos resized and converted to WebP with their sizes before and after, and the command bar previewing a trim of the selected video">
  <img src="docs/screenshot-skills.png" width="49%" alt="Every skill that fits the files, grouped by family">
</p>
<p>
  <img src="docs/screenshot-single-light.png" width="49%" alt="The one-photo view in light mode">
  <img src="docs/screenshot-settings.png" width="49%" alt="The settings panel: theme, accent, reduced motion and transparency, export folder and file names">
</p>

## Everything stays on your computer

Asteria makes no network requests to process files and has no telemetry, accounts or analytics. All conversions run locally, either in Go code inside the app or in ffmpeg on your machine. What it stores:

- your settings and how often you use each skill (for ranking), in your user config folder (`~/Library/Application Support/asteria` on macOS);
- work-in-progress outputs in your user cache folder, deleted when you quit and cleaned up on the next launch after a crash.

## Install

Once the first release is published, grab a build from [Releases](https://github.com/trevor050/asteria/releases): a DMG for macOS (Apple Silicon and Intel), an installer or portable `.exe` for Windows, and a `.deb`, AppImage or tarball for Linux (Ubuntu 24.04 / Debian 13 or newer, since it uses GTK 4 and WebKitGTK 6).

The builds aren't signed yet, so your OS will warn you the first time:

- **macOS:** "Asteria can't be opened because Apple cannot check it for malicious software." Right-click the app in Applications and choose **Open**, or run `xattr -dr com.apple.quarantine /Applications/Asteria.app`.
- **Windows:** SmartScreen says "Windows protected your PC." Click **More info**, then **Run anyway**.

## Make your own skills

Skills are JSON files, and the built-in ones use the same format you would. Type `open skills folder`, drop in a `.json`, and it shows up immediately. A skill can reuse any built-in operation with different settings, chain other skills, or run a program you have installed (pandoc, ImageMagick, ...). See [skills/README.md](skills/README.md) for the format and [skills/examples](skills/examples) for an example.

## Build from source

You need Go 1.26+, Node 22+ and the Wails 3 CLI:

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
```

On Linux you also need `build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev`.

```bash
wails3 dev               # run with hot reload
wails3 build             # build for this machine into bin/
wails3 package           # .app on macOS, NSIS installer on Windows, AppImage/deb/rpm on Linux

go test -tags nodynamic ./internal/...   # skill tests (media tests run when ffmpeg is installed)
cd frontend && npm run check              # type-check the UI
```

The screenshots and the GIF above come from the real app, driven by a headless Chromium against the server-mode build: `go build -tags server,nodynamic -o bin/asteria-server .`, then `cd scripts/capture && npm install && node capture.mjs readme gif` (needs ffmpeg; the demo photos are NASA's, public domain). `node capture.mjs states` captures every UI state at three window sizes in both themes.

The tests exercise built-in operations on real files in [`testdata/fixtures`](testdata/fixtures) (a HEIC written by Apple's own encoder, a 3-page PDF, a short video, CSV/JSON/YAML with Unicode) and check the output itself: pixels, page counts, durations, exact text.

### Cutting a release

```bash
git tag v0.1.0 && git push origin v0.1.0
```

[`release.yml`](.github/workflows/release.yml) stamps the version into the build files, builds the macOS universal DMG, Windows installer and Linux packages, and attaches them to a **draft** release for you to check and publish. Signing isn't set up: removing the warnings above needs an Apple Developer ID (99 USD a year, plus notarization in the workflow) and a Windows code-signing certificate.

## Project layout

```text
app.go, main.go, preview.go   Wails service, window, preview file server
internal/ops                  native operations (image, pdf, media, archive, data, cli)
internal/engine               runs skills: chains, replay, undo, cancel, progress
internal/session              tray files, workspace, collision-safe export
internal/skills               skill format, loader, hot reload, search ranking
internal/inspect              dimensions, page counts, durations, thumbnails
skills/core                   built-in skills (JSON, embedded in the app)
frontend/                     Svelte 5 UI (type: Geist and Geist Mono, OFL; icons: Lucide, ISC; PDF pages: pdf.js, Apache-2.0)
scripts/capture               screenshots, demo GIF and app icon render
build/                        icons, platform config and Taskfiles
```

## What's next

- **PDF to images** as a skill needs a PDF renderer in Go; there isn't a solid pure-Go one yet. (The one-file view already shows a PDF's first page, rendered in the UI with pdf.js.)
- **Keep photo metadata.** Re-encoding drops EXIF (location included), which is good for privacy but not always what you want.
- **Animated GIF/WebP** editing. Image skills refuse animated files today instead of silently keeping only the first frame.
- **The agentic layer.** The pipeline is plain JSON on purpose, so a planner can turn "convert these to PNG, resize to 1200 wide and zip them" into the same skills you'd type by hand, with the same previews.
