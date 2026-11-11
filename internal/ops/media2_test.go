package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"asteria/internal/tools"
)

// ffRun runs ffmpeg directly (test inputs and measurements).
func ffRun(t *testing.T, f *tools.Finder, args ...string) string {
	t.Helper()
	bin, _ := f.Lookup("ffmpeg")
	full := append([]string{"-hide_banner", "-nostdin", "-y", "-nostats", "-loglevel", "info"}, args...)
	var stderr bytes.Buffer
	cmd := tools.Command(context.Background(), bin, full...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, stderr.String())
	}
	return stderr.String()
}

// makeClip writes a testsrc2 clip (with a sine track when audio is true).
func makeClip(t *testing.T, f *tools.Finder, name string, w, h int, dur float64, audio bool) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	args := []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=%g", w, h, dur)}
	if audio {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:sample_rate=44100:duration=%g", dur))
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	if audio {
		args = append(args, "-c:a", "aac")
	}
	ffRun(t, f, append(args, out)...)
	return out
}

func detail(t *testing.T, f *tools.Finder, path string) mediaDetail {
	t.Helper()
	d, err := probeDetail(context.Background(), f, path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	return d
}

func absFixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func within(got, want, tolFrac float64) bool {
	return got >= want*(1-tolFrac) && got <= want*(1+tolFrac)
}

var integratedRe = regexp.MustCompile(`I:\s+(-?\d+(?:\.\d+)?) LUFS`)

func integratedLoudness(t *testing.T, f *tools.Finder, path string) float64 {
	t.Helper()
	out := ffRun(t, f, "-i", path, "-af", "ebur128", "-f", "null", "-")
	m := integratedRe.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		t.Fatalf("no loudness in ffmpeg output:\n%s", out)
	}
	v, _ := strconv.ParseFloat(m[len(m)-1][1], 64)
	return v
}

func TestMedia2RequiresAbsolutePaths(t *testing.T) {
	for _, p := range []string{"", "clip.mp4", "-i", "../x.mp4"} {
		if requireAbs(p) == nil {
			t.Errorf("requireAbs(%q) should fail", p)
		}
	}
	if requireAbs(absFixture(t, "clip.mp4")) != nil {
		t.Error("absolute path rejected")
	}
	_, err := run(t, "media.speed", []string{"clip.mp4"}, "x.mp4", nil)
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Fatalf("relative input should be a user error, got %v", err)
	}
}

func TestFFFilterPathEscape(t *testing.T) {
	cases := map[string]string{
		"/a/b.srt":          "/a/b.srt",
		"C:/Users/me/a.srt": `C\\:/Users/me/a.srt`,
		"/x/it's, a:b.srt":  `/x/it\\\'s\, a\\:b.srt`,
		"/x/[a];b=c.srt":    `/x/\[a\]\;b\\=c.srt`,
	}
	for in, want := range cases {
		if got := ffFilterPathEscape(in); got != want {
			t.Errorf("ffFilterPathEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// The escaped path must survive ffmpeg's two parsing layers. The movie
// source takes its file name the same way the subtitles filter does and
// needs no libass.
func TestFFFilterPathEscapeRoundTrip(t *testing.T) {
	f := requireFFmpeg(t)
	src := makeClip(t, f, "c.mp4", 64, 64, 1, false)
	odd := filepath.Join(t.TempDir(), "it's, a:b=c [x];y.mp4")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(odd, data, 0o644); err != nil {
		t.Skipf("file system rejects the odd name: %v", err)
	}
	ffRun(t, f, "-f", "lavfi", "-i", "movie=filename="+ffFilterPathEscape(odd), "-frames:v", "1", "-f", "null", "-")
}

func TestExtractAudio(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 160, 120, 2, true)
	m4a := mustRun(t, "media.extract_audio", []string{clip}, "a.m4a", nil)
	d := detail(t, f, m4a)
	if d.first("audio") == nil || d.first("video") != nil || d.first("audio").Codec != "aac" {
		t.Errorf("m4a streams: %+v", d.Streams)
	}
	mp3 := mustRun(t, "media.extract_audio", []string{clip}, "a.mp3", nil)
	if d := detail(t, f, mp3); d.first("audio") == nil || d.first("audio").Codec != "mp3" {
		t.Errorf("mp3 streams: %+v", d.Streams)
	}
	silent := makeClip(t, f, "s.mp4", 160, 120, 1, false)
	if _, err := run(t, "media.extract_audio", []string{silent}, "s.m4a", nil); err == nil {
		t.Error("a silent video should be refused")
	}
}

func TestChangeSpeed(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 160, 120, 8, true)
	for _, c := range []struct {
		speed string
		want  float64
	}{{"2x", 4}, {"0.5x", 16}, {"4", 2}} {
		out := mustRun(t, "media.speed", []string{clip}, "s.mp4", Args{"speed": c.speed})
		if got := detail(t, f, out).Duration; !within(got, c.want, 0.05) {
			t.Errorf("speed %s: duration %.2f, want %.2f", c.speed, got, c.want)
		}
	}
	for _, bad := range []string{"8x", "0.1", "abc"} {
		if _, err := run(t, "media.speed", []string{clip}, "s.mp4", Args{"speed": bad}); err == nil {
			t.Errorf("speed %q should fail", bad)
		}
	}
	// Audio only works too.
	wav := absFixture(t, "tone.wav")
	out := mustRun(t, "media.speed", []string{wav}, "t.wav", Args{"speed": "2x"})
	if got := detail(t, f, out).Duration; !within(got, 1, 0.1) {
		t.Errorf("tone at 2x: duration %.2f, want about 1", got)
	}
	if d := detail(t, f, out); d.first("video") != nil || d.first("audio") == nil {
		t.Errorf("audio-only result has streams %+v", d.Streams)
	}
}

func TestAtempoFilterChain(t *testing.T) {
	cases := map[float64]string{
		2: "atempo=2", 1.5: "atempo=1.5", 4: "atempo=2.0,atempo=2", 0.25: "atempo=0.5,atempo=0.5",
	}
	for in, want := range cases {
		if got := atempoFilter(in); got != want {
			t.Errorf("atempoFilter(%g) = %q, want %q", in, got, want)
		}
	}
}

func TestRotateAndFlip(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 320, 180, 1, true)
	for angle, want := range map[string][2]int{"90": {180, 320}, "270": {180, 320}, "180": {320, 180}} {
		out := mustRun(t, "media.rotate", []string{clip}, "r.mp4", Args{"angle": angle})
		v := detail(t, f, out).first("video")
		if v.Width != want[0] || v.Height != want[1] {
			t.Errorf("rotate %s: %dx%d, want %dx%d", angle, v.Width, v.Height, want[0], want[1])
		}
		if detail(t, f, out).first("audio") == nil {
			t.Errorf("rotate %s lost the audio", angle)
		}
	}
	if _, err := run(t, "media.rotate", []string{clip}, "r.mp4", Args{"angle": "45"}); err == nil {
		t.Error("45 degrees should be refused")
	}
	out := mustRun(t, "media.flip", []string{clip}, "f.mp4", nil)
	if v := detail(t, f, out).first("video"); v.Width != 320 || v.Height != 180 {
		t.Errorf("flip changed size to %dx%d", v.Width, v.Height)
	}
}

func TestCropAspect(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 320, 180, 1, false)
	for _, c := range []struct {
		spec string
		w, h int
	}{{"1:1", 1, 1}, {"9:16", 9, 16}, {"16:9", 16, 9}, {"4:5", 4, 5}} {
		out := mustRun(t, "media.crop", []string{clip}, "c.mp4", Args{"aspect": c.spec})
		v := detail(t, f, out).first("video")
		if v.Width*c.h != v.Height*c.w {
			t.Errorf("crop %s gave %dx%d, not that aspect", c.spec, v.Width, v.Height)
		}
		if v.Width%2 != 0 || v.Height%2 != 0 {
			t.Errorf("crop %s gave odd size %dx%d", c.spec, v.Width, v.Height)
		}
	}
	if _, err := run(t, "media.crop", []string{clip}, "c.mp4", Args{"aspect": "wide"}); err == nil {
		t.Error("a bad aspect should fail")
	}
}

func TestResizeVideo(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 640, 360, 1, true)
	out := mustRun(t, "media.resize", []string{clip}, "r.mp4", Args{"height": "240"})
	v := detail(t, f, out).first("video")
	if v.Height != 240 || v.Width%2 != 0 || v.Width < 424 || v.Width > 428 {
		t.Errorf("resize gave %dx%d", v.Width, v.Height)
	}
	_, err := run(t, "media.resize", []string{clip}, "r.mp4", Args{"height": "720"})
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "upscale") {
		t.Errorf("upscaling should be a user error mentioning it, got %v", err)
	}
}

func TestMuteVideo(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 160, 120, 1, true)
	out := mustRun(t, "media.mute", []string{clip}, "m.mp4", nil)
	d := detail(t, f, out)
	if d.first("audio") != nil || d.first("video") == nil || d.first("video").Codec != "h264" {
		t.Errorf("mute streams: %+v", d.Streams)
	}
}

func TestNormalizeLoudness(t *testing.T) {
	f := requireFFmpeg(t)
	quiet := filepath.Join(t.TempDir(), "quiet.mp4")
	ffRun(t, f, "-f", "lavfi", "-i", "testsrc2=size=160x120:rate=25:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=12",
		"-af", "volume=-30dB", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", quiet)
	if before := integratedLoudness(t, f, quiet); before > -30 {
		t.Fatalf("test input is not quiet: %.1f LUFS", before)
	}
	out := mustRun(t, "media.normalize", []string{quiet}, "n.mp4", nil)
	if got := integratedLoudness(t, f, out); got < -17 || got > -15 {
		t.Errorf("normalized loudness %.2f LUFS, want -16 +-1", got)
	}
	d := detail(t, f, out)
	if d.first("video") == nil || d.first("video").Codec != "h264" || d.first("audio") == nil {
		t.Errorf("normalize streams: %+v", d.Streams)
	}
	// Audio-only, WAV out, keeps its sample rate.
	wav := filepath.Join(t.TempDir(), "q.wav")
	ffRun(t, f, "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=22050:duration=10", "-af", "volume=-25dB", wav)
	nw := mustRun(t, "media.normalize", []string{wav}, "n.wav", nil)
	if got := integratedLoudness(t, f, nw); got < -17 || got > -15 {
		t.Errorf("normalized wav loudness %.2f LUFS", got)
	}
	if rate := detail(t, f, nw).first("audio").SampleRate; rate != 22050 {
		t.Errorf("sample rate changed to %d", rate)
	}
	silent := filepath.Join(t.TempDir(), "silent.wav")
	ffRun(t, f, "-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "2", silent)
	if _, err := run(t, "media.normalize", []string{silent}, "n.wav", nil); err == nil {
		t.Error("a silent file should be refused")
	}
}

func TestThumbnailAndContactSheet(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 320, 180, 4, true)
	thumb := mustRun(t, "media.thumbnail", []string{clip}, "t.jpg", nil)
	cfg := jpegConfig(t, thumb)
	if cfg.Width != 320 || cfg.Height != 180 {
		t.Errorf("thumbnail is %dx%d", cfg.Width, cfg.Height)
	}
	sheet := mustRun(t, "media.contact_sheet", []string{clip}, "s.jpg", nil)
	cfg = jpegConfig(t, sheet)
	if cfg.Width != 4*320 || cfg.Height != 4*180 {
		t.Errorf("contact sheet is %dx%d, want %dx%d", cfg.Width, cfg.Height, 4*320, 4*180)
	}
}

func jpegConfig(t *testing.T, path string) (cfg struct{ Width, Height int }) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	c, err := jpeg.DecodeConfig(file)
	if err != nil {
		t.Fatalf("%s is not a JPEG: %v", path, err)
	}
	cfg.Width, cfg.Height = c.Width, c.Height
	return cfg
}

func TestExtractFrames(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 160, 120, 4, false)
	for _, fps := range []int{1, 5} {
		out := mustRun(t, "media.frames", []string{clip}, "frames", Args{"fps": fps})
		entries, err := os.ReadDir(out)
		if err != nil {
			t.Fatal(err)
		}
		want := fps * 4
		if len(entries) < want-1 || len(entries) > want+1 {
			t.Errorf("fps %d: %d frames, want %d +-1", fps, len(entries), want)
		}
		if len(entries) > 0 && entries[0].Name() != "frame-00001.png" {
			t.Errorf("first frame is %s", entries[0].Name())
		}
	}
	long := makeClip(t, f, "long.mp4", 64, 64, 120, false)
	out, err := run(t, "media.frames", []string{long}, "frames", Args{"fps": 10})
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "1000") {
		t.Errorf("over-cap extraction should be a user error naming the limit, got %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("a refused extraction left a folder behind")
	}
}

func TestJoin(t *testing.T) {
	f := requireFFmpeg(t)
	a := makeClip(t, f, "a.mp4", 320, 180, 2, true)
	b := makeClip(t, f, "b.mp4", 320, 180, 3, true)
	joined := mustRun(t, "media.join", []string{a, b}, "joined.mp4", nil)
	d := detail(t, f, joined)
	if d.Duration < 4.9 || d.Duration > 5.1 || d.first("audio") == nil {
		t.Errorf("copy join: duration %.2f, streams %+v", d.Duration, d.Streams)
	}
	// Different size and one clip without audio forces a re-encode.
	c := makeClip(t, f, "c.mp4", 200, 100, 2, false)
	mixed := mustRun(t, "media.join", []string{a, c}, "mixed.mp4", nil)
	d = detail(t, f, mixed)
	v := d.first("video")
	if d.Duration < 3.9 || d.Duration > 4.1 || v.Width != 320 || v.Height != 180 || d.first("audio") == nil {
		t.Errorf("re-encode join: duration %.2f, %dx%d, streams %+v", d.Duration, v.Width, v.Height, d.Streams)
	}
	// Audio.
	wav := absFixture(t, "tone.wav")
	aj := mustRun(t, "media.join", []string{wav, wav}, "aj.m4a", nil)
	if d := detail(t, f, aj); d.Duration < 3.9 || d.Duration > 4.1 || d.first("video") != nil {
		t.Errorf("audio join: duration %.2f, streams %+v", d.Duration, d.Streams)
	}
	// Kinds must match the skill.
	if _, err := run(t, "media.join", []string{a, wav}, "x.mp4", nil); err == nil {
		t.Error("joining a video with audio-only into a video should fail")
	}
}

const testSRT = "1\n00:00:00,200 --> 00:00:01,800\nHello subtitle\n\n"

func writeSRT(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(testSRT), 0o644); err != nil {
		t.Skipf("cannot write %q: %v", name, err)
	}
	return p
}

func TestAddSubtitles(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 160, 120, 2, true)
	srt := writeSRT(t, "s.srt")
	for _, order := range [][]string{{clip, srt}, {srt, clip}} {
		out := mustRun(t, "media.add_subtitles", order, "v.mkv", nil)
		d := detail(t, f, out)
		if d.first("subtitle") == nil || d.first("video") == nil || d.first("audio") == nil {
			t.Errorf("mkv streams: %+v", d.Streams)
		}
	}
	mp4 := mustRun(t, "media.add_subtitles", []string{clip, srt}, "v.mp4", nil)
	if d := detail(t, f, mp4); d.first("subtitle") == nil || d.first("subtitle").Codec != "mov_text" {
		t.Errorf("mp4 streams: %+v", d.Streams)
	}
	if _, err := run(t, "media.add_subtitles", []string{clip}, "v.mkv", nil); err == nil {
		t.Error("a video alone should be refused")
	}
	if _, err := run(t, "media.add_subtitles", []string{clip, clip, srt}, "v.mkv", nil); err == nil {
		t.Error("two videos should be refused")
	}
}

func hasFilter(t *testing.T, f *tools.Finder, name string) bool {
	t.Helper()
	bin, _ := f.Lookup("ffmpeg")
	out, err := tools.Command(context.Background(), bin, "-hide_banner", "-filters").Output()
	if err != nil {
		return false
	}
	return regexp.MustCompile(`(?m)^\s*\S+\s+` + regexp.QuoteMeta(name) + `\s`).Match(out)
}

func frameMD5(t *testing.T, f *tools.Finder, path string) string {
	t.Helper()
	out := ffRun(t, f, "-ss", "1", "-i", path, "-frames:v", "1", "-f", "md5", "-")
	m := regexp.MustCompile(`MD5=([0-9a-f]+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no md5 in %q", out)
	}
	return m[1]
}

func TestBurnSubtitles(t *testing.T) {
	f := requireFFmpeg(t)
	if !hasFilter(t, f, "subtitles") {
		t.Skip("this ffmpeg has no subtitles filter (libass)")
	}
	clip := makeClip(t, f, "c.mp4", 320, 180, 2, true)
	for _, name := range []string{"plain.srt", "it's, a:b.srt"} {
		srt := writeSRT(t, name)
		out := mustRun(t, "media.burn_subtitles", []string{clip, srt}, "burn.mp4", nil)
		d := detail(t, f, out)
		if d.first("subtitle") != nil || d.first("video") == nil {
			t.Errorf("%s: burned output streams %+v", name, d.Streams)
		}
		if frameMD5(t, f, out) == frameMD5(t, f, clip) {
			t.Errorf("%s: burned frame equals the source frame", name)
		}
	}
}

func TestConvertAVI(t *testing.T) {
	f := requireFFmpeg(t)
	clip := makeClip(t, f, "c.mp4", 322, 182, 1, true)
	out := mustRun(t, "media.convert_avi", []string{clip}, "o.avi", nil)
	d := detail(t, f, out)
	if v, a := d.first("video"), d.first("audio"); v == nil || a == nil || v.Codec != "mpeg4" || a.Codec != "mp3" {
		t.Errorf("avi streams: %+v", d.Streams)
	}
}
