package ops

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"asteria/internal/img"
)

// Color and tone family. Handlers are named "adjust.*"; skills live in
// skills/core/adjust. See IMAGE_SKILLS.md for the conventions.

func init() {
	register(Handler{Name: "adjust.saturate", Pixel: pixSaturate, Live: true, Video: videoSaturate})
}

// pixSaturate scales chroma in OKLCh, so lightness and hue stay put while
// colourfulness changes ("saturate" = +40%, "saturate 150%", "saturate -30").
func pixSaturate(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := args.Amount("amount", 1.4, 0, 4)
	if err != nil {
		return nil, err
	}
	if k == 1 {
		return m, nil
	}
	f := float32(k)
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		return img.OKLabToLinear(L, A*f, B*f)
	}), nil
}

func videoSaturate(args Args) (string, error) {
	k, err := args.Amount("amount", 1.4, 0, 3)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("eq=saturation=%.3f", math.Max(0, k)), nil
}

// ---------------------------------------------------------------------------
// Colour and tone skills. Helpers are prefixed adj to avoid clashing with
// other families in this package.
// ---------------------------------------------------------------------------

func init() {
	for _, h := range []Handler{
		{Name: "adjust.vibrance", Pixel: adjVibrance, Live: true},
		{Name: "adjust.brightness", Pixel: adjBrightness, Live: true},
		{Name: "adjust.exposure", Pixel: adjExposure, Live: true},
		// ffmpeg's eq contrast/gamma act on luma only; close enough for video.
		{Name: "adjust.contrast", Pixel: adjContrast, Live: true, Video: adjVideoContrast},
		{Name: "adjust.gamma", Pixel: adjGamma, Live: true, Video: adjVideoGamma},
		{Name: "adjust.shadows", Pixel: adjShadows, Live: true},
		{Name: "adjust.highlights", Pixel: adjHighlights, Live: true},
		{Name: "adjust.levels", Pixel: adjLevels, Live: true},
		{Name: "adjust.autolevels", Pixel: adjAutoLevels, Live: true},
		{Name: "adjust.autocontrast", Pixel: adjAutoContrast, Live: true},
		{Name: "adjust.enhance", Pixel: adjEnhance, Live: true},
		{Name: "adjust.whitebalance", Pixel: adjWhiteBalance, Live: true},
		{Name: "adjust.temperature", Pixel: adjTemperature, Live: true},
		{Name: "adjust.tint", Pixel: adjTint, Live: true},
		// ffmpeg's hue filter rotates chroma in YUV, not OKLCh: hue angles
		// differ slightly from the image result but the look is the same.
		{Name: "adjust.hue", Pixel: adjHue, Live: true, Video: adjVideoHue},
		{Name: "adjust.invert", Pixel: adjInvert, Live: true, Video: func(Args) (string, error) { return "negate", nil }},
		// hue=s=0 drops chroma in YUV (video luma), the closest ffmpeg match
		// for an OKLab-lightness grey.
		{Name: "adjust.lightness", Pixel: adjLightness, Live: true, Video: func(Args) (string, error) { return "hue=s=0", nil }},
		{Name: "adjust.channel", Pixel: adjChannel, Live: true, Video: adjVideoChannel},
		{Name: "adjust.sepia", Pixel: adjSepia, Live: true, Video: adjVideoSepia},
		{Name: "adjust.duotone", Pixel: adjDuotone, Live: true},
		{Name: "adjust.colorize", Pixel: adjColorize, Live: true},
		{Name: "adjust.posterize", Pixel: adjPosterize, Live: true},
		{Name: "adjust.threshold", Pixel: adjThreshold, Live: true},
		{Name: "adjust.swap", Pixel: adjSwap, Live: true, Video: adjVideoSwap},
		{Name: "adjust.replace", Pixel: adjReplace, Live: true},
		{Name: "adjust.opacity", Pixel: adjOpacity, Live: true},
	} {
		register(h)
	}
}

// adjVibranceMaxChroma is roughly the largest OKLab chroma inside sRGB; a
// pixel at this chroma is "fully vivid" and gets no boost.
const adjVibranceMaxChroma = 0.32

// adjVibrance boosts chroma in OKLCh with a weight that falls off linearly as
// chroma rises: muted colours gain the full amount, vivid ones almost
// nothing, neutrals stay neutral (zero chroma scales to zero).
func adjVibrance(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := args.Amount("amount", 1.4, 0, 3)
	if err != nil {
		return nil, err
	}
	return adjVibranceBy(m, float32(k-1)), nil
}

func adjVibranceBy(m *img.Image, a float32) *img.Image {
	if a == 0 {
		return m
	}
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		c := float32(math.Hypot(float64(A), float64(B)))
		w := 1 - min(c/adjVibranceMaxChroma, 1)
		s := max(1+a*w, 0)
		return img.OKLabToLinear(L, A*s, B*s)
	})
}

// adjBrightness multiplies linear light (physically a brightness change).
func adjBrightness(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := args.Amount("amount", 1.2, 0, 4)
	if err != nil {
		return nil, err
	}
	if k == 1 {
		return m, nil
	}
	f := float32(k)
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		return r * f, g * f, b * f
	}), nil
}

// adjExposure scales linear light by 2^stops, like a camera EV change.
func adjExposure(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	ev, err := args.Signed("stops", 1, -5, 5)
	if err != nil {
		return nil, err
	}
	if ev == 0 {
		return m, nil
	}
	f := float32(math.Exp2(ev))
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		return r * f, g * f, b * f
	}), nil
}

// adjContrast pivots encoded values around 0.5 (CSS contrast()), which is
// what other editors' contrast sliders show.
func adjContrast(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := args.Amount("amount", 1.25, 0, 4)
	if err != nil {
		return nil, err
	}
	if k == 1 {
		return m, nil
	}
	f := float32(k)
	return img.LUT16(m, func(v float32) float32 { return (v-0.5)*f + 0.5 }), nil
}

func adjVideoContrast(args Args) (string, error) {
	k, err := args.Amount("amount", 1.25, 0, 4)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("eq=contrast=%.3f", k), nil
}

// adjGamma is v^(1/g) on encoded values: above 1 brightens midtones.
func adjGamma(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	g, err := adjGammaArg(args)
	if err != nil {
		return nil, err
	}
	if g == 1 {
		return m, nil
	}
	inv := 1 / g
	return img.LUT16(m, func(v float32) float32 { return float32(math.Pow(float64(v), inv)) }), nil
}

func adjGammaArg(args Args) (float64, error) {
	g, err := args.Float("gamma", 1.2)
	if err != nil {
		return 0, err
	}
	if g < 0.1 || g > 5 {
		return 0, Userf("gamma must be between 0.1 and 5 (got %g)", g)
	}
	return g, nil
}

func adjVideoGamma(args Args) (string, error) {
	g, err := adjGammaArg(args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("eq=gamma=%.3f", g), nil
}

func adjSmooth(t float32) float32 {
	t = img.Clamp01(t)
	return t * t * (3 - 2*t)
}

// adjToneMid is OKLab lightness of sRGB mid grey; shadow and highlight masks
// fade to zero here.
const adjToneMid = 0.6

// adjToneL moves OKLab lightness by 0.25*a*mask. a, A and B stay fixed so
// hue and chroma are preserved. The mask is smooth (smoothstep) and its
// steepest slope times 0.25 stays below 1, so the tone curve never inverts.
func adjToneL(m *img.Image, a float32, shadows bool) *img.Image {
	if a == 0 {
		return m
	}
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		var w float32
		if shadows {
			w = 1 - adjSmooth(L/adjToneMid)
		} else {
			w = adjSmooth((L - adjToneMid) / (1 - adjToneMid))
		}
		return img.OKLabToLinear(L+0.25*a*w, A, B)
	})
}

// adjShadows lifts (positive) or deepens (negative) dark tones.
func adjShadows(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := args.Signed("amount", 30, -100, 100)
	if err != nil {
		return nil, err
	}
	return adjToneL(m, float32(v/100), true), nil
}

// adjHighlights recovers (negative) or boosts (positive) bright tones.
func adjHighlights(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := args.Signed("amount", -30, -100, 100)
	if err != nil {
		return nil, err
	}
	return adjToneL(m, float32(v/100), false), nil
}

// adjParseLevels reads "black white [gamma]"; points are 0-255 or percent.
func adjParseLevels(s string) (black, white, gamma float64, err error) {
	bad := Userf("levels takes \"black white [gamma]\", like 10 245, 5%% 95%% or 10 245 1.2")
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '/' })
	if len(parts) != 2 && len(parts) != 3 {
		return 0, 0, 0, bad
	}
	point := func(p string) (float64, error) {
		div := 255.0
		if strings.HasSuffix(p, "%") {
			p, div = strings.TrimSuffix(p, "%"), 100
		}
		v, e := strconv.ParseFloat(p, 64)
		if e != nil || v < 0 || v > div {
			return 0, bad
		}
		return v / div, nil
	}
	if black, err = point(parts[0]); err != nil {
		return
	}
	if white, err = point(parts[1]); err != nil {
		return
	}
	if white <= black {
		return 0, 0, 0, Userf("levels: the white point must be above the black point")
	}
	gamma = 1
	if len(parts) == 3 {
		gamma, err = strconv.ParseFloat(parts[2], 64)
		if err != nil || gamma < 0.1 || gamma > 10 {
			return 0, 0, 0, Userf("levels: gamma must be a number between 0.1 and 10")
		}
	}
	return
}

// adjLevels maps black..white to 0..1 on encoded values, then applies a
// midtone gamma (above 1 brightens).
func adjLevels(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	b, w, g, err := adjParseLevels(args.String("levels", "10 245"))
	if err != nil {
		return nil, err
	}
	if b == 0 && w == 1 && g == 1 {
		return m, nil
	}
	bf, span, inv := float32(b), float32(w-b), 1/g
	return img.LUT16(m, func(v float32) float32 {
		t := img.Clamp01((v - bf) / span)
		if inv != 1 {
			t = float32(math.Pow(float64(t), inv))
		}
		return t
	}), nil
}

// adjRank returns the value at 0-based sorted position k of a histogram.
func adjRank(h []uint32, k int) uint16 {
	cum := 0
	for v, c := range h {
		cum += int(c)
		if cum > k {
			return uint16(v)
		}
	}
	return 0xffff
}

// adjAutoLevels stretches each channel separately between its 0.5% and 99.5%
// percentiles (by rank among non-transparent pixels), on encoded values. It
// also removes colour casts, which is why it is per channel.
func adjAutoLevels(ctx context.Context, m *img.Image, _ Args) (*img.Image, error) {
	var hist [3][]uint32
	for c := range hist {
		hist[c] = make([]uint32, 65536)
	}
	n := 0
	for i := 0; i < len(m.Pix); i += 4 {
		if m.Pix[i+3] == 0 {
			continue
		}
		n++
		hist[0][m.Pix[i]]++
		hist[1][m.Pix[i+1]]++
		hist[2][m.Pix[i+2]]++
	}
	if n == 0 {
		return m, nil
	}
	kLo := n / 200
	kHi := n - 1 - kLo
	var luts [3][]uint16
	changed := false
	for c := range luts {
		lo, hi := adjRank(hist[c], kLo), adjRank(hist[c], kHi)
		if hi <= lo || (lo == 0 && hi == 0xffff) {
			continue
		}
		changed = true
		span := float32(hi - lo)
		lut := make([]uint16, 65536)
		for v := range lut {
			lut[v] = img.Q((float32(v) - float32(lo)) / span)
		}
		luts[c] = lut
	}
	if !changed {
		return m, nil
	}
	out := m.NewLike()
	img.Rows(ctx, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			for c := 0; c < 3; c++ {
				if luts[c] != nil {
					out.Pix[i+c] = luts[c][m.Pix[i+c]]
				} else {
					out.Pix[i+c] = m.Pix[i+c]
				}
			}
			out.Pix[i+3] = m.Pix[i+3]
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// adjAutoContrast stretches encoded luma (Rec.709 weights) between its 0.5%
// and 99.5% percentiles and applies that one curve to R, G and B, so hue
// relationships are kept (apart from clipping).
func adjAutoContrast(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	return adjAutoContrastImg(m), nil
}

func adjAutoContrastImg(m *img.Image) *img.Image {
	hist := make([]uint32, 65536)
	n := 0
	for i := 0; i < len(m.Pix); i += 4 {
		if m.Pix[i+3] == 0 {
			continue
		}
		n++
		y := 0.2126*img.F(m.Pix[i]) + 0.7152*img.F(m.Pix[i+1]) + 0.0722*img.F(m.Pix[i+2])
		hist[img.Q(y)]++
	}
	if n == 0 {
		return m
	}
	kLo := n / 200
	lo, hi := adjRank(hist, kLo), adjRank(hist, n-1-kLo)
	if hi <= lo || (lo == 0 && hi == 0xffff) {
		return m
	}
	lof, span := float32(lo)/65535, float32(hi-lo)/65535
	return img.LUT16(m, func(v float32) float32 { return (v - lof) / span })
}

// adjEnhance is the one-click fix: auto contrast, then a gentle vibrance.
func adjEnhance(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	return adjVibranceBy(adjAutoContrastImg(m), 0.15), nil
}

// adjWhiteBalance is gray-world in linear light: scale each channel so the
// average colour is neutral at the image's mean luminance. Gains are limited
// to 0.25..4 so a single-colour image is not blown out.
func adjWhiteBalance(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	var sum [3]float64
	n := 0
	for i := 0; i < len(m.Pix); i += 4 {
		if m.Pix[i+3] == 0 {
			continue
		}
		n++
		sum[0] += float64(img.Lin(m.Pix[i]))
		sum[1] += float64(img.Lin(m.Pix[i+1]))
		sum[2] += float64(img.Lin(m.Pix[i+2]))
	}
	if n == 0 {
		return m, nil
	}
	var mean [3]float64
	for c := range mean {
		mean[c] = sum[c] / float64(n)
	}
	target := float64(img.Luma(float32(mean[0]), float32(mean[1]), float32(mean[2])))
	var g [3]float32
	same := true
	for c := range g {
		gain := 4.0
		if mean[c] > 1e-6 {
			gain = clampF(target/mean[c], 0.25, 4)
		}
		g[c] = float32(gain)
		if math.Abs(gain-1) > 1e-4 {
			same = false
		}
	}
	if same {
		return m, nil
	}
	return img.MapColor(m, img.LinearLight, func(r, gg, b float32) (float32, float32, float32) {
		return r * g[0], gg * g[1], b * g[2]
	}), nil
}

// adjTemperature gains linear red up and blue down by 2^(±amount/200), so
// +100 is a one-half-stop shift each way (a strong but usable warm look).
func adjTemperature(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	t, err := args.Signed("amount", 25, -100, 100)
	if err != nil {
		return nil, err
	}
	if t == 0 {
		return m, nil
	}
	gr := float32(math.Exp2(0.5 * t / 100))
	gb := 1 / gr
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		return r * gr, g, b * gb
	}), nil
}

// adjTint gains linear green by 2^(-amount/200): positive is magenta,
// negative is green.
func adjTint(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	t, err := args.Signed("amount", 15, -100, 100)
	if err != nil {
		return nil, err
	}
	if t == 0 {
		return m, nil
	}
	gg := float32(math.Exp2(-0.5 * t / 100))
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		return r, g * gg, b
	}), nil
}

// adjHue rotates (a, b) in OKLab, i.e. hue in OKLCh; L and chroma stay.
func adjHue(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	deg, err := adjHueArg(args)
	if err != nil {
		return nil, err
	}
	if math.Mod(deg, 360) == 0 {
		return m, nil
	}
	rad := deg * math.Pi / 180
	c, s := float32(math.Cos(rad)), float32(math.Sin(rad))
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		return img.OKLabToLinear(L, A*c-B*s, A*s+B*c)
	}), nil
}

func adjHueArg(args Args) (float64, error) { return args.Degrees("degrees", 30) }

func adjVideoHue(args Args) (string, error) {
	deg, err := adjHueArg(args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("hue=h=%.3f", deg), nil
}

// adjInvert is 1-v on encoded values (a photographic negative); alpha stays.
func adjInvert(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	return img.LUT16(m, func(v float32) float32 { return 1 - v }), nil
}

// adjLightness is black and white from OKLab lightness (perceptual), unlike
// image.grayscale which uses linear luminance.
func adjLightness(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, _, _ := img.LinearToOKLab(r, g, b)
		return img.OKLabToLinear(L, 0, 0)
	}), nil
}

var adjChannelNames = []string{"red", "green", "blue"}

func adjChannelIndex(args Args) (int, error) {
	c, err := args.Choice("channel", "red", adjChannelNames, map[string]string{"r": "red", "g": "green", "b": "blue"})
	if err != nil {
		return 0, err
	}
	for i, n := range adjChannelNames {
		if n == c {
			return i, nil
		}
	}
	return 0, nil
}

// adjChannel shows one encoded channel as grey (how channel views look in
// other editors).
func adjChannel(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	ch, err := adjChannelIndex(args)
	if err != nil {
		return nil, err
	}
	return img.MapColor(m, img.Encoded, func(r, g, b float32) (float32, float32, float32) {
		v := [3]float32{r, g, b}[ch]
		return v, v, v
	}), nil
}

// adjMixer builds a colorchannelmixer filter where output channel i takes
// source channel src[i].
func adjMixer(src [3]int) string {
	var parts []string
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			v := 0
			if src[i] == j {
				v = 1
			}
			parts = append(parts, fmt.Sprintf("%c%c=%d", "rgb"[i], "rgb"[j], v))
		}
	}
	return "colorchannelmixer=" + strings.Join(parts, ":")
}

func adjVideoChannel(args Args) (string, error) {
	ch, err := adjChannelIndex(args)
	if err != nil {
		return "", err
	}
	return adjMixer([3]int{ch, ch, ch}), nil
}

// adjSepiaMatrix is the standard sepia matrix from the W3C Filter Effects
// spec (the sepia() function), applied to encoded sRGB.
var adjSepiaMatrix = [3][3]float32{
	{0.393, 0.769, 0.189},
	{0.349, 0.686, 0.168},
	{0.272, 0.534, 0.131},
}

// adjPercent reads a 0..100 strength ("50", "50%") as 0..1.
func adjPercent(args Args, key string, def float64) (float64, error) {
	v, err := args.Signed(key, def, 0, 100)
	return v / 100, err
}

func adjSepia(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	t, err := adjPercent(args, "amount", 100)
	if err != nil {
		return nil, err
	}
	if t == 0 {
		return m, nil
	}
	f := float32(t)
	s := adjSepiaMatrix
	return img.MapColor(m, img.Encoded, func(r, g, b float32) (float32, float32, float32) {
		nr := s[0][0]*r + s[0][1]*g + s[0][2]*b
		ng := s[1][0]*r + s[1][1]*g + s[1][2]*b
		nb := s[2][0]*r + s[2][1]*g + s[2][2]*b
		return r + f*(nr-r), g + f*(ng-g), b + f*(nb-b)
	}), nil
}

// adjVideoSepia blends the sepia matrix with identity, like the image op.
func adjVideoSepia(args Args) (string, error) {
	t, err := adjPercent(args, "amount", 100)
	if err != nil {
		return "", err
	}
	var parts []string
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			id := 0.0
			if i == j {
				id = 1
			}
			parts = append(parts, fmt.Sprintf("%c%c=%.4f", "rgb"[i], "rgb"[j], id+t*(float64(adjSepiaMatrix[i][j])-id)))
		}
	}
	return "colorchannelmixer=" + strings.Join(parts, ":"), nil
}

// adjTokens splits on spaces outside parentheses and drops joiner words, so
// "red to blue", "#f00 -> #00f" and "rgb(1, 2, 3) blue" all work.
func adjTokens(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		t := cur.String()
		cur.Reset()
		switch strings.ToLower(t) {
		case "to", "->", "→", "=>", "into", "with", "|":
			return
		}
		out = append(out, t)
	}
	for _, r := range s {
		switch {
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && depth == 0:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func adjParseColor(s string) (img.Color, error) {
	c, err := img.ParseColor(s)
	if err != nil {
		return img.Color{}, Userf("%s", err.Error())
	}
	return c, nil
}

// adjColorPair reads exactly two colours from an argument.
func adjColorPair(args Args, key, def, what string) (a, b img.Color, err error) {
	toks := adjTokens(args.String(key, def))
	if len(toks) != 2 {
		return a, b, Userf("%s needs two colours, like %q", what, def)
	}
	if a, err = adjParseColor(toks[0]); err != nil {
		return
	}
	b, err = adjParseColor(toks[1])
	return
}

func adjLin(c img.Color) (r, g, b float32) {
	return float32(img.SRGBToLinear(float64(c.R))), float32(img.SRGBToLinear(float64(c.G))), float32(img.SRGBToLinear(float64(c.B)))
}

// adjDuotone maps linear luminance onto a gradient between two colours,
// interpolating in linear light. Using linear luminance for the position
// means black->white reproduces the plain greyscale exactly.
func adjDuotone(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	sh, hi, err := adjColorPair(args, "colors", "#1d2b53 #ffd28f", "duotone")
	if err != nil {
		return nil, err
	}
	sr, sg, sb := adjLin(sh)
	hr, hg, hb := adjLin(hi)
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		t := img.Clamp01(img.Luma(r, g, b))
		return sr + (hr-sr)*t, sg + (hg-sg)*t, sb + (hb-sb)*t
	}), nil
}

// adjColorize takes hue and chroma from a colour while each pixel keeps its
// own OKLab lightness; "color [NN%]" sets the mix (50% by default).
func adjColorize(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	toks := adjTokens(args.String("color", "#ff9a3c"))
	mix := 0.5
	if n := len(toks); n > 1 && strings.HasSuffix(toks[n-1], "%") {
		p, err := strconv.ParseFloat(strings.TrimSuffix(toks[n-1], "%"), 64)
		if err != nil || p < 0 || p > 100 {
			return nil, Userf("colorize strength must be 0-100%%, like \"teal 70%%\"")
		}
		mix, toks = p/100, toks[:n-1]
	}
	c, err := adjParseColor(strings.Join(toks, " "))
	if err != nil {
		return nil, err
	}
	tr, tg, tb := adjLin(c)
	_, ta, tbb := img.LinearToOKLab(tr, tg, tb)
	f := float32(mix)
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		return img.OKLabToLinear(L, A+(ta-A)*f, B+(tbb-B)*f)
	}), nil
}

// adjPosterize snaps encoded values to n evenly spaced levels per channel.
func adjPosterize(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	n, err := args.IntInRange("levels", 4, 2, 32)
	if err != nil {
		return nil, err
	}
	nf := float32(n)
	return img.LUT16(m, func(v float32) float32 {
		return min(float32(math.Floor(float64(v*nf))), nf-1) / (nf - 1)
	}), nil
}

// adjThreshold compares linear luminance with the threshold (a percent of
// the encoded scale, so 50% matches a mid grey in any editor).
func adjThreshold(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	t, err := adjPercent(args, "level", 50)
	if err != nil {
		return nil, err
	}
	lim := float32(img.SRGBToLinear(t))
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		if img.Luma(r, g, b) >= lim {
			return 1, 1, 1
		}
		return 0, 0, 0
	}), nil
}

// adjSwapOrder reads "bgr"-style orders: output channel i takes source order[i].
func adjSwapOrder(args Args) ([3]int, error) {
	s := strings.ToLower(strings.TrimSpace(args.String("order", "bgr")))
	var src [3]int
	if len(s) != 3 {
		return src, Userf("order needs three letters from r, g, b, like bgr or grb (got %q)", s)
	}
	for i, ch := range s {
		j := strings.IndexRune("rgb", ch)
		if j < 0 {
			return src, Userf("order needs three letters from r, g, b, like bgr or grb (got %q)", s)
		}
		src[i] = j
	}
	return src, nil
}

func adjSwap(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	src, err := adjSwapOrder(args)
	if err != nil {
		return nil, err
	}
	if src == [3]int{0, 1, 2} {
		return m, nil
	}
	return img.MapColor(m, img.Encoded, func(r, g, b float32) (float32, float32, float32) {
		v := [3]float32{r, g, b}
		return v[src[0]], v[src[1]], v[src[2]]
	}), nil
}

func adjVideoSwap(args Args) (string, error) {
	src, err := adjSwapOrder(args)
	if err != nil {
		return "", err
	}
	return adjMixer(src), nil
}

// Colour replace uses a soft mask on OKLab distance
// d = sqrt((0.5*dL)^2 + da^2 + db^2): full strength within adjReplaceInner,
// fading smoothly to nothing at adjReplaceOuter. Lightness counts half so a
// shaded version of the colour is still caught.
const (
	adjReplaceInner = 0.10
	adjReplaceOuter = 0.25
)

// adjReplace moves matched pixels from one colour to another by rotating
// their hue and rescaling chroma by the same amounts as from->to, and
// shifting lightness by the same offset, so texture and shading survive.
func adjReplace(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	from, to, err := adjColorPair(args, "colors", "red blue", "replace color")
	if err != nil {
		return nil, err
	}
	fr, fg, fb := adjLin(from)
	tr, tg, tb := adjLin(to)
	Lf, Af, Bf := img.LinearToOKLab(fr, fg, fb)
	Lt, At, Bt := img.LinearToOKLab(tr, tg, tb)
	cf := math.Hypot(float64(Af), float64(Bf))
	ct := math.Hypot(float64(At), float64(Bt))
	dL := Lt - Lf
	// A grey "from" has no hue to rotate from, so translate a,b instead.
	translate := cf < 0.03
	dh := math.Atan2(float64(Bt), float64(At)) - math.Atan2(float64(Bf), float64(Af))
	cs, sn := float32(math.Cos(dh)), float32(math.Sin(dh))
	scale := float32(1)
	if !translate {
		scale = float32(ct / cf)
	}
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		L, A, B := img.LinearToOKLab(r, g, b)
		x, y, z := 0.5*(L-Lf), A-Af, B-Bf
		d := float32(math.Sqrt(float64(x*x + y*y + z*z)))
		w := 1 - adjSmooth((d-adjReplaceInner)/(adjReplaceOuter-adjReplaceInner))
		if w <= 0 {
			return r, g, b
		}
		var A2, B2 float32
		if translate {
			A2, B2 = A+(At-Af), B+(Bt-Bf)
		} else {
			A2, B2 = (A*cs-B*sn)*scale, (A*sn+B*cs)*scale
		}
		return img.OKLabToLinear(L+w*dL, A+w*(A2-A), B+w*(B2-B))
	}), nil
}

// adjOpacity multiplies straight alpha; colour is untouched.
func adjOpacity(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	k, err := adjPercent(args, "amount", 70)
	if err != nil {
		return nil, err
	}
	if k == 1 {
		return m, nil
	}
	f := float32(k)
	return img.MapRGBA(m, img.Encoded, func(r, g, b, a float32) (float32, float32, float32, float32) {
		return r, g, b, a * f
	}), nil
}
