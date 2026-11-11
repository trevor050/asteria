package ops

import (
	"bufio"
	"context"
	"errors"
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
		{Name: "image.convert", Run: imageConvert, OutputExt: imageOutputExt},
		{Name: "image.resize", Run: imageResize, OutputExt: imageOutputExt},
		{Name: "image.compress", Run: imageCompress, OutputExt: imageOutputExt},
		{Name: "image.rotate", Run: imageRotate, OutputExt: imageOutputExt},
		{Name: "image.grayscale", Run: imageGrayscale, OutputExt: imageOutputExt},
		{Name: "image.blur", Run: imageBlur, OutputExt: imageOutputExt},
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

func imageConvert(_ context.Context, job Job) error {
	img, err := DecodeImage(job.Input())
	if err != nil {
		return err
	}
	job.Progress(0.5)
	q, err := quality(job, 90)
	if err != nil {
		return err
	}
	return EncodeImage(img, job.Output, q, false)
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

func imageResize(_ context.Context, job Job) error {
	img, err := DecodeImage(job.Input())
	if err != nil {
		return err
	}
	b := img.Bounds()
	nw, nh, err := ParseSize(job.Args.String("size", "50%"), b.Dx(), b.Dy())
	if err != nil {
		return err
	}
	job.Progress(0.3)
	if nw != b.Dx() || nh != b.Dy() {
		img = imaging.Resize(img, nw, nh, imaging.Lanczos)
	}
	job.Progress(0.7)
	q, err := quality(job, 90)
	if err != nil {
		return err
	}
	return EncodeImage(img, job.Output, q, false)
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

func imageRotate(_ context.Context, job Job) error {
	img, err := DecodeImage(job.Input())
	if err != nil {
		return err
	}
	deg, err := job.Args.Float("angle", 90)
	if err != nil {
		return err
	}
	d := ((int(math.Round(deg)) % 360) + 360) % 360
	switch d {
	case 90:
		img = imaging.Rotate270(img) // imaging rotates counter-clockwise
	case 180:
		img = imaging.Rotate180(img)
	case 270:
		img = imaging.Rotate90(img)
	case 0:
	default:
		return Userf("rotate supports 90, 180 or 270 degrees (got %v)", deg)
	}
	q, err := quality(job, 92)
	if err != nil {
		return err
	}
	return EncodeImage(img, job.Output, q, false)
}

func imageGrayscale(_ context.Context, job Job) error {
	img, err := DecodeImage(job.Input())
	if err != nil {
		return err
	}
	q, err := quality(job, 92)
	if err != nil {
		return err
	}
	return EncodeImage(imaging.Grayscale(img), job.Output, q, false)
}

func imageBlur(_ context.Context, job Job) error {
	img, err := DecodeImage(job.Input())
	if err != nil {
		return err
	}
	radius, err := job.Args.Float("radius", 3)
	if err != nil {
		return err
	}
	if radius <= 0 || radius > 100 {
		return Userf("blur radius must be between 0 and 100")
	}
	q, err := quality(job, 92)
	if err != nil {
		return err
	}
	return EncodeImage(imaging.Blur(img, radius), job.Output, q, false)
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
