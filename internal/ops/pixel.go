package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"slices"
	"strings"

	"asteria/internal/img"
)

// DecodeWorking reads an image file into the pixel core's working format:
// orientation applied, embedded matrix/TRC colour profiles converted to sRGB,
// 16-bit sources kept at 16 bits.
func DecodeWorking(path string) (*img.Image, error) {
	std, err := DecodeImage(path)
	if err != nil {
		return nil, err
	}
	m := img.FromStd(std)
	if icc := img.ReadICC(path); icc != nil {
		if p := img.ParseProfile(icc); p != nil {
			m = img.ToSRGB(m, p)
		}
	}
	return m, nil
}

// EncodeWorking writes the working image in the format implied by path's
// extension, at the quality carried on the image (or the format default).
func EncodeWorking(m *img.Image, path string) error {
	q := m.Quality
	if q <= 0 {
		q = defaultQuality(filepath.Ext(path))
	}
	return EncodeImage(m.Std(), path, q, false)
}

func defaultQuality(ext string) int {
	switch strings.ToLower(ext) {
	case ".heic", ".heif":
		return 80
	case ".webp":
		return 90
	}
	return 92
}

// IsImageType reports whether a file type token is an image the core reads.
func IsImageType(t string) bool { return slices.Contains(ImageInputExts, strings.ToLower(t)) }

// IsVideoType reports whether a file type token is a video.
func IsVideoType(t string) bool { return slices.Contains(VideoExts, strings.ToLower(t)) }

// RunPixel applies a Pixel handler to an already-decoded image, reserving
// working memory for the input, the output and one temporary of the same
// size first.
func RunPixel(ctx context.Context, h Handler, m *img.Image, args Args) (*img.Image, error) {
	release, err := img.Reserve(ctx, 3*m.Bytes())
	if err != nil {
		return nil, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := h.Pixel(ctx, m, args)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New(h.Name + " returned no image")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// standalonePixel is the Run used when a Pixel handler is invoked without the
// engine's cache (tests, pipelines of foreign tools): decode, apply, encode.
func standalonePixel(h Handler) func(context.Context, Job) error {
	return func(ctx context.Context, job Job) error {
		in := job.Input()
		if IsVideoType(filepath.Ext(in)) {
			if h.Video == nil {
				return Userf("%s works on images only", h.Name)
			}
			return RunVideoFilter(ctx, job, h, job.Args)
		}
		m, err := DecodeWorking(in)
		if err != nil {
			return err
		}
		job.Progress(0.3)
		out, err := RunPixel(ctx, h, m, job.Args)
		if err != nil {
			return err
		}
		job.Progress(0.8)
		return EncodeWorking(out, job.Output)
	}
}

// PreviewBytes encodes a (proxy) image for display: JPEG when opaque, PNG when
// it has transparency.
func PreviewBytes(m *img.Image) ([]byte, string, error) {
	var buf bytes.Buffer
	if m.Opaque() {
		if err := jpeg.Encode(&buf, m.NRGBA(), &jpeg.Options{Quality: 86}); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/jpeg", nil
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, m.NRGBA()); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/png", nil
}

// RunVideoFilter applies a handler's ffmpeg filter to a video, keeping audio.
func RunVideoFilter(ctx context.Context, job Job, h Handler, args Args) error {
	in := job.Input()
	info, _ := ProbeMedia(ctx, job.Tools, in)
	// Sizes given as a percent of the picture resolve against the frame.
	filter, err := h.Video(Merge(args, Args{"_width": float64(info.Width)}))
	if err != nil {
		return err
	}
	out := []string{"-i", in, "-vf", filter}
	out = append(out, videoEncodeArgs(filepath.Ext(job.Output))...)
	out = append(out, "-c:a", "copy", job.Output)
	if err := runFFmpeg(ctx, job, info.Duration, out); err != nil {
		return fmt.Errorf("%s: %w", h.Name, err)
	}
	return nil
}

// VideoLength reads a size for a video filter: pixels, or a percent of the
// frame width (RunVideoFilter supplies it; 1920 when unknown).
func VideoLength(args Args, key string, defPx float64) (float64, error) {
	w, _ := args.Float("_width", 1920)
	if w <= 0 {
		w = 1920
	}
	return args.Length(key, defPx, int(w), 1, 0)
}

// videoEncodeArgs picks a high-quality encoder for a re-encode that should
// look like the source.
func videoEncodeArgs(ext string) []string {
	switch strings.ToLower(ext) {
	case ".webm":
		return []string{"-c:v", "libvpx-vp9", "-crf", "30", "-b:v", "0", "-row-mt", "1"}
	case ".gif":
		return nil
	}
	return []string{"-c:v", "libx264", "-crf", "17", "-preset", "medium", "-pix_fmt", "yuv420p", "-movflags", "+faststart"}
}
