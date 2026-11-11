package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"asteria/internal/tools"
)

// More audio and video operations built on ffmpeg: extracting audio, speed,
// rotation, cropping, resizing, muting, loudness, stills, joining and
// subtitles. Every argv uses absolute input and output paths (ffmpeg has no
// "--"), so a path can never be read as an option.

func init() {
	for _, h := range []Handler{
		{Name: "media.extract_audio", Run: mediaExtractAudio},
		{Name: "media.speed", Run: mediaSpeed},
		{Name: "media.rotate", Run: mediaRotate},
		{Name: "media.flip", Run: mediaFlip},
		{Name: "media.crop", Run: mediaCrop},
		{Name: "media.resize", Run: mediaResize},
		{Name: "media.mute", Run: mediaMute},
		{Name: "media.normalize", Run: mediaNormalize},
		{Name: "media.thumbnail", Run: mediaThumbnail},
		{Name: "media.contact_sheet", Run: mediaContactSheet},
		{Name: "media.frames", Run: mediaFrames},
		{Name: "media.convert_avi", Run: mediaConvertAVI},
		{Name: "media.join", Run: mediaJoin, Combine: true},
		{Name: "media.add_subtitles", Run: mediaAddSubtitles, Combine: true},
		{Name: "media.burn_subtitles", Run: mediaBurnSubtitles, Combine: true},
	} {
		h.Requires = []string{"ffmpeg", "ffprobe"}
		h.Concurrency = 1
		register(h)
	}
}

// ---------------------------------------------------------------- probing

type streamDetail struct {
	Type       string
	Codec      string
	PixFmt     string
	FrameRate  string
	Width      int
	Height     int
	SampleRate int
	Channels   int
}

type mediaDetail struct {
	Duration float64
	Streams  []streamDetail
}

func (d mediaDetail) first(kind string) *streamDetail {
	for i := range d.Streams {
		if d.Streams[i].Type == kind {
			return &d.Streams[i]
		}
	}
	return nil
}

func (d mediaDetail) info() MediaInfo {
	mi := MediaInfo{Duration: d.Duration}
	if v := d.first("video"); v != nil {
		mi.HasVideo, mi.Width, mi.Height = true, v.Width, v.Height
	}
	mi.HasAudio = d.first("audio") != nil
	return mi
}

func probeDetail(ctx context.Context, finder *tools.Finder, path string) (mediaDetail, error) {
	if err := requireAbs(path); err != nil {
		return mediaDetail{}, err
	}
	probeBin, ok := finder.Lookup("ffprobe")
	if !ok {
		return mediaDetail{}, &MissingToolError{Tool: "ffprobe"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := tools.Command(ctx, probeBin, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return mediaDetail{}, Userf("%s is not a media file ffmpeg can read", filepath.Base(path))
	}
	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			PixFmt       string `json:"pix_fmt"`
			RFrameRate   string `json:"r_frame_rate"`
			AvgFrameRate string `json:"avg_frame_rate"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			SampleRate   string `json:"sample_rate"`
			Channels     int    `json:"channels"`
			Disposition  struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return mediaDetail{}, err
	}
	d := mediaDetail{}
	d.Duration, _ = strconv.ParseFloat(parsed.Format.Duration, 64)
	for _, s := range parsed.Streams {
		if s.CodecType == "video" && s.Disposition.AttachedPic == 1 {
			continue // album art is not a video
		}
		sr, _ := strconv.Atoi(s.SampleRate)
		rate := s.AvgFrameRate
		if !validRate(rate) {
			rate = s.RFrameRate
		}
		d.Streams = append(d.Streams, streamDetail{
			Type: s.CodecType, Codec: s.CodecName, PixFmt: s.PixFmt, FrameRate: rate,
			Width: s.Width, Height: s.Height, SampleRate: sr, Channels: s.Channels,
		})
	}
	return d, nil
}

var rateRe = regexp.MustCompile(`^\d+(/\d+)?$`)

func validRate(r string) bool {
	if !rateRe.MatchString(r) {
		return false
	}
	return r != "0/0" && !strings.HasPrefix(r, "0/") && r != "0"
}

// requireAbs rejects any path ffmpeg could mistake for an option or a
// relative reference.
func requireAbs(paths ...string) error {
	for _, p := range paths {
		if p == "" || !filepath.IsAbs(p) {
			return Userf("internal error: %q is not an absolute path", p)
		}
	}
	return nil
}

func fnum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ---------------------------------------------------------------- encoders

// vArgs returns video encoder arguments for a container extension.
func vArgs(ext string, crf int) ([]string, bool) {
	c := strconv.Itoa(crf)
	switch ext {
	case ".mp4", ".m4v", ".mov", ".mkv", ".flv", ".3gp":
		return []string{"-c:v", "libx264", "-preset", "medium", "-crf", c, "-pix_fmt", "yuv420p"}, true
	case ".webm":
		return []string{"-c:v", "libvpx-vp9", "-crf", strconv.Itoa(crf + 8), "-b:v", "0", "-row-mt", "1", "-pix_fmt", "yuv420p"}, true
	case ".avi":
		return []string{"-c:v", "mpeg4", "-q:v", "4", "-pix_fmt", "yuv420p"}, true
	case ".wmv":
		return []string{"-c:v", "wmv2", "-q:v", "4", "-pix_fmt", "yuv420p"}, true
	case ".mpg", ".mpeg":
		return []string{"-c:v", "mpeg2video", "-q:v", "3", "-pix_fmt", "yuv420p"}, true
	}
	return nil, false
}

// aArgs returns audio encoder arguments in the codec family of a container.
func aArgs(ext string) ([]string, bool) {
	switch ext {
	case ".mp4", ".m4v", ".mov", ".mkv", ".flv", ".3gp":
		return []string{"-c:a", "aac", "-b:a", "160k"}, true
	case ".m4a", ".aac":
		return []string{"-c:a", "aac", "-b:a", "192k"}, true
	case ".webm", ".opus":
		return []string{"-c:a", "libopus", "-b:a", "128k"}, true
	case ".avi":
		return []string{"-c:a", "libmp3lame", "-q:a", "4"}, true
	case ".mp3":
		return []string{"-c:a", "libmp3lame", "-q:a", "2"}, true
	case ".wmv", ".wma":
		return []string{"-c:a", "wmav2", "-b:a", "160k"}, true
	case ".mpg", ".mpeg":
		return []string{"-c:a", "mp2", "-b:a", "192k"}, true
	case ".wav":
		return []string{"-c:a", "pcm_s16le"}, true
	case ".flac":
		return []string{"-c:a", "flac"}, true
	case ".ogg":
		return []string{"-c:a", "libvorbis", "-q:a", "5"}, true
	case ".aiff", ".aif":
		return []string{"-c:a", "pcm_s16be"}, true
	}
	return nil, false
}

func containerArgs(ext string) []string {
	switch ext {
	case ".mp4", ".m4v", ".mov", ".3gp":
		return []string{"-movflags", "+faststart"}
	}
	return nil
}

type encOpts struct {
	CRF       int
	CopyVideo bool
	CopyAudio bool
}

// mediaCodecArgs maps the first video and audio stream into the container
// implied by ext, re-encoding (or copying) each as asked. Filters are the
// caller's business.
func mediaCodecArgs(ext string, info MediaInfo, o encOpts) ([]string, error) {
	ext = strings.ToLower(ext)
	if o.CRF == 0 {
		o.CRF = 20
	}
	if _, isVideo := vArgs(ext, o.CRF); isVideo {
		if !info.HasVideo {
			return nil, Userf("this file has no video track")
		}
		args := []string{"-map", "0:v:0"}
		if o.CopyVideo {
			args = append(args, "-c:v", "copy")
		} else {
			v, _ := vArgs(ext, o.CRF)
			args = append(args, v...)
		}
		if info.HasAudio {
			args = append(args, "-map", "0:a:0")
			if o.CopyAudio {
				args = append(args, "-c:a", "copy")
			} else if a, ok := aArgs(ext); ok {
				args = append(args, a...)
			}
		}
		return append(args, containerArgs(ext)...), nil
	}
	a, ok := aArgs(ext)
	if !ok {
		return nil, Userf("cannot write %s files", ext)
	}
	if !info.HasAudio {
		return nil, Userf("this file has no audio track")
	}
	return append([]string{"-vn", "-map", "0:a:0"}, a...), nil
}

func outExt(job Job) string { return strings.ToLower(filepath.Ext(job.Output)) }

// runIn runs ffmpeg for one input: -i input, extra pre-output args, output.
func runIn(ctx context.Context, job Job, duration float64, args ...string) error {
	return runFFmpeg(ctx, job, duration, args)
}

// ---------------------------------------------------------------- extract audio

func mediaExtractAudio(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	a := d.first("audio")
	if a == nil {
		return Userf("%s has no audio track", job.Name(0))
	}
	var enc []string
	switch outExt(job) {
	case ".m4a":
		if a.Codec == "aac" {
			enc = []string{"-c:a", "copy"}
		} else {
			enc = []string{"-c:a", "aac", "-b:a", "192k"}
		}
	case ".mp3":
		if a.Codec == "mp3" {
			enc = []string{"-c:a", "copy"}
		} else {
			enc = []string{"-c:a", "libmp3lame", "-q:a", "2"}
		}
	default:
		return Userf("extract audio writes .m4a or .mp3")
	}
	args := append([]string{"-i", job.Input(), "-vn", "-map", "0:a:0"}, enc...)
	return runIn(ctx, job, d.Duration, append(args, job.Output)...)
}

// ---------------------------------------------------------------- speed

func atempoFilter(s float64) string {
	var parts []string
	for s > 2+1e-9 {
		parts = append(parts, "atempo=2.0")
		s /= 2
	}
	for s < 0.5-1e-9 {
		parts = append(parts, "atempo=0.5")
		s *= 2
	}
	parts = append(parts, "atempo="+fnum(s))
	return strings.Join(parts, ",")
}

func mediaSpeed(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	speed, err := job.Args.Float("speed", 2)
	if err != nil {
		return err
	}
	if speed < 0.25 || speed > 4 {
		return Userf("speed must be between 0.25x and 4x (got %sx)", fnum(speed))
	}
	speed = math.Round(speed*1000) / 1000
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	info := d.info()
	enc, err := mediaCodecArgs(outExt(job), info, encOpts{CRF: 20})
	if err != nil {
		return err
	}
	args := []string{"-i", job.Input()}
	if info.HasVideo {
		args = append(args, "-vf", "setpts=PTS/"+fnum(speed))
	}
	if info.HasAudio {
		args = append(args, "-af", atempoFilter(speed))
	}
	args = append(args, enc...)
	return runIn(ctx, job, d.Duration/speed, append(args, job.Output)...)
}

// ---------------------------------------------------------------- rotate and flip

func mediaRotate(ctx context.Context, job Job) error {
	deg, err := job.Args.Float("angle", 90)
	if err != nil {
		return err
	}
	norm := math.Mod(math.Mod(deg, 360)+360, 360)
	var vf string
	switch norm {
	case 90:
		vf = "transpose=1"
	case 180:
		vf = "hflip,vflip"
	case 270:
		vf = "transpose=2"
	default:
		return Userf("angle must be 90, 180 or 270 (got %s)", fnum(deg))
	}
	return videoFilter(ctx, job, vf, nil)
}

func mediaFlip(ctx context.Context, job Job) error {
	return videoFilter(ctx, job, "hflip", nil)
}

// videoFilter re-encodes the video through one -vf chain, copying the audio.
// check, when set, validates the probed source first.
func videoFilter(ctx context.Context, job Job, vf string, check func(MediaInfo) error) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	info := d.info()
	if !info.HasVideo {
		return Userf("%s has no video track", job.Name(0))
	}
	if check != nil {
		if err := check(info); err != nil {
			return err
		}
	}
	enc, err := mediaCodecArgs(outExt(job), info, encOpts{CRF: 20, CopyAudio: true})
	if err != nil {
		return err
	}
	args := append([]string{"-i", job.Input(), "-vf", vf}, enc...)
	return runIn(ctx, job, d.Duration, append(args, job.Output)...)
}

// ---------------------------------------------------------------- crop

func parseAspect(s string) (w, h int, err error) {
	t := strings.ToLower(strings.TrimSpace(s))
	for _, sep := range []string{":", "x", "/", "-"} {
		if i := strings.Index(t, sep); i > 0 {
			a, e1 := strconv.Atoi(strings.TrimSpace(t[:i]))
			b, e2 := strconv.Atoi(strings.TrimSpace(t[i+1:]))
			if e1 == nil && e2 == nil && a > 0 && b > 0 && a <= 100 && b <= 100 {
				g := gcd(a, b)
				return a / g, b / g, nil
			}
			break
		}
	}
	return 0, 0, Userf("%q is not an aspect ratio; try 1:1, 9:16, 16:9 or 4:5", s)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func mediaCrop(ctx context.Context, job Job) error {
	aw, ah, err := parseAspect(job.Args.String("aspect", "1:1"))
	if err != nil {
		return err
	}
	// Width = aw*s*k and height = ah*s*k for the largest integer k that
	// fits: the ratio is exact and s makes both sides even.
	s := 1
	if aw%2 != 0 || ah%2 != 0 {
		s = 2
	}
	ws, hs := aw*s, ah*s
	k := fmt.Sprintf(`floor(min(iw/%d\,ih/%d))`, ws, hs)
	vf := fmt.Sprintf("crop=w=%d*%s:h=%d*%s", ws, k, hs, k)
	return videoFilter(ctx, job, vf, func(mi MediaInfo) error {
		if mi.Width < ws || mi.Height < hs {
			return Userf("the video (%dx%d) is too small to crop to %d:%d", mi.Width, mi.Height, aw, ah)
		}
		return nil
	})
}

// ---------------------------------------------------------------- resize

func mediaResize(ctx context.Context, job Job) error {
	h, err := job.Args.IntInRange("height", 720, 16, 8640)
	if err != nil {
		return err
	}
	h -= h % 2
	return videoFilter(ctx, job, fmt.Sprintf("scale=-2:%d", h), func(mi MediaInfo) error {
		if mi.Height <= h {
			return Userf("%s is already %dp, which is not taller than %dp; Asteria never upscales", job.Name(0), mi.Height, h)
		}
		return nil
	})
}

// ---------------------------------------------------------------- mute

func mediaMute(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	if d.first("video") == nil {
		return Userf("%s has no video track", job.Name(0))
	}
	args := []string{"-i", job.Input(), "-map", "0:v:0", "-c:v", "copy", "-an"}
	args = append(args, containerArgs(outExt(job))...)
	return runIn(ctx, job, d.Duration, append(args, job.Output)...)
}

// ---------------------------------------------------------------- loudness

const (
	loudI   = "-16"
	loudTP  = "-1.5"
	loudLRA = "11"
)

type loudStats struct {
	InputI      string `json:"input_i"`
	InputTP     string `json:"input_tp"`
	InputLRA    string `json:"input_lra"`
	InputThresh string `json:"input_thresh"`
	Offset      string `json:"target_offset"`
}

func mediaNormalize(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	a := d.first("audio")
	if a == nil {
		return Userf("%s has no audio track", job.Name(0))
	}
	bin, ok := job.Tools.Lookup("ffmpeg")
	if !ok {
		return &MissingToolError{Tool: "ffmpeg"}
	}
	// Pass 1: measure.
	measure := fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:print_format=json", loudI, loudTP, loudLRA)
	cmd := tools.Command(ctx, bin, "-hide_banner", "-nostdin", "-nostats", "-loglevel", "info",
		"-i", job.Input(), "-map", "0:a:0", "-af", measure, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return Userf("could not measure the loudness of %s", job.Name(0))
	}
	text := stderr.String()
	start, end := strings.LastIndex(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return Userf("could not measure the loudness of %s", job.Name(0))
	}
	var st loudStats
	if err := json.Unmarshal([]byte(text[start:end+1]), &st); err != nil {
		return Userf("could not read the loudness measurement of %s", job.Name(0))
	}
	if v, err := strconv.ParseFloat(st.InputI, 64); err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return Userf("%s is silent; there is nothing to normalize", job.Name(0))
	}
	job.Progress(0.2)

	// Pass 2: apply the measured values (linear), keeping the sample rate.
	rate := a.SampleRate
	if rate <= 0 {
		rate = 48000
	}
	apply := fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true:print_format=summary,aresample=%d",
		loudI, loudTP, loudLRA, st.InputI, st.InputTP, st.InputLRA, st.InputThresh, st.Offset, rate)
	info := d.info()
	enc, err := mediaCodecArgs(outExt(job), info, encOpts{CRF: 20, CopyVideo: true})
	if err != nil {
		return err
	}
	args := append([]string{"-i", job.Input(), "-af", apply}, enc...)
	sub := job
	sub.Progress = func(f float64) { job.Progress(0.2 + 0.8*f) }
	return runIn(ctx, sub, d.Duration, append(args, job.Output)...)
}

// ---------------------------------------------------------------- stills

func mediaThumbnail(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	if d.first("video") == nil {
		return Userf("%s has no video track", job.Name(0))
	}
	at := d.Duration * 0.1
	args := []string{"-ss", fmt.Sprintf("%.3f", at), "-i", job.Input(), "-an", "-frames:v", "1", "-update", "1", "-q:v", "2", job.Output}
	return runIn(ctx, job, 0, args...)
}

const sheetFrames = 16

func mediaContactSheet(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	if d.first("video") == nil {
		return Userf("%s has no video track", job.Name(0))
	}
	if d.Duration <= 0 {
		return Userf("cannot tell how long %s is", job.Name(0))
	}
	// Start half a step in so the samples are centered in each 1/16th, then
	// take one frame per step. Tiles are 320 px wide, 4x4.
	step := d.Duration / sheetFrames
	vf := fmt.Sprintf("fps=%.8f,scale=320:-2,tile=4x4", 1/step)
	args := []string{"-ss", fmt.Sprintf("%.3f", step/2), "-i", job.Input(), "-an", "-vf", vf,
		"-frames:v", "1", "-update", "1", "-q:v", "3", job.Output}
	return runIn(ctx, job, 0, args...)
}

const maxExtractFrames = 1000

func mediaFrames(ctx context.Context, job Job) (err error) {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	if strings.Contains(job.Output, "%") {
		return Userf("internal error: output folder contains %%")
	}
	fps, err := job.Args.Float("fps", 1)
	if err != nil {
		return err
	}
	if fps < 0.05 || fps > 60 {
		return Userf("fps must be between 0.05 and 60 (got %s)", fnum(fps))
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	if d.first("video") == nil {
		return Userf("%s has no video track", job.Name(0))
	}
	if n := int(math.Floor(d.Duration*fps)) + 1; d.Duration > 0 && n > maxExtractFrames {
		return Userf("%s would give about %d frames at %s fps, over the limit of %d; lower the fps or trim the video first",
			job.Name(0), n, fnum(fps), maxExtractFrames)
	}
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(job.Output)
		}
	}()
	args := []string{"-i", job.Input(), "-an", "-vf", "fps=" + fnum(fps), "-frames:v", strconv.Itoa(maxExtractFrames),
		filepath.Join(job.Output, "frame-%05d.png")}
	return runIn(ctx, job, d.Duration, args...)
}

// ---------------------------------------------------------------- AVI

func mediaConvertAVI(ctx context.Context, job Job) error {
	if err := requireAbs(job.Input(), job.Output); err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, job.Input())
	if err != nil {
		return err
	}
	info := d.info()
	enc, err := mediaCodecArgs(".avi", info, encOpts{})
	if err != nil {
		return err
	}
	args := append([]string{"-i", job.Input(), "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2"}, enc...)
	return runIn(ctx, job, d.Duration, append(args, job.Output)...)
}

// ---------------------------------------------------------------- join

func isAudioOutput(ext string) bool {
	if _, v := vArgs(ext, 20); v {
		return false
	}
	_, ok := aArgs(ext)
	return ok
}

func sameCodecSet(codec string, allowed ...string) bool {
	for _, a := range allowed {
		if codec == a {
			return true
		}
	}
	return false
}

// canStreamCopy reports whether the files can be concatenated without
// re-encoding: identical codec parameters, in codecs the container takes.
func canStreamCopy(ds []mediaDetail, video bool) bool {
	if len(ds) < 2 {
		return false
	}
	first := ds[0]
	fa, fv := first.first("audio"), first.first("video")
	for _, d := range ds {
		a, v := d.first("audio"), d.first("video")
		if (a == nil) != (fa == nil) {
			return false
		}
		if a != nil && (a.Codec != fa.Codec || a.SampleRate != fa.SampleRate || a.Channels != fa.Channels ||
			!sameCodecSet(a.Codec, "aac", "mp3")) {
			return false
		}
		if video {
			if v == nil || v.Codec != fv.Codec || v.Width != fv.Width || v.Height != fv.Height ||
				v.PixFmt != fv.PixFmt || v.FrameRate != fv.FrameRate || !sameCodecSet(v.Codec, "h264", "mpeg4") {
				return false
			}
		}
	}
	return true
}

func concatListEntry(p string) string {
	return "file '" + strings.ReplaceAll(filepath.ToSlash(p), "'", `'\''`) + "'\n"
}

func mediaJoin(ctx context.Context, job Job) error {
	if err := requireAbs(append([]string{job.Output}, job.Inputs...)...); err != nil {
		return err
	}
	ext := outExt(job)
	audioOut := isAudioOutput(ext)
	if _, ok := aArgs(ext); !ok {
		return Userf("cannot write %s files", ext)
	}
	ds := make([]mediaDetail, len(job.Inputs))
	total := 0.0
	for i, p := range job.Inputs {
		d, err := probeDetail(ctx, job.Tools, p)
		if err != nil {
			return err
		}
		if audioOut && d.first("audio") == nil {
			return Userf("%s has no audio track", job.Name(i))
		}
		if !audioOut && d.first("video") == nil {
			return Userf("%s has no video track; join videos with videos", job.Name(i))
		}
		ds[i] = d
		total += d.Duration
	}

	if canStreamCopy(ds, !audioOut) && (audioOut && ds[0].first("audio").Codec == "aac" || !audioOut) {
		list, err := os.CreateTemp(filepath.Dir(job.Output), ".join-*.txt")
		if err != nil {
			return err
		}
		defer os.Remove(list.Name())
		var sb strings.Builder
		for _, p := range job.Inputs {
			sb.WriteString(concatListEntry(p))
		}
		if _, err := list.WriteString(sb.String()); err != nil {
			list.Close()
			return err
		}
		if err := list.Close(); err != nil {
			return err
		}
		args := []string{"-f", "concat", "-safe", "0", "-i", list.Name()}
		if audioOut {
			args = append(args, "-vn", "-map", "0:a:0")
		} else {
			args = append(args, "-map", "0:v:0", "-map", "0:a:0?")
		}
		args = append(args, "-c", "copy")
		args = append(args, containerArgs(ext)...)
		return runIn(ctx, job, total, append(args, job.Output)...)
	}

	// Re-encode through the concat filter, matching the first file.
	var args, chains []string
	var labels strings.Builder
	anyAudio := false
	if !audioOut {
		for _, d := range ds {
			if d.first("audio") != nil {
				anyAudio = true
			}
		}
	}
	w, h, rate := 1280, 720, "30"
	if !audioOut {
		v := ds[0].first("video")
		w, h = v.Width&^1, v.Height&^1
		if w <= 0 || h <= 0 {
			w, h = 1280, 720
		}
		if validRate(v.FrameRate) {
			rate = v.FrameRate
		}
	}
	for i, p := range job.Inputs {
		args = append(args, "-i", p)
		if !audioOut {
			chains = append(chains, fmt.Sprintf("[%d:v:0]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%s,format=yuv420p[v%d]",
				i, w, h, w, h, rate, i))
			labels.WriteString(fmt.Sprintf("[v%d]", i))
		}
		if audioOut || anyAudio {
			if ds[i].first("audio") != nil {
				chains = append(chains, fmt.Sprintf("[%d:a:0]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo[a%d]", i, i))
			} else {
				chains = append(chains, fmt.Sprintf("anullsrc=r=48000:cl=stereo,atrim=duration=%.3f,aformat=sample_fmts=fltp:channel_layouts=stereo[a%d]", ds[i].Duration, i))
			}
			labels.WriteString(fmt.Sprintf("[a%d]", i))
		}
	}
	nv, na := 1, 0
	var maps []string
	switch {
	case audioOut:
		nv, na = 0, 1
		maps = []string{"-map", "[a]"}
	case anyAudio:
		na = 1
		maps = []string{"-map", "[v]", "-map", "[a]"}
	default:
		maps = []string{"-map", "[v]"}
	}
	outLabels := ""
	if nv == 1 {
		outLabels += "[v]"
	}
	if na == 1 {
		outLabels += "[a]"
	}
	chains = append(chains, fmt.Sprintf("%sconcat=n=%d:v=%d:a=%d%s", labels.String(), len(job.Inputs), nv, na, outLabels))
	args = append(args, "-filter_complex", strings.Join(chains, ";"))
	args = append(args, maps...)
	if nv == 1 {
		v, ok := vArgs(ext, 20)
		if !ok {
			return Userf("cannot write %s files", ext)
		}
		args = append(args, v...)
	}
	if na == 1 {
		a, _ := aArgs(ext)
		if audioOut && ext == ".m4a" {
			a = []string{"-c:a", "aac", "-b:a", "192k"}
		}
		args = append(args, a...)
	}
	args = append(args, containerArgs(ext)...)
	return runIn(ctx, job, total, append(args, job.Output)...)
}

// ---------------------------------------------------------------- subtitles

var ffSubtitleExts = map[string]bool{".srt": true, ".vtt": true, ".ass": true, ".ssa": true}

// splitVideoAndSubtitle picks the one video and the one subtitle file out of
// a combine job, in any tray order.
func splitVideoAndSubtitle(job Job) (video, sub string, err error) {
	var videos, subs []int
	for i := range job.Inputs {
		ext := strings.ToLower(filepath.Ext(job.Name(i)))
		if ext == "" {
			ext = strings.ToLower(filepath.Ext(job.Inputs[i]))
		}
		switch {
		case ffSubtitleExts[ext]:
			subs = append(subs, i)
		case containsFold(SubtitleExts, ext):
			return "", "", Userf("%s: convert the subtitles to .srt first", job.Name(i))
		default:
			videos = append(videos, i)
		}
	}
	if len(videos) != 1 || len(subs) != 1 {
		return "", "", Userf("add one video and one subtitle file (.srt, .vtt or .ass); you gave %d video and %d subtitle files", len(videos), len(subs))
	}
	return job.Inputs[videos[0]], job.Inputs[subs[0]], nil
}

func containsFold(list []string, s string) bool {
	for _, e := range list {
		if strings.EqualFold(e, s) {
			return true
		}
	}
	return false
}

func mediaAddSubtitles(ctx context.Context, job Job) error {
	if err := requireAbs(append([]string{job.Output}, job.Inputs...)...); err != nil {
		return err
	}
	video, sub, err := splitVideoAndSubtitle(job)
	if err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, video)
	if err != nil {
		return err
	}
	if d.first("video") == nil {
		return Userf("the video file has no video track")
	}
	subExt := strings.ToLower(filepath.Ext(sub))
	isASS := subExt == ".ass" || subExt == ".ssa"
	var codec string
	switch outExt(job) {
	case ".mp4", ".m4v", ".mov":
		if isASS {
			return Userf("MP4 can only hold plain-text subtitles; use an .srt or .vtt")
		}
		codec = "mov_text"
	default:
		codec = "srt"
		if isASS {
			codec = "ass"
		}
	}
	args := []string{"-i", video, "-i", sub, "-map", "0:v", "-map", "0:a?", "-map", "1:0",
		"-c", "copy", "-c:s", codec}
	args = append(args, containerArgs(outExt(job))...)
	return runIn(ctx, job, d.Duration, append(args, job.Output)...)
}

// ffFilterPathEscape escapes a file path for use as a filter option value
// inside a -vf/-filter_complex string. Two layers apply: the option layer
// (\ ' : =) and the filtergraph layer (\ ' , ; [ ]), so ':' becomes "\\:" and
// a Windows "C:\x" becomes "C\\:/x" once its separators are slashes.
func ffFilterPathEscape(p string) string {
	p = filepath.ToSlash(p)
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '\\':
			b.WriteString(`\\\\`)
		case '\'':
			b.WriteString(`\\\'`)
		case ':':
			b.WriteString(`\\:`)
		case '=':
			b.WriteString(`\\=`)
		case ',', ';', '[', ']':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mediaBurnSubtitles(ctx context.Context, job Job) error {
	if err := requireAbs(append([]string{job.Output}, job.Inputs...)...); err != nil {
		return err
	}
	video, sub, err := splitVideoAndSubtitle(job)
	if err != nil {
		return err
	}
	d, err := probeDetail(ctx, job.Tools, video)
	if err != nil {
		return err
	}
	info := d.info()
	enc, err := mediaCodecArgs(outExt(job), info, encOpts{CRF: 20})
	if err != nil {
		return err
	}
	vf := "subtitles=filename=" + ffFilterPathEscape(sub) + ",scale=trunc(iw/2)*2:trunc(ih/2)*2"
	args := append([]string{"-i", video, "-vf", vf}, enc...)
	err = runIn(ctx, job, d.Duration, append(args, job.Output)...)
	if err != nil && strings.Contains(err.Error(), "No such filter") {
		return Userf("this ffmpeg build cannot burn in subtitles (it was built without libass)")
	}
	return err
}
