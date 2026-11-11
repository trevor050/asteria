# Writing image skills

Every image skill runs on one pixel core (`internal/img`) through a `Pixel` handler. Follow this file exactly; a skill that does not meet the bar at the end is cut, not shipped.

## The core, in one screen

```go
// internal/img
type Image struct {
    W, H    int
    Pix     []uint16 // R,G,B,A per pixel, row-major, 16-bit, STRAIGHT alpha, sRGB-ENCODED
    Deep    bool     // source had >8 bits; keep as is
    Scale   float64  // 1 = full resolution; <1 = preview proxy. Multiply pixel-unit params by Scale.
    Quality int      // encoder quality carried along the chain; leave alone
}
img.New(w, h) *Image                      // transparent, Scale 1
m.NewLike() / m.Sized(w, h) *Image        // blank, carries Deep/Scale/Quality  <- use these for outputs
m.Clone() *Image
m.At(x, y) (r, g, b, a uint16); m.Set(x, y, r, g, b, a); m.Off(x, y) int
m.Opaque() bool

// Channel conversions
img.F(u16) float32         // encoded 0..1
img.Q(float32) uint16      // 0..1 -> 16-bit, clamped and rounded
img.Lin(u16) float32       // encoded 16-bit -> linear light (LUT)
img.Enc(float32) uint16    // linear light -> encoded 16-bit (clamped)
img.SRGBToLinear / img.LinearToSRGB (float64)
img.Luma(r, g, b float32) float32            // linear-light luminance (Rec.709)
img.LinearToOKLab(r, g, b) (L, A, B)         // input LINEAR rgb
img.OKLabToLinear(L, A, B) (r, g, b)         // may leave gamut; MapColor clamps

// Per-pixel helpers (parallel, alpha preserved, results clamped)
img.MapColor(m, img.Encoded|img.LinearLight, func(r, g, b float32) (r, g, b float32)) *Image
img.MapRGBA(m, space, func(r, g, b, a float32) (r, g, b, a float32)) *Image
img.LUT16(m, func(v float32) float32) *Image   // same curve on R,G,B in ENCODED space; fastest

// Spatial work: premultiplied linear light
type Linear struct{ W, H int; Pix []float32 } // R,G,B premultiplied, A
m.ToLinear() *Linear;  l.ToImage(like *Image) *Image;  img.NewLinear(w, h)
img.Blur(l, sigmaX, sigmaY float64) *Linear     // gaussian, any sigma, edges clamp
img.Resize(m, w, h, img.Lanczos3|img.CatmullRom|img.Nearest) *Image
img.Fit(w, h, maxW, maxH) (int, int); img.Proxy(m, maxSide) *Image
img.Rotate90/Rotate180/Rotate270/FlipH/FlipV(m) *Image
img.Rows(ctx, h, func(y0, y1 int))   // parallel over row bands; use for custom loops (ctx may be nil)
img.ParseColor(s) (img.Color, error); img.Color{R,G,B,A float32} (encoded, straight); c.Hex()
img.Clamp01(v float32) float32
```

### Which space

- **Linear light** (`img.LinearLight`, `ToLinear`): anything that mixes pixels or light. Blurs, sharpening, resampling, rotation by arbitrary angles, compositing, glow/bloom, exposure (a linear multiply), white balance / temperature (linear channel gains), vignette (darkening light), noise added as light, chromatic aberration.
- **Encoded sRGB** (`img.Encoded`, `LUT16`): tone and curve operations that people tune by eye and compare with other editors. Brightness, contrast, gamma, levels, curves, highlights/shadows tone curves, posterize, threshold, invert, sepia matrix, duotone mapping.
- **OKLab / OKLCh**: saturation, vibrance, hue rotation, colour distance (palette, colour replace, dithering to a palette).
- **Alpha**: straight alpha. Colour edits never change alpha. Anything spatial goes through premultiplied `Linear` so transparent pixels never bleed their (meaningless) colour. Opacity edits change only alpha.

Write a one-line comment on each handler saying which space it uses and why, if it is not obvious.

## A handler

```go
// internal/ops/<family>.go — package ops
func init() {
    register(Handler{Name: "adjust.saturate", Pixel: pixSaturate, Live: true, Video: videoSaturate})
}

func pixSaturate(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
    k, err := args.Amount("amount", 1.4, 0, 4)   // default must visibly do something
    if err != nil { return nil, err }
    if k == 1 { return m, nil }                   // no-op: return the input
    ...
    return out, nil                               // NEVER modify m
}
```

- `Pixel` funcs must not modify their input. Return `m` itself when nothing changes.
- `Live: true` only if the op finishes in well under 100 ms on a 1-megapixel proxy (per-pixel colour ops, small blurs). Heavy ones (big motion blur, denoise, median) leave it false.
- `Video` (optional) returns an ffmpeg `-vf` filter string for the same arguments. Only add it when the ffmpeg filter really matches what the image op does; then add `"@video"` to the skill's `inputTypes`.
- Pixel-unit parameters (radius, offset, width, size) are multiplied by `m.Scale` (use `args.Length(...)` which does it for you). Percent-of-size parameters are relative to the current `m.W`/`m.H` and need no scaling.
- Long loops: use `img.Rows(ctx, ...)` so work is parallel and stops on cancel; return `ctx.Err()` if cancelled.
- Errors the user should read: `Userf("...")` with a sentence that says how to fix it.
- Handler names: `<family>.<verb>`, e.g. `adjust.levels`, `effect.vignette`, `geo.crop`, `compose.watermark`, `info.palette`.
- Non-pixel operations (several inputs, folder outputs, metadata-only, JSON output): use `Run func(ctx, Job) error` as before, with `Combine: true` for many-in-one-out. Use `DecodeWorking(path)` and `EncodeWorking(m, path)` so they share decode/encode behaviour.

### Arguments (internal/ops/params.go)

```go
args.Amount(key, def, lo, hi)   // multiplier, 1 = unchanged: "150%"->1.5, "30%"->0.3, "+50"/"50"->1.5, "-30"->0.7, "2x"->2; JSON numbers are relative (+N%)
args.Signed(key, def, lo, hi)   // plain number in range: "+20", "-15", "0.4" (percent sign ignored)
args.Float(key, def); args.IntInRange(key, def, min, max)
args.Degrees(key, def)          // "90", "-15°", "45deg", "0.25turn"
args.Length(key, defPx, refPx, m.Scale, maxPx) // "12", "12px" (scaled), "5%" of refPx
args.Color(key, def)            // "#ff8800", "f80", "rgb(...)", "255,136,0", "white", "transparent"
args.Aspect(key, def)           // "16:9", "4x5", "1.5", "square"
args.Choice(key, def, allowed, aliases)
args.Bool(key, def); args.String(key, def)
```

A skill has **at most one user parameter** (UI contract). Variants become separate skills with fixed `"with"` values, e.g. `flip` and `mirror` as two skills on one handler.

## A skill file

`skills/core/<family>/<id>.json`, same format users write (see skills/README.md):

```json
{
  "id": "saturate",
  "name": "Saturate",
  "version": "1.0.0",
  "author": "asteria",
  "aliases": ["saturation", "more color", "vivid", "desaturate", "less color", "colorful"],
  "category": "adjust",
  "description": "Make colours richer (+40% by default) or duller with a negative amount",
  "inputTypes": ["@image", "@video"],
  "outputType": "",
  "params": [{
    "name": "amount", "label": "Amount", "type": "string", "default": "+40",
    "presets": ["+20", "+40", "+80", "-30", "-60"],
    "min": -100, "max": 200, "unit": "%",
    "hint": "+40 or 140% = richer · -30 or 70% = duller · -100 = grey"
  }],
  "executor": { "type": "native", "handler": "adjust.saturate" },
  "permissions": ["files.read", "files.write", "files.temp"],
  "dangerLevel": 0
}
```

- `id`: lower-case, unique across all of skills/core (check with `grep -r '"id": "x"' skills/core`).
- `name`: Title Case, 1-3 words, what people call it. `description`: one line saying what will happen, with the default.
- `aliases`: words people actually type, including common misspellings and the opposite word where one skill covers both (`desaturate`).
- `category`: `adjust` (colour and tone), `filter` (detail and effects), `transform` (geometry), `compose` (compositing and output), `info` (information and hygiene).
- Params: `type` is `"string"` for anything with units or signs, `"float"`/`"int"` for plain numbers. Give `presets` (Tab cycles them) and a `hint`. Give `min`/`max` for numeric scrubbing where it makes sense (they drive the live slider).
- The **bare command must visibly work** with defaults ("saturate" alone saturates). Choose defaults a person would pick.
- `inputTypes`: `["@image"]`; add `"@video"` only when `Video` is implemented.
- `outputType`: `""` keeps the input format; set it (e.g. `".png"`) when the result needs transparency or is a different kind of file (`".json"`, `"folder"`).

## Tests

`internal/ops/<family>_test.go`, package `ops`. Shared helpers live in `pixel_helpers_test.go`: `scene()` (48x32: grey ramp, colour patches, half-transparent band), `solid(w,h,r,g,b,a)`, `apply(t, handler, m, args)`, `px(m,x,y) [4]float64`, `closePx(a,b,tol)`, reference `srgbToLin`, `linToSRGB`, `oklch`.

Every handler needs a test that checks **output pixels against known-correct values**, computed by hand or by an independent reference written in the test (not by calling the same code path). Examples of acceptable checks:

- exact values: invert of (0.2, 0.4, 0.6) is (0.8, 0.6, 0.4); threshold at 50% maps 0.49 to 0 and 0.51 to 1.
- properties with a reference: saturate keeps OKLab lightness and scales chroma by the factor; exposure +1 stop doubles linear light; a blur of a flat area is unchanged and conserves energy.
- geometry: a marked pixel lands at the computed coordinate; output size is exact.
- alpha: colour ops leave alpha untouched; spatial ops do not bleed transparent colour (put opaque blue next to transparent red; no red appears).
- the default (bare) command changes the picture, and an obviously wrong value returns an error.

"It ran without error" is not a test. Keep test images tiny (≤ 64x64) so the suite stays fast.

## Acceptance bar

A skill ships only if: the handler is correct in the documented colour space, the JSON loads, the bare command does something sensible, the parameter formats in the hint all work, it has pixel-checking tests, and it does not modify its input. Anything else is cut and listed with the reason.

## Rules for team members

- Edit only the files assigned to you. Do not edit internal/img, params.go, engine, app or UI code. If you need a helper, write it unexported in your own file with your family prefix (e.g. `geoAffine`).
- Do not run `go build`, `go test`, `go vet` or `npm`. `gofmt -l` / `gofmt -w` on your own files is fine. The lead compiles and tests, and will send you errors to fix.
- Go 1.26: `min`/`max` builtins, `for i := range n` are available.
