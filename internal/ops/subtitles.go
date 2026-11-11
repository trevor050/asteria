package ops

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SubtitleExts are the subtitle formats the subtitles.* handlers read.
var SubtitleExts = []string{".srt", ".vtt", ".ass", ".ssa", ".sbv", ".ttml", ".dfxp"}

// Subtitles are parsed into one small model (cues with plain text lines) and
// written back out in any supported format. Inline styling is kept as the
// three HTML-like tags <i>, <b> and <u>; formats that cannot express them
// (SBV, TTML here) drop the tags, and ASS override blocks such as {\an8} or
// {\pos(10,20)} are dropped when reading, except italic/bold/underline which
// become those tags. Positioning, colours and fonts are not preserved.

type subCue struct {
	Start, End time.Duration
	Lines      []string
}

func init() {
	register(Handler{Name: "subtitles.convert", Run: subConvert})
	register(Handler{Name: "subtitles.shift", Run: subShift})
	register(Handler{Name: "subtitles.retime", Run: subRetime})
}

func subKind(ext string) string {
	switch strings.ToLower(ext) {
	case ".srt":
		return "srt"
	case ".vtt":
		return "vtt"
	case ".ass", ".ssa":
		return "ass"
	case ".sbv":
		return "sbv"
	case ".ttml", ".dfxp", ".xml":
		return "ttml"
	}
	return ""
}

var subSBVLineRe = regexp.MustCompile(`(?m)^\d+:\d\d:\d\d\.\d+,\d+:\d\d:\d\d\.\d+\s*$`)

// subSniff guesses the format from content when the extension is unknown.
func subSniff(s string) string {
	t := strings.TrimSpace(s)
	low := strings.ToLower(t)
	switch {
	case strings.HasPrefix(t, "WEBVTT"):
		return "vtt"
	case strings.Contains(low, "[script info]") || strings.Contains(low, "[events]"):
		return "ass"
	case strings.Contains(low, "<tt") && strings.Contains(low, "<p"):
		return "ttml"
	case subSBVLineRe.MatchString(s):
		return "sbv"
	}
	return "srt"
}

func subLoad(job Job) ([]subCue, error) {
	raw, err := os.ReadFile(job.Input())
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDataBytes {
		return nil, Userf("%s is too large to convert in memory", job.Name(0))
	}
	text := subNormalize(txtDecodeString(raw))
	kind := subKind(filepath.Ext(job.Name(0)))
	if kind == "" {
		kind = subKind(filepath.Ext(job.Input()))
	}
	if kind == "" {
		kind = subSniff(text)
	}
	cues, err := subParse(kind, text)
	if err != nil {
		return nil, err
	}
	if len(cues) == 0 {
		return nil, Userf("no subtitles found in %s", job.Name(0))
	}
	return cues, nil
}

func subSave(job Job, cues []subCue) error {
	ext := filepath.Ext(job.Output)
	kind := subKind(ext)
	if kind == "" {
		return Userf("cannot write %s subtitles", ext)
	}
	return os.WriteFile(job.Output, subWrite(kind, cues), 0o644)
}

func subConvert(_ context.Context, job Job) error {
	cues, err := subLoad(job)
	if err != nil {
		return err
	}
	job.Progress(0.9)
	return subSave(job, cues)
}

func subShift(_ context.Context, job Job) error {
	off, err := subParseOffset(job.Args.String("offset", "+1s"))
	if err != nil {
		return err
	}
	cues, err := subLoad(job)
	if err != nil {
		return err
	}
	out := subShiftCues(cues, off)
	if len(out) == 0 {
		return Userf("shifting by %s would move every subtitle before 0:00", job.Args.String("offset", "+1s"))
	}
	return subSave(job, out)
}

func subRetime(_ context.Context, job Job) error {
	factor, err := subParseFactor(job.Args.String("speed", "23.976 25"))
	if err != nil {
		return err
	}
	cues, err := subLoad(job)
	if err != nil {
		return err
	}
	return subSave(job, subScaleCues(cues, factor))
}

// subShiftCues moves every cue by off. Cues that end at or before 0 are
// dropped and cues that start before 0 are clamped to 0.
func subShiftCues(cues []subCue, off time.Duration) []subCue {
	out := make([]subCue, 0, len(cues))
	for _, c := range cues {
		end := c.End + off
		if end <= 0 {
			continue
		}
		start := c.Start + off
		if start < 0 {
			start = 0
		}
		out = append(out, subCue{Start: start, End: end, Lines: c.Lines})
	}
	return out
}

// subScaleCues multiplies every time by factor.
func subScaleCues(cues []subCue, factor float64) []subCue {
	out := make([]subCue, len(cues))
	for i, c := range cues {
		out[i] = subCue{
			Start: time.Duration(math.Round(float64(c.Start) * factor)),
			End:   time.Duration(math.Round(float64(c.End) * factor)),
			Lines: c.Lines,
		}
	}
	return out
}

// subParseOffset reads "+1.5s", "-500ms", "2", "-1m", "-0:02.3" or "1:02:03.5".
func subParseOffset(raw string) (time.Duration, error) {
	bad := Userf("%q is not a time offset; try +1.5s, -500ms or -0:02.3", raw)
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.NewReplacer(" ", "", "\u2212", "-").Replace(s)
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if s == "" {
		return 0, bad
	}
	var d time.Duration
	if strings.Contains(s, ":") {
		v, ok := subClock(s)
		if !ok {
			return 0, bad
		}
		d = v
	} else {
		unit := time.Second
		num := s
		for _, u := range []struct {
			suf string
			d   time.Duration
		}{{"ms", time.Millisecond}, {"min", time.Minute}, {"sec", time.Second}, {"s", time.Second}, {"m", time.Minute}, {"h", time.Hour}} {
			if strings.HasSuffix(s, u.suf) {
				unit, num = u.d, strings.TrimSuffix(s, u.suf)
				break
			}
		}
		f, err := strconv.ParseFloat(num, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f*float64(unit)) > float64(100*time.Hour) {
			return 0, bad
		}
		d = time.Duration(math.Round(f * float64(unit)))
	}
	if neg {
		d = -d
	}
	return d, nil
}

var subNumberRe = regexp.MustCompile(`\d+(?:\.\d+)?|\.\d+`)

// subParseFactor reads "23.976 25" (from to, in fps; factor = from/to) or a
// plain multiplier such as "1.001".
func subParseFactor(raw string) (float64, error) {
	bad := Userf("%q is not a speed fix; try 23.976 25 (frame rates) or 1.001 (multiplier)", raw)
	nums := subNumberRe.FindAllString(raw, -1)
	var f float64
	switch len(nums) {
	case 1:
		v, err := strconv.ParseFloat(nums[0], 64)
		if err != nil {
			return 0, bad
		}
		f = v
	case 2:
		from, e1 := strconv.ParseFloat(nums[0], 64)
		to, e2 := strconv.ParseFloat(nums[1], 64)
		if e1 != nil || e2 != nil || from <= 0 || to <= 0 {
			return 0, bad
		}
		f = from / to
	default:
		return 0, bad
	}
	if !(f > 0.01 && f < 100) {
		return 0, bad
	}
	return f, nil
}

// subClock parses [[h:]m:]s[.fraction] (decimal point or comma).
func subClock(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	var base int64
	for _, p := range parts[:len(parts)-1] {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 {
			return 0, false
		}
		base = base*60 + int64(n)
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	whole, frac := last, ""
	if i := strings.IndexAny(last, ".,"); i >= 0 {
		whole, frac = last[:i], last[i+1:]
	}
	var sec int64
	if whole != "" {
		n, err := strconv.Atoi(whole)
		if err != nil || n < 0 {
			return 0, false
		}
		sec = int64(n)
	} else if frac == "" {
		return 0, false
	}
	var ns int64
	if frac != "" {
		for _, r := range frac {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		if len(frac) > 9 {
			frac = frac[:9]
		}
		frac += strings.Repeat("0", 9-len(frac))
		ns, _ = strconv.ParseInt(frac, 10, 64)
	}
	return time.Duration(base*60+sec)*time.Second + time.Duration(ns), true
}

func subStamp(d time.Duration, sep string, padHours bool) string {
	if d < 0 {
		d = 0
	}
	ms := int64((d + 500*time.Microsecond) / time.Millisecond)
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	if padHours {
		return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms)
	}
	return fmt.Sprintf("%d:%02d:%02d%s%03d", h, m, s, sep, ms)
}

func subStampASS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	cs := int64((d + 5*time.Millisecond) / (10 * time.Millisecond))
	h := cs / 360000
	cs %= 360000
	m := cs / 6000
	cs %= 6000
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, cs/100, cs%100)
}

func subNormalize(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func subBlocks(s string) [][]string {
	var blocks [][]string
	var cur []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, strings.TrimRight(ln, " \t"))
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

func subParse(kind, text string) ([]subCue, error) {
	switch kind {
	case "srt", "vtt":
		return subParseArrow(text, kind == "vtt"), nil
	case "ass":
		return subParseASS(text), nil
	case "sbv":
		return subParseSBV(text), nil
	case "ttml":
		return subParseTTML(text)
	}
	return nil, Userf("unknown subtitle format")
}

func subArrowLine(ln string) (time.Duration, time.Duration, bool) {
	i := strings.Index(ln, "-->")
	if i < 0 {
		return 0, 0, false
	}
	start, ok1 := subClock(ln[:i])
	right := strings.Fields(ln[i+3:])
	if !ok1 || len(right) == 0 {
		return 0, 0, false
	}
	end, ok2 := subClock(right[0])
	if !ok2 {
		return 0, 0, false
	}
	return start, end, true
}

func subAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// subParseArrow reads SRT and WebVTT ("start --> end"). Cue numbers and
// identifiers, WEBVTT headers and NOTE/STYLE/REGION blocks are skipped.
func subParseArrow(text string, vtt bool) []subCue {
	var cues []subCue
	for _, blk := range subBlocks(text) {
		first := blk[0]
		if first == "NOTE" || strings.HasPrefix(first, "NOTE ") || strings.HasPrefix(first, "NOTE\t") ||
			first == "STYLE" || first == "REGION" {
			continue
		}
		cur := -1
		for i, ln := range blk {
			if st, en, ok := subArrowLine(ln); ok {
				cues = append(cues, subCue{Start: st, End: en})
				cur = len(cues) - 1
				continue
			}
			if cur < 0 {
				continue
			}
			if subAllDigits(strings.TrimSpace(ln)) && i+1 < len(blk) {
				if _, _, ok := subArrowLine(blk[i+1]); ok {
					continue
				}
			}
			if t := subMarkup(ln, vtt); t != "" {
				cues[cur].Lines = append(cues[cur].Lines, t)
			}
		}
	}
	return cues
}

var (
	subTagRe      = regexp.MustCompile(`</?[A-Za-z][^<>]*>|<\d+:\d\d[^<>]*>`)
	subOverrideRe = regexp.MustCompile(`\{\\[^{}]*\}`)
)

func subTagName(tag string) string {
	inner := strings.Trim(tag, "</>")
	end := 0
	for end < len(inner) && (inner[end] >= 'a' && inner[end] <= 'z' || inner[end] >= 'A' && inner[end] <= 'Z') {
		end++
	}
	return strings.ToLower(inner[:end])
}

// subMarkup keeps <i>, <b> and <u> and drops every other tag and {\...}
// override block. For WebVTT it also resolves entities.
func subMarkup(line string, vtt bool) string {
	line = subOverrideRe.ReplaceAllString(line, "")
	line = subTagRe.ReplaceAllStringFunc(line, func(tag string) string {
		switch n := subTagName(tag); n {
		case "i", "b", "u":
			if strings.HasPrefix(tag, "</") {
				return "</" + n + ">"
			}
			return "<" + n + ">"
		}
		return ""
	})
	if vtt {
		line = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&nbsp;", " ", "&lrm;", "", "&rlm;", "", "&amp;", "&").Replace(line)
	}
	return strings.TrimSpace(line)
}

func subStrip(line string) string {
	return subTagRe.ReplaceAllString(line, "")
}

func subParseASS(text string) []subCue {
	var cues []subCue
	section := ""
	var cols []string
	col := func(name string) int {
		for i, c := range cols {
			if c == name {
				return i
			}
		}
		return -1
	}
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "[") && strings.HasSuffix(ln, "]") {
			section = strings.ToLower(ln)
			continue
		}
		if section != "[events]" {
			continue
		}
		key, val, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "format":
			cols = nil
			for _, c := range strings.Split(val, ",") {
				cols = append(cols, strings.ToLower(strings.TrimSpace(c)))
			}
		case "dialogue":
			if cols == nil {
				cols = []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
			}
			f := strings.SplitN(strings.TrimLeft(val, " "), ",", len(cols))
			iS, iE, iT := col("start"), col("end"), col("text")
			if len(f) < len(cols) || iS < 0 || iE < 0 || iT < 0 {
				continue
			}
			st, ok1 := subClock(f[iS])
			en, ok2 := subClock(f[iE])
			if !ok1 || !ok2 {
				continue
			}
			cues = append(cues, subCue{Start: st, End: en, Lines: subASSText(f[iT])})
		}
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
	return cues
}

// subASSText turns an ASS text field into lines: \N and \n break lines, \h
// is a space, {\i1}/{\b1}/{\u1} become tags and every other override is
// dropped.
func subASSText(raw string) []string {
	var sb strings.Builder
	open := map[string]bool{}
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == '{':
			j := strings.IndexByte(raw[i:], '}')
			if j < 0 {
				sb.WriteByte(c)
				i++
				continue
			}
			for _, seg := range strings.Split(raw[i+1:i+j], "\\") {
				seg = strings.TrimSpace(seg)
				if len(seg) != 2 || (seg[0] != 'i' && seg[0] != 'b' && seg[0] != 'u') || (seg[1] != '0' && seg[1] != '1') {
					continue
				}
				tag := seg[:1]
				if seg[1] == '1' && !open[tag] {
					sb.WriteString("<" + tag + ">")
					open[tag] = true
				} else if seg[1] == '0' && open[tag] {
					sb.WriteString("</" + tag + ">")
					open[tag] = false
				}
			}
			i += j + 1
		case c == '\\' && i+1 < len(raw):
			switch raw[i+1] {
			case 'N', 'n':
				sb.WriteByte('\n')
				i += 2
			case 'h':
				sb.WriteByte(' ')
				i += 2
			case '{', '}':
				sb.WriteByte(raw[i+1])
				i += 2
			default:
				sb.WriteByte(c)
				i++
			}
		default:
			sb.WriteByte(c)
			i++
		}
	}
	for _, t := range []string{"u", "b", "i"} {
		if open[t] {
			sb.WriteString("</" + t + ">")
		}
	}
	var lines []string
	for _, l := range strings.Split(sb.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func subParseSBV(text string) []subCue {
	var cues []subCue
	for _, blk := range subBlocks(text) {
		parts := strings.Split(blk[0], ",")
		if len(parts) == 4 { // comma decimals: 0:00:01,000,0:00:03,500
			parts = []string{parts[0] + "," + parts[1], parts[2] + "," + parts[3]}
		}
		if len(parts) != 2 {
			continue
		}
		st, ok1 := subClock(parts[0])
		en, ok2 := subClock(parts[1])
		if !ok1 || !ok2 {
			continue
		}
		c := subCue{Start: st, End: en}
		for _, ln := range blk[1:] {
			if t := strings.TrimSpace(ln); t != "" {
				c.Lines = append(c.Lines, t)
			}
		}
		cues = append(cues, c)
	}
	return cues
}

func subTTMLTime(s string, frameRate, tickRate float64) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	toDur := func(sec float64) (time.Duration, bool) {
		if math.IsNaN(sec) || math.IsInf(sec, 0) {
			return 0, false
		}
		return time.Duration(math.Round(sec * 1e9)), true
	}
	if c := s[len(s)-1]; c >= 'a' && c <= 'z' {
		for _, u := range []struct {
			suf  string
			mult float64
		}{{"ms", 0.001}, {"h", 3600}, {"m", 60}, {"s", 1}, {"f", 0}, {"t", 0}} {
			if !strings.HasSuffix(s, u.suf) {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suf), 64)
			if err != nil {
				return 0, false
			}
			switch u.suf {
			case "f":
				if frameRate <= 0 {
					return 0, false
				}
				return toDur(v / frameRate)
			case "t":
				if tickRate <= 0 {
					return 0, false
				}
				return toDur(v / tickRate)
			}
			return toDur(v * u.mult)
		}
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) == 4 { // hh:mm:ss:frames
		fr, err := strconv.Atoi(parts[3])
		base, ok := subClock(strings.Join(parts[:3], ":"))
		if err != nil || !ok || frameRate <= 0 {
			return 0, false
		}
		extra, _ := toDur(float64(fr) / frameRate)
		return base + extra, true
	}
	return subClock(s)
}

// subParseTTML reads <p begin end|dur> cues. Supported time forms: clock
// time "00:00:01.500", clock time with frames "00:00:01:12", and offsets
// "1.5s", "1500ms", "2m", "1h", "45f" (frames, ttp:frameRate and
// ttp:frameRateMultiplier, default 30 fps) and "900t" (ticks, ttp:tickRate).
// <br/> breaks lines; italic/bold/underline spans become <i>/<b>/<u>.
func subParseTTML(data string) ([]subCue, error) {
	d := xml.NewDecoder(strings.NewReader(data))
	d.Strict = false
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	frameRate, tickRate := 30.0, 0.0
	var cues []subCue
	inP := false
	var stack []string
	var sb strings.Builder
	var begin, end, dur string
	finish := func() {
		inP = false
		lines := []string{}
		for _, l := range strings.Split(sb.String(), "\n") {
			if l = strings.Join(strings.Fields(l), " "); l != "" {
				lines = append(lines, l)
			}
		}
		st, ok := subTTMLTime(begin, frameRate, tickRate)
		if !ok {
			return
		}
		en, ok := subTTMLTime(end, frameRate, tickRate)
		if !ok {
			du, okd := subTTMLTime(dur, frameRate, tickRate)
			if !okd {
				return
			}
			en = st + du
		}
		cues = append(cues, subCue{Start: st, End: en, Lines: lines})
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(cues) > 0 {
				break
			}
			return nil, Userf("could not read the TTML file: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if inP {
				closer := ""
				switch name {
				case "br":
					sb.WriteString("\n")
				case "span":
					for _, a := range t.Attr {
						v := strings.ToLower(a.Value)
						switch {
						case a.Name.Local == "fontStyle" && (v == "italic" || v == "oblique"):
							sb.WriteString("<i>")
							closer = "</i>" + closer
						case a.Name.Local == "fontWeight" && v == "bold":
							sb.WriteString("<b>")
							closer = "</b>" + closer
						case a.Name.Local == "textDecoration" && strings.Contains(v, "underline") && !strings.Contains(v, "nounderline"):
							sb.WriteString("<u>")
							closer = "</u>" + closer
						}
					}
				}
				stack = append(stack, closer)
				continue
			}
			switch name {
			case "tt":
				var mulN, mulD float64 = 1, 1
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "frameRate":
						if v, e := strconv.ParseFloat(strings.TrimSpace(a.Value), 64); e == nil && v > 0 {
							frameRate = v
						}
					case "tickRate":
						if v, e := strconv.ParseFloat(strings.TrimSpace(a.Value), 64); e == nil && v > 0 {
							tickRate = v
						}
					case "frameRateMultiplier":
						f := strings.Fields(a.Value)
						if len(f) == 2 {
							n, e1 := strconv.ParseFloat(f[0], 64)
							dn, e2 := strconv.ParseFloat(f[1], 64)
							if e1 == nil && e2 == nil && n > 0 && dn > 0 {
								mulN, mulD = n, dn
							}
						}
					}
				}
				frameRate *= mulN / mulD
			case "p":
				inP, stack = true, nil
				sb.Reset()
				begin, end, dur = "", "", ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "begin":
						begin = a.Value
					case "end":
						end = a.Value
					case "dur":
						dur = a.Value
					}
				}
			}
		case xml.CharData:
			if inP {
				sb.WriteString(string(t))
			}
		case xml.EndElement:
			if !inP {
				continue
			}
			if len(stack) == 0 {
				finish()
				continue
			}
			sb.WriteString(stack[len(stack)-1])
			stack = stack[:len(stack)-1]
		}
	}
	return cues, nil
}

// ---- writers ----

func subWrite(kind string, cues []subCue) []byte {
	var b bytes.Buffer
	switch kind {
	case "srt":
		for i, c := range cues {
			fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, subStamp(c.Start, ",", true), subStamp(c.End, ",", true))
			for _, l := range c.Lines {
				b.WriteString(l + "\n")
			}
			b.WriteString("\n")
		}
	case "vtt":
		b.WriteString("WEBVTT\n\n")
		for _, c := range cues {
			fmt.Fprintf(&b, "%s --> %s\n", subStamp(c.Start, ".", true), subStamp(c.End, ".", true))
			for _, l := range c.Lines {
				b.WriteString(subEscapeVTT(l) + "\n")
			}
			b.WriteString("\n")
		}
	case "sbv":
		for _, c := range cues {
			fmt.Fprintf(&b, "%s,%s\n", subStamp(c.Start, ".", false), subStamp(c.End, ".", false))
			for _, l := range c.Lines {
				b.WriteString(subStrip(l) + "\n")
			}
			b.WriteString("\n")
		}
	case "ass":
		b.WriteString(subASSHeader)
		for _, c := range cues {
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", subStampASS(c.Start), subStampASS(c.End), subASSEscape(c.Lines))
		}
	case "ttml":
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<tt xmlns=\"http://www.w3.org/ns/ttml\" xml:lang=\"\">\n  <body>\n    <div>\n")
		for _, c := range cues {
			fmt.Fprintf(&b, "      <p begin=\"%s\" end=\"%s\">", subStamp(c.Start, ".", true), subStamp(c.End, ".", true))
			for i, l := range c.Lines {
				if i > 0 {
					b.WriteString("<br/>")
				}
				var esc bytes.Buffer
				_ = xml.EscapeText(&esc, []byte(subStrip(l)))
				b.Write(esc.Bytes())
			}
			b.WriteString("</p>\n")
		}
		b.WriteString("    </div>\n  </body>\n</tt>\n")
	}
	return b.Bytes()
}

const subASSHeader = `[Script Info]
; Script generated by Asteria
ScriptType: v4.00+
PlayResX: 384
PlayResY: 288
ScaledBorderAndShadow: yes
WrapStyle: 0

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,1,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`

// subEscapeVTT escapes & and < (except the <i>, <b>, <u> tags we keep).
func subEscapeVTT(line string) string {
	line = strings.ReplaceAll(line, "&", "&amp;")
	line = strings.ReplaceAll(line, "-->", "--&gt;")
	var sb strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '<' {
			rest := line[i:]
			keep := false
			for _, t := range []string{"<i>", "</i>", "<b>", "</b>", "<u>", "</u>"} {
				if strings.HasPrefix(rest, t) {
					keep = true
					break
				}
			}
			if !keep {
				sb.WriteString("&lt;")
				continue
			}
		}
		sb.WriteByte(line[i])
	}
	return sb.String()
}

func subASSEscape(lines []string) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		l = strings.NewReplacer("{", `\{`, "}", `\}`).Replace(l)
		parts[i] = subTagRe.ReplaceAllStringFunc(l, func(tag string) string {
			switch n := subTagName(tag); n {
			case "i", "b", "u":
				if strings.HasPrefix(tag, "</") {
					return `{\` + n + `0}`
				}
				return `{\` + n + `1}`
			}
			return ""
		})
	}
	return strings.Join(parts, `\N`)
}
