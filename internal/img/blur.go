package img

import "math"

// Blur returns a gaussian blur of l with standard deviation sigma (pixels),
// in premultiplied linear light. Small sigmas use an exact separable kernel;
// large ones use three box passes, which match a gaussian closely and cost
// the same at any radius. Edges clamp.
func Blur(l *Linear, sigmaX, sigmaY float64) *Linear {
	out := l
	if sigmaX > 0.01 {
		out = blurAxis(out, sigmaX, true)
	}
	if sigmaY > 0.01 {
		out = blurAxis(out, sigmaY, false)
	}
	if out == l {
		out = &Linear{W: l.W, H: l.H, Pix: append([]float32(nil), l.Pix...)}
	}
	return out
}

func blurAxis(l *Linear, sigma float64, horizontal bool) *Linear {
	if sigma <= 6 {
		return convolve(l, gaussKernel(sigma), horizontal)
	}
	out := l
	for _, b := range boxesForGauss(sigma, 3) {
		out = boxBlur(out, (b-1)/2, horizontal)
	}
	return out
}

func gaussKernel(sigma float64) []float32 {
	r := int(math.Ceil(sigma * 3))
	k := make([]float32, 2*r+1)
	var sum float64
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = float32(v)
		sum += v
	}
	for i := range k {
		k[i] /= float32(sum)
	}
	return k
}

// boxesForGauss returns n odd box widths whose successive application
// approximates a gaussian of the given sigma (Kovesi).
func boxesForGauss(sigma float64, n int) []int {
	wIdeal := math.Sqrt(12*sigma*sigma/float64(n) + 1)
	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	wu := wl + 2
	mIdeal := (12*sigma*sigma - float64(n*wl*wl) - 4*float64(n*wl) - 3*float64(n)) / (-4*float64(wl) - 4)
	m := int(math.Round(mIdeal))
	out := make([]int, n)
	for i := range out {
		if i < m {
			out[i] = wl
		} else {
			out[i] = wu
		}
	}
	return out
}

// convolve applies a 1-D kernel along one axis.
func convolve(l *Linear, k []float32, horizontal bool) *Linear {
	r := len(k) / 2
	out := NewLinear(l.W, l.H)
	if horizontal {
		Rows(nil, l.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := l.Pix[4*y*l.W : 4*(y+1)*l.W]
				dst := out.Pix[4*y*l.W : 4*(y+1)*l.W]
				for x := 0; x < l.W; x++ {
					var s0, s1, s2, s3 float32
					for i, wt := range k {
						xx := min(max(x+i-r, 0), l.W-1)
						p := row[4*xx : 4*xx+4]
						s0 += p[0] * wt
						s1 += p[1] * wt
						s2 += p[2] * wt
						s3 += p[3] * wt
					}
					dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = s0, s1, s2, s3
				}
			}
		})
		return out
	}
	Rows(nil, l.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			dst := out.Pix[4*y*l.W : 4*(y+1)*l.W]
			for i, wt := range k {
				yy := min(max(y+i-r, 0), l.H-1)
				src := l.Pix[4*yy*l.W : 4*(yy+1)*l.W]
				for j, v := range src {
					dst[j] += v * wt
				}
			}
		}
	})
	return out
}

// boxBlur is a running-sum box blur of radius r along one axis.
func boxBlur(l *Linear, r int, horizontal bool) *Linear {
	if r < 1 {
		return l
	}
	out := NewLinear(l.W, l.H)
	n, lines, step, lineStep := l.W, l.H, 4, 4*l.W
	if !horizontal {
		n, lines, step, lineStep = l.H, l.W, 4*l.W, 4
	}
	inv := 1 / float32(2*r+1)
	Rows(nil, lines, func(a0, a1 int) {
		for line := a0; line < a1; line++ {
			base := line * lineStep
			at := func(i int) []float32 {
				i = min(max(i, 0), n-1)
				o := base + i*step
				return l.Pix[o : o+4]
			}
			var s [4]float32
			for i := -r; i <= r; i++ {
				p := at(i)
				s[0] += p[0]
				s[1] += p[1]
				s[2] += p[2]
				s[3] += p[3]
			}
			for i := 0; i < n; i++ {
				o := base + i*step
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = s[0]*inv, s[1]*inv, s[2]*inv, s[3]*inv
				add, sub := at(i+r+1), at(i-r)
				s[0] += add[0] - sub[0]
				s[1] += add[1] - sub[1]
				s[2] += add[2] - sub[2]
				s[3] += add[3] - sub[3]
			}
		}
	})
	return out
}
