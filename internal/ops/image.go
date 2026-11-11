package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/h265/heic"
	"github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"

	"asteria/internal/img"
)

// ImageInputExts lists the image formats Asteria can decode.
var ImageInputExts = []string{".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tif", ".tiff", ".webp", ".heic", ".heif"}

// imageOutputExts lists the formats Asteria can encode.
var imageOutputExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".bmp": true, ".tif": true, ".tiff": true,
	".heic": true, ".heif": true,
}

func init() {
	for _, h := range []Handler{
		{Name: "image.convert", Pixel: pixConvert, OutputExt: imageOutputExt},
		{Name: "image.resize", Pixel: pixResize, OutputExt: imageOutputExt},
		{Name: "image.compress", Run: imageCompress, OutputExt: imageOutputExt},
		{Name: "image.rotate", Pixel: pixRotate, OutputExt: imageOutputExt, Video: videoRotate},
		{Name: "image.grayscale", Pixel: pixGrayscale, OutputExt: imageOutputExt, Live: true, Video: func(Args) (string, error) { return "hue=s=0", nil }},
		{Name: "image.blur", Pixel: pixBlur, OutputExt: imageOutputExt, Live: true, Video: videoBlur},
	} {
		h.Concurrency = 3
		register(h)
	}
}

// imageOutputExt keeps the planned extension when we can encode it and falls
// back to JPEG otherwise.
func imageOutputExt(planned, _ string) string {
	planned = strings.ToLower(planned)
	if imageOutputExts[planned] {
		return planned
	}
	return ".jpg"
}

// DecodeImage opens an image file, applying EXIF orientation. Animated GIFs
// are rejected so frames are never silently dropped.
func DecodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".gif" {
		g, err := gif.DecodeAll(bufio.NewReader(f))
		if err != nil {
			return nil, Userf("could not read GIF: %v", err)
		}
		if len(g.Image) > 1 {
			return nil, Userf("animated GIF (%d frames) is not supported by image skills yet", len(g.Image))
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	var img image.Image
	if ext == ".heic" || ext == ".heif" {
		// Pure-Go HEVC decoder; AutoRotate applies the irot/imir transforms
		// iPhones use for portrait photos.
		img, err = heic.Decode(bufio.NewReader(f), heic.Options{AutoRotate: true})
		if err != nil {
			return nil, Userf("could not read HEIC image: %v", err)
		}
		return img, nil
	}
	img, err = imaging.Decode(bufio.NewReader(f), imaging.AutoOrientation(true))
	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			return nil, Userf("%s is not an image Asteria can read", filepath.Base(path))
		}
		return nil, Userf("could not read image: %v", err)
	}
	return img, nil
}

// EncodeImage writes img to path using the format implied by its extension.
// JPEG output is flattened onto white so transparent areas do not turn black.
func EncodeImage(img image.Image, path string, quality int, bestCompression bool) (err error) {
	ext := strings.ToLower(filepath.Ext(path))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	w := bufio.NewWriter(f)
	switch ext {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(w, flatten(img, color.White), &jpeg.Options{Quality: quality})
	case ".png":
		// Go's PNG encoder writes 16-bit files for YCbCr/CMYK sources (JPEGs),
		// doubling their size for nothing; convert those to 8-bit first.
		switch img.(type) {
		case *image.YCbCr, *image.CMYK:
			img = imaging.Clone(img)
		}
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if bestCompression {
			enc.CompressionLevel = png.BestCompression
		}
		err = enc.Encode(w, img)
	case ".webp":
		err = webp.Encode(w, img, webp.Options{Quality: quality, Method: 4})
	case ".gif":
		err = gif.Encode(w, img, &gif.Options{NumColors: 256})
	case ".heic", ".heif":
		err = heic.Encode(w, img, heic.EncodeOptions{Quality: quality})
	case ".bmp":
		err = bmp.Encode(w, img)
	case ".tif", ".tiff":
		err = tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate})
	default:
		return Userf("cannot write %s images", ext)
	}
	if err != nil {
		return err
	}
	return w.Flush()
}

func flatten(img image.Image, bg color.Color) image.Image {
	if !hasAlpha(img) {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, &image.Uniform{C: bg}, image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

func hasAlpha(img image.Image) bool {
	switch t := img.(type) {
	case *image.YCbCr, *image.Gray, *image.Gray16, *image.CMYK:
		return false
	case *image.NRGBA:
		return !t.Opaque()
	case *image.RGBA:
		return !t.Opaque()
	case interface{ Opaque() bool }:
		return !t.Opaque()
	}
	return true
}

func quality(job Job, fallback int) (int, error) {
	return job.Args.IntInRange("quality", fallback, 1, 100)
}

var sizeRe = regexp.MustCompile(`^\s*(\d*)\s*[x×]?\s*(\d*)\s*$`)

// ParseSize turns "50%", "1920", "x1080" or "1920x1080" into a target size
// for an image of the given dimensions. Box sizes fit within the box and keep
// the aspect ratio. Images are never upscaled by pixel sizes.
func ParseSize(spec string, w, h int) (int, int, error) {
	s := strings.ToLower(strings.TrimSpace(spec))
	s = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(s, "px")), " ")
	if s == "" {
		return 0, 0, Userf("enter a size such as 50%%, 1920 or 1920x1080")
	}
	if strings.HasSuffix(s, "%") {
		pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		if err != nil || pct <= 0 || pct > 1000 {
			return 0, 0, Userf("%q is not a valid percentage", spec)
		}
		nw := int(math.Max(1, math.Round(float64(w)*pct/100)))
		nh := int(math.Max(1, math.Round(float64(h)*pct/100)))
		return nw, nh, nil
	}
	m := sizeRe.FindStringSubmatch(s)
	if m == nil || (m[1] == "" && m[2] == "") {
		return 0, 0, Userf("%q is not a size; try 50%%, 1920 or 1920x1080", spec)
	}
	bw, _ := strconv.Atoi(m[1])
	bh, _ := strconv.Atoi(m[2])
	if (m[1] != "" && bw <= 0) || (m[2] != "" && bh <= 0) {
		return 0, 0, Userf("%q is not a size", spec)
	}
	scale := math.Inf(1)
	if bw > 0 {
		scale = math.Min(scale, float64(bw)/float64(w))
	}
	if bh > 0 {
		scale = math.Min(scale, float64(bh)/float64(h))
	}
	if scale >= 1 {
		return w, h, nil
	}
	nw := int(math.Max(1, math.Round(float64(w)*scale)))
	nh := int(math.Max(1, math.Round(float64(h)*scale)))
	return nw, nh, nil
}

func imageCompress(_ context.Context, job Job) error {
	img, err := DecodeImage(job.Input())
	if err != nil {
		return err
	}
	q, err := quality(job, 75)
	if err != nil {
		return err
	}
	job.Progress(0.5)
	if err := EncodeImage(img, job.Output, q, true); err != nil {
		return err
	}
	// A "compressed" file that is bigger than the input is a surprise; keep
	// the original bytes when re-encoding does not help and the format matches.
	in, err1 := os.Stat(job.Input())
	out, err2 := os.Stat(job.Output)
	sameExt := strings.EqualFold(normExt(filepath.Ext(job.Input())), normExt(filepath.Ext(job.Output)))
	if err1 == nil && err2 == nil && sameExt && out.Size() >= in.Size() {
		if err := os.Remove(job.Output); err != nil {
			return err
		}
		return CopyFile(job.Input(), job.Output)
	}
	return nil
}

func normExt(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".jpeg":
		return ".jpg"
	case ".tif":
		return ".tiff"
	}
	return ext
}

// CopyFile copies src to a new file at dst.
func CopyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	_, err = io.Copy(out, in)
	return err
}

// ---- pixel-core versions of the original image skills ----------------------

// pixConvert changes only the encoder settings; the pixels stay lossless in
// the chain and are encoded once, at this quality, in the target format.
func pixConvert(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	q, err := args.IntInRange("quality", 90, 1, 100)
	if err != nil {
		return nil, err
	}
	out := *m // pixels are shared, never modified
	out.Quality = q
	return &out, nil
}

func pixResize(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	// Sizes are in full-resolution pixels; a proxy scales them by Scale.
	fw, fh := int(math.Round(float64(m.W)/m.Scale)), int(math.Round(float64(m.H)/m.Scale))
	nw, nh, err := ParseSize(args.String("size", "50%"), fw, fh)
	if err != nil {
		return nil, err
	}
	if nw == fw && nh == fh {
		return m, nil
	}
	tw := max(1, int(math.Round(float64(nw)*m.Scale)))
	th := max(1, int(math.Round(float64(nh)*m.Scale)))
	out := img.Resize(m, tw, th, img.Lanczos3)
	if m.Scale != 1 {
		out.Scale = m.Scale // a proxy stays a proxy of the new size
	}
	return out, nil
}

func pixRotate(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	deg, err := args.Degrees("angle", 90)
	if err != nil {
		return nil, err
	}
	switch ((int(math.Round(deg)) % 360) + 360) % 360 {
	case 0:
		return m, nil
	case 90:
		return img.Rotate90(m), nil
	case 180:
		return img.Rotate180(m), nil
	case 270:
		return img.Rotate270(m), nil
	}
	return nil, Userf("rotate supports 90, 180 or 270 degrees (got %v); use straighten for other angles", deg)
}

// pixGrayscale uses linear-light luminance, so mid-tones keep their
// brightness (gamma-space averages darken saturated colours).
func pixGrayscale(_ context.Context, m *img.Image, _ Args) (*img.Image, error) {
	return img.MapColor(m, img.LinearLight, func(r, g, b float32) (float32, float32, float32) {
		y := img.Luma(r, g, b)
		return y, y, y
	}), nil
}

func pixBlur(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	radius, err := args.Float("radius", 3)
	if err != nil {
		return nil, err
	}
	if radius <= 0 || radius > 200 {
		return nil, Userf("blur radius must be between 0 and 200")
	}
	s := radius * m.Scale
	return img.Blur(m.ToLinear(), s, s).ToImage(m), ctx.Err()
}

func videoRotate(args Args) (string, error) {
	deg, err := args.Degrees("angle", 90)
	if err != nil {
		return "", err
	}
	switch ((int(math.Round(deg)) % 360) + 360) % 360 {
	case 90:
		return "transpose=clock", nil
	case 180:
		return "hflip,vflip", nil
	case 270:
		return "transpose=cclock", nil
	}
	return "", Userf("video rotate supports 90, 180 or 270 degrees")
}

func videoBlur(args Args) (string, error) {
	radius, err := args.Float("radius", 3)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("gblur=sigma=%.2f", math.Max(0.1, math.Min(radius, 100))), nil
}
