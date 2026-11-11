package ops

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"asteria/internal/img"
)

// Parameter helpers shared by every image skill. They accept what people
// type in the command bar and return friendly errors (UserError).

// Amount reads a strength as a multiplier where 1 means "unchanged":
//
//	"150%" -> 1.5   absolute level, 100% = unchanged
//	"30%"  -> 0.3
//	"+50"  -> 1.5   relative change in percent
//	"-30"  -> 0.7
//	"50"   -> 1.5   a bare number is a relative change too
//	"1.5x" -> 1.5   explicit factor
//
// The result is clamped to [lo, hi].
func (a Args) Amount(key string, def, lo, hi float64) (float64, error) {
	raw, ok := a[key]
	if !ok || raw == nil || strings.TrimSpace(fmt.Sprint(raw)) == "" {
		return def, nil
	}
	if f, isNum := raw.(float64); isNum {
		return clampF(1+f/100, lo, hi), nil
	}
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fmt.Sprint(raw)), " ", ""))
	var v float64
	var err error
	switch {
	case strings.HasSuffix(s, "x"):
		v, err = strconv.ParseFloat(strings.TrimSuffix(s, "x"), 64)
	case strings.HasSuffix(s, "%") && !strings.HasPrefix(s, "+") && !strings.HasPrefix(s, "-"):
		v, err = strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		v /= 100
	default:
		v, err = strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		v = 1 + v/100
	}
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, Userf("%q is not an amount; try 150%%, +40 or -30", fmt.Sprint(raw))
	}
	return clampF(v, lo, hi), nil
}

// Signed reads a value such as "+20", "-15", "20%" or "0.4" in [lo, hi]
// (percent signs are dropped; the number is used as is).
func (a Args) Signed(key string, def, lo, hi float64) (float64, error) {
	v, err := a.Float(key, def)
	if err != nil {
		return 0, err
	}
	if v < lo || v > hi {
		return 0, Userf("%s must be between %g and %g (got %g)", key, lo, hi, v)
	}
	return v, nil
}

// Degrees reads an angle: "90", "-15°", "45deg", "0.25turn".
func (a Args) Degrees(key string, def float64) (float64, error) {
	raw := strings.ToLower(strings.TrimSpace(a.String(key, "")))
	if raw == "" {
		return def, nil
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(raw, "turn"):
		raw, mult = strings.TrimSuffix(raw, "turn"), 360
	case strings.HasSuffix(raw, "deg"):
		raw = strings.TrimSuffix(raw, "deg")
	}
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "°")
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, Userf("%q is not an angle; try 90 or -15", a.String(key, ""))
	}
	return v * mult, nil
}

// Length reads a size in pixels ("12", "12px") or as a percent of ref
// ("5%"), already multiplied by scale (the working image's Scale, so proxies
// preview the same look). The result is clamped to [0, max] when max > 0.
func (a Args) Length(key string, def float64, ref int, scale float64, max float64) (float64, error) {
	raw := strings.ToLower(strings.TrimSpace(a.String(key, "")))
	var v float64
	switch {
	case raw == "":
		v = def * scale
	case strings.HasSuffix(raw, "%"):
		p, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "%")), 64)
		if err != nil {
			return 0, Userf("%q is not a size; try 12 or 5%%", a.String(key, ""))
		}
		v = p / 100 * float64(ref) // ref is already the working size
	default:
		p, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "px")), 64)
		if err != nil {
			return 0, Userf("%q is not a size; try 12 or 5%%", a.String(key, ""))
		}
		v = p * scale
	}
	if v < 0 {
		return 0, Userf("%s can't be negative", key)
	}
	if max > 0 && v > max {
		v = max
	}
	return v, nil
}

// Color reads a colour (see img.ParseColor).
func (a Args) Color(key, def string) (img.Color, error) {
	c, err := img.ParseColor(a.String(key, def))
	if err != nil {
		return img.Color{}, Userf("%s", err.Error())
	}
	return c, nil
}

// Aspect reads a ratio such as "16:9", "4x5", "1.5" or "square".
func (a Args) Aspect(key, def string) (float64, error) {
	s := strings.ToLower(strings.TrimSpace(a.String(key, def)))
	switch s {
	case "square":
		return 1, nil
	case "portrait":
		return 4.0 / 5, nil
	case "landscape", "wide":
		return 16.0 / 9, nil
	}
	for _, sep := range []string{":", "x", "/", "×"} {
		if l, r, ok := strings.Cut(s, sep); ok {
			x, e1 := strconv.ParseFloat(strings.TrimSpace(l), 64)
			y, e2 := strconv.ParseFloat(strings.TrimSpace(r), 64)
			if e1 != nil || e2 != nil || x <= 0 || y <= 0 {
				break
			}
			return x / y, nil
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, Userf("%q is not an aspect ratio; try 16:9 or 1:1", s)
	}
	return v, nil
}

// Choice reads one of the allowed words (case-insensitive). Aliases map
// other spellings to an allowed word.
func (a Args) Choice(key, def string, allowed []string, aliases map[string]string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(a.String(key, def)))
	if v, ok := aliases[s]; ok {
		s = v
	}
	for _, w := range allowed {
		if s == w {
			return s, nil
		}
	}
	return "", Userf("%s must be one of %s (got %q)", key, strings.Join(allowed, ", "), s)
}

// Bool reads yes/no style values.
func (a Args) Bool(key string, def bool) bool {
	switch strings.ToLower(a.String(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func clampF(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
