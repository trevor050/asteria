package ops

import (
	"context"
	"fmt"
	"math"

	"asteria/internal/img"
)

// Detail and effects family. Handlers are named "effect.*"; skills live in
// skills/core/effects. See IMAGE_SKILLS.md for the conventions.

func init() {
	register(Handler{Name: "effect.sharpen", Pixel: pixSharpen, Live: true, Video: videoSharpen})
	register(Handler{Name: "effect.boxblur", Pixel: pixBoxBlur, Live: true, Video: videoBoxBlur})
	register(Handler{Name: "effect.motionblur", Pixel: pixMotionBlur})
	register(Handler{Name: "effect.zoomblur", Pixel: pixZoomBlur})
	register(Handler{Name: "effect.noise", Pixel: pixNoise, Live: true, Video: videoNoise})
	register(Handler{Name: "effect.grain", Pixel: pixGrain, Live: true})
	register(Handler{Name: "effect.denoise", Pixel: pixDenoise})
	register(Handler{Name: "effect.pixelate", Pixel: pixPixelate, Live: true, Video: videoPixelate})
	register(Handler{Name: "effect.mosaic", Pixel: pixMosaic, Live: true})
	register(Handler{Name: "effect.halftone", Pixel: pixHalftone})
	register(Handler{Name: "effect.dither", Pixel: pixDither})
	register(Handler{Name: "effect.vignette", Pixel: pixVignette, Live: true, Video: videoVignette})
	register(Handler{Name: "effect.glow", Pixel: pixGlow})
	register(Handler{Name: "effect.emboss", Pixel: pixEmboss, Live: true})
	register(Handler{Name: "effect.edges", Pixel: pixEdges, Live: true, Video: videoEdges})
	register(Handler{Name: "effect.aberration", Pixel: pixAberration, Live: true})
	register(Handler{Name: "effect.scanlines", Pixel: pixScanlines, Live: true})
	register(Handler{Name: "effect.glitch", Pixel: pixGlitch})
}

// ---------------------------------------------------------------------------
// Shared helpers (fx prefix)

// fxSeed fixes all pseudo-random effects so results are reproducible.
const fxSeed uint32 = 0x5eed1234

// fxHash is a small integer hash of a pixel position and a seed.
func fxHash(x, y int, seed uint32) uint32 {
	h := uint32(x)*0x9E3779B1 ^ uint32(y)*0x85EBCA77 ^ seed*0xC2B2AE3D
	h ^= h >> 15
	h *= 0x2C1B3C6D
	h ^= h >> 12
	h *= 0x297A2D39
	h ^= h >> 15
	return h
}

// fxRand returns a deterministic value in [0,1) for a position and seed.
func fxRand(x, y int, seed uint32) float32 {
	return float32(fxHash(x, y, seed)>>8) / float32(1<<24)
}

func fxSmooth(e0, e1, x float32) float32 {
	t := min(max((x-e0)/(e1-e0), 0), 1)
	return t * t * (3 - 2*t)
}

func fxClamp01(v float32) float32 { return min(max(v, 0), 1) }

// fxSample bilinearly samples a premultiplied linear image at (x,y), where
// pixel i has its centre at coordinate i. Edges clamp.
func fxSample(l *img.Linear, x, y float32) (r, g, b, a float32) {
	x = min(max(x, 0), float32(l.W-1))
	y = min(max(y, 0), float32(l.H-1))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, l.W-1), min(y0+1, l.H-1)
	fx, fy := x-float32(x0), y-float32(y0)
	p00 := l.Pix[4*(y0*l.W+x0):]
	p10 := l.Pix[4*(y0*l.W+x1):]
	p01 := l.Pix[4*(y1*l.W+x0):]
	p11 := l.Pix[4*(y1*l.W+x1):]
	w00, w10, w01, w11 := (1-fx)*(1-fy), fx*(1-fy), (1-fx)*fy, fx*fy
	r = p00[0]*w00 + p10[0]*w10 + p01[0]*w01 + p11[0]*w11
	g = p00[1]*w00 + p10[1]*w10 + p01[1]*w01 + p11[1]*w11
	b = p00[2]*w00 + p10[2]*w10 + p01[2]*w01 + p11[2]*w11
	a = p00[3]*w00 + p10[3]*w10 + p01[3]*w01 + p11[3]*w11
	return
}

// fxPlaneAt bilinearly samples a single-channel plane.
func fxPlaneAt(p []float32, w, h int, x, y float32) float32 {
	x = min(max(x, 0), float32(w-1))
	y = min(max(y, 0), float32(h-1))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := x-float32(x0), y-float32(y0)
	return p[y0*w+x0]*(1-fx)*(1-fy) + p[y0*w+x1]*fx*(1-fy) + p[y1*w+x0]*(1-fx)*fy + p[y1*w+x1]*fx*fy
}

// fxMapXY applies fn to every non-transparent pixel's straight linear colour
// with its coordinates. Alpha is untouched and transparent pixels stay blank.
func fxMapXY(ctx context.Context, m *img.Image, fn func(x, y int, r, g, b float32) (float32, float32, float32)) *img.Image {
	out := m.NewLike()
	img.Rows(ctx, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < m.W; x++ {
				i := 4 * (y*m.W + x)
				a := m.Pix[i+3]
				if a == 0 {
					continue
				}
				r, g, b := fn(x, y, img.Lin(m.Pix[i]), img.Lin(m.Pix[i+1]), img.Lin(m.Pix[i+2]))
				out.Pix[i], out.Pix[i+1], out.Pix[i+2] = img.Enc(r), img.Enc(g), img.Enc(b)
				out.Pix[i+3] = a
			}
		}
	})
	return out
}

// fxBoxAxis is a true box blur of radius r along one axis of a linear image.
func fxBoxAxis(ctx context.Context, l *img.Linear, r int, horizontal bool) *img.Linear {
	out := img.NewLinear(l.W, l.H)
	n, lines, step, lineStep := l.W, l.H, 4, 4*l.W
	if !horizontal {
		n, lines, step, lineStep = l.H, l.W, 4*l.W, 4
	}
	inv := 1 / float32(2*r+1)
	img.Rows(ctx, lines, func(a0, a1 int) {
		for line := a0; line < a1; line++ {
			base := line * lineStep
			at := func(i int) int { return base + min(max(i, 0), n-1)*step }
			var s [4]float32
			for i := -r; i <= r; i++ {
				o := at(i)
				for c := range s {
					s[c] += l.Pix[o+c]
				}
			}
			for i := 0; i < n; i++ {
				o := base + i*step
				for c := range s {
					out.Pix[o+c] = s[c] * inv
				}
				add, sub := at(i+r+1), at(i-r)
				for c := range s {
					s[c] += l.Pix[add+c] - l.Pix[sub+c]
				}
			}
		}
	})
	return out
}

// fxClampPremul clamps a premultiplied channel to [0, alpha] so results stay
// valid premultiplied colour.
func fxClampPremul(v, a float32) float32 { return min(max(v, 0), a) }

// ---------------------------------------------------------------------------
// Sharpen

// pixSharpen is an unsharp mask on premultiplied linear light: the detail
// (original minus blur) is added back, so flat areas and alpha are untouched.
func pixSharpen(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	amt, err := args.Amount("amount", 1.8, 1, 5)
	if err != nil {
		return nil, err
	}
	k := float32(amt - 1)
	if k <= 0 {
		return m, nil
	}
	l := m.ToLinear()
	sigma := 1.2 * m.Scale
	b := img.Blur(l, sigma, sigma)
	out := img.NewLinear(l.W, l.H)
	img.Rows(ctx, l.H, func(y0, y1 int) {
		for i := 4 * y0 * l.W; i < 4*y1*l.W; i += 4 {
			a := l.Pix[i+3]
			for c := 0; c < 3; c++ {
				v := l.Pix[i+c] + k*(l.Pix[i+c]-b.Pix[i+c])
				out.Pix[i+c] = fxClampPremul(v, a)
			}
			out.Pix[i+3] = a
		}
	})
	return out.ToImage(m), ctx.Err()
}

func videoSharpen(args Args) (string, error) {
	amt, err := args.Amount("amount", 1.8, 1, 5)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("unsharp=5:5:%.2f:5:5:0.0", amt-1), nil
}

// ---------------------------------------------------------------------------
// Blurs

// pixBoxBlur averages a (2r+1) square in premultiplied linear light.
func pixBoxBlur(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := args.Length("radius", 6, m.W, m.Scale, 500)
	if err != nil {
		return nil, err
	}
	r := int(math.Round(v))
	if r < 1 {
		return m, nil
	}
	l := fxBoxAxis(ctx, fxBoxAxis(ctx, m.ToLinear(), r, true), r, false)
	return l.ToImage(m), ctx.Err()
}

func videoBoxBlur(args Args) (string, error) {
	r, err := VideoLength(args, "radius", 6)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("boxblur=%d:1", int(math.Max(1, math.Min(math.Round(r), 100)))), nil
}

// pixMotionBlur averages bilinear taps along a line (horizontal unless the
// skill's fixed "angle" says otherwise) in premultiplied linear light.
func pixMotionBlur(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	length, err := args.Length("length", 20, m.W, m.Scale, 2000)
	if err != nil {
		return nil, err
	}
	ang, err := args.Degrees("angle", 0)
	if err != nil {
		return nil, err
	}
	if length < 1 {
		return m, nil
	}
	l := m.ToLinear()
	n := int(math.Ceil(length)) + 1
	cs, sn := float32(math.Cos(ang*math.Pi/180)), float32(math.Sin(ang*math.Pi/180))
	out := img.NewLinear(l.W, l.H)
	inv := 1 / float32(n)
	img.Rows(ctx, l.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < l.W; x++ {
				var s0, s1, s2, s3 float32
				for i := 0; i < n; i++ {
					t := (float32(i)/float32(n-1) - 0.5) * float32(length)
					r, g, b, a := fxSample(l, float32(x)+cs*t, float32(y)+sn*t)
					s0, s1, s2, s3 = s0+r, s1+g, s2+b, s3+a
				}
				o := 4 * (y*l.W + x)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = s0*inv, s1*inv, s2*inv, s3*inv
			}
		}
	})
	return out.ToImage(m), ctx.Err()
}

// pixZoomBlur averages taps along the ray through each pixel and the image
// centre (a radial blur), in premultiplied linear light.
func pixZoomBlur(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("strength", 15, 0, 100)
	if err != nil {
		return nil, err
	}
	s := float32(pct / 100)
	if s <= 0 {
		return m, nil
	}
	l := m.ToLinear()
	cx, cy := float32(l.W-1)/2, float32(l.H-1)/2
	rmax := math.Hypot(float64(cx), float64(cy))
	n := min(max(int(math.Ceil(float64(s)*rmax))+1, 3), 48)
	out := img.NewLinear(l.W, l.H)
	inv := 1 / float32(n)
	img.Rows(ctx, l.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < l.W; x++ {
				vx, vy := float32(x)-cx, float32(y)-cy
				var s0, s1, s2, s3 float32
				for i := 0; i < n; i++ {
					f := 1 + s*(0.5-float32(i)/float32(n-1))
					r, g, b, a := fxSample(l, cx+vx*f, cy+vy*f)
					s0, s1, s2, s3 = s0+r, s1+g, s2+b, s3+a
				}
				o := 4 * (y*l.W + x)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = s0*inv, s1*inv, s2*inv, s3*inv
			}
		}
	})
	return out.ToImage(m), ctx.Err()
}

// ---------------------------------------------------------------------------
// Noise and grain

// pixNoise adds independent per-channel triangular noise as light (linear).
// amount 12 gives a standard deviation of about 12/255 on mid-tones.
func pixNoise(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	amt, err := args.Signed("amount", 12, 0, 100)
	if err != nil {
		return nil, err
	}
	if amt == 0 {
		return m, nil
	}
	A := float32(amt / 100 * 0.9)
	tri := func(x, y int, seed uint32) float32 {
		return fxRand(x, y, seed) + fxRand(x, y, seed+101) - 1
	}
	out := fxMapXY(ctx, m, func(x, y int, r, g, b float32) (float32, float32, float32) {
		return r + A*tri(x, y, fxSeed+1), g + A*tri(x, y, fxSeed+2), b + A*tri(x, y, fxSeed+3)
	})
	return out, ctx.Err()
}

func videoNoise(args Args) (string, error) {
	amt, err := args.Signed("amount", 12, 0, 100)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("noise=alls=%d:allf=t", int(math.Max(1, math.Round(amt)))), nil
}

// pixGrain adds luminance-only grain: one noise field (lightly blurred with a
// 3x3 binomial kernel) moves all channels by the same linear amount, weighted
// toward mid-tones and highlights.
func pixGrain(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	amt, err := args.Signed("amount", 20, 0, 100)
	if err != nil {
		return nil, err
	}
	if amt == 0 {
		return m, nil
	}
	w, h := m.W, m.H
	raw := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			raw[y*w+x] = fxRand(x, y, fxSeed+7)*2 - 1
		}
	}
	// 3x3 binomial blur; its noise gain is 1/0.375, restored below.
	field := make([]float32, w*h)
	k := [3]float32{0.25, 0.5, 0.25}
	img.Rows(ctx, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				var s float32
				for dy := -1; dy <= 1; dy++ {
					yy := min(max(y+dy, 0), h-1)
					for dx := -1; dx <= 1; dx++ {
						xx := min(max(x+dx, 0), w-1)
						s += raw[yy*w+xx] * k[dy+1] * k[dx+1]
					}
				}
				field[y*w+x] = s / 0.375
			}
		}
	})
	A := float32(amt / 100 * 1.2)
	out := fxMapXY(ctx, m, func(x, y int, r, g, b float32) (float32, float32, float32) {
		d := field[y*w+x] * A * (0.08 + 0.92*img.Luma(r, g, b))
		return r + d, g + d, b + d
	})
	return out, ctx.Err()
}

// ---------------------------------------------------------------------------
// Denoise

var fxExpLUT = func() []float32 {
	t := make([]float32, 1025)
	for i := range t {
		t[i] = float32(math.Exp(-float64(i) / 128))
	}
	return t
}()

// pixDenoise is a two-pass bilateral filter in OKLab: neighbours are weighted
// by spatial distance and by how close their colour is, so flat noisy areas
// average out while strong edges keep their weight.
func pixDenoise(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	st, err := args.Signed("strength", 50, 0, 100)
	if err != nil {
		return nil, err
	}
	if st <= 0 {
		return m, nil
	}
	sigR := float32(0.02 + 0.18*st/100)
	sigS := math.Max(0.8, 1.5*m.Scale)
	rad := int(math.Ceil(2 * sigS))
	side := 2*rad + 1
	spatial := make([]float32, side*side)
	for dy := -rad; dy <= rad; dy++ {
		for dx := -rad; dx <= rad; dx++ {
			spatial[(dy+rad)*side+dx+rad] = float32(math.Exp(-float64(dx*dx+dy*dy) / (2 * sigS * sigS)))
		}
	}
	w, h := m.W, m.H
	alpha := make([]float32, w*h)
	cur := make([]float32, 3*w*h)
	for i := 0; i < w*h; i++ {
		alpha[i] = img.F(m.Pix[4*i+3])
		if alpha[i] == 0 {
			continue
		}
		L, A, B := img.LinearToOKLab(img.Lin(m.Pix[4*i]), img.Lin(m.Pix[4*i+1]), img.Lin(m.Pix[4*i+2]))
		cur[3*i], cur[3*i+1], cur[3*i+2] = L, A, B
	}
	inv2 := 1 / (2 * sigR * sigR)
	for pass := 0; pass < 2; pass++ {
		next := make([]float32, len(cur))
		img.Rows(ctx, h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					if alpha[i] == 0 {
						continue
					}
					cL, cA, cB := cur[3*i], cur[3*i+1], cur[3*i+2]
					var sw, sL, sA, sB float32
					for dy := -rad; dy <= rad; dy++ {
						yy := y + dy
						if yy < 0 || yy >= h {
							continue
						}
						for dx := -rad; dx <= rad; dx++ {
							xx := x + dx
							if xx < 0 || xx >= w {
								continue
							}
							j := yy*w + xx
							if alpha[j] == 0 {
								continue
							}
							dL, dA, dB := cur[3*j]-cL, cur[3*j+1]-cA, cur[3*j+2]-cB
							q := (dL*dL + dA*dA + dB*dB) * inv2
							if q >= 8 {
								continue
							}
							wt := spatial[(dy+rad)*side+dx+rad] * fxExpLUT[int(q*128)] * alpha[j]
							sw += wt
							sL += wt * cur[3*j]
							sA += wt * cur[3*j+1]
							sB += wt * cur[3*j+2]
						}
					}
					next[3*i], next[3*i+1], next[3*i+2] = sL/sw, sA/sw, sB/sw
				}
			}
		})
		cur = next
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	out := m.NewLike()
	img.Rows(ctx, h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			if alpha[i] == 0 {
				continue
			}
			r, g, b := img.OKLabToLinear(cur[3*i], cur[3*i+1], cur[3*i+2])
			out.Pix[4*i], out.Pix[4*i+1], out.Pix[4*i+2] = img.Enc(r), img.Enc(g), img.Enc(b)
			out.Pix[4*i+3] = m.Pix[4*i+3]
		}
	})
	return out, ctx.Err()
}

// ---------------------------------------------------------------------------
// Pixelate and mosaic

// fxBlocks replaces every b x b block (anchored at 0,0) with its average in
// premultiplied linear light.
func fxBlocks(ctx context.Context, l *img.Linear, b int) *img.Linear {
	out := img.NewLinear(l.W, l.H)
	rows := (l.H + b - 1) / b
	img.Rows(ctx, rows, func(r0, r1 int) {
		for br := r0; br < r1; br++ {
			y0, y1 := br*b, min((br+1)*b, l.H)
			for x0 := 0; x0 < l.W; x0 += b {
				x1 := min(x0+b, l.W)
				var s [4]float32
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						o := 4 * (y*l.W + x)
						for c := range s {
							s[c] += l.Pix[o+c]
						}
					}
				}
				inv := 1 / float32((y1-y0)*(x1-x0))
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						o := 4 * (y*l.W + x)
						for c := range s {
							out.Pix[o+c] = s[c] * inv
						}
					}
				}
			}
		}
	})
	return out
}

func pixPixelate(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := args.Length("size", 12, m.W, m.Scale, 2000)
	if err != nil {
		return nil, err
	}
	b := int(math.Round(v))
	if b < 2 {
		return m, nil
	}
	return fxBlocks(ctx, m.ToLinear(), b).ToImage(m), ctx.Err()
}

func videoPixelate(args Args) (string, error) {
	v, err := VideoLength(args, "size", 12)
	if err != nil {
		return "", err
	}
	n := int(math.Max(2, math.Min(math.Round(v), 500)))
	return fmt.Sprintf("pixelize=w=%d:h=%d", n, n), nil
}

// pixMosaic is pixelate with a darker grout line along the top and left edge
// of every tile (grout darkens linear light by 45%).
func pixMosaic(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := args.Length("size", 16, m.W, m.Scale, 2000)
	if err != nil {
		return nil, err
	}
	b := int(math.Round(v))
	if b < 2 {
		return m, nil
	}
	l := fxBlocks(ctx, m.ToLinear(), b)
	if b >= 4 {
		g := max(1, int(math.Round(float64(b)/12)))
		img.Rows(ctx, l.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < l.W; x++ {
					if x%b < g || y%b < g {
						o := 4 * (y*l.W + x)
						l.Pix[o] *= 0.55
						l.Pix[o+1] *= 0.55
						l.Pix[o+2] *= 0.55
					}
				}
			}
		})
	}
	return l.ToImage(m), ctx.Err()
}

// ---------------------------------------------------------------------------
// Halftone

// fxDotRadius maps ink coverage (0..1) to a dot radius in pixels. Up to
// pi/4 coverage the dot area matches the coverage; beyond that dots grow
// until they merge into solid black.
func fxDotRadius(cov, cell float32) float32 {
	if cov <= 0.785 {
		return cell * float32(math.Sqrt(float64(cov)/math.Pi))
	}
	t := (cov - 0.785) / 0.215
	return cell * (0.5 + 0.3*min(t, 1))
}

// pixHalftone draws black dots on white on a 45-degree grid; each dot's area
// matches the darkness (1 - linear luminance over white) of the blurred
// source at the dot centre, so the dots average back to the original tone.
func pixHalftone(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	v, err := args.Length("size", 8, m.W, m.Scale, 500)
	if err != nil {
		return nil, err
	}
	cell := float32(math.Max(v, 3))
	w, h := m.W, m.H
	bl := img.Blur(m.ToLinear(), float64(cell)*0.3, float64(cell)*0.3)
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = img.Luma(bl.Pix[4*i], bl.Pix[4*i+1], bl.Pix[4*i+2]) + (1 - bl.Pix[4*i+3])
	}
	const c = float32(0.70710678)
	out := m.NewLike()
	img.Rows(ctx, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				a := m.Pix[4*(y*w+x)+3]
				if a == 0 {
					continue
				}
				qx, qy := float32(x), float32(y)
				U, V := (qx+qy)*c, (qy-qx)*c
				iu, iv := int(math.Floor(float64(U/cell))), int(math.Floor(float64(V/cell)))
				var best float32
				for dj := -1; dj <= 1; dj++ {
					for di := -1; di <= 1; di++ {
						cu, cv := (float32(iu+di)+0.5)*cell, (float32(iv+dj)+0.5)*cell
						sx, sy := (cu-cv)*c, (cu+cv)*c
						cov := fxClamp01(1 - fxPlaneAt(plane, w, h, sx, sy))
						r := fxDotRadius(cov, cell)
						if r <= 0 {
							continue
						}
						d := float32(math.Hypot(float64(U-cu), float64(V-cv)))
						cv2 := fxClamp01(r - d + 0.5)
						if r < 0.5 {
							cv2 *= 4 * r * r
						}
						best = max(best, cv2)
					}
				}
				e := img.Enc(1 - best)
				i := 4 * (y*w + x)
				out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = e, e, e, a
			}
		}
	})
	return out, ctx.Err()
}

// ---------------------------------------------------------------------------
// Dither

var fxPalettes = map[string][]uint32{
	"bw":      {0x000000, 0xffffff},
	"gameboy": {0x0f380f, 0x306230, 0x8bac0f, 0x9bbc0f},
	"cga":     {0x000000, 0x55ffff, 0xff55ff, 0xffffff},
	"pico8":   {0x000000, 0x1d2b53, 0x7e2553, 0x008751, 0xab5236, 0x5f574f, 0xc2c3c7, 0xfff1e8, 0xff004d, 0xffa300, 0xffec27, 0x00e436, 0x29adff, 0x83769c, 0xff77a8, 0xffccaa},
	"web":     nil, // 6x6x6 cube, built in fxPalette
}

// fxPalette returns the palette as 8-bit hex colours.
func fxPalette(name string) []uint32 {
	if name == "web" {
		out := make([]uint32, 0, 216)
		for r := 0; r < 6; r++ {
			for g := 0; g < 6; g++ {
				for b := 0; b < 6; b++ {
					out = append(out, uint32(r*51)<<16|uint32(g*51)<<8|uint32(b*51))
				}
			}
		}
		return out
	}
	return fxPalettes[name]
}

// pixDither is Floyd-Steinberg error diffusion (serpentine) to a fixed
// palette. Error is diffused in linear light and the nearest colour is chosen
// in the same space (luminance-weighted), so the average tone is preserved: a
// 50% grey becomes ~21% white dots, as much light as the grey. Choosing in a
// perceptual space while diffusing linear light biases every tone brighter.
// Alpha is preserved and transparent pixels do not take part.
func pixDither(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	name, err := args.Choice("palette", "bw", []string{"bw", "gameboy", "cga", "pico8", "web"}, map[string]string{
		"gb": "gameboy", "game boy": "gameboy", "1bit": "bw", "1-bit": "bw", "black and white": "bw", "mono": "bw",
		"pico": "pico8", "pico-8": "pico8", "websafe": "web", "216": "web", "ega": "cga",
	})
	if err != nil {
		return nil, err
	}
	hex := fxPalette(name)
	n := len(hex)
	enc := make([][3]uint16, n)
	lin := make([][3]float32, n)
	for i, c := range hex {
		enc[i] = [3]uint16{uint16(c>>16&0xff) * 257, uint16(c>>8&0xff) * 257, uint16(c&0xff) * 257}
		lin[i] = [3]float32{img.Lin(enc[i][0]), img.Lin(enc[i][1]), img.Lin(enc[i][2])}
	}
	w, h := m.W, m.H
	out := m.NewLike()
	cur := make([]float32, 3*(w+2))
	nxt := make([]float32, 3*(w+2))
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for i := range nxt {
			nxt[i] = 0
		}
		dir, x0 := 1, 0
		if y%2 == 1 {
			dir, x0 = -1, w-1
		}
		for k := 0; k < w; k++ {
			x := x0 + dir*k
			i := 4 * (y*w + x)
			a := m.Pix[i+3]
			if a == 0 {
				continue
			}
			e := 3 * (x + 1)
			var v [3]float32
			for c := 0; c < 3; c++ {
				v[c] = fxClamp01(img.Lin(m.Pix[i+c]) + cur[e+c])
			}
			best, bd := 0, float32(math.MaxFloat32)
			for j := range lin {
				dr, dg, db := lin[j][0]-v[0], lin[j][1]-v[1], lin[j][2]-v[2]
				if d := 0.2126*dr*dr + 0.7152*dg*dg + 0.0722*db*db; d < bd {
					best, bd = j, d
				}
			}
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = enc[best][0], enc[best][1], enc[best][2], a
			fw := 3 * (x + 1 + dir) // next pixel in scan direction
			bk := 3 * (x + 1 - dir) // behind
			for c := 0; c < 3; c++ {
				er := v[c] - lin[best][c]
				cur[fw+c] += er * 7 / 16
				nxt[bk+c] += er * 3 / 16
				nxt[e+c] += er * 5 / 16
				nxt[fw+c] += er * 1 / 16
			}
		}
		cur, nxt = nxt, cur
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Vignette

// pixVignette multiplies linear light by a smooth radial falloff that is 1 in
// the middle and (1 - strength) at the corners of the (elliptical) frame.
func pixVignette(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("amount", 50, 0, 100)
	if err != nil {
		return nil, err
	}
	s := float32(pct / 100)
	if s <= 0 {
		return m, nil
	}
	hx, hy := float32(m.W)/2, float32(m.H)/2
	out := fxMapXY(ctx, m, func(x, y int, r, g, b float32) (float32, float32, float32) {
		dx, dy := (float32(x)+0.5-hx)/hx, (float32(y)+0.5-hy)/hy
		rr := float32(math.Sqrt(float64(dx*dx+dy*dy))) / 1.4142135
		f := 1 - s*fxSmooth(0.3, 1, rr)
		return r * f, g * f, b * f
	})
	return out, ctx.Err()
}

func videoVignette(args Args) (string, error) {
	pct, err := args.Signed("amount", 50, 0, 100)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("vignette=angle=%.3f", math.Max(0.05, pct/100*math.Pi/2)), nil
}

// ---------------------------------------------------------------------------
// Glow

// pixGlow blurs the bright parts of the picture and screens them back on, in
// premultiplied linear light.
func pixGlow(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("strength", 40, 0, 100)
	if err != nil {
		return nil, err
	}
	k := float32(pct / 100 * 1.5)
	if k <= 0 {
		return m, nil
	}
	l := m.ToLinear()
	bright := img.NewLinear(l.W, l.H)
	img.Rows(ctx, l.H, func(y0, y1 int) {
		for i := 4 * y0 * l.W; i < 4*y1*l.W; i += 4 {
			a := l.Pix[i+3]
			if a <= 0 {
				continue
			}
			y := img.Luma(l.Pix[i], l.Pix[i+1], l.Pix[i+2]) / a
			w := fxSmooth(0.2, 0.7, y)
			for c := 0; c < 4; c++ {
				bright.Pix[i+c] = l.Pix[i+c] * w
			}
		}
	})
	sigma := math.Max(1, 0.025*float64(max(l.W, l.H)))
	blur := img.Blur(bright, sigma, sigma)
	out := img.NewLinear(l.W, l.H)
	img.Rows(ctx, l.H, func(y0, y1 int) {
		for i := 4 * y0 * l.W; i < 4*y1*l.W; i += 4 {
			var v [4]float32
			for c := 0; c < 4; c++ {
				g := fxClamp01(k * blur.Pix[i+c])
				v[c] = l.Pix[i+c] + g - l.Pix[i+c]*g
			}
			a := fxClamp01(v[3])
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = fxClampPremul(v[0], a), fxClampPremul(v[1], a), fxClampPremul(v[2], a), a
		}
	})
	return out.ToImage(m), ctx.Err()
}

// ---------------------------------------------------------------------------
// Emboss and edges

// pixEmboss shows the diagonal gradient of encoded luminance as grey relief
// around 0.5 (transparent areas count as mid grey). Light comes from the
// top left.
func pixEmboss(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("strength", 50, 0, 100)
	if err != nil {
		return nil, err
	}
	gain := float32(pct / 100)
	w, h := m.W, m.H
	plane := make([]float32, w*h)
	for i := range plane {
		a := img.F(m.Pix[4*i+3])
		y := 0.2126*img.F(m.Pix[4*i]) + 0.7152*img.F(m.Pix[4*i+1]) + 0.0722*img.F(m.Pix[4*i+2])
		plane[i] = a*y + (1-a)*0.5
	}
	kern := [9]float32{-1, -1, 0, -1, 0, 1, 0, 1, 1}
	out := m.NewLike()
	img.Rows(ctx, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := 4 * (y*w + x)
				a := m.Pix[i+3]
				if a == 0 {
					continue
				}
				var d float32
				for dy := -1; dy <= 1; dy++ {
					yy := min(max(y+dy, 0), h-1)
					for dx := -1; dx <= 1; dx++ {
						xx := min(max(x+dx, 0), w-1)
						d += kern[(dy+1)*3+dx+1] * plane[yy*w+xx]
					}
				}
				e := img.Q(0.5 + gain*d)
				out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = e, e, e, a
			}
		}
	})
	return out, ctx.Err()
}

// pixEdges is a Sobel filter on linear luminance, drawn as white edges on
// black. The magnitude is normalised so a black-to-white step is 1, then
// multiplied by 2*strength/100.
func pixEdges(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("strength", 100, 0, 100)
	if err != nil {
		return nil, err
	}
	gain := float32(pct / 100 * 2)
	l := m.ToLinear()
	w, h := m.W, m.H
	plane := make([]float32, w*h)
	for i := range plane {
		plane[i] = img.Luma(l.Pix[4*i], l.Pix[4*i+1], l.Pix[4*i+2])
	}
	out := m.NewLike()
	img.Rows(ctx, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			ym, yp := max(y-1, 0), min(y+1, h-1)
			for x := 0; x < w; x++ {
				i := 4 * (y*w + x)
				a := m.Pix[i+3]
				if a == 0 {
					continue
				}
				xm, xp := max(x-1, 0), min(x+1, w-1)
				p := func(xx, yy int) float32 { return plane[yy*w+xx] }
				gx := (p(xp, ym) + 2*p(xp, y) + p(xp, yp)) - (p(xm, ym) + 2*p(xm, y) + p(xm, yp))
				gy := (p(xm, yp) + 2*p(x, yp) + p(xp, yp)) - (p(xm, ym) + 2*p(x, ym) + p(xp, ym))
				mag := float32(math.Sqrt(float64(gx*gx+gy*gy))) / 4
				e := img.Enc(mag * gain)
				out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = e, e, e, a
			}
		}
	})
	return out, ctx.Err()
}

func videoEdges(Args) (string, error) { return "edgedetect", nil }

// ---------------------------------------------------------------------------
// Chromatic aberration

// pixAberration shifts red outward and blue inward along the radius, by
// shift*r/rmax pixels (so it grows toward the corners), sampling bilinearly
// in premultiplied linear light. Green stays put.
func pixAberration(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	shift, err := args.Length("shift", 4, m.W, m.Scale, 200)
	if err != nil {
		return nil, err
	}
	if shift <= 0 {
		return m, nil
	}
	l := m.ToLinear()
	cx, cy := float64(l.W-1)/2, float64(l.H-1)/2
	rmax := math.Hypot(cx, cy)
	if rmax < 1 {
		return m, nil
	}
	out := img.NewLinear(l.W, l.H)
	img.Rows(ctx, l.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < l.W; x++ {
				dx, dy := float64(x)-cx, float64(y)-cy
				r := math.Hypot(dx, dy)
				var sx, sy float32
				if r > 1e-6 {
					sh := shift * r / rmax
					sx, sy = float32(dx/r*sh), float32(dy/r*sh)
				}
				fx, fy := float32(x), float32(y)
				rr, _, _, ar := fxSample(l, fx-sx, fy-sy)
				o := 4 * (y*l.W + x)
				gg, ag := l.Pix[o+1], l.Pix[o+3]
				_, _, bb, ab := fxSample(l, fx+sx, fy+sy)
				a := max(ar, ag, ab)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = fxClampPremul(rr, a), fxClampPremul(gg, a), fxClampPremul(bb, a), a
			}
		}
	})
	return out.ToImage(m), ctx.Err()
}

// ---------------------------------------------------------------------------
// Scanlines

// pixScanlines darkens alternate pairs of rows (linear light) and applies a
// faint RGB triad column mask: every third column keeps one channel at full
// strength and dims the other two by 12%.
func pixScanlines(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("amount", 35, 0, 100)
	if err != nil {
		return nil, err
	}
	s := float32(pct / 100)
	if s <= 0 {
		return m, nil
	}
	band := max(1, int(math.Round(2*m.Scale)))
	col := max(1, int(math.Round(m.Scale)))
	out := fxMapXY(ctx, m, func(x, y int, r, g, b float32) (float32, float32, float32) {
		f := float32(1)
		if (y/band)%2 == 1 {
			f = 1 - s
		}
		mk := [3]float32{0.88, 0.88, 0.88}
		mk[(x/col)%3] = 1
		return r * f * mk[0], g * f * mk[1], b * f * mk[2]
	})
	return out, ctx.Err()
}

// ---------------------------------------------------------------------------
// Glitch

// pixGlitch cuts the picture into horizontal bands (heights from a fixed
// hash), slides some sideways with wrap-around, and on some bands splits the
// channels apart. Integer shifts only, so it is exact and reproducible.
func pixGlitch(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	pct, err := args.Signed("intensity", 50, 0, 100)
	if err != nil {
		return nil, err
	}
	k := float32(pct / 100)
	if k <= 0 {
		return m, nil
	}
	w, h := m.W, m.H
	disp := make([]int, h)
	split := make([]int, h)
	minH := max(1, h/40)
	maxH := max(minH, h/6)
	for y, band := 0, 0; y < h; band++ {
		bh := min(minH+int(fxRand(band, 1, fxSeed)*float32(maxH-minH+1)), maxH)
		d, s := 0, 0
		if fxRand(band, 2, fxSeed) < 0.15+0.55*k {
			d = int((fxRand(band, 3, fxSeed)*2 - 1) * k * 0.2 * float32(w))
		}
		if fxRand(band, 4, fxSeed) < 0.1+0.5*k {
			s = 1 + int((0.5+0.5*fxRand(band, 5, fxSeed))*k*0.04*float32(w))
			if fxRand(band, 6, fxSeed) < 0.5 {
				s = -s
			}
		}
		for j := 0; j < bh && y < h; j++ {
			disp[y], split[y] = d, s
			y++
		}
	}
	wrap := func(x int) int { return ((x % w) + w) % w }
	l := m.ToLinear()
	out := img.NewLinear(w, h)
	img.Rows(ctx, h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				sx := wrap(x + disp[y])
				ro := 4 * (y*w + wrap(sx+split[y]))
				go_ := 4 * (y*w + sx)
				bo := 4 * (y*w + wrap(sx-split[y]))
				a := max(l.Pix[ro+3], l.Pix[go_+3], l.Pix[bo+3])
				o := 4 * (y*w + x)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = fxClampPremul(l.Pix[ro], a), fxClampPremul(l.Pix[go_+1], a), fxClampPremul(l.Pix[bo+2], a), a
			}
		}
	})
	return out.ToImage(m), ctx.Err()
}
