package img

import (
	"fmt"
	"strconv"
	"strings"
)

// Color is an sRGB-encoded colour with straight alpha, channels in 0..1.
type Color struct{ R, G, B, A float32 }

// RGBA16 returns the colour as 16-bit straight-alpha channels.
func (c Color) RGBA16() (r, g, b, a uint16) { return Q(c.R), Q(c.G), Q(c.B), Q(c.A) }

var named = map[string]string{
	"white": "#ffffff", "black": "#000000", "red": "#ff0000", "green": "#008000", "lime": "#00ff00",
	"blue": "#0000ff", "yellow": "#ffff00", "orange": "#ffa500", "purple": "#800080", "pink": "#ffc0cb",
	"gray": "#808080", "grey": "#808080", "silver": "#c0c0c0", "navy": "#000080", "teal": "#008080",
	"cyan": "#00ffff", "magenta": "#ff00ff", "maroon": "#800000", "olive": "#808000", "brown": "#a52a2a",
	"gold": "#ffd700", "beige": "#f5f5dc", "ivory": "#fffff0", "coral": "#ff7f50", "salmon": "#fa8072",
	"indigo": "#4b0082", "violet": "#ee82ee", "turquoise": "#40e0d0", "crimson": "#dc143c", "khaki": "#f0e68c",
	"cream": "#fffdd0", "sepia": "#704214", "transparent": "#00000000", "none": "#00000000",
}

// ParseColor reads "#rgb", "#rrggbb", "#rrggbbaa", "rrggbb", "rgb(255,128,0)",
// "rgba(255,128,0,0.5)", "255,128,0" or a common colour name.
func ParseColor(s string) (Color, error) {
	in := strings.ToLower(strings.TrimSpace(s))
	if hex, ok := named[in]; ok {
		in = hex
	}
	bad := fmt.Errorf("%q is not a colour; try #ff8800, 255,136,0 or a name like white", s)
	if strings.HasPrefix(in, "rgb") {
		open, close := strings.IndexByte(in, '('), strings.LastIndexByte(in, ')')
		if open < 0 || close < open {
			return Color{}, bad
		}
		in = in[open+1 : close]
	}
	if strings.ContainsAny(in, ", ") && !strings.HasPrefix(in, "#") {
		parts := strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
		if len(parts) != 3 && len(parts) != 4 {
			return Color{}, bad
		}
		var v [4]float64
		v[3] = 1
		for i, p := range parts {
			pct := strings.HasSuffix(p, "%")
			f, err := strconv.ParseFloat(strings.TrimSuffix(p, "%"), 64)
			if err != nil {
				return Color{}, bad
			}
			switch {
			case pct:
				f /= 100
			case i < 3:
				f /= 255
			}
			if f < 0 || f > 1 {
				return Color{}, bad
			}
			v[i] = f
		}
		return Color{float32(v[0]), float32(v[1]), float32(v[2]), float32(v[3])}, nil
	}
	h := strings.TrimPrefix(in, "#")
	switch len(h) {
	case 3, 4:
		var b strings.Builder
		for _, c := range h {
			b.WriteRune(c)
			b.WriteRune(c)
		}
		h = b.String()
	case 6, 8:
	default:
		return Color{}, bad
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return Color{}, bad
	}
	if len(h) == 6 {
		n = n<<8 | 0xff
	}
	return Color{float32(n>>24&0xff) / 255, float32(n>>16&0xff) / 255, float32(n>>8&0xff) / 255, float32(n&0xff) / 255}, nil
}

// Hex formats a colour as #rrggbb (or #rrggbbaa when not opaque).
func (c Color) Hex() string {
	r, g, b, a := To8(Q(c.R)), To8(Q(c.G)), To8(Q(c.B)), To8(Q(c.A))
	if a == 255 {
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", r, g, b, a)
}
