package img

import "math"

// Transfer curve tables. toLinear maps every 16-bit encoded value to linear
// light; fromLinear maps linear light (0..1, 1/4096 steps) to encoded 16-bit
// and is interpolated, which keeps the round trip within one 16-bit step.
var (
	toLinear   [65536]float32
	fromLinear [4097]float32
)

func init() {
	for i := range toLinear {
		toLinear[i] = float32(SRGBToLinear(float64(i) / 65535))
	}
	for i := range fromLinear {
		fromLinear[i] = float32(LinearToSRGB(float64(i) / 4096))
	}
}

// SRGBToLinear applies the sRGB EOTF to an encoded value in 0..1.
func SRGBToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// LinearToSRGB applies the inverse sRGB EOTF to a linear value in 0..1.
func LinearToSRGB(v float64) float64 {
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// Lin returns the linear-light value of an encoded 16-bit channel.
func Lin(v uint16) float32 { return toLinear[v] }

// Enc returns the encoded 16-bit channel for a linear value (clamped).
func Enc(v float32) uint16 {
	if !(v > 0) {
		return 0
	}
	if v >= 1 {
		return 0xffff
	}
	if v < 0.0031308 {
		return Q(v * 12.92)
	}
	f := v * 4096
	i := int(f)
	t := f - float32(i)
	e := fromLinear[i]*(1-t) + fromLinear[min(i+1, 4096)]*t
	return Q(e)
}

// Q quantizes a 0..1 value to 16 bits with rounding and clamping.
func Q(v float32) uint16 {
	if !(v > 0) {
		return 0
	}
	if v >= 1 {
		return 0xffff
	}
	return uint16(v*65535 + 0.5)
}

// F returns an encoded 16-bit channel as 0..1.
func F(v uint16) float32 { return float32(v) / 65535 }

// Luma is linear-light relative luminance (Rec. 709 / sRGB primaries).
func Luma(r, g, b float32) float32 { return 0.2126*r + 0.7152*g + 0.0722*b }

// LinearToOKLab converts linear sRGB to OKLab (Björn Ottosson, 2020).
func LinearToOKLab(r, g, b float32) (L, A, B float32) {
	l := 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
	m := 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
	s := 0.0883024619*r + 0.2817188376*g + 0.6299787005*b
	l, m, s = cbrt(l), cbrt(m), cbrt(s)
	L = 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	A = 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	B = 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	return
}

// OKLabToLinear converts OKLab back to linear sRGB (may leave the gamut;
// callers clamp or gamut-map).
func OKLabToLinear(L, A, B float32) (r, g, b float32) {
	l := L + 0.3963377774*A + 0.2158037573*B
	m := L - 0.1055613458*A - 0.0638541728*B
	s := L - 0.0894841775*A - 1.2914855480*B
	l, m, s = l*l*l, m*m*m, s*s*s
	r = 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g = -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	b = -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	return
}

func cbrt(v float32) float32 { return float32(math.Cbrt(float64(v))) }

// Space selects how MapColor presents channel values to a function.
type Space int

const (
	// Encoded: sRGB-encoded values in 0..1, as colour pickers show them.
	Encoded Space = iota
	// LinearLight: linear-light values in 0..1.
	LinearLight
)

// MapColor applies fn to every pixel's colour in the chosen space and returns
// a new image. Alpha is passed through untouched (straight alpha: colour
// edits never change coverage). Results are clamped to 0..1.
func MapColor(m *Image, space Space, fn func(r, g, b float32) (float32, float32, float32)) *Image {
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			p := m.Pix[i : i+4 : i+4]
			o := out.Pix[i : i+4 : i+4]
			if space == LinearLight {
				r, g, b := fn(toLinear[p[0]], toLinear[p[1]], toLinear[p[2]])
				o[0], o[1], o[2] = Enc(r), Enc(g), Enc(b)
			} else {
				r, g, b := fn(F(p[0]), F(p[1]), F(p[2]))
				o[0], o[1], o[2] = Q(r), Q(g), Q(b)
			}
			o[3] = p[3]
		}
	})
	return out
}

// MapRGBA is MapColor with alpha visible and writable (straight alpha).
func MapRGBA(m *Image, space Space, fn func(r, g, b, a float32) (float32, float32, float32, float32)) *Image {
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			p := m.Pix[i : i+4 : i+4]
			o := out.Pix[i : i+4 : i+4]
			if space == LinearLight {
				r, g, b, a := fn(toLinear[p[0]], toLinear[p[1]], toLinear[p[2]], F(p[3]))
				o[0], o[1], o[2], o[3] = Enc(r), Enc(g), Enc(b), Q(a)
			} else {
				r, g, b, a := fn(F(p[0]), F(p[1]), F(p[2]), F(p[3]))
				o[0], o[1], o[2], o[3] = Q(r), Q(g), Q(b), Q(a)
			}
		}
	})
	return out
}

// LUT16 applies a per-channel lookup table on encoded values (fast path for
// curves, levels, gamma, posterize...). Alpha is untouched.
func LUT16(m *Image, lut func(v float32) float32) *Image {
	var t [65536]uint16
	for i := range t {
		t[i] = Q(lut(float32(i) / 65535))
	}
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			out.Pix[i] = t[m.Pix[i]]
			out.Pix[i+1] = t[m.Pix[i+1]]
			out.Pix[i+2] = t[m.Pix[i+2]]
			out.Pix[i+3] = m.Pix[i+3]
		}
	})
	return out
}
