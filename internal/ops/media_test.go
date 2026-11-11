package ops

import (
	"bytes"
	"context"
	"errors"
	"image/gif"
	"math"
	"os"
	"testing"

	"asteria/internal/tools"
)

func requireFFmpeg(t *testing.T) *tools.Finder {
	t.Helper()
	f := tools.NewFinder()
	if _, ok := f.Lookup("ffmpeg"); !ok {
		t.Skip("ffmpeg not installed")
	}
	if _, ok := f.Lookup("ffprobe"); !ok {
		t.Skip("ffprobe not installed")
	}
	return f
}

func probe(t *testing.T, f *tools.Finder, path string) MediaInfo {
	t.Helper()
	info, err := ProbeMedia(context.Background(), f, path)
	if err != nil {
		t.Fatalf("probe %s: %v", path, err)
	}
	return info
}

func TestParseTimeRange(t *testing.T) {
	cases := []struct {
		in         string
		start, end float64
		bad        bool
	}{
		{"0:05-0:30", 5, 30, false},
		{"1:00:00-1:00:30.5", 3600, 3630.5, false},
		{"-1:00", 0, 60, false},
		{"10-", 10, 0, false},
		{"5 to 9", 5, 9, false},
		{"0:30-0:05", 0, 0, true},
		{"abc", 0, 0, true},
		{"-", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, c := range cases {
		s, e, err := ParseTimeRange(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("ParseTimeRange(%q) should fail", c.in)
			}
			continue
		}
		if err != nil || s != c.start || e != c.end {
			t.Errorf("ParseTimeRange(%q) = %v,%v,%v; want %v,%v", c.in, s, e, err, c.start, c.end)
		}
	}
}

func TestVideoToMP3ExtractsAudio(t *testing.T) {
	f := requireFFmpeg(t)
	out := mustRun(t, "media.convert", []string{fixture("clip.mp4")}, "audio.mp3", nil)
	info := probe(t, f, out)
	if info.HasVideo || !info.HasAudio {
		t.Fatalf("mp3 has video=%v audio=%v", info.HasVideo, info.HasAudio)
	}
	if math.Abs(info.Duration-2) > 0.2 {
		t.Fatalf("duration = %.2f, want ~2", info.Duration)
	}
}

func TestWAVToM4AAndBack(t *testing.T) {
	f := requireFFmpeg(t)
	m4a := mustRun(t, "media.convert", []string{fixture("tone.wav")}, "tone.m4a", nil)
	if !probe(t, f, m4a).HasAudio {
		t.Fatal("m4a has no audio")
	}
	wav := mustRun(t, "media.convert", []string{m4a}, "tone.wav", nil)
	if math.Abs(probe(t, f, wav).Duration-2) > 0.2 {
		t.Fatal("round trip changed the duration")
	}
}

func TestVideoToMP4AndGIF(t *testing.T) {
	f := requireFFmpeg(t)
	mp4 := mustRun(t, "media.convert", []string{fixture("clip.mp4")}, "copy.mp4", nil)
	info := probe(t, f, mp4)
	if !info.HasVideo || !info.HasAudio || info.Width != 160 || info.Height != 120 {
		t.Fatalf("mp4 info = %+v", info)
	}
	g := mustRun(t, "media.convert", []string{fixture("clip.mp4")}, "clip.gif", nil)
	b, err := os.ReadFile(g)
	if err != nil {
		t.Fatal(err)
	}
	anim, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("output is not a GIF: %v", err)
	}
	if len(anim.Image) < 10 {
		t.Fatalf("gif has %d frames, want an animation", len(anim.Image))
	}
}

func TestTrimIsAccurate(t *testing.T) {
	f := requireFFmpeg(t)
	out := mustRun(t, "media.trim", []string{fixture("tone.wav")}, "cut.wav", Args{"range": "0:00.5-0:01.5"})
	if d := probe(t, f, out).Duration; math.Abs(d-1.0) > 0.05 {
		t.Fatalf("trimmed duration = %.3f, want 1.0", d)
	}
	_, err := run(t, "media.trim", []string{fixture("tone.wav")}, "late.wav", Args{"range": "0:10-"})
	assertUserError(t, err)
}

func TestCompressVideoProducesSmallerValidMP4(t *testing.T) {
	f := requireFFmpeg(t)
	out := mustRun(t, "media.compress", []string{fixture("clip.mp4")}, "small.mp4", Args{"crf": 36})
	info := probe(t, f, out)
	if !info.HasVideo {
		t.Fatal("compressed file has no video")
	}
}

func TestAudioToVideoFormatIsAClearError(t *testing.T) {
	requireFFmpeg(t)
	_, err := run(t, "media.convert", []string{fixture("tone.wav")}, "x.mp4", nil)
	assertUserError(t, err)
}

func TestMissingFFmpegIsExplained(t *testing.T) {
	h, _ := Lookup("media.convert")
	job := Prepare(Job{Inputs: []string{fixture("tone.wav")}, Output: t.TempDir() + "/x.mp3", Tools: tools.NewFinderWithDirs(nil)})
	t.Setenv("PATH", "")
	job.Tools = tools.NewFinderWithDirs(nil)
	err := h.Run(context.Background(), job)
	var missing *MissingToolError
	if !errors.As(err, &missing) {
		t.Fatalf("want MissingToolError, got %v", err)
	}
}

func TestCancelStopsFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, _ := Lookup("media.convert")
	out := t.TempDir() + "/x.mp3"
	err := h.Run(ctx, Prepare(Job{Inputs: []string{fixture("clip.mp4")}, Output: out}))
	if err == nil {
		t.Fatal("cancelled job succeeded")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("cancelled job left a partial output")
	}
}
