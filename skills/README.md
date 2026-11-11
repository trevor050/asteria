# Skills

Everything Asteria can do is a skill: one JSON file that says what it accepts, what it produces and how it runs. The built-in skills in [`core/`](core/) use exactly the same format as the ones you write, so if a built-in does something close to what you want, copy it and change it.

## Adding your own

1. In Asteria, type `open skills folder` and press Enter. (It's `~/Library/Application Support/asteria/skills` on macOS, `%AppData%\asteria\skills` on Windows and `~/.config/asteria/skills` on Linux.)
2. Drop a `.json` file in there. Subfolders are fine.
3. That's it. Asteria watches the folder and picks up new or edited skills immediately. If a file has a mistake, Asteria tells you which file and what's wrong instead of silently ignoring it.

A skill in your folder with the same `id` as a built-in replaces the built-in.

## A small example

[`examples/web_thumbnail.json`](examples/web_thumbnail.json) chains two built-ins: fit the image inside 1280 px, then save it as WebP at 80% quality.

```json
{
  "id": "web_thumbnail",
  "name": "Web Thumbnail",
  "version": "1.0.0",
  "aliases": ["thumb", "blog image"],
  "category": "transform",
  "description": "Fit inside 1280 px and save as WebP at 80% quality",
  "inputTypes": ["@image"],
  "outputType": ".webp",
  "executor": {
    "type": "pipeline",
    "steps": [
      { "skillId": "resize", "params": { "size": "1280" } },
      { "skillId": "convert_to_webp", "params": { "quality": 80 } }
    ]
  }
}
```

Copy it into your skills folder and type `thumb`.

## Format

| Field | Required | What it does |
| --- | --- | --- |
| `id` | yes | Unique id: lower-case letters, digits, `_`, `-`, `.` |
| `name` | yes | What you see and search for in the command bar |
| `version` | yes | Any version string, for your own bookkeeping |
| `inputTypes` | yes | File types it accepts: extensions (`".png"`), groups (`"@image"`), `"folder"` or `"*"` |
| `outputType` | no | Extension of the result (`".jpg"`), `"folder"`, or empty to keep the input's type |
| `executor` | yes | How it runs: `native`, `pipeline` or `cli` (see below) |
| `params` | no | At most **one** parameter the user types (see below) |
| `aliases` | no | Extra words that should find it (`"jpg"`, `"extract audio"`) |
| `description`, `category`, `author` | no | Shown in suggestions. `category` is the family the skill is listed under and nudges ranking: `convert`, `transform` (size and shape), `compress`, `adjust` (colour and tone), `filter` (effects), `compose`, `info`, `sizes`, `pdf`, `media`, `archive`, `data` |
| `priority` | no | 0-20. Picks which skill stands for its family in the suggestions shown before anything is typed, and breaks ties between equally good matches |
| `combine` | no | Many files in, one file out. Inferred from native handlers that combine |
| `outputName` | no | Base name for combined results, e.g. `"merged"` |
| `requires` | no | External programs it needs. Native handlers and CLI commands add theirs automatically |
| `permissions` | no | See [Permissions](#permissions) |

Input groups: `@image` (png, jpg, gif, bmp, tiff, webp, heic), `@video`, `@audio`, `@media` (video + audio), `@pdf`, `@archive` (zip, tar, tar.gz), `@data` (csv, tsv, json, yaml, md).

### Parameters

A skill takes zero or one parameter. One value keeps the command bar fast: `resize 50%`, `trim 0:05-0:30` and `extract pages 1-3, 5` are all "skill name, then value".

```json
"params": [{
  "name": "size",
  "label": "Size",
  "type": "string",
  "default": "50%",
  "presets": ["75%", "50%", "25%", "1920"],
  "unit": "",
  "hint": "50% · 1920 (width) · x1080 (height) · 1920x1080 (fit inside)"
}]
```

Tab cycles through `presets`. Values arrive as typed and the handler validates them, so a bad value gives a readable error on that file instead of a crash.

### `native`: reuse a built-in operation

```json
"executor": { "type": "native", "handler": "image.convert", "with": { "quality": 60 } }
```

`with` sets fixed arguments; the user's parameter, if any, is layered on top. For example, a "JPEG for email" skill is `image.convert` with `outputType: ".jpg"` and `with: { "quality": 60 }`.

| Handler | Arguments | Notes |
| --- | --- | --- |
| `image.convert` | `quality` (1-100) | Writes the format of `outputType`: png, jpg, webp, heic, gif, bmp, tiff |
| `image.resize` | `size`, `quality` | `50%`, `1920` (width), `x1080` (height), `1920x1080` (fit inside). Never upscales by pixels |
| `image.compress` | `quality` | Lossy for JPEG/WebP/HEIC, lossless max for PNG; keeps the original if re-encoding doesn't help |
| `image.rotate` | `angle` (90, 180, 270) | Clockwise |
| `image.grayscale` | | |
| `image.blur` | `radius` | Gaussian |
| `pdf.from_images` | | Combine. One page per image, sized to the image |
| `pdf.merge` | | Combine. In tray order |
| `pdf.split` | | Folder output: `page-001.pdf`, `page-002.pdf`, ... |
| `pdf.extract` | `pages` | `1-3, 5, 8-`, in the order typed |
| `pdf.compress` | | Lossless optimization; keeps the original if it can't shrink it |
| `media.convert` | `crf` | Needs ffmpeg. Encoding follows `outputType`: mp4, webm, gif, mp3, m4a, wav, flac, ogg, opus |
| `media.compress` | `crf` (18-40) | Needs ffmpeg. H.264 at the given quality |
| `media.trim` | `range` | Needs ffmpeg. `0:05-0:30`, `-1:00`, `1:00-` |
| `archive.zip` | | Combine. Folders are zipped with their structure |
| `archive.extract` | | Folder output. zip, tar, tar.gz, gz. Blocks path traversal and archive bombs |
| `data.convert` | | csv/tsv → json or md, json → csv or yaml, yaml → json, md → html |

### `pipeline`: run other skills in order

```json
"executor": { "type": "pipeline", "steps": [{ "skillId": "resize", "params": { "size": "1280" } }, { "skillId": "convert_to_webp" }] }
```

Each step gets the previous step's output. Step `params` override the user's value. Intermediate files are cleaned up; only the final result becomes a step in the tray.

### `cli`: run a program you have installed

```json
{
  "id": "docx_from_markdown",
  "name": "Markdown to Word",
  "version": "1.0.0",
  "inputTypes": [".md"],
  "outputType": ".docx",
  "executor": { "type": "cli", "command": "pandoc", "args": ["{{input}}", "-o", "{{output}}"], "timeoutMs": 60000 },
  "permissions": ["files.read", "files.write", "tools.exec"]
}
```

Arguments are passed straight to the program, never through a shell. `{{input}}`, `{{output}}` and `{{<param name>}}` are substituted; a lone `{{inputs}}` argument expands to every input of a combine skill. The program must write `{{output}}`; if it exits non-zero, its last lines of stderr are shown on that file.

Asteria finds programs on your `PATH` and in the usual Homebrew, winget, Scoop and Chocolatey folders (GUI apps on macOS don't inherit your shell's `PATH`). If the program isn't installed, the suggestion says so and Enter shows how to install it.

## Permissions

Built-in skills are trusted. Skills from your folder declare what they need:

- Base, allowed automatically: `files.read`, `files.write`, `files.temp`, `tools.exec` (running ffmpeg, ffprobe, magick, pandoc, cwebp, gs or qpdf).
- Elevated, asked once per skill: `tools.exec.any` (any other program), `files.anywhere`, `network`, `system`.

A CLI skill that runs something outside the list above has to declare `tools.exec.any`, and Asteria asks before the first run.

## Where things live

Core skills are embedded in the app binary. When you run Asteria from a source checkout, `skills/core` is also watched, so editing a built-in reloads it without rebuilding.
