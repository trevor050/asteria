package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"asteria/internal/tools"
)

// Audio and video extensions Asteria hands to ffmpeg.
var (
	VideoExts = []string{".mp4", ".mov", ".m4v", ".mkv", ".webm", ".avi", ".wmv", ".flv", ".mpg", ".mpeg", ".3gp"}
	AudioExts = []string{".mp3", ".wav", ".m4a", ".aac", ".flac", ".ogg", ".opus", ".wma", ".aiff", ".aif"}
)

func init() {
	for _, h := range []Handler{
		{Name: "media.convert", Run: mediaConvert},
		{Name: "media.compress", Run: mediaCompress},
		{Name: "media.trim", Run: mediaTrim},
	} {
		h.Requires = []string{"ffmpeg"}
		h.Concurrency = 1
		register(h)
	}
}

// MissingToolError is returned when a required external program is absent.
type MissingToolError struct{ Tool string }

func (e *MissingToolError) Error() string {
	return fmt.Sprintf("%s is not installed. Install it with: %s", e.Tool, tools.InstallHint(e.Tool))
}

// MediaInfo is the subset of ffprobe output Asteria uses.
type MediaInfo struct {
	Duration float64
	Width    int
	Height   int
	HasVideo bool
	HasAudio bool
}

// ProbeMedia runs ffprobe on path. It returns an error if ffprobe is missing.
func ProbeMedia(ctx context.Context, finder *tools.Finder, path string) (MediaInfo, error) {
	probe, ok := finder.Lookup("ffprobe")
	if !ok {
		return MediaInfo{}, &MissingToolError{Tool: "ffprobe"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, probe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return MediaInfo{}, Userf("%s is not a media file ffmpeg can read", filepath.Base(path))
	}
	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType   string `json:"codec_type"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return MediaInfo{}, err
	}
	info := MediaInfo{}
	info.Duration, _ = strconv.ParseFloat(parsed.Format.Duration, 64)
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			if s.Disposition.AttachedPic == 1 {
				continue // album art in an MP3 is not a video
			}
			info.HasVideo = true
			if info.Width == 0 {
				info.Width, info.Height = s.Width, s.Height
			}
		case "audio":
			info.HasAudio = true
		}
	}
	return info, nil
}

// encodeArgs returns ffmpeg output arguments for the given extension.
func encodeArgs(ext string, info MediaInfo, crf int) ([]string, error) {
	evenScale := "scale=trunc(iw/2)*2:trunc(ih/2)*2"
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v", ".mov", ".mkv":
		if !info.HasVideo {
			return nil, Userf("this file has no video track; convert it to an audio format instead")
		}
		args := []string{"-map", "0:v:0", "-c:v", "libx264", "-preset", "medium", "-crf", strconv.Itoa(crf), "-pix_fmt", "yuv420p", "-vf", evenScale}
		if info.HasAudio {
			args = append(args, "-map", "0:a:0?", "-c:a", "aac", "-b:a", "160k")
		}
		if ext != ".mkv" {
			args = append(args, "-movflags", "+faststart")
		}
		return args, nil
	case ".webm":
		if !info.HasVideo {
			return nil, Userf("this file has no video track")
		}
		args := []string{"-map", "0:v:0", "-c:v", "libvpx-vp9", "-crf", strconv.Itoa(crf + 8), "-b:v", "0", "-row-mt", "1", "-vf", evenScale}
		if info.HasAudio {
			args = append(args, "-map", "0:a:0?", "-c:a", "libopus", "-b:a", "128k")
		}
		return args, nil
	case ".gif":
		if !info.HasVideo {
			return nil, Userf("this file has no video track")
		}
		return []string{"-an", "-vf", "fps=12,scale='min(640,iw)':-2:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4", "-loop", "0"}, nil
	}
	if !info.HasAudio {
		return nil, Userf("this file has no audio track")
	}
	switch strings.ToLower(ext) {
	case ".mp3":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "libmp3lame", "-q:a", "2"}, nil
	case ".m4a", ".aac":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "aac", "-b:a", "192k"}, nil
	case ".wav":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "pcm_s16le"}, nil
	case ".flac":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "flac"}, nil
	case ".ogg":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "libvorbis", "-q:a", "5"}, nil
	case ".opus":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "libopus", "-b:a", "128k"}, nil
	case ".aiff", ".aif":
		return []string{"-vn", "-map", "0:a:0", "-c:a", "pcm_s16be"}, nil
	}
	return nil, Userf("cannot write %s files", ext)
}

// runFFmpeg runs ffmpeg with the given arguments, reporting progress against
// duration (seconds) and returning a readable error with ffmpeg's last lines.
func runFFmpeg(ctx context.Context, job Job, duration float64, args []string) error {
	bin, ok := job.Tools.Lookup("ffmpeg")
	if !ok {
		return &MissingToolError{Tool: "ffmpeg"}
	}
	full := append([]string{"-hide_banner", "-nostdin", "-y", "-progress", "pipe:1", "-nostats", "-loglevel", "error"}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if duration > 0 && strings.HasPrefix(line, "out_time_us=") {
			us, err := strconv.ParseFloat(strings.TrimPrefix(line, "out_time_us="), 64)
			if err == nil && us > 0 {
				f := us / 1e6 / duration
				if f > 0.99 {
					f = 0.99
				}
				job.Progress(f)
			}
		}
	}
	err = cmd.Wait()
	if err != nil {
		_ = os.Remove(job.Output)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if len(lines) > 4 {
			lines = lines[len(lines)-4:]
		}
		msg := strings.TrimSpace(strings.Join(lines, " "))
		if msg == "" {
			msg = err.Error()
		}
		return Userf("ffmpeg failed: %s", msg)
	}
	if _, err := os.Stat(job.Output); err != nil {
		return errors.New("ffmpeg finished without writing output")
	}
	return nil
}

func mediaConvert(ctx context.Context, job Job) error {
	info, err := ProbeMedia(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	crf, err := job.Args.IntInRange("crf", 23, 0, 51)
	if err != nil {
		return err
	}
	enc, err := encodeArgs(filepath.Ext(job.Output), info, crf)
	if err != nil {
		return err
	}
	args := append([]string{"-i", job.Input()}, enc...)
	return runFFmpeg(ctx, job, info.Duration, append(args, job.Output))
}

func mediaCompress(ctx context.Context, job Job) error {
	info, err := ProbeMedia(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	if !info.HasVideo {
		return Userf("compress video needs a video file")
	}
	crf, err := job.Args.IntInRange("crf", 28, 18, 40)
	if err != nil {
		return err
	}
	enc, err := encodeArgs(filepath.Ext(job.Output), info, crf)
	if err != nil {
		return err
	}
	args := append([]string{"-i", job.Input()}, enc...)
	return runFFmpeg(ctx, job, info.Duration, append(args, job.Output))
}

// ParseTimeRange parses "0:05-0:30", "-1:00" (first minute) or "1:00-"
// (from 1:00 to the end) into start and end seconds. end is 0 for "to the end".
func ParseTimeRange(spec string) (start, end float64, err error) {
	s := strings.TrimSpace(spec)
	s = strings.ReplaceAll(s, "–", "-")
	s = strings.ReplaceAll(s, " to ", "-")
	if s == "" {
		return 0, 0, Userf("enter a range such as 0:05-0:30")
	}
	i := strings.Index(s, "-")
	if i < 0 {
		return 0, 0, Userf("%q is not a range; try 0:05-0:30", spec)
	}
	lo, hi := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	if lo != "" {
		if start, err = parseClock(lo); err != nil {
			return 0, 0, err
		}
	}
	if hi != "" {
		if end, err = parseClock(hi); err != nil {
			return 0, 0, err
		}
		if end <= start {
			return 0, 0, Userf("the end (%s) must be after the start", hi)
		}
	}
	if lo == "" && hi == "" {
		return 0, 0, Userf("enter a range such as 0:05-0:30")
	}
	return start, end, nil
}

func parseClock(s string) (float64, error) {
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, Userf("%q is not a time", s)
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(p), "s"), 64)
		if err != nil || v < 0 {
			return 0, Userf("%q is not a time", s)
		}
		total = total*60 + v
	}
	return total, nil
}

func mediaTrim(ctx context.Context, job Job) error {
	info, err := ProbeMedia(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	start, end, err := ParseTimeRange(job.Args.String("range", ""))
	if err != nil {
		return err
	}
	if info.Duration > 0 && start >= info.Duration {
		return Userf("the start is past the end of the file (%s long)", FormatDuration(info.Duration))
	}
	if end == 0 || (info.Duration > 0 && end > info.Duration) {
		end = info.Duration
	}
	enc, err := encodeArgs(filepath.Ext(job.Output), info, 20)
	if err != nil {
		return err
	}
	args := []string{"-ss", fmt.Sprintf("%.3f", start), "-i", job.Input()}
	length := end - start
	if length > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", length))
	}
	args = append(args, enc...)
	return runFFmpeg(ctx, job, length, append(args, job.Output))
}

// FormatDuration renders seconds as m:ss or h:mm:ss.
func FormatDuration(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(sec + 0.5)
	h, m, s := t/3600, (t%3600)/60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
