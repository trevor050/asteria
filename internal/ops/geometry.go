package ops

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"asteria/internal/img"
)

// Geometry family: crop, trim, rotate, flip, pad, border, rounded corners,
// circle, tile, fit/fill/stretch and pixel-art upscale. Handlers are named
// "geo.*"; skills live in skills/core/geometry. See IMAGE_SKILLS.md.
//
// Integer-pixel operations copy pixels exactly. Anything that resamples
// (arbitrary rotation, fit/fill/stretch) works in premultiplied linear light
// so transparent pixels never bleed colour and edges do not darken.

const geoMaxSide = 16384

func init() {
	register(Handler{Name: "geo.crop", Pixel: pixGeoCrop})
	register(Handler{Name: "geo.trimborders", Pixel: pixGeoTrimBorders})
	register(Handler{Name: "geo.trimtransparent", Pixel: pixGeoTrimTransparent})
	register(Handler{Name: "geo.rotate", Pixel: pixGeoRotate, Video: videoGeoRotate})
	register(Handler{Name: "geo.straighten", Pixel: pixGeoStraighten})
	register(Handler{Name: "geo.flip", Pixel: pixGeoFlip, Live: true, Video: videoGeoFlip})
	register(Handler{Name: "geo.pad", Pixel: pixGeoPad, Live: true})
	register(Handler{Name: "geo.border", Pixel: pixGeoBorder, Live: true})
	register(Handler{Name: "geo.roundcorners", Pixel: pixGeoRoundCorners, Live: true})
	register(Handler{Name: "geo.circle", Pixel: pixGeoCircle})
	register(Handler{Name: "geo.tile", Pixel: pixGeoTile})
	register(Handler{Name: "geo.fit", Pixel: pixGeoFit})
	register(Handler{Name: "geo.fill", Pixel: pixGeoFill})
	register(Handler{Name: "geo.stretch", Pixel: pixGeoStretch})
	register(Handler{Name: "geo.upscale", Pixel: pixGeoUpscale})
}

// ---------------------------------------------------------------- helpers

var geoDimRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*[x×*]\s*(\d+(?:\.\d+)?)$`)

// geoCopyRect returns the w x h rectangle at (x0, y0) as a new image
// (exact pixel copy). The rectangle must lie inside m.
func geoCopyRect(m *img.Image, x0, y0, w, h int) *img.Image {
	out := m.Sized(w, h)
	for y := 0; y < h; y++ {
		s := m.Off(x0, y0+y)
		copy(out.Pix[4*y*w:4*(y+1)*w], m.Pix[s:s+4*w])
	}
	return out
}

// geoBlit copies src into dst with its top-left corner at (dx, dy); src must fit.
func geoBlit(dst, src *img.Image, dx, dy int) {
	for y := 0; y < src.H; y++ {
		d := dst.Off(dx, dy+y)
		copy(dst.Pix[d:d+4*src.W], src.Pix[4*y*src.W:4*(y+1)*src.W])
	}
}

// geoFill paints the whole image with one straight-alpha colour.
func geoFill(m *img.Image, c img.Color) {
	r, g, b, a := c.RGBA16()
	if r == 0 && g == 0 && b == 0 && a == 0 {
		return
	}
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = r, g, b, a
	}
}

// geoSizeArg reads "WxH" (or a single number for a square) in full-resolution
// pixels and returns the target size for the working image (scaled for proxies).
func geoSizeArg(args Args, key, def string, scale float64) (int, int, error) {
	raw := strings.ToLower(strings.TrimSpace(args.String(key, def)))
	var a, b float64
	if mm := geoDimRe.FindStringSubmatch(raw); mm != nil {
		a, _ = strconv.ParseFloat(mm[1], 64)
		b, _ = strconv.ParseFloat(mm[2], 64)
	} else if v, err := strconv.ParseFloat(strings.TrimSuffix(raw, "px"), 64); err == nil {
		a, b = v, v
	} else {
		return 0, 0, Userf("%q is not a size; try 1080x1080", raw)
	}
	a, b = math.Round(a), math.Round(b)
	if a < 1 || b < 1 || a > geoMaxSide || b > geoMaxSide {
		return 0, 0, Userf("size must be between 1 and %d pixels per side (got %q)", geoMaxSide, raw)
	}
	return max(1, int(math.Round(a*scale))), max(1, int(math.Round(b*scale))), nil
}

// geoLength reads a length where a blank value means defFrac of ref.
func geoLength(args Args, key string, defFrac float64, ref int, scale, maxPx float64) (float64, error) {
	if args.String(key, "") == "" {
		return math.Min(defFrac*float64(ref), maxPx), nil
	}
	return args.Length(key, 0, ref, scale, maxPx)
}

// geoKeepScale makes the result a proxy of the same scale as the input.
func geoKeepScale(out, in *img.Image) *img.Image {
	if in.Scale != 1 {
		out.Scale = in.Scale
	}
	return out
}

// ------------------------------------------------------------------- crop

// pixGeoCrop crops around the centre: "16:9"/"square" = largest centred crop
// of that ratio, "800x600" = that size (clamped), "80%" = keep the middle 80%.
func pixGeoCrop(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	raw := strings.ToLower(strings.TrimSpace(args.String("size", "1:1")))
	cw, ch := m.W, m.H
	switch {
	case strings.HasSuffix(raw, "%"):
		p, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "%")), 64)
		if err != nil || p <= 0 || p > 100 {
			return nil, Userf("%q is not a percentage; try 80%% to keep the middle 80%%", raw)
		}
		cw = max(1, int(math.Round(float64(m.W)*p/100)))
		ch = max(1, int(math.Round(float64(m.H)*p/100)))
	default:
		mm := geoDimRe.FindStringSubmatch(raw)
		isSize := false
		if mm != nil {
			a, _ := strconv.ParseFloat(mm[1], 64)
			b, _ := strconv.ParseFloat(mm[2], 64)
			// "16x9" is a ratio, "800x600" a size.
			if a > 32 || b > 32 {
				isSize = true
				cw = min(m.W, max(1, int(math.Round(a*m.Scale))))
				ch = min(m.H, max(1, int(math.Round(b*m.Scale))))
			}
		}
		if !isSize {
			r, err := args.Aspect("size", "1:1")
			if err != nil {
				return nil, Userf("%q is not a crop; try 16:9, 1:1, 800x600 or 80%%", raw)
			}
			if float64(m.W)/float64(m.H) > r {
				cw = min(m.W, max(1, int(math.Round(float64(m.H)*r))))
			} else {
				ch = min(m.H, max(1, int(math.Round(float64(m.W)/r))))
			}
		}
	}
	if cw == m.W && ch == m.H {
		return m, nil
	}
	return geoCopyRect(m, (m.W-cw)/2, (m.H-ch)/2, cw, ch), nil
}

// ------------------------------------------------------------------- trim

// geoBounds is the inclusive pixel box of everything for which keep is true.
func geoBounds(m *img.Image, keep func(r, g, b, a uint16) bool) (x0, y0, x1, y1 int, ok bool) {
	x0, y0, x1, y1 = m.W, m.H, -1, -1
	for y := 0; y < m.H; y++ {
		row := m.Pix[4*y*m.W : 4*(y+1)*m.W]
		for x := 0; x < m.W; x++ {
			p := row[4*x : 4*x+4]
			if keep(p[0], p[1], p[2], p[3]) {
				x0, x1 = min(x0, x), max(x1, x)
				y0, y1 = min(y0, y), max(y1, y)
			}
		}
	}
	return x0, y0, x1, y1, x1 >= 0
}

// pixGeoTrimBorders removes uniform borders: everything matching the
// top-left pixel (within 3% per channel) is background.
func pixGeoTrimBorders(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	const tol = 0.03 * 65535
	rr, rg, rb, ra := m.At(0, 0)
	d := func(a, b uint16) float64 { return math.Abs(float64(a) - float64(b)) }
	x0, y0, x1, y1, ok := geoBounds(m, func(r, g, b, a uint16) bool {
		if float64(ra) <= tol && float64(a) <= tol {
			return false // both clear: same "colour"
		}
		return !(d(r, rr) <= tol && d(g, rg) <= tol && d(b, rb) <= tol && d(a, ra) <= tol)
	})
	if !ok {
		return m, nil // all one colour: nothing sensible to keep
	}
	return geoTrimTo(m, x0, y0, x1, y1), nil
}

// pixGeoTrimTransparent crops to the box of everything that is not fully transparent.
func pixGeoTrimTransparent(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	x0, y0, x1, y1, ok := geoBounds(m, func(_, _, _, a uint16) bool { return a != 0 })
	if !ok {
		return nil, Userf("the image is fully transparent, so there is nothing to keep")
	}
	return geoTrimTo(m, x0, y0, x1, y1), nil
}

func geoTrimTo(m *img.Image, x0, y0, x1, y1 int) *img.Image {
	if x0 == 0 && y0 == 0 && x1 == m.W-1 && y1 == m.H-1 {
		return m
	}
	return geoCopyRect(m, x0, y0, x1-x0+1, y1-y0+1)
}

// ----------------------------------------------------------------- rotate

// geoQuarter reports the exact quarter turn (0..3, clockwise) for deg, if deg is one.
func geoQuarter(deg float64) (int, bool) {
	d := math.Mod(deg, 360)
	if d < 0 {
		d += 360
	}
	q := math.Round(d / 90)
	if math.Abs(d-q*90) < 1e-6 {
		return int(q) % 4, true
	}
	return 0, false
}

// pixGeoRotate rotates clockwise by any angle. Quarter turns are lossless
// pixel moves; other angles resample in premultiplied linear light and grow
// the canvas, leaving the corners transparent.
func pixGeoRotate(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	deg, err := args.Degrees("angle", 90)
	if err != nil {
		return nil, err
	}
	if q, exact := geoQuarter(deg); exact {
		switch q {
		case 1:
			return img.Rotate90(m), nil
		case 2:
			return img.Rotate180(m), nil
		case 3:
			return img.Rotate270(m), nil
		}
		return m, nil
	}
	out := geoRotateAny(ctx, m, deg)
	return out, ctx.Err()
}

func videoGeoRotate(args Args) (string, error) {
	deg, err := args.Degrees("angle", 90)
	if err != nil {
		return "", err
	}
	if q, exact := geoQuarter(deg); exact {
		switch q {
		case 1:
			return "transpose=clock", nil
		case 2:
			return "hflip,vflip", nil
		case 3:
			return "transpose=cclock", nil
		}
		return "null", nil
	}
	a := fmt.Sprintf("%.6f*PI/180", deg)
	return fmt.Sprintf("rotate=%s:ow=rotw(%s):oh=roth(%s):c=black@0,scale=trunc(iw/2)*2:trunc(ih/2)*2", a, a, a), nil
}

// geoCubic is the Catmull-Rom weight for distance d.
func geoCubic(d float64) float64 {
	d = math.Abs(d)
	switch {
	case d <= 1:
		return (1.5*d-2.5)*d*d + 1
	case d < 2:
		return ((-0.5*d+2.5)*d-4)*d + 2
	}
	return 0
}

// geoRotateAny rotates clockwise by deg with a grown canvas, bicubic
// (Catmull-Rom) in premultiplied linear light. Samples outside the source
// are transparent, so the rotated edge is antialiased.
func geoRotateAny(ctx context.Context, m *img.Image, deg float64) *img.Image {
	th := deg * math.Pi / 180
	c, s := math.Cos(th), math.Sin(th)
	w, h := float64(m.W), float64(m.H)
	ow := max(1, int(math.Ceil(w*math.Abs(c)+h*math.Abs(s)-1e-6)))
	oh := max(1, int(math.Ceil(w*math.Abs(s)+h*math.Abs(c)-1e-6)))
	src := m.ToLinear()
	dst := img.NewLinear(ow, oh)
	cxo, cyo := float64(ow)/2, float64(oh)/2
	img.Rows(ctx, oh, func(y0, y1 int) {
		var wx, wy [4]float64
		for y := y0; y < y1; y++ {
			for x := 0; x < ow; x++ {
				dx, dy := float64(x)+0.5-cxo, float64(y)+0.5-cyo
				sx := dx*c + dy*s + w/2 - 0.5
				sy := -dx*s + dy*c + h/2 - 0.5
				if sx < -2 || sy < -2 || sx > w+1 || sy > h+1 {
					continue
				}
				ix, iy := math.Floor(sx), math.Floor(sy)
				fx, fy := sx-ix, sy-iy
				for j := 0; j < 4; j++ {
					wx[j] = geoCubic(fx - float64(j-1))
					wy[j] = geoCubic(fy - float64(j-1))
				}
				var acc [4]float64
				for j := 0; j < 4; j++ {
					yy := int(iy) + j - 1
					if yy < 0 || yy >= m.H {
						continue
					}
					for i := 0; i < 4; i++ {
						xx := int(ix) + i - 1
						if xx < 0 || xx >= m.W {
							continue
						}
						k := wx[i] * wy[j]
						p := src.Pix[4*(yy*m.W+xx) : 4*(yy*m.W+xx)+4]
						acc[0] += k * float64(p[0])
						acc[1] += k * float64(p[1])
						acc[2] += k * float64(p[2])
						acc[3] += k * float64(p[3])
					}
				}
				a := math.Max(0, math.Min(1, acc[3]))
				o := dst.Pix[4*(y*ow+x) : 4*(y*ow+x)+4]
				o[0] = float32(math.Max(0, math.Min(a, acc[0])))
				o[1] = float32(math.Max(0, math.Min(a, acc[1])))
				o[2] = float32(math.Max(0, math.Min(a, acc[2])))
				o[3] = float32(a)
			}
		}
	})
	return dst.ToImage(m)
}

// pixGeoStraighten rotates by a small angle, then crops to the largest
// centred rectangle with the original aspect ratio that has no empty corners.
func pixGeoStraighten(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	deg, err := args.Degrees("angle", 2)
	if err != nil {
		return nil, err
	}
	if deg < -45 || deg > 45 {
		return nil, Userf("straighten works between -45 and 45 degrees (got %g); use rotate for bigger turns", deg)
	}
	if math.Abs(deg) < 1e-6 {
		return m, nil
	}
	rot := geoRotateAny(ctx, m, deg)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cw, ch := geoInscribed(float64(m.W), float64(m.H), deg*math.Pi/180, 3)
	cw, ch = min(cw, rot.W), min(ch, rot.H)
	return geoCopyRect(rot, (rot.W-cw)/2, (rot.H-ch)/2, cw, ch), nil
}

// geoInscribed is the largest w:h rectangle, centred, that fits inside a
// w x h rectangle rotated by th, shrunk by margin pixels per side so the
// resampling kernel never touches the transparent border.
func geoInscribed(w, h, th, margin float64) (int, int) {
	c, s := math.Abs(math.Cos(th)), math.Abs(math.Sin(th))
	k := math.Min(w/(w*c+h*s), h/(w*s+h*c))
	k = math.Max(0, k-2*margin/math.Min(w, h))
	return max(1, int(math.Floor(k*w))), max(1, int(math.Floor(k*h)))
}

// ------------------------------------------------------------------- flip

func geoAxis(args Args) (string, error) {
	return args.Choice("axis", "vertical", []string{"vertical", "horizontal"},
		map[string]string{"v": "vertical", "updown": "vertical", "h": "horizontal", "leftright": "horizontal"})
}

func pixGeoFlip(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	axis, err := geoAxis(args)
	if err != nil {
		return nil, err
	}
	if axis == "horizontal" {
		return img.FlipH(m), nil
	}
	return img.FlipV(m), nil
}

func videoGeoFlip(args Args) (string, error) {
	axis, err := geoAxis(args)
	if err != nil {
		return "", err
	}
	if axis == "horizontal" {
		return "hflip", nil
	}
	return "vflip", nil
}

// -------------------------------------------------------------- pad/border

// geoPadded returns m surrounded by n pixels of colour c.
func geoPadded(m *img.Image, n int, c img.Color) (*img.Image, error) {
	if n <= 0 {
		return m, nil
	}
	if float64(m.W+2*n)/m.Scale > geoMaxSide || float64(m.H+2*n)/m.Scale > geoMaxSide {
		return nil, Userf("that would make the picture bigger than %d pixels; use a smaller amount", geoMaxSide)
	}
	out := m.Sized(m.W+2*n, m.H+2*n)
	geoFill(out, c)
	geoBlit(out, m, n, n)
	return out, nil
}

// pixGeoPad adds transparent space around the picture (default 10% of the shorter side).
func pixGeoPad(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := geoLength(args, "amount", 0.10, min(m.W, m.H), m.Scale, geoMaxSide*m.Scale)
	if err != nil {
		return nil, err
	}
	return geoPadded(m, int(math.Round(v)), img.Color{})
}

// pixGeoBorder adds a solid border (default 4% of the shorter side, white).
func pixGeoBorder(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := geoLength(args, "width", 0.04, min(m.W, m.H), m.Scale, geoMaxSide*m.Scale)
	if err != nil {
		return nil, err
	}
	col, err := args.Color("color", "white")
	if err != nil {
		return nil, err
	}
	n := int(math.Round(v))
	if v > 0 && n < 1 {
		n = 1
	}
	return geoPadded(m, n, col)
}

// ---------------------------------------------------------------- masking

// geoCover is the antialiased coverage of a pixel whose centre is d from the
// edge of a shape of radius r (inside = positive).
func geoCover(r, d float64) float64 {
	return math.Max(0, math.Min(1, r-d+0.5))
}

// pixGeoRoundCorners rounds the corners with an antialiased quarter circle.
func pixGeoRoundCorners(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	short := min(m.W, m.H)
	r, err := geoLength(args, "radius", 0.08, short, m.Scale, float64(short)/2)
	if err != nil {
		return nil, err
	}
	if r <= 0 {
		return m, nil
	}
	out := m.Clone()
	w, h := float64(m.W), float64(m.H)
	img.Rows(ctx, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			py := float64(y) + 0.5
			cy := -1.0
			if py < r {
				cy = r
			} else if py > h-r {
				cy = h - r
			}
			if cy < 0 {
				continue
			}
			for x := 0; x < m.W; x++ {
				px := float64(x) + 0.5
				cx := -1.0
				if px < r {
					cx = r
				} else if px > w-r {
					cx = w - r
				}
				if cx < 0 {
					continue
				}
				k := geoCover(r, math.Hypot(px-cx, py-cy))
				if k >= 1 {
					continue
				}
				i := out.Off(x, y)
				out.Pix[i+3] = img.Q(float32(float64(out.Pix[i+3]) / 65535 * k))
			}
		}
	})
	return out, ctx.Err()
}

// pixGeoCircle crops the largest centred square and masks it to a circle.
func pixGeoCircle(ctx context.Context, m *img.Image, _ Args) (*img.Image, error) {
	s := min(m.W, m.H)
	sq := geoCopyRect(m, (m.W-s)/2, (m.H-s)/2, s, s)
	r := float64(s) / 2
	img.Rows(ctx, s, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < s; x++ {
				k := geoCover(r, math.Hypot(float64(x)+0.5-r, float64(y)+0.5-r))
				if k >= 1 {
					continue
				}
				i := sq.Off(x, y)
				if k <= 0 {
					sq.Pix[i], sq.Pix[i+1], sq.Pix[i+2], sq.Pix[i+3] = 0, 0, 0, 0
					continue
				}
				sq.Pix[i+3] = img.Q(float32(float64(sq.Pix[i+3]) / 65535 * k))
			}
		}
	})
	return sq, ctx.Err()
}

// ------------------------------------------------------------------- tile

func pixGeoTile(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	raw := strings.ToLower(strings.TrimSpace(args.String("grid", "2x2")))
	gx, gy := 0, 0
	if mm := geoDimRe.FindStringSubmatch(raw); mm != nil {
		a, _ := strconv.ParseFloat(mm[1], 64)
		b, _ := strconv.ParseFloat(mm[2], 64)
		gx, gy = int(a), int(b)
	} else if n, err := strconv.Atoi(raw); err == nil {
		gx, gy = n, n
	}
	if gx < 1 || gy < 1 || gx > 10 || gy > 10 {
		return nil, Userf("%q is not a grid; try 3x3 (1 to 10 each way)", raw)
	}
	if gx == 1 && gy == 1 {
		return m, nil
	}
	if float64(m.W*gx)/m.Scale > geoMaxSide || float64(m.H*gy)/m.Scale > geoMaxSide {
		return nil, Userf("the tiled picture would be bigger than %d pixels; use a smaller grid", geoMaxSide)
	}
	out := m.Sized(m.W*gx, m.H*gy)
	rowLen := 4 * m.W
	for ty := 0; ty < gy; ty++ {
		for y := 0; y < m.H; y++ {
			base := 4 * ((ty*m.H + y) * out.W)
			for tx := 0; tx < gx; tx++ {
				copy(out.Pix[base+tx*rowLen:base+(tx+1)*rowLen], m.Pix[y*rowLen:(y+1)*rowLen])
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------- fit / fill / stretch

// pixGeoFit shrinks or grows the picture to fit inside WxH, keeping the
// aspect ratio, and centres it on a transparent WxH canvas (letterbox).
func pixGeoFit(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	tw, th, err := geoSizeArg(args, "size", "1080x1080", m.Scale)
	if err != nil {
		return nil, err
	}
	k := math.Min(float64(tw)/float64(m.W), float64(th)/float64(m.H))
	nw := min(tw, max(1, int(math.Round(float64(m.W)*k))))
	nh := min(th, max(1, int(math.Round(float64(m.H)*k))))
	inner := m
	if nw != m.W || nh != m.H {
		inner = geoKeepScale(img.Resize(m, nw, nh, img.Lanczos3), m)
	}
	if nw == tw && nh == th {
		return inner, nil
	}
	out := m.Sized(tw, th)
	geoBlit(out, inner, (tw-nw)/2, (th-nh)/2)
	return out, nil
}

// pixGeoFill covers WxH exactly: the largest centred crop of that aspect
// ratio is resized to WxH.
func pixGeoFill(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	tw, th, err := geoSizeArg(args, "size", "1080x1080", m.Scale)
	if err != nil {
		return nil, err
	}
	cw, ch := m.W, m.H
	if int64(m.W)*int64(th) > int64(tw)*int64(m.H) { // source is wider than the target
		cw = min(m.W, max(1, int(math.Round(float64(m.H)*float64(tw)/float64(th)))))
	} else {
		ch = min(m.H, max(1, int(math.Round(float64(m.W)*float64(th)/float64(tw)))))
	}
	src := m
	if cw != m.W || ch != m.H {
		src = geoCopyRect(m, (m.W-cw)/2, (m.H-ch)/2, cw, ch)
	}
	if src.W == tw && src.H == th {
		return src, nil
	}
	return geoKeepScale(img.Resize(src, tw, th, img.Lanczos3), m), nil
}

// pixGeoStretch resizes to exactly WxH, ignoring the aspect ratio.
func pixGeoStretch(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	tw, th, err := geoSizeArg(args, "size", "1080x1080", m.Scale)
	if err != nil {
		return nil, err
	}
	if tw == m.W && th == m.H {
		return m, nil
	}
	return geoKeepScale(img.Resize(m, tw, th, img.Lanczos3), m), nil
}

// ---------------------------------------------------------------- upscale

// pixGeoUpscale enlarges by a whole factor with nearest-neighbour, so every
// pixel becomes an exact factor x factor block.
func pixGeoUpscale(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	raw := strings.ToLower(strings.TrimSpace(args.String("factor", "4x")))
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(raw, "x"), "×"))
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f != math.Trunc(f) || f < 1 {
		return nil, Userf("%q is not a whole factor; try 4 or 4x", args.String("factor", ""))
	}
	k := int(f)
	if k == 1 {
		return m, nil
	}
	fullW, fullH := float64(m.W)/m.Scale, float64(m.H)/m.Scale
	if fullW*f > geoMaxSide || fullH*f > geoMaxSide {
		maxK := int(math.Floor(geoMaxSide / math.Max(fullW, fullH)))
		if maxK < 2 {
			return nil, Userf("this picture is already too large to enlarge (limit %d pixels per side)", geoMaxSide)
		}
		return nil, Userf("%dx would exceed %d pixels per side; the most for this picture is %dx", k, geoMaxSide, maxK)
	}
	out := m.Sized(m.W*k, m.H*k)
	img.Rows(ctx, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			first := 4 * (y * k * out.W)
			for x := 0; x < m.W; x++ {
				s := m.Off(x, y)
				for i := 0; i < k; i++ {
					copy(out.Pix[first+4*(x*k+i):first+4*(x*k+i)+4], m.Pix[s:s+4])
				}
			}
			for j := 1; j < k; j++ {
				copy(out.Pix[first+4*j*out.W:first+4*(j+1)*out.W], out.Pix[first:first+4*out.W])
			}
		}
	})
	return out, ctx.Err()
}
