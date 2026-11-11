// Package img is the pixel core every image skill uses.
//
// One working representation: Image holds 16-bit RGBA with straight
// (non-premultiplied) alpha in the sRGB transfer curve. Operations choose the
// space they work in explicitly:
//
//   - Linear light (Linear, premultiplied float32): anything that mixes
//     neighbouring pixels or light — resampling, blurs, compositing, glow,
//     exposure, white balance. Mixing in gamma space darkens edges and
//     produces halos; mixing straight alpha bleeds the colour of
//     transparent pixels.
//   - Encoded sRGB (0..1, the values people see in colour pickers): tone and
//     curve operations — brightness, contrast, levels, gamma, posterize,
//     threshold, invert — so numbers match what other editors do.
//   - OKLab / OKLCh (perceptual): saturation, vibrance, hue rotation and
//     colour distance, so a hue shift keeps perceived lightness.
//
// Orientation and colour profiles are resolved at decode (see codec.go):
// EXIF/HEIF orientation is applied to the pixels and matrix/TRC ICC profiles
// (Display P3, Adobe RGB, ...) are converted to sRGB, so every operation and
// every encoder sees plain sRGB. Deep tracks whether the source had more
// than 8 bits so 16-bit PNG/TIFF sources are written back as 16-bit.
//
// Operations never modify their input; they return a new Image (or the same
// pointer when nothing changes). That lets the engine cache results and
// replay chains without copying.
package img

import (
	"image"
	"image/color"
)

// Image is the working image. Pix holds R,G,B,A per pixel, row-major,
// 16 bits per channel, straight alpha, sRGB-encoded.
type Image struct {
	W, H int
	Pix  []uint16
	// Deep is true when the source had more than 8 bits per channel.
	Deep bool
	// Scale is 1 for full resolution and smaller for a preview proxy.
	// Operations multiply pixel-unit parameters (radii, offsets, border
	// widths, font sizes) by Scale so a proxy previews the same look.
	Scale float64
	// Quality is the encoder quality carried along a chain (set by convert
	// skills); 0 means the format's default.
	Quality int
}

// New returns a transparent image.
func New(w, h int) *Image {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &Image{W: w, H: h, Pix: make([]uint16, 4*w*h), Scale: 1}
}

// NewLike returns a blank image with m's size and metadata.
func (m *Image) NewLike() *Image {
	n := New(m.W, m.H)
	n.Deep, n.Scale, n.Quality = m.Deep, m.Scale, m.Quality
	return n
}

// Sized returns a blank image of the given size carrying m's metadata.
func (m *Image) Sized(w, h int) *Image {
	n := New(w, h)
	n.Deep, n.Scale, n.Quality = m.Deep, m.Scale, m.Quality
	return n
}

// Clone returns a deep copy.
func (m *Image) Clone() *Image {
	n := *m
	n.Pix = append([]uint16(nil), m.Pix...)
	return &n
}

// Off returns the index of pixel (x, y) in Pix.
func (m *Image) Off(x, y int) int { return 4 * (y*m.W + x) }

// At returns the straight-alpha 16-bit channels at (x, y).
func (m *Image) At(x, y int) (r, g, b, a uint16) {
	i := m.Off(x, y)
	p := m.Pix[i : i+4 : i+4]
	return p[0], p[1], p[2], p[3]
}

// Set writes straight-alpha 16-bit channels at (x, y).
func (m *Image) Set(x, y int, r, g, b, a uint16) {
	i := m.Off(x, y)
	p := m.Pix[i : i+4 : i+4]
	p[0], p[1], p[2], p[3] = r, g, b, a
}

// Opaque reports whether every pixel is fully opaque.
func (m *Image) Opaque() bool {
	for i := 3; i < len(m.Pix); i += 4 {
		if m.Pix[i] != 0xffff {
			return false
		}
	}
	return true
}

// Bytes is the memory the image holds.
func (m *Image) Bytes() int64 { return int64(len(m.Pix)) * 2 }

// FromStd converts any image.Image to the working representation.
func FromStd(src image.Image) *Image {
	b := src.Bounds()
	m := New(b.Dx(), b.Dy())
	switch s := src.(type) {
	case *image.NRGBA:
		Rows(nil, m.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := s.Pix[s.PixOffset(b.Min.X, y+b.Min.Y):][:4*m.W]
				o := 4 * y * m.W
				for i, v := range row {
					m.Pix[o+i] = uint16(v) * 0x101
				}
			}
		})
	case *image.YCbCr:
		Rows(nil, m.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < m.W; x++ {
					yi := s.YOffset(x+b.Min.X, y+b.Min.Y)
					ci := s.COffset(x+b.Min.X, y+b.Min.Y)
					r, g, bb := color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
					o := m.Off(x, y)
					m.Pix[o], m.Pix[o+1], m.Pix[o+2], m.Pix[o+3] = uint16(r)*0x101, uint16(g)*0x101, uint16(bb)*0x101, 0xffff
				}
			}
		})
	case *image.Gray:
		Rows(nil, m.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < m.W; x++ {
					v := uint16(s.Pix[s.PixOffset(x+b.Min.X, y+b.Min.Y)]) * 0x101
					o := m.Off(x, y)
					m.Pix[o], m.Pix[o+1], m.Pix[o+2], m.Pix[o+3] = v, v, v, 0xffff
				}
			}
		})
	case *image.NRGBA64:
		m.Deep = true
		Rows(nil, m.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < 4*m.W; x++ {
					i := s.PixOffset(b.Min.X, y+b.Min.Y) + 2*x
					m.Pix[4*y*m.W+x] = uint16(s.Pix[i])<<8 | uint16(s.Pix[i+1])
				}
			}
		})
	default:
		switch src.(type) {
		case *image.RGBA64, *image.Gray16:
			m.Deep = true
		}
		Rows(nil, m.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < m.W; x++ {
					c := color.NRGBA64Model.Convert(src.At(x+b.Min.X, y+b.Min.Y)).(color.NRGBA64)
					m.Set(x, y, c.R, c.G, c.B, c.A)
				}
			}
		})
	}
	return m
}

// Std returns an image.Image for encoders: NRGBA64 when Deep, else NRGBA.
func (m *Image) Std() image.Image {
	if m.Deep {
		out := image.NewNRGBA64(image.Rect(0, 0, m.W, m.H))
		Rows(nil, m.H, func(y0, y1 int) {
			for i := 4 * y0 * m.W; i < 4*y1*m.W; i++ {
				v := m.Pix[i]
				out.Pix[2*i], out.Pix[2*i+1] = uint8(v>>8), uint8(v)
			}
		})
		return out
	}
	return m.NRGBA()
}

// NRGBA returns an 8-bit copy (rounded).
func (m *Image) NRGBA() *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, m.W, m.H))
	Rows(nil, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i++ {
			out.Pix[i] = To8(m.Pix[i])
		}
	})
	return out
}

// To8 rounds a 16-bit channel to 8 bits.
func To8(v uint16) uint8 { return uint8((uint32(v)*255 + 32767) / 65535) }
