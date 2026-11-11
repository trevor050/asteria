package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const subFormats = "../../testdata/formats"

func subTempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func subReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func subParseFile(t *testing.T, p string) []subCue {
	t.Helper()
	cues, err := subParse(subKind(filepath.Ext(p)), subNormalize(subReadFile(t, p)))
	if err != nil {
		t.Fatal(err)
	}
	return cues
}

func TestSubtitlesSRTVTTRoundTripIsExact(t *testing.T) {
	// BOM, CRLF, missing final newline, overlapping cues, tags, ampersand.
	src := "\ufeff1\r\n00:00:01,000 --> 00:00:03,500\r\nAsteria <i>matrix</i> check\r\nTom & Jerry\r\n\r\n" +
		"2\r\n00:00:03,000 --> 00:00:05,125\r\nOverlaps the first cue\r\n\r\n" +
		"3\r\n01:02:03,999 --> 01:02:04,001\r\nLast one"
	srt := subTempFile(t, "in.srt", src)
	vtt := mustRun(t, "subtitles.convert", []string{srt}, "out.vtt", nil)
	wantVTT := "WEBVTT\n\n" +
		"00:00:01.000 --> 00:00:03.500\nAsteria <i>matrix</i> check\nTom &amp; Jerry\n\n" +
		"00:00:03.000 --> 00:00:05.125\nOverlaps the first cue\n\n" +
		"01:02:03.999 --> 01:02:04.001\nLast one\n\n"
	if got := subReadFile(t, vtt); got != wantVTT {
		t.Fatalf("vtt:\n%q\nwant\n%q", got, wantVTT)
	}
	back := mustRun(t, "subtitles.convert", []string{vtt}, "back.srt", nil)
	wantSRT := "1\n00:00:01,000 --> 00:00:03,500\nAsteria <i>matrix</i> check\nTom & Jerry\n\n" +
		"2\n00:00:03,000 --> 00:00:05,125\nOverlaps the first cue\n\n" +
		"3\n01:02:03,999 --> 01:02:04,001\nLast one\n\n"
	if got := subReadFile(t, back); got != wantSRT {
		t.Fatalf("srt:\n%q\nwant\n%q", got, wantSRT)
	}
	cues := subParseFile(t, back)
	if cues[2].Start != time.Hour+2*time.Minute+3*time.Second+999*time.Millisecond {
		t.Fatalf("start = %v", cues[2].Start)
	}
}

func TestSubtitlesVTTHeadersAndBlocksSkipped(t *testing.T) {
	src := "WEBVTT - title\nKind: captions\n\nSTYLE\n::cue { color: red }\n\nNOTE this is a comment\n00:00:09.000 --> 00:00:09.500\n\n" +
		"intro\n00:00:01.5 --> 00:00:02.25 align:start position:10%\n<v Bob>Hello <c.yellow>there</c></v>&nbsp;&lt;3\n\n" +
		"00:03.000 --> 00:04.000\nShort timestamp"
	cues := subParseFile(t, subTempFile(t, "x.vtt", src))
	if len(cues) != 2 {
		t.Fatalf("got %d cues: %+v", len(cues), cues)
	}
	if cues[0].Start != 1500*time.Millisecond || cues[0].End != 2250*time.Millisecond {
		t.Fatalf("times %v %v", cues[0].Start, cues[0].End)
	}
	if got := strings.Join(cues[0].Lines, "|"); got != "Hello there <3" {
		t.Fatalf("text %q", got)
	}
	if cues[1].Start != 3*time.Second || cues[1].Lines[0] != "Short timestamp" {
		t.Fatalf("cue 2 %+v", cues[1])
	}
}

func TestSubtitlesSRTWithoutNumbersAndDotMillis(t *testing.T) {
	src := "00:00:01.5 --> 00:00:02.0\nno number here\n\n\n\n00:00:03,250 --> 00:00:04,000\n<font color=\"red\">red</font> {\\an8}top\n"
	cues := subParseFile(t, subTempFile(t, "x.srt", src))
	if len(cues) != 2 || cues[0].Start != 1500*time.Millisecond || cues[1].Start != 3250*time.Millisecond {
		t.Fatalf("%+v", cues)
	}
	if cues[1].Lines[0] != "red top" {
		t.Fatalf("text %q", cues[1].Lines[0])
	}
}

func TestSubtitlesASSToSRTStripsOverrideTags(t *testing.T) {
	src := "[Script Info]\nTitle: x\n\n[V4+ Styles]\nFormat: Name\nStyle: Default\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Comment: 0,0:00:00.00,0:00:01.00,Default,,0,0,0,,not a cue\n" +
		"Dialogue: 0,0:00:01.50,0:00:03.07,Default,,0,0,0,,{\\an8}{\\pos(10,20)\\fs40}Hello {\\i1}world{\\i0}\\Nsecond, line with, commas\n" +
		"Dialogue: 0,0:00:00.50,0:00:01.00,Default,,0,0,0,,{\\b1}bold to the end\n"
	out := mustRun(t, "subtitles.convert", []string{subTempFile(t, "in.ass", src)}, "out.srt", nil)
	want := "1\n00:00:00,500 --> 00:00:01,000\n<b>bold to the end</b>\n\n" +
		"2\n00:00:01,500 --> 00:00:03,070\nHello <i>world</i>\nsecond, line with, commas\n\n"
	if got := subReadFile(t, out); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if strings.Contains(subReadFile(t, out), "{") {
		t.Fatal("override block leaked")
	}
}

func TestSubtitlesASSWriter(t *testing.T) {
	srt := subTempFile(t, "in.srt", "1\n00:00:01,000 --> 00:00:03,500\nAsteria <i>matrix</i> check\nsecond {line}\n\n")
	out := subReadFile(t, mustRun(t, "subtitles.convert", []string{srt}, "out.ass", nil))
	for _, want := range []string{"[Script Info]", "[V4+ Styles]", "[Events]", "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
		`Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,Asteria {\i1}matrix{\i0} check\Nsecond \{line\}`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	// and it reads back
	cues := subParseFile(t, subTempFile(t, "back.ass", out))
	if len(cues) != 1 || cues[0].Lines[0] != "Asteria <i>matrix</i> check" {
		t.Fatalf("%+v", cues)
	}
}

func TestSubtitlesTTMLTimeForms(t *testing.T) {
	src := `<?xml version="1.0" encoding="utf-8"?>
<tt xmlns="http://www.w3.org/ns/ttml" xmlns:ttp="http://www.w3.org/ns/ttml#parameter" xmlns:tts="http://www.w3.org/ns/ttml#styling" ttp:frameRate="30" ttp:tickRate="10000000">
 <body><div>
  <p begin="00:00:01.500" end="00:00:03.000">clock time</p>
  <p begin="1.5s" dur="2s">offset seconds and <br/> a break with <span tts:fontStyle="italic">italic</span> &amp; more</p>
  <p begin="45f" end="90f">frames</p>
  <p begin="1500ms" end="2m">millis</p>
  <p begin="00:00:01:15" end="00:00:02:00">clock with frames</p>
  <p begin="15000000t" end="30000000t">ticks</p>
  <p end="5s">no begin: skipped</p>
 </div></body>
</tt>`
	cues, err := subParseTTML(src)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		s, e time.Duration
		text string
	}
	ms := time.Millisecond
	wants := []want{
		{1500 * ms, 3000 * ms, "clock time"},
		{1500 * ms, 3500 * ms, "offset seconds and|a break with <i>italic</i> & more"},
		{1500 * ms, 3000 * ms, "frames"},
		{1500 * ms, 2 * time.Minute, "millis"},
		{1500 * ms, 2000 * ms, "clock with frames"},
		{1500 * ms, 3000 * ms, "ticks"},
	}
	if len(cues) != len(wants) {
		t.Fatalf("got %d cues: %+v", len(cues), cues)
	}
	for i, w := range wants {
		c := cues[i]
		if c.Start != w.s || c.End != w.e || strings.Join(c.Lines, "|") != w.text {
			t.Errorf("cue %d: got %v-%v %q, want %v-%v %q", i, c.Start, c.End, strings.Join(c.Lines, "|"), w.s, w.e, w.text)
		}
	}
}

func TestSubtitlesTTMLWriterEscapesAndBreaks(t *testing.T) {
	srt := subTempFile(t, "in.srt", "1\n00:00:01,000 --> 00:00:03,500\nA <i>&</i> B\nline two <3\n\n")
	out := subReadFile(t, mustRun(t, "subtitles.convert", []string{srt}, "out.ttml", nil))
	if !strings.Contains(out, `<p begin="00:00:01.000" end="00:00:03.500">A &amp; B<br/>line two &lt;3</p>`) {
		t.Fatalf("got\n%s", out)
	}
	cues := subParseFile(t, subTempFile(t, "back.ttml", out))
	if len(cues) != 1 || cues[0].Lines[1] != "line two <3" {
		t.Fatalf("%+v", cues)
	}
}

func TestSubtitlesSBV(t *testing.T) {
	srt := subTempFile(t, "in.srt", "1\n00:00:01,000 --> 00:00:03,500\n<i>Hello</i>\n\n2\n01:00:00,000 --> 01:00:01,250\nHour mark\n\n")
	sbv := mustRun(t, "subtitles.convert", []string{srt}, "out.sbv", nil)
	want := "0:00:01.000,0:00:03.500\nHello\n\n1:00:00.000,1:00:01.250\nHour mark\n\n"
	if got := subReadFile(t, sbv); got != want {
		t.Fatalf("got %q", got)
	}
	back := mustRun(t, "subtitles.convert", []string{sbv}, "back.srt", nil)
	if got := subReadFile(t, back); !strings.Contains(got, "01:00:00,000 --> 01:00:01,250\nHour mark") {
		t.Fatalf("got %q", got)
	}
}

func TestSubtitlesFormatMatrixFromFixtures(t *testing.T) {
	exts := []string{".srt", ".vtt", ".ass", ".sbv", ".ttml"}
	for _, from := range exts {
		for _, to := range exts {
			if from == to {
				continue
			}
			in := filepath.Join(subFormats, "sample"+from)
			out := mustRun(t, "subtitles.convert", []string{in}, "out"+to, nil)
			cues := subParseFile(t, out)
			if len(cues) != 2 {
				t.Fatalf("%s -> %s: %d cues", from, to, len(cues))
			}
			if cues[0].Start != time.Second || cues[0].End != 3500*time.Millisecond || strings.Join(cues[0].Lines, "") != "Asteria matrix check" {
				t.Errorf("%s -> %s: first cue %+v", from, to, cues[0])
			}
			if cues[1].Start != 4*time.Second || cues[1].End != 6250*time.Millisecond {
				t.Errorf("%s -> %s: second cue %+v", from, to, cues[1])
			}
		}
	}
}

func TestSubtitlesShift(t *testing.T) {
	src := "1\n00:00:00,300 --> 00:00:01,000\nbefore zero\n\n2\n00:00:00,800 --> 00:00:02,000\nclamped\n\n3\n00:01:00,000 --> 00:01:01,500\nlater\n\n"
	srt := subTempFile(t, "in.srt", src)
	out := mustRun(t, "subtitles.shift", []string{srt}, "out.srt", Args{"offset": "-500ms"})
	// cue 1 ends at 500 ms (kept, start clamped to 0)
	want := "1\n00:00:00,000 --> 00:00:00,500\nbefore zero\n\n2\n00:00:00,300 --> 00:00:01,500\nclamped\n\n3\n00:00:59,500 --> 00:01:01,000\nlater\n\n"
	if got := subReadFile(t, out); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// cues ending before 0 are dropped
	out = mustRun(t, "subtitles.shift", []string{srt}, "out2.srt", Args{"offset": "-1.5s"})
	if got := subReadFile(t, out); !strings.HasPrefix(got, "1\n00:00:00,000 --> 00:00:00,500\nclamped") || strings.Contains(got, "before zero") {
		t.Fatalf("got %q", got)
	}
	// colon form, default and bare numbers
	for offset, want := range map[string]time.Duration{
		"-0:02.3": -2300 * time.Millisecond, "+1.5s": 1500 * time.Millisecond, "1": time.Second,
		"-500ms": -500 * time.Millisecond, "2m": 2 * time.Minute, "+1:02:03.5": time.Hour + 2*time.Minute + 3500*time.Millisecond,
	} {
		got, err := subParseOffset(offset)
		if err != nil || got != want {
			t.Errorf("offset %q = %v, %v; want %v", offset, got, err, want)
		}
	}
	if _, err := subParseOffset("soon"); err == nil {
		t.Error("expected error for 'soon'")
	}
	def := mustRun(t, "subtitles.shift", []string{srt}, "def.srt", nil)
	if got := subReadFile(t, def); !strings.Contains(got, "00:01:01,000 --> 00:01:02,500") {
		t.Fatalf("default +1s: %q", got)
	}
}

func TestSubtitlesShiftKeepsFormat(t *testing.T) {
	out := mustRun(t, "subtitles.shift", []string{filepath.Join(subFormats, "sample.vtt")}, "out.vtt", Args{"offset": "+2s"})
	got := subReadFile(t, out)
	if !strings.HasPrefix(got, "WEBVTT\n\n00:00:03.000 --> 00:00:05.500\nAsteria matrix check") {
		t.Fatalf("got %q", got)
	}
}

func TestSubtitlesRetimeFramerate(t *testing.T) {
	src := "1\n00:00:10,000 --> 00:01:00,000\na\n\n2\n01:00:00,000 --> 01:00:01,000\nb\n\n"
	srt := subTempFile(t, "in.srt", src)
	out := mustRun(t, "subtitles.retime", []string{srt}, "out.srt", Args{"speed": "23.976 25"})
	// t * 23.976 / 25: 10 s -> 9.5904 s, 60 s -> 57.5424 s, 3600 s -> 3452.544 s, 3601 s -> 3453.50304 s
	want := "1\n00:00:09,590 --> 00:00:57,542\na\n\n2\n00:57:32,544 --> 00:57:33,503\nb\n\n"
	if got := subReadFile(t, out); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// in the other direction and as a plain factor
	out = mustRun(t, "subtitles.retime", []string{srt}, "out2.srt", Args{"speed": "25 → 23.976"})
	if got := subReadFile(t, out); !strings.HasPrefix(got, "1\n00:00:10,427 --> 00:01:02,563") {
		t.Fatalf("got %q", got)
	}
	out = mustRun(t, "subtitles.retime", []string{srt}, "out3.srt", Args{"speed": "1.001"})
	if got := subReadFile(t, out); !strings.HasPrefix(got, "1\n00:00:10,010 --> 00:01:00,060") {
		t.Fatalf("got %q", got)
	}
	// exact math on the model, no rounding to ms involved
	cues := subScaleCues([]subCue{{Start: 3600 * time.Second, End: 3601 * time.Second}}, 23.976/25)
	if cues[0].Start != 3452544*time.Millisecond {
		t.Fatalf("start %v", cues[0].Start)
	}
	if _, err := subParseFactor("fast"); err == nil {
		t.Error("expected error")
	}
	if _, err := subParseFactor("0 25"); err == nil {
		t.Error("expected error for zero fps")
	}
	// default is 23.976 -> 25
	def := mustRun(t, "subtitles.retime", []string{srt}, "def.srt", nil)
	if got := subReadFile(t, def); !strings.Contains(got, "00:00:09,590") {
		t.Fatalf("default: %q", got)
	}
}

func TestSubtitlesLatin1AndUTF16Input(t *testing.T) {
	// Windows-1252 SRT with an accented letter and curly quotes.
	cp := subTempFile(t, "in.srt", "1\n00:00:01,000 --> 00:00:02,000\ncaf\xe9 \x93quote\x94\n\n")
	out := mustRun(t, "subtitles.convert", []string{cp}, "out.vtt", nil)
	if got := subReadFile(t, out); !strings.Contains(got, "café “quote”") {
		t.Fatalf("got %q", got)
	}
	// UTF-16LE with BOM.
	var b []byte
	b = append(b, 0xFF, 0xFE)
	for _, r := range "1\n00:00:01,000 --> 00:00:02,000\nhi\n\n" {
		b = append(b, byte(r), byte(r>>8))
	}
	u16 := subTempFile(t, "u16.srt", string(b))
	out = mustRun(t, "subtitles.convert", []string{u16}, "out2.vtt", nil)
	if got := subReadFile(t, out); !strings.Contains(got, "00:00:01.000 --> 00:00:02.000\nhi\n") {
		t.Fatalf("got %q", got)
	}
}

func TestSubtitlesEmptyFileIsUserError(t *testing.T) {
	_, err := run(t, "subtitles.convert", []string{subTempFile(t, "e.srt", "just some words\n")}, "o.vtt", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
