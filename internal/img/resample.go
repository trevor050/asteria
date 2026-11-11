package img

import "math"

// Filter selects a resampling kernel.
type Filter int

const (
	// Lanczos3 is sharp and the default for photos.
	Lanczos3 Filter = iota
	// CatmullRom is a little softer, with less ringing.
	CatmullRom
	// Nearest keeps hard pixel edges (pixel art, upscaling icons).
	Nearest
)

func (f Filter) support() float64 {
	switch f {
	case CatmullRom:
		return 2
	case Nearest:
		return 0.5
	}
	return 3
}

func (f Filter) weight(x float64) float64 {
	x = math.Abs(x)
	switch f {
	case CatmullRom:
		if x < 1 {
			return 1.5*x*x*x - 2.5*x*x + 1
		}
		if x < 2 {
			return -0.5*x*x*x + 2.5*x*x - 4*x + 2
		}
		return 0
	case Nearest:
		if x < 0.5 {
			return 1
		}
		return 0
	}
	if x == 0 {
		return 1
	}
	if x >= 3 {
		return 0
	}
	px := math.Pi * x
	return 3 * math.Sin(px) * math.Sin(px/3) / (px * px)
}

// Resize scales to w x h. Smooth filters work in premultiplied linear light;
// Nearest copies pixels exactly. Large reductions first average whole blocks
// (exact for area) so a 24 MP photo becomes a preview proxy quickly.
func Resize(m *Image, w, h int, f Filter) *Image {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w == m.W && h == m.H {
		return m
	}
	if f == Nearest {
		out := m.Sized(w, h)
		Rows(nil, h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				sy := min(m.H-1, int((float64(y)+0.5)*float64(m.H)/float64(h)))
				for x := 0; x < w; x++ {
					sx := min(m.W-1, int((float64(x)+0.5)*float64(m.W)/float64(w)))
					copy(out.Pix[out.Off(x, y):out.Off(x, y)+4], m.Pix[m.Off(sx, sy):m.Off(sx, sy)+4])
				}
			}
		})
		return out
	}
	l := m.ToLinear()
	if fx, fy := m.W/(2*w), m.H/(2*h); fx >= 2 || fy >= 2 {
		l = boxReduce(l, max(1, fx), max(1, fy))
	}
	l = resampleH(l, w, f)
	l = resampleV(l, h, f)
	return l.ToImage(m)
}

// Fit returns the largest size with the source aspect ratio inside maxW x maxH.
func Fit(w, h, maxW, maxH int) (int, int) {
	s := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	return max(1, int(math.Round(float64(w)*s))), max(1, int(math.Round(float64(h)*s)))
}

// Proxy returns a reduced copy whose long side is at most maxSide, with Scale
// set so pixel-unit parameters shrink to match. Small images come back as is.
func Proxy(m *Image, maxSide int) *Image {
	if max(m.W, m.H) <= maxSide {
		return m
	}
	w, h := Fit(m.W, m.H, maxSide, maxSide)
	p := Resize(m, w, h, CatmullRom)
	p.Scale = m.Scale * float64(w) / float64(m.W)
	return p
}

func boxReduce(l *Linear, fx, fy int) *Linear {
	w, h := l.W/fx, l.H/fy
	out := NewLinear(w, h)
	inv := 1 / float32(fx*fy)
	Rows(nil, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				var r, g, b, a float32
				for dy := 0; dy < fy; dy++ {
					row := 4 * ((y*fy+dy)*l.W + x*fx)
					for dx := 0; dx < fx; dx++ {
						p := l.Pix[row+4*dx : row+4*dx+4]
						r += p[0]
						g += p[1]
						b += p[2]
						a += p[3]
					}
				}
				o := 4 * (y*w + x)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = r*inv, g*inv, b*inv, a*inv
			}
		}
	})
	return out
}

type contrib struct {
	start   int
	weights []float32
}

func contributions(in, out int, f Filter) []contrib {
	scale := float64(out) / float64(in)
	support := f.support()
	stretch := 1.0
	if scale < 1 {
		stretch = 1 / scale
	}
	cs := make([]contrib, out)
	for i := range cs {
		center := (float64(i)+0.5)/scale - 0.5
		lo := int(math.Floor(center - support*stretch))
		hi := int(math.Ceil(center + support*stretch))
		ws := make([]float32, 0, hi-lo+1)
		var sum float64
		start := -1
		for j := lo; j <= hi; j++ {
			wv := f.weight((float64(j) - center) / stretch)
			if j < 0 || j >= in {
				// Clamp to the edge by folding the weight onto the border pixel.
				if wv == 0 {
					continue
				}
				jj := min(max(j, 0), in-1)
				if start < 0 {
					start = jj
				}
				idx := jj - start
				for len(ws) <= idx {
					ws = append(ws, 0)
				}
				if idx >= 0 {
					ws[idx] += float32(wv)
				}
				sum += wv
				continue
			}
			if start < 0 {
				start = j
			}
			idx := j - start
			for len(ws) <= idx {
				ws = append(ws, 0)
			}
			ws[idx] += float32(wv)
			sum += wv
		}
		if sum != 0 {
			for k := range ws {
				ws[k] /= float32(sum)
			}
		}
		if start < 0 {
			start = 0
		}
		cs[i] = contrib{start: start, weights: ws}
	}
	return cs
}

func resampleH(l *Linear, w int, f Filter) *Linear {
	if w == l.W {
		return l
	}
	cs := contributions(l.W, w, f)
	out := NewLinear(w, l.H)
	Rows(nil, l.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			src := l.Pix[4*y*l.W : 4*(y+1)*l.W]
			dst := out.Pix[4*y*w : 4*(y+1)*w]
			for x, c := range cs {
				var r, g, b, a float32
				for k, wt := range c.weights {
					p := src[4*(c.start+k) : 4*(c.start+k)+4]
					r += p[0] * wt
					g += p[1] * wt
					b += p[2] * wt
					a += p[3] * wt
				}
				dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = r, g, b, clamp01(a)
			}
		}
	})
	return out
}

func resampleV(l *Linear, h int, f Filter) *Linear {
	if h == l.H {
		return l
	}
	cs := contributions(l.H, h, f)
	out := NewLinear(l.W, h)
	Rows(nil, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			c := cs[y]
			dst := out.Pix[4*y*l.W : 4*(y+1)*l.W]
			for k, wt := range c.weights {
				src := l.Pix[4*(c.start+k)*l.W : 4*(c.start+k+1)*l.W]
				for i, v := range src {
					dst[i] += v * wt
				}
			}
			for i := 3; i < len(dst); i += 4 {
				dst[i] = clamp01(dst[i])
			}
		}
	})
	return out
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Clamp01 clamps v to 0..1.
func Clamp01(v float32) float32 { return clamp01(v) }
