package ops

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/webp"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"asteria/internal/img"
)

// Compose family: watermarks, backgrounds, shadows, collages, strips,
// overlays, animations, sprite sheets, tiles and icon sets. Handlers are
// named "compose.*"; skills live in skills/core/compose.
//
// All compositing happens in linear light with straight-alpha storage
// (see cmpOverPx), so semi-transparent edges and shadows mix like light
// instead of darkening in gamma space. Resampling goes through img.Resize
// (premultiplied linear light). Inputs are never modified.

const (
	// cmpLoadBudget caps the total pixels held while combining images, so a
	// collage of many full-size photos stays within memory.
	cmpLoadBudget = 24e6
	// cmpCanvasCap caps the pixels of a combined output.
	cmpCanvasCap = 36e6
	// cmpAnimMaxSide caps the longer side of animation frames.
	cmpAnimMaxSide = 1280
	// Removal thresholds (OKLab distance) for compose.removebg.
	cmpKeyInner = 0.06
	cmpKeyOuter = 0.16
)

func init() {
	register(Handler{Name: "compose.text", Pixel: pixCmpText})
	register(Handler{Name: "compose.background", Pixel: pixCmpBackground})
	register(Handler{Name: "compose.removebg", Pixel: pixCmpRemoveBG})
	register(Handler{Name: "compose.shadow", Pixel: pixCmpShadow})

	register(Handler{Name: "compose.grid", Run: cmpRunGrid, Combine: true})
	register(Handler{Name: "compose.contactsheet", Run: cmpRunContactSheet, Combine: true})
	register(Handler{Name: "compose.strip", Run: cmpRunStrip, Combine: true})
	register(Handler{Name: "compose.overlay", Run: cmpRunOverlay, Combine: true})
	register(Handler{Name: "compose.watermarkimage", Run: cmpRunWatermarkImage, Combine: true})
	register(Handler{Name: "compose.gif", Run: cmpRunGIF, Combine: true})
	register(Handler{Name: "compose.webpanim", Run: cmpRunWebPAnim, Combine: true})
	register(Handler{Name: "compose.spritesheet", Run: cmpRunSpriteSheet, Combine: true})

	register(Handler{Name: "compose.split", Run: cmpRunSplit})
	register(Handler{Name: "compose.frames", Run: cmpRunFrames})
	register(Handler{Name: "compose.favicon", Run: cmpRunFavicon})
	register(Handler{Name: "compose.appicon", Run: cmpRunAppIcon})
}

// ---------------------------------------------------------------------------
// Compositing primitives

// cmpOverPx composites a straight-alpha source of linear colour (lr,lg,lb)
// and alpha sa over the straight-alpha encoded pixel p, in linear light.
func cmpOverPx(p []uint16, lr, lg, lb, sa float32) {
	if sa <= 0 {
		return
	}
	da := img.F(p[3])
	if da == 0 || sa >= 1 {
		p[0], p[1], p[2], p[3] = img.Enc(lr), img.Enc(lg), img.Enc(lb), img.Q(sa)
		return
	}
	k := da * (1 - sa)
	oa := sa + k
	p[0] = img.Enc((lr*sa + img.Lin(p[0])*k) / oa)
	p[1] = img.Enc((lg*sa + img.Lin(p[1])*k) / oa)
	p[2] = img.Enc((lb*sa + img.Lin(p[2])*k) / oa)
	p[3] = img.Q(oa)
}

// cmpBlit composites src onto dst with its top-left at (ox, oy), scaling the
// source alpha by op (0..1). Fully opaque source pixels at full opacity are
// copied exactly.
func cmpBlit(dst, src *img.Image, ox, oy int, op float32) {
	x0, y0 := max(0, ox), max(0, oy)
	x1, y1 := min(dst.W, ox+src.W), min(dst.H, oy+src.H)
	if x1 <= x0 || y1 <= y0 {
		return
	}
	img.Rows(nil, y1-y0, func(a, b int) {
		for y := y0 + a; y < y0+b; y++ {
			for x := x0; x < x1; x++ {
				s := src.Pix[src.Off(x-ox, y-oy):][:4]
				d := dst.Pix[dst.Off(x, y):][:4]
				if s[3] == 0 {
					continue
				}
				if s[3] == 0xffff && op >= 1 {
					d[0], d[1], d[2], d[3] = s[0], s[1], s[2], 0xffff
					continue
				}
				cmpOverPx(d, img.Lin(s[0]), img.Lin(s[1]), img.Lin(s[2]), img.F(s[3])*op)
			}
		}
	})
}

// cmpBlitMask paints a solid linear colour through an alpha mask.
func cmpBlitMask(dst *img.Image, mask *image.Alpha, ox, oy int, lr, lg, lb, op float32) {
	b := mask.Bounds()
	for y := 0; y < b.Dy(); y++ {
		dy := oy + y
		if dy < 0 || dy >= dst.H {
			continue
		}
		for x := 0; x < b.Dx(); x++ {
			dx := ox + x
			if dx < 0 || dx >= dst.W {
				continue
			}
			c := mask.Pix[mask.PixOffset(b.Min.X+x, b.Min.Y+y)]
			if c == 0 {
				continue
			}
			cmpOverPx(dst.Pix[dst.Off(dx, dy):][:4], lr, lg, lb, float32(c)/255*op)
		}
	}
}

// cmpFill sets every pixel of m.
func cmpFill(m *img.Image, r, g, b, a uint16) {
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = r, g, b, a
	}
}

// cmpWhite returns an opaque white canvas carrying like's metadata.
func cmpWhite(like *img.Image, w, h int) *img.Image {
	c := like.Sized(w, h)
	cmpFill(c, 0xffff, 0xffff, 0xffff, 0xffff)
	return c
}

// cmpCrop copies the rectangle [x0,x1) x [y0,y1) of m.
func cmpCrop(m *img.Image, x0, y0, x1, y1 int) *img.Image {
	out := m.Sized(x1-x0, y1-y0)
	for y := y0; y < y1; y++ {
		copy(out.Pix[out.Off(0, y-y0):out.Off(0, y-y0)+4*out.W], m.Pix[m.Off(x0, y):m.Off(x0, y)+4*out.W])
	}
	return out
}

// cmpFitSize returns the size of a w x h image fitted inside maxW x maxH
// (never enlarging unless up is set).
func cmpFitSize(w, h, maxW, maxH int, up bool) (int, int) {
	if !up && w <= maxW && h <= maxH {
		return w, h
	}
	return img.Fit(w, h, maxW, maxH)
}

// cmpFitted resizes m to fit inside maxW x maxH.
func cmpFitted(m *img.Image, maxW, maxH int, up bool) *img.Image {
	w, h := cmpFitSize(m.W, m.H, maxW, maxH, up)
	return img.Resize(m, w, h, img.Lanczos3)
}

// cmpEightBit returns a view of m that encodes as 8-bit (icons, tiles).
func cmpEightBit(m *img.Image) *img.Image {
	c := *m
	c.Deep = false
	return &c
}

// cmpSave writes m to path (format by extension).
func cmpSave(m *img.Image, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return EncodeWorking(m, path)
}

// cmpLoad decodes every input, shrinking each so that all together hold at
// most budget pixels (and optionally so the longer side is at most maxSide).
// Inputs arrive in the user's order and the result keeps it.
func cmpLoad(ctx context.Context, job Job, budget float64, maxSide int) ([]*img.Image, error) {
	n := len(job.Inputs)
	if n == 0 {
		return nil, Userf("select at least one image")
	}
	per := budget / float64(n)
	out := make([]*img.Image, 0, n)
	for i, in := range job.Inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := DecodeWorking(in)
		if err != nil {
			return nil, err
		}
		f := 1.0
		if a := float64(m.W) * float64(m.H); a > per {
			f = math.Sqrt(per / a)
		}
		if maxSide > 0 {
			f = math.Min(f, float64(maxSide)/float64(max(m.W, m.H)))
		}
		if f < 1 {
			m = img.Resize(m, max(1, int(math.Round(float64(m.W)*f))), max(1, int(math.Round(float64(m.H)*f))), img.Lanczos3)
		}
		out = append(out, m)
		job.Progress(0.5 * float64(i+1) / float64(n))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Text

var (
	cmpFontOnce sync.Once
	cmpFontData *opentype.Font
	cmpFontErr  error
)

// cmpFace returns Go Regular at size pixels.
func cmpFace(size float64) (font.Face, error) {
	cmpFontOnce.Do(func() { cmpFontData, cmpFontErr = opentype.Parse(goregular.TTF) })
	if cmpFontErr != nil {
		return nil, cmpFontErr
	}
	return opentype.NewFace(cmpFontData, &opentype.FaceOptions{Size: math.Max(size, 1), DPI: 72, Hinting: font.HintingNone})
}

// cmpTextMask draws s as antialiased coverage, tight to the font's ascent
// and descent with one pixel of slack on each side.
func cmpTextMask(face font.Face, s string) *image.Alpha {
	met := face.Metrics()
	asc, desc := met.Ascent.Ceil(), met.Descent.Ceil()
	w := font.MeasureString(face, s).Ceil() + 2
	dst := image.NewAlpha(image.Rect(0, 0, w, asc+desc))
	d := font.Drawer{Dst: dst, Src: image.Opaque, Face: face, Dot: fixed.P(1, asc)}
	d.DrawString(s)
	return dst
}

// cmpTextWidth is the advance width of s in pixels.
func cmpTextWidth(face font.Face, s string) float64 {
	return float64(font.MeasureString(face, s)) / 64
}

// Watermark text: white at 70% over a soft dark shadow, bottom-right.
// Size is 4% and margin 3% of the shorter side, so a proxy previews the
// same look. Composited in linear light.
func pixCmpText(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	text := args.String("text", "© "+strconv.Itoa(time.Now().Year()))
	if strings.TrimSpace(text) == "" {
		return nil, Userf("enter the text to stamp on the picture")
	}
	short := min(m.W, m.H)
	margin := int(math.Round(0.03 * float64(short)))
	size := 0.04 * float64(short)
	face, err := cmpFace(size)
	if err != nil {
		return nil, err
	}
	// Shrink to fit when the text is wider than the picture allows.
	if avail := float64(m.W - 2*margin); avail > 0 {
		if w := cmpTextWidth(face, text); w > avail {
			size *= avail / w
			if face, err = cmpFace(size); err != nil {
				return nil, err
			}
		}
	}
	mask := cmpTextMask(face, text)
	mw, mh := mask.Bounds().Dx(), mask.Bounds().Dy()
	sigma := math.Max(0.6, size*0.06)
	off := max(1, int(math.Round(size*0.06)))
	pad := int(math.Ceil(3*sigma)) + off

	// Shadow: coverage blurred in a padded region, then sampled shifted
	// down-right.
	rw, rh := mw+2*pad, mh+2*pad
	sh := img.NewLinear(rw, rh)
	for y := 0; y < mh; y++ {
		for x := 0; x < mw; x++ {
			sh.Pix[4*((y+pad)*rw+x+pad)+3] = float32(mask.Pix[mask.PixOffset(x, y)]) / 255
		}
	}
	sh = img.Blur(sh, sigma, sigma)
	shadowAt := func(x, y int) float32 {
		x, y = x-off, y-off
		if x < 0 || y < 0 || x >= rw || y >= rh {
			return 0
		}
		return sh.Pix[4*(y*rw+x)+3]
	}

	tx, ty := m.W-margin-mw, m.H-margin-mh
	out := m.Clone()
	for y := 0; y < rh; y++ {
		iy := ty - pad + y
		if iy < 0 || iy >= m.H {
			continue
		}
		for x := 0; x < rw; x++ {
			ix := tx - pad + x
			if ix < 0 || ix >= m.W {
				continue
			}
			p := out.Pix[out.Off(ix, iy):][:4]
			if s := min(1, shadowAt(x, y)*1.5) * 0.65; s > 0 {
				cmpOverPx(p, 0, 0, 0, s)
			}
			mx, my := x-pad, y-pad
			if mx >= 0 && my >= 0 && mx < mw && my < mh {
				if c := mask.Pix[mask.PixOffset(mx, my)]; c > 0 {
					cmpOverPx(p, 1, 1, 1, float32(c)/255*0.7)
				}
			}
		}
	}
	return out, ctx.Err()
}

// ---------------------------------------------------------------------------
// Background

// Fill background: the picture over a solid colour in linear light; the
// result is opaque. The colour's own alpha is ignored.
func pixCmpBackground(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	c, err := args.Color("color", "white")
	if err != nil {
		return nil, err
	}
	if c.A == 0 {
		return nil, Userf("pick a solid colour for the background, such as white or #ff8800")
	}
	if m.Opaque() {
		return m, nil
	}
	br := float32(img.SRGBToLinear(float64(c.R)))
	bg := float32(img.SRGBToLinear(float64(c.G)))
	bb := float32(img.SRGBToLinear(float64(c.B)))
	out := m.NewLike()
	img.Rows(ctx, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			a := img.F(m.Pix[i+3])
			k := 1 - a
			out.Pix[i] = img.Enc(img.Lin(m.Pix[i])*a + br*k)
			out.Pix[i+1] = img.Enc(img.Lin(m.Pix[i+1])*a + bg*k)
			out.Pix[i+2] = img.Enc(img.Lin(m.Pix[i+2])*a + bb*k)
			out.Pix[i+3] = 0xffff
		}
	})
	return out, ctx.Err()
}

// cmpCornerColor returns the linear colour that dominates the four corner
// regions (5% of the shorter side, at least 1 pixel), ignoring mostly
// transparent pixels. ok is false when the corners hold no opaque pixels.
func cmpCornerColor(m *img.Image) (r, g, b float32, ok bool) {
	cs := max(1, min(m.W, m.H)/20)
	type bin struct {
		n       int
		r, g, b uint64
	}
	bins := map[int]*bin{}
	for _, c := range [][2]int{{0, 0}, {m.W - cs, 0}, {0, m.H - cs}, {m.W - cs, m.H - cs}} {
		for y := max(0, c[1]); y < min(m.H, c[1]+cs); y++ {
			for x := max(0, c[0]); x < min(m.W, c[0]+cs); x++ {
				pr, pg, pb, pa := m.At(x, y)
				if pa < 0x8000 {
					continue
				}
				k := int(pr>>11)<<10 | int(pg>>11)<<5 | int(pb>>11)
				e := bins[k]
				if e == nil {
					e = &bin{}
					bins[k] = e
				}
				e.n++
				e.r += uint64(pr)
				e.g += uint64(pg)
				e.b += uint64(pb)
			}
		}
	}
	var best *bin
	bestKey := -1
	for k, e := range bins {
		if best == nil || e.n > best.n || (e.n == best.n && k < bestKey) {
			best, bestKey = e, k
		}
	}
	if best == nil {
		return 0, 0, 0, false
	}
	n := uint64(best.n)
	return img.Lin(uint16(best.r / n)), img.Lin(uint16(best.g / n)), img.Lin(uint16(best.b / n)), true
}

// Remove background colour: OKLab distance to the key colour. Within 0.06
// the pixel becomes fully transparent, from 0.06 to 0.16 alpha ramps up
// linearly, beyond 0.16 the pixel is untouched. Partly transparent pixels
// are de-fringed by un-blending the key from their colour in linear light
// (C = t*F + (1-t)*K, solve for F), so no halo of the old background stays.
func pixCmpRemoveBG(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	spec := strings.ToLower(args.String("color", "auto"))
	var kr, kg, kb float32
	if spec == "auto" {
		var ok bool
		kr, kg, kb, ok = cmpCornerColor(m)
		if !ok {
			return m, nil
		}
	} else {
		c, err := args.Color("color", "auto")
		if err != nil {
			return nil, err
		}
		kr = float32(img.SRGBToLinear(float64(c.R)))
		kg = float32(img.SRGBToLinear(float64(c.G)))
		kb = float32(img.SRGBToLinear(float64(c.B)))
	}
	kL, kA, kB := img.LinearToOKLab(kr, kg, kb)
	out := m.NewLike()
	img.Rows(ctx, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			p := m.Pix[i : i+4 : i+4]
			o := out.Pix[i : i+4 : i+4]
			if p[3] == 0 {
				continue
			}
			r, g, b := img.Lin(p[0]), img.Lin(p[1]), img.Lin(p[2])
			L, A, B := img.LinearToOKLab(r, g, b)
			d := float32(math.Sqrt(float64((L-kL)*(L-kL) + (A-kA)*(A-kA) + (B-kB)*(B-kB))))
			t := (d - cmpKeyInner) / (cmpKeyOuter - cmpKeyInner)
			switch {
			case t >= 1:
				copy(o, p)
			case t <= 0:
				// fully keyed out: stays transparent
			default:
				inv := 1 - t
				o[0] = img.Enc((r - inv*kr) / t)
				o[1] = img.Enc((g - inv*kg) / t)
				o[2] = img.Enc((b - inv*kb) / t)
				o[3] = img.Q(img.F(p[3]) * t)
			}
		}
	})
	return out, ctx.Err()
}

// ---------------------------------------------------------------------------
// Drop shadow

// cmpShadowGeometry returns blur sigma, offset and canvas padding for an
// image of the given size: sigma 2%, offset (1.2%, 2%) of the shorter side.
func cmpShadowGeometry(w, h int) (sigma float64, dx, dy, pad int) {
	s := float64(min(w, h))
	sigma = math.Max(0.5, 0.02*s)
	dx = int(math.Round(0.012 * s))
	dy = int(math.Round(0.02 * s))
	pad = int(math.Ceil(3*sigma)) + max(dx, dy)
	return
}

// Drop shadow: black at 45% opacity (times strength), blurred, offset down
// and right behind the picture's alpha, on an enlarged transparent canvas.
// Blur and compositing in premultiplied linear light.
func pixCmpShadow(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := args.Amount("strength", 1, 0, 2)
	if err != nil {
		return nil, err
	}
	op := float32(math.Min(1, 0.45*k))
	sigma, dx, dy, pad := cmpShadowGeometry(m.W, m.H)
	W, H := m.W+2*pad, m.H+2*pad
	src := m.ToLinear()
	sh := img.NewLinear(W, H)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			sh.Pix[4*((y+pad+dy)*W+x+pad+dx)+3] = src.Pix[4*(y*m.W+x)+3] * op
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sh = img.Blur(sh, sigma, sigma)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			s := src.Pix[4*(y*m.W+x):][:4]
			d := sh.Pix[4*((y+pad)*W+x+pad):][:4]
			k := 1 - s[3]
			d[0], d[1], d[2], d[3] = s[0]+d[0]*k, s[1]+d[1]*k, s[2]+d[2]*k, s[3]+d[3]*k
		}
	}
	return sh.ToImage(m), ctx.Err()
}

// ---------------------------------------------------------------------------
// Grid, contact sheet, strip

var cmpCRRe = regexp.MustCompile(`^(\d+)(?:\s*[x×*]\s*(\d+))?$`)

// cmpParseCR reads "3" or "3x2".
func cmpParseCR(s string) (a, b int, hasB, ok bool) {
	g := cmpCRRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if g == nil {
		return 0, 0, false, false
	}
	a, _ = strconv.Atoi(g[1])
	if g[2] != "" {
		b, _ = strconv.Atoi(g[2])
		hasB = true
	}
	return a, b, hasB, a > 0 && (!hasB || b > 0)
}

// cmpGridShape picks columns and rows for n images from the "columns"
// parameter ("auto", "3" or "3x2").
func cmpGridShape(spec string, n int) (cols, rows int, err error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec == "" || spec == "auto" {
		cols = int(math.Ceil(math.Sqrt(float64(n))))
	} else {
		a, b, hasB, ok := cmpParseCR(spec)
		if !ok || a > 50 || b > 50 {
			return 0, 0, Userf("%q is not a layout; try 3 (columns) or 3x2 (columns x rows)", spec)
		}
		cols = a
		if hasB && a*b < n {
			return 0, 0, Userf("%dx%d holds %d pictures but %d are selected; use more columns or rows", a, b, a*b, n)
		}
	}
	cols = max(1, min(cols, n))
	return cols, (n + cols - 1) / cols, nil
}

// cmpGridLayout computes the canvas size of a grid.
func cmpGridLayout(cols, rows, cw, ch, label, gap int) (int, int) {
	return cols*cw + (cols+1)*gap, rows*(ch+label) + (rows+1)*gap
}

// cmpCellSize returns the cell size (largest input), gap, label height and
// font size for a grid, shrinking the cell until the canvas fits the cap.
func cmpCellSize(ims []*img.Image, cols, rows int, labels bool) (cw, ch, gap, label, fs int) {
	for _, m := range ims {
		cw, ch = max(cw, m.W), max(ch, m.H)
	}
	for {
		gap = max(1, int(math.Round(0.02*float64(max(cw, ch)))))
		label, fs = 0, 0
		if labels {
			fs = max(9, int(math.Round(0.05*float64(cw))))
			label = int(math.Round(1.8 * float64(fs)))
		}
		w, h := cmpGridLayout(cols, rows, cw, ch, label, gap)
		if float64(w)*float64(h) <= cmpCanvasCap || cw < 8 || ch < 8 {
			return
		}
		cw, ch = int(float64(cw)*0.9), int(float64(ch)*0.9)
	}
}

func cmpRunGrid(ctx context.Context, job Job) error {
	return cmpGridJob(ctx, job, false)
}

func cmpRunContactSheet(ctx context.Context, job Job) error {
	return cmpGridJob(ctx, job, true)
}

// Grid: uniform cells the size of the largest input, each picture fitted
// inside its cell and centred, 2% gaps (also around the edge), white page.
// The contact sheet adds each file name in small grey text under its cell.
func cmpGridJob(ctx context.Context, job Job, labels bool) error {
	ims, err := cmpLoad(ctx, job, cmpLoadBudget, 0)
	if err != nil {
		return err
	}
	cols, rows, err := cmpGridShape(job.Args.String("columns", "auto"), len(ims))
	if err != nil {
		return err
	}
	cw, ch, gap, label, fs := cmpCellSize(ims, cols, rows, labels)
	W, H := cmpGridLayout(cols, rows, cw, ch, label, gap)
	canvas := cmpWhite(ims[0], W, H)
	var face font.Face
	var grey float32
	if labels {
		if face, err = cmpFace(float64(fs)); err != nil {
			return err
		}
		grey = float32(img.SRGBToLinear(0.4))
	}
	for i, m := range ims {
		if err := ctx.Err(); err != nil {
			return err
		}
		x := gap + (i%cols)*(cw+gap)
		y := gap + (i/cols)*(ch+label+gap)
		f := cmpFitted(m, cw, ch, true)
		cmpBlit(canvas, f, x+(cw-f.W)/2, y+(ch-f.H)/2, 1)
		if labels {
			name := cmpEllipsize(face, job.Name(i), float64(cw-4))
			mask := cmpTextMask(face, name)
			mw, mh := mask.Bounds().Dx(), mask.Bounds().Dy()
			cmpBlitMask(canvas, mask, x+(cw-mw)/2, y+ch+(label-mh)/2, grey, grey, grey, 1)
		}
		job.Progress(0.5 + 0.5*float64(i+1)/float64(len(ims)))
	}
	return cmpSave(canvas, job.Output)
}

// cmpEllipsize shortens s with "…" until it fits width pixels.
func cmpEllipsize(face font.Face, s string, width float64) string {
	if cmpTextWidth(face, s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if c := strings.TrimRight(string(r), " ") + "…"; cmpTextWidth(face, c) <= width {
			return c
		}
	}
	return "…"
}

// Strip: pictures side by side (or stacked with direction "vertical"),
// scaled to the smallest height (width) among them so none is enlarged.
func cmpRunStrip(ctx context.Context, job Job) error {
	dir, err := job.Args.Choice("direction", "horizontal", []string{"horizontal", "vertical"}, map[string]string{"h": "horizontal", "v": "vertical", "row": "horizontal", "column": "vertical"})
	if err != nil {
		return err
	}
	vertical := dir == "vertical"
	ims, err := cmpLoad(ctx, job, cmpLoadBudget, 0)
	if err != nil {
		return err
	}
	// t is the shared cross dimension (height for horizontal).
	t := math.MaxInt
	for _, m := range ims {
		t = min(t, map[bool]int{false: m.H, true: m.W}[vertical])
	}
	sizes := func(t int) (ws, hs []int, total int) {
		for _, m := range ims {
			var w, h int
			if vertical {
				w, h = t, max(1, int(math.Round(float64(m.H)*float64(t)/float64(m.W))))
				total += h
			} else {
				w, h = max(1, int(math.Round(float64(m.W)*float64(t)/float64(m.H)))), t
				total += w
			}
			ws, hs = append(ws, w), append(hs, h)
		}
		return
	}
	ws, hs, total := sizes(t)
	for float64(total)*float64(t) > cmpCanvasCap && t > 8 {
		t = int(float64(t) * 0.9)
		ws, hs, total = sizes(t)
	}
	W, H := total, t
	if vertical {
		W, H = t, total
	}
	canvas := ims[0].Sized(W, H)
	pos := 0
	for i, m := range ims {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := img.Resize(m, ws[i], hs[i], img.Lanczos3)
		if vertical {
			cmpBlit(canvas, f, 0, pos, 1)
			pos += hs[i]
		} else {
			cmpBlit(canvas, f, pos, 0, 1)
			pos += ws[i]
		}
		job.Progress(0.5 + 0.5*float64(i+1)/float64(len(ims)))
	}
	return cmpSave(canvas, job.Output)
}

// ---------------------------------------------------------------------------
// Overlay and image watermark

func cmpLoadPair(ctx context.Context, job Job) (base, top *img.Image, err error) {
	if len(job.Inputs) != 2 {
		return nil, nil, Userf("select exactly 2 images: the base picture first, then the one to place on top (you selected %d)", len(job.Inputs))
	}
	if base, err = DecodeWorking(job.Inputs[0]); err != nil {
		return nil, nil, err
	}
	if top, err = DecodeWorking(job.Inputs[1]); err != nil {
		return nil, nil, err
	}
	job.Progress(0.4)
	return base, top, ctx.Err()
}

// Overlay: the second picture centred over the first, shrunk to fit inside
// it when larger (never enlarged), normal alpha compositing in linear light.
func cmpRunOverlay(ctx context.Context, job Job) error {
	base, top, err := cmpLoadPair(ctx, job)
	if err != nil {
		return err
	}
	f := cmpFitted(top, base.W, base.H, false)
	out := base.Clone()
	cmpBlit(out, f, (base.W-f.W)/2, (base.H-f.H)/2, 1)
	job.Progress(0.8)
	return cmpSave(out, job.Output)
}

// Image watermark: the second picture as a logo, 20% of the base width (at
// most 40% of its height), 80% opacity, bottom-right with a 3% margin.
func cmpRunWatermarkImage(ctx context.Context, job Job) error {
	base, logo, err := cmpLoadPair(ctx, job)
	if err != nil {
		return err
	}
	w := max(1, int(math.Round(0.2*float64(base.W))))
	h := max(1, int(math.Round(float64(logo.H)*float64(w)/float64(logo.W))))
	if mh := max(1, int(math.Round(0.4*float64(base.H)))); h > mh {
		h = mh
		w = max(1, int(math.Round(float64(logo.W)*float64(h)/float64(logo.H))))
	}
	f := img.Resize(logo, w, h, img.Lanczos3)
	margin := int(math.Round(0.03 * float64(min(base.W, base.H))))
	out := base.Clone()
	cmpBlit(out, f, base.W-margin-f.W, base.H-margin-f.H, 0.8)
	job.Progress(0.8)
	return cmpSave(out, job.Output)
}

// ---------------------------------------------------------------------------
// Animation

// cmpAnimFrames loads the inputs and returns opaque frames, each fitted
// inside the first image's size and centred on white.
func cmpAnimFrames(ctx context.Context, job Job) ([]*img.Image, error) {
	if len(job.Inputs) < 2 {
		return nil, Userf("select at least 2 images to make an animation")
	}
	ims, err := cmpLoad(ctx, job, cmpLoadBudget, cmpAnimMaxSide)
	if err != nil {
		return nil, err
	}
	w, h := ims[0].W, ims[0].H
	frames := make([]*img.Image, len(ims))
	for i, m := range ims {
		c := cmpWhite(m, w, h)
		f := cmpFitted(m, w, h, true)
		cmpBlit(c, f, (w-f.W)/2, (h-f.H)/2, 1)
		frames[i] = c
		ims[i] = nil
	}
	return frames, nil
}

// cmpDelay reads the "delay" parameter in seconds.
func cmpDelay(args Args) (float64, error) {
	d, err := args.Float("delay", 0.5)
	if err != nil {
		return 0, err
	}
	if d < 0.02 || d > 60 {
		return 0, Userf("delay must be between 0.02 and 60 seconds (got %g)", d)
	}
	return d, nil
}

// Animated GIF: frames in order, each with its own 256-colour median-cut
// palette and Floyd-Steinberg dithering; loops forever.
func cmpRunGIF(ctx context.Context, job Job) error {
	delay, err := cmpDelay(job.Args)
	if err != nil {
		return err
	}
	frames, err := cmpAnimFrames(ctx, job)
	if err != nil {
		return err
	}
	g := &gif.GIF{LoopCount: 0}
	cs := int(math.Round(delay * 100))
	for i, f := range frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		src := f.NRGBA()
		pal := image.NewPaletted(src.Bounds(), cmpQuantize(src, 256))
		draw.FloydSteinberg.Draw(pal, src.Bounds(), src, src.Bounds().Min)
		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, cs)
		job.Progress(0.5 + 0.45*float64(i+1)/float64(len(frames)))
	}
	return cmpWriteFile(job.Output, func(w io.Writer) error { return gif.EncodeAll(w, g) })
}

// Animated WebP (gen2brain/webp EncodeAll): same frames as the GIF, lossy
// quality 85, loops forever.
func cmpRunWebPAnim(ctx context.Context, job Job) error {
	delay, err := cmpDelay(job.Args)
	if err != nil {
		return err
	}
	frames, err := cmpAnimFrames(ctx, job)
	if err != nil {
		return err
	}
	anim := &webp.WEBP{LoopCount: 0}
	ms := int(math.Round(delay * 1000))
	for _, f := range frames {
		anim.Image = append(anim.Image, f.NRGBA())
		anim.Delay = append(anim.Delay, ms)
	}
	job.Progress(0.7)
	return cmpWriteFile(job.Output, func(w io.Writer) error {
		return webp.EncodeAll(w, anim, webp.Options{Quality: 85, Method: 4})
	})
}

// cmpWriteFile creates path exclusively, writes via fn and removes the file
// on failure.
func cmpWriteFile(path string, fn func(io.Writer) error) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	return fn(f)
}

// cmpQuantize builds a palette of at most maxColors colours by median cut
// over a 15-bit colour histogram (each box splits at the weighted median of
// its widest axis; entries are the exact mean colour of the pixels in the
// bin, so flat colours stay exact).
func cmpQuantize(src *image.NRGBA, maxColors int) color.Palette {
	type bin struct {
		n       uint32
		r, g, b uint64
	}
	bins := make([]bin, 1<<15)
	b := src.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := src.Pix[src.PixOffset(b.Min.X, y):][:4*b.Dx()]
		for i := 0; i < len(row); i += 4 {
			k := int(row[i]>>3)<<10 | int(row[i+1]>>3)<<5 | int(row[i+2]>>3)
			e := &bins[k]
			e.n++
			e.r += uint64(row[i])
			e.g += uint64(row[i+1])
			e.b += uint64(row[i+2])
		}
	}
	type ent struct {
		n float64
		c [3]float64
	}
	var ents []ent
	for _, e := range bins {
		if e.n > 0 {
			n := float64(e.n)
			ents = append(ents, ent{n, [3]float64{float64(e.r) / n, float64(e.g) / n, float64(e.b) / n}})
		}
	}
	widest := func(box []ent) (axis int, rng float64) {
		for a := 0; a < 3; a++ {
			lo, hi := 255.0, 0.0
			for _, e := range box {
				lo, hi = math.Min(lo, e.c[a]), math.Max(hi, e.c[a])
			}
			if hi-lo > rng {
				axis, rng = a, hi-lo
			}
		}
		return
	}
	boxes := [][]ent{ents}
	for len(boxes) < maxColors {
		best, bestScore := -1, 0.0
		for i, box := range boxes {
			if len(box) < 2 {
				continue
			}
			_, rng := widest(box)
			var total float64
			for _, e := range box {
				total += e.n
			}
			if s := rng * math.Sqrt(total); s > bestScore {
				best, bestScore = i, s
			}
		}
		if best < 0 {
			break
		}
		box := boxes[best]
		axis, _ := widest(box)
		sort.Slice(box, func(i, j int) bool { return box[i].c[axis] < box[j].c[axis] })
		var total float64
		for _, e := range box {
			total += e.n
		}
		cut, acc := 1, 0.0
		for i, e := range box {
			acc += e.n
			if acc >= total/2 {
				cut = i + 1
				break
			}
		}
		cut = max(1, min(cut, len(box)-1))
		boxes[best] = box[:cut]
		boxes = append(boxes, box[cut:])
	}
	pal := make(color.Palette, 0, len(boxes))
	for _, box := range boxes {
		var n, r, g, bl float64
		for _, e := range box {
			n += e.n
			r += e.c[0] * e.n
			g += e.c[1] * e.n
			bl += e.c[2] * e.n
		}
		if n == 0 {
			continue
		}
		pal = append(pal, color.RGBA{uint8(math.Round(r / n)), uint8(math.Round(g / n)), uint8(math.Round(bl / n)), 255})
	}
	if len(pal) == 0 {
		pal = color.Palette{color.RGBA{0, 0, 0, 255}}
	}
	return pal
}

// ---------------------------------------------------------------------------
// Sprite sheet

// Sprite sheet: frames left to right in rows of ceil(sqrt(n)) columns, no
// gaps, transparent background, every cell the size of the largest input
// with the input centred in it.
func cmpRunSpriteSheet(ctx context.Context, job Job) error {
	ims, err := cmpLoad(ctx, job, cmpLoadBudget, 0)
	if err != nil {
		return err
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(ims)))))
	rows := (len(ims) + cols - 1) / cols
	cw, ch := 0, 0
	for _, m := range ims {
		cw, ch = max(cw, m.W), max(ch, m.H)
	}
	if float64(cols*cw)*float64(rows*ch) > cmpCanvasCap {
		return Userf("the sheet would be %dx%d pixels; use smaller pictures or fewer of them", cols*cw, rows*ch)
	}
	canvas := ims[0].Sized(cols*cw, rows*ch)
	for i, m := range ims {
		cmpBlit(canvas, m, (i%cols)*cw+(cw-m.W)/2, (i/cols)*ch+(ch-m.H)/2, 1)
	}
	job.Progress(0.8)
	return cmpSave(canvas, job.Output)
}

// ---------------------------------------------------------------------------
// Folder outputs

// cmpFolder runs fn to fill the output folder and removes it on failure.
func cmpFolder(job Job, fn func() error) error {
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_ = os.RemoveAll(job.Output)
		return err
	}
	return nil
}

// Split into tiles: "3x3" (columns x rows, "3" = 3x3, default 2x2). Tiles
// cover the picture exactly; the last column and row take the remainder.
// Written as PNG (JPEG sources as high-quality JPEG).
func cmpRunSplit(ctx context.Context, job Job) error {
	spec := job.Args.String("grid", "2x2")
	cols, rows, hasRows, ok := cmpParseCR(spec)
	if !ok || cols > 50 || rows > 50 {
		return Userf("%q is not a grid; try 3x3 (columns x rows) or 3x1", spec)
	}
	if !hasRows {
		rows = cols
	}
	if cols*rows < 2 {
		return Userf("a 1x1 grid would not split anything; try 2x2 or 3x3")
	}
	m, err := DecodeWorking(job.Input())
	if err != nil {
		return err
	}
	if m.W < cols || m.H < rows {
		return Userf("the picture (%dx%d) is too small for a %dx%d grid", m.W, m.H, cols, rows)
	}
	ext := ".png"
	switch strings.ToLower(filepath.Ext(job.Name(0))) {
	case ".jpg", ".jpeg":
		ext = ".jpg"
		if m.Quality <= 0 {
			m.Quality = 95
		}
	}
	tw, th := m.W/cols, m.H/rows
	width := len(strconv.Itoa(max(cols, rows)))
	return cmpFolder(job, func() error {
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				x0, y0 := c*tw, r*th
				x1, y1 := x0+tw, y0+th
				if c == cols-1 {
					x1 = m.W
				}
				if r == rows-1 {
					y1 = m.H
				}
				name := fmt.Sprintf("tile_r%0*d_c%0*d%s", width, r+1, width, c+1, ext)
				if err := cmpSave(cmpCrop(m, x0, y0, x1, y1), filepath.Join(job.Output, name)); err != nil {
					return err
				}
				job.Progress(float64(r*cols+c+1) / float64(rows*cols))
			}
		}
		return nil
	})
}

// cmpGIFFrames composites every frame of an animated GIF onto the logical
// screen, honouring frame offsets and disposal methods (background clears the
// frame's rectangle to transparent; previous restores the earlier canvas).
func cmpGIFFrames(g *gif.GIF) []*image.NRGBA {
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 {
		var u image.Rectangle
		for _, f := range g.Image {
			u = u.Union(f.Bounds())
		}
		w, h = u.Max.X, u.Max.Y
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))
	clone := func(c *image.NRGBA) *image.NRGBA {
		n := image.NewNRGBA(c.Rect)
		copy(n.Pix, c.Pix)
		return n
	}
	var out []*image.NRGBA
	for i, fr := range g.Image {
		disp := byte(gif.DisposalNone)
		if i < len(g.Disposal) {
			disp = g.Disposal[i]
		}
		var prev *image.NRGBA
		if disp == gif.DisposalPrevious {
			prev = clone(canvas)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		out = append(out, clone(canvas))
		switch disp {
		case gif.DisposalBackground:
			r := fr.Bounds().Intersect(canvas.Bounds())
			for y := r.Min.Y; y < r.Max.Y; y++ {
				row := canvas.Pix[canvas.PixOffset(r.Min.X, y):][:4*r.Dx()]
				for j := range row {
					row[j] = 0
				}
			}
		case gif.DisposalPrevious:
			canvas = prev
		}
	}
	return out
}

// Extract frames: every frame of an animated GIF (or animated WebP) as a
// fully composited frame_001.png, frame_002.png, ...
func cmpRunFrames(ctx context.Context, job Job) error {
	data, err := os.ReadFile(job.Input())
	if err != nil {
		return err
	}
	var frames []image.Image
	switch {
	case bytes.HasPrefix(data, []byte("GIF8")):
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return Userf("could not read GIF: %v", err)
		}
		for _, f := range cmpGIFFrames(g) {
			frames = append(frames, f)
		}
	case len(data) > 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		a, err := webp.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return Userf("could not read WebP: %v", err)
		}
		frames = a.Image
	default:
		return Userf("%s is not an animated GIF or WebP", job.Name(0))
	}
	if len(frames) == 0 {
		return Userf("%s has no frames", job.Name(0))
	}
	width := max(3, len(strconv.Itoa(len(frames))))
	return cmpFolder(job, func() error {
		for i, f := range frames {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := fmt.Sprintf("frame_%0*d.png", width, i+1)
			if err := cmpSave(cmpEightBit(img.FromStd(f)), filepath.Join(job.Output, name)); err != nil {
				return err
			}
			job.Progress(float64(i+1) / float64(len(frames)))
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Icons

// cmpSquare centre-crops m to a square.
func cmpSquare(m *img.Image) *img.Image {
	if m.W == m.H {
		return m
	}
	s := min(m.W, m.H)
	x0, y0 := (m.W-s)/2, (m.H-s)/2
	return cmpCrop(m, x0, y0, x0+s, y0+s)
}

// cmpWriteICO writes an ICO container whose entries are PNG payloads.
func cmpWriteICO(path string, sizes []int, pngs [][]byte) error {
	var buf bytes.Buffer
	le := binary.LittleEndian
	_ = binary.Write(&buf, le, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := byte(s)
		if s >= 256 {
			dim = 0
		}
		buf.Write([]byte{dim, dim, 0, 0})
		_ = binary.Write(&buf, le, [2]uint16{1, 32})
		_ = binary.Write(&buf, le, [2]uint32{uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// cmpIconSet writes PNGs for files (name -> size, with flatten naming the
// opaque-on-white ones) and app.ico/favicon.ico with the given sizes.
func cmpIconSet(ctx context.Context, job Job, pngSizes map[string]int, opaque map[string]bool, icoName string, icoSizes []int) error {
	m, err := DecodeWorking(job.Input())
	if err != nil {
		return err
	}
	sq := cmpEightBit(cmpSquare(m))
	cache := map[int]*img.Image{}
	at := func(s int) *img.Image {
		if r, ok := cache[s]; ok {
			return r
		}
		r := cmpEightBit(img.Resize(sq, s, s, img.Lanczos3))
		cache[s] = r
		return r
	}
	return cmpFolder(job, func() error {
		names := make([]string, 0, len(pngSizes))
		for n := range pngSizes {
			names = append(names, n)
		}
		sort.Strings(names)
		for i, n := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			r := at(pngSizes[n])
			if opaque[n] {
				c := cmpWhite(r, r.W, r.H)
				cmpBlit(c, r, 0, 0, 1)
				r = c
			}
			if err := cmpSave(r, filepath.Join(job.Output, n)); err != nil {
				return err
			}
			job.Progress(0.9 * float64(i+1) / float64(len(names)))
		}
		var pngs [][]byte
		for _, s := range icoSizes {
			var b bytes.Buffer
			if err := png.Encode(&b, at(s).NRGBA()); err != nil {
				return err
			}
			pngs = append(pngs, b.Bytes())
		}
		return cmpWriteICO(filepath.Join(job.Output, icoName), icoSizes, pngs)
	})
}

// Favicon set: favicon.ico (16, 32, 48) plus PNGs for browsers and
// home screens; the picture is centre-cropped to a square first.
func cmpRunFavicon(ctx context.Context, job Job) error {
	return cmpIconSet(ctx, job, map[string]int{
		"favicon-16.png":       16,
		"favicon-32.png":       32,
		"apple-touch-icon.png": 180,
		"icon-192.png":         192,
		"icon-512.png":         512,
	}, map[string]bool{"apple-touch-icon.png": true}, "favicon.ico", []int{16, 32, 48})
}

// App icon set: icon_<size>.png for 16 to 1024 plus app.ico (16, 32, 48, 256).
func cmpRunAppIcon(ctx context.Context, job Job) error {
	files := map[string]int{}
	for _, s := range []int{16, 32, 64, 128, 256, 512, 1024} {
		files[fmt.Sprintf("icon_%d.png", s)] = s
	}
	return cmpIconSet(ctx, job, files, nil, "app.ico", []int{16, 32, 48, 256})
}
