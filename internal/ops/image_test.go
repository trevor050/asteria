package ops

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// The JPEG/HEIC/WebP fixtures are a vertical gradient from indigo (#4f46e5)
// at the top to amber (#f59e0b) at the bottom, 120x80.
var (
	indigo = [3]int{0x4f, 0x46, 0xe5}
	amber  = [3]int{0xf5, 0x9e, 0x0b}
)

func TestConvertJPEGToPNGKeepsPixels(t *testing.T) {
	out := mustRun(t, "image.convert", []string{fixture("sample.jpg")}, "out.png", nil)
	img, format := decode(t, out)
	if format != "png" {
		t.Fatalf("format = %s, want png", format)
	}
	if b := img.Bounds(); b.Dx() != 120 || b.Dy() != 80 {
		t.Fatalf("size = %v, want 120x80", b.Size())
	}
	assertColor(t, img, 60, 0, indigo, 12)
	assertColor(t, img, 60, 79, amber, 12)
	// An 8-bit JPEG must not turn into a 16-bit PNG.
	f, _ := os.Open(out)
	defer f.Close()
	cfg, _, _ := image.DecodeConfig(f)
	if cfg.ColorModel == color.RGBA64Model || cfg.ColorModel == color.NRGBA64Model {
		t.Fatal("JPEG converted to a 16-bit PNG")
	}
}

func TestConvertTransparentPNGToJPEGUsesWhiteBackground(t *testing.T) {
	out := mustRun(t, "image.convert", []string{fixture("sample.png")}, "out.jpg", Args{"quality": "95"})
	img, format := decode(t, out)
	if format != "jpeg" {
		t.Fatalf("format = %s, want jpeg", format)
	}
	assertColor(t, img, 2, 2, [3]int{255, 255, 255}, 6)       // was transparent
	assertColor(t, img, 60, 40, [3]int{0x10, 0xb9, 0x81}, 12) // green square
}

func TestConvertHEICDecodesRealAppleFile(t *testing.T) {
	out := mustRun(t, "image.convert", []string{fixture("sample.heic")}, "out.jpg", nil)
	img, _ := decode(t, out)
	if b := img.Bounds(); b.Dx() != 120 || b.Dy() != 80 {
		t.Fatalf("size = %v, want 120x80", b.Size())
	}
	assertColor(t, img, 60, 2, indigo, 24)
	assertColor(t, img, 60, 77, amber, 24)
}

func TestConvertToWebPAndBack(t *testing.T) {
	out := mustRun(t, "image.convert", []string{fixture("sample.jpg")}, "out.webp", Args{"quality": 90})
	img, format := decode(t, out)
	if format != "webp" {
		t.Fatalf("format = %s, want webp", format)
	}
	// Lossy WebP subsamples chroma, so allow a wider tolerance.
	assertColor(t, img, 60, 4, indigo, 32)
	// WebP input works too.
	out2 := mustRun(t, "image.convert", []string{fixture("sample.webp")}, "out.png", nil)
	img2, _ := decode(t, out2)
	assertColor(t, img2, 60, 77, amber, 24)
}

func TestHEICRoundTrip(t *testing.T) {
	// Encode to HEIC, decode it again, and check the pixels survived.
	out := mustRun(t, "image.convert", []string{fixture("sample.jpg")}, "out.heic", Args{"quality": 90})
	back := mustRun(t, "image.convert", []string{out}, "back.png", nil)
	img, _ := decode(t, back)
	if b := img.Bounds(); b.Dx() != 120 || b.Dy() != 80 {
		t.Fatalf("size = %v, want 120x80", b.Size())
	}
	assertColor(t, img, 60, 4, indigo, 24)
	assertColor(t, img, 60, 75, amber, 24)
	// "Same format" skills keep HEIC as HEIC now that it can be written.
	if got := imageOutputExt(".heic", ".heic"); got != ".heic" {
		t.Fatalf("imageOutputExt = %s", got)
	}
	if got := imageOutputExt(".xyz", ".heic"); got != ".jpg" {
		t.Fatalf("unknown output should fall back to .jpg, got %s", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		spec       string
		w, h       int
		wantW, wnH int
		wantErr    bool
	}{
		{"50%", 120, 80, 60, 40, false},
		{"200%", 10, 10, 20, 20, false},
		{"60", 120, 80, 60, 40, false},
		{"60px", 120, 80, 60, 40, false},
		{"x40", 120, 80, 60, 40, false},
		{"60x60", 120, 80, 60, 40, false},
		{"1920", 120, 80, 120, 80, false}, // never upscale by pixels
		{"1920×1080", 3840, 2160, 1920, 1080, false},
		{"0", 10, 10, 0, 0, true},
		{"abc", 10, 10, 0, 0, true},
		{"", 10, 10, 0, 0, true},
		{"-5%", 10, 10, 0, 0, true},
	}
	for _, c := range cases {
		w, h, err := ParseSize(c.spec, c.w, c.h)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSize(%q) succeeded, want error", c.spec)
			}
			continue
		}
		if err != nil || w != c.wantW || h != c.wnH {
			t.Errorf("ParseSize(%q, %d, %d) = %d,%d,%v; want %d,%d", c.spec, c.w, c.h, w, h, err, c.wantW, c.wnH)
		}
	}
}

func TestResizeKeepsFormatAndAspect(t *testing.T) {
	out := mustRun(t, "image.resize", []string{fixture("sample.jpg")}, "out.jpg", Args{"size": "50%"})
	img, format := decode(t, out)
	if format != "jpeg" || img.Bounds().Dx() != 60 || img.Bounds().Dy() != 40 {
		t.Fatalf("got %s %v, want jpeg 60x40", format, img.Bounds().Size())
	}
	_, err := run(t, "image.resize", []string{fixture("sample.jpg")}, "bad.jpg", Args{"size": "huge"})
	assertUserError(t, err)
}

func TestCompressShrinksJPEG(t *testing.T) {
	// Build a noisy 400x300 JPEG at quality 98 so there is something to save.
	src := filepath.Join(t.TempDir(), "big.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 7 % 256), uint8(y * 13 % 256), uint8((x * y) % 256), 255})
		}
	}
	f, _ := os.Create(src)
	_ = jpeg.Encode(f, img, &jpeg.Options{Quality: 98})
	f.Close()

	out := mustRun(t, "image.compress", []string{src}, "small.jpg", Args{"quality": 60})
	if size(t, out) >= size(t, src) {
		t.Fatalf("compressed %d bytes >= original %d", size(t, out), size(t, src))
	}
	decoded, format := decode(t, out)
	if format != "jpeg" || decoded.Bounds().Dx() != 400 {
		t.Fatalf("compressed output is %s %v", format, decoded.Bounds())
	}
}

func TestCompressNeverGrowsFile(t *testing.T) {
	// The tiny paletted PNG fixture is already optimal; re-encoding it as
	// RGBA would grow it, so the original bytes must be kept.
	out := mustRun(t, "image.compress", []string{fixture("sample.png")}, "out.png", nil)
	if size(t, out) > size(t, fixture("sample.png")) {
		t.Fatalf("compress grew the file: %d > %d", size(t, out), size(t, fixture("sample.png")))
	}
}

func TestRotateClockwise(t *testing.T) {
	out := mustRun(t, "image.rotate", []string{fixture("sample.jpg")}, "out.jpg", Args{"angle": "90"})
	img, _ := decode(t, out)
	if img.Bounds().Dx() != 80 || img.Bounds().Dy() != 120 {
		t.Fatalf("size = %v, want 80x120", img.Bounds().Size())
	}
	// Clockwise: the top edge (indigo) moves to the right edge.
	assertColor(t, img, 78, 60, indigo, 20)
	assertColor(t, img, 1, 60, amber, 20)
	_, err := run(t, "image.rotate", []string{fixture("sample.jpg")}, "bad.jpg", Args{"angle": "45"})
	assertUserError(t, err)
}

func TestGrayscaleAndBlur(t *testing.T) {
	out := mustRun(t, "image.grayscale", []string{fixture("sample.jpg")}, "g.png", nil)
	img, _ := decode(t, out)
	r, g, b, _ := rgb(img.At(60, 40))
	if r != g || g != b {
		t.Fatalf("grayscale pixel = %d,%d,%d", r, g, b)
	}
	blurred := mustRun(t, "image.blur", []string{fixture("sample.png")}, "b.png", Args{"radius": 4})
	bimg, _ := decode(t, blurred)
	// The hard edge of the green square must be softened.
	_, _, _, a := rgb(bimg.At(20, 40))
	if a == 0 || a == 255 {
		t.Fatalf("edge alpha = %d, want partial after blur", a)
	}
}

func TestAnimatedGIFIsRejectedNotTruncated(t *testing.T) {
	_, err := run(t, "image.convert", []string{fixture("animated.gif")}, "out.png", nil)
	assertUserError(t, err)
}

func TestNonImageInputGivesReadableError(t *testing.T) {
	_, err := run(t, "image.convert", []string{fixture("people.csv")}, "out.png", nil)
	assertUserError(t, err)
}

// writeOrientedJPEG writes a 40x20 JPEG (red left half, blue right half)
// carrying the given EXIF orientation tag.
func writeOrientedJPEG(t *testing.T, orientation uint16) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 20 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var raw bytes.Buffer
	_ = jpeg.Encode(&raw, img, &jpeg.Options{Quality: 95})
	// Minimal little-endian TIFF with one IFD entry: Orientation.
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0}
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:], 3) // SHORT
	binary.LittleEndian.PutUint32(entry[4:], 1)
	binary.LittleEndian.PutUint16(entry[8:], orientation)
	tiff = append(tiff, entry...)
	tiff = append(tiff, 0, 0, 0, 0)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	b := raw.Bytes()
	out := append([]byte{}, b[:2]...)
	out = append(out, seg...)
	out = append(out, b[2:]...)
	p := filepath.Join(t.TempDir(), "oriented.jpg")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEXIFOrientationIsApplied(t *testing.T) {
	p := writeOrientedJPEG(t, 6) // "rotate 90 CW to display"
	if got := JPEGOrientation(p); got != 6 {
		t.Fatalf("JPEGOrientation = %d, want 6", got)
	}
	if got := JPEGOrientation(fixture("sample.jpg")); got != 0 {
		t.Fatalf("JPEGOrientation(sample) = %d, want 0", got)
	}
	out := mustRun(t, "image.convert", []string{p}, "upright.png", nil)
	img, _ := decode(t, out)
	if img.Bounds().Dx() != 20 || img.Bounds().Dy() != 40 {
		t.Fatalf("upright size = %v, want 20x40", img.Bounds().Size())
	}
	// Red (left) ends up on top after a clockwise rotation.
	assertColor(t, img, 10, 2, [3]int{255, 0, 0}, 40)
	assertColor(t, img, 10, 37, [3]int{0, 0, 255}, 40)
}
