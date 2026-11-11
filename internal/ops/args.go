package ops

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Args is a loosely typed argument bag. Values may arrive as JSON numbers,
// strings typed into the command bar, or booleans.
type Args map[string]any

// Merge returns a new Args with b's entries layered over a's.
func Merge(a, b map[string]any) Args {
	out := Args{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// String returns the value as a trimmed string.
func (a Args) String(key, fallback string) string {
	v, ok := a[key]
	if !ok || v == nil {
		return fallback
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return fallback
	}
	return s
}

// Float parses a number, tolerating a trailing unit such as "%" or "px".
func (a Args) Float(key string, fallback float64) (float64, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return fallback, nil
	}
	switch t := v.(type) {
	case float64:
		return t, nil
	case float32:
		return float64(t), nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return fallback, nil
	}
	s = strings.TrimRight(s, "%pxPX° ")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, Userf("%q is not a number", fmt.Sprint(v))
	}
	return f, nil
}

// IntInRange parses an integer and validates it against [min,max].
func (a Args) IntInRange(key string, fallback, min, max int) (int, error) {
	f, err := a.Float(key, float64(fallback))
	if err != nil {
		return 0, err
	}
	n := int(math.Round(f))
	if n < min || n > max {
		return 0, Userf("%s must be between %d and %d (got %d)", key, min, max, n)
	}
	return n, nil
}
