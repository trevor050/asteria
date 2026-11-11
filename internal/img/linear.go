package img

// Linear is a premultiplied linear-light float image: the space for anything
// that mixes pixels (resampling, blurs, compositing). Pix holds R,G,B,A per
// pixel with colour already multiplied by alpha.
type Linear struct {
	W, H int
	Pix  []float32
}

// NewLinear returns a transparent linear image.
func NewLinear(w, h int) *Linear { return &Linear{W: w, H: h, Pix: make([]float32, 4*w*h)} }

// ToLinear converts to premultiplied linear light.
func (m *Image) ToLinear() *Linear {
	l := NewLinear(m.W, m.H)
	Rows(nil, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			a := F(m.Pix[i+3])
			l.Pix[i] = toLinear[m.Pix[i]] * a
			l.Pix[i+1] = toLinear[m.Pix[i+1]] * a
			l.Pix[i+2] = toLinear[m.Pix[i+2]] * a
			l.Pix[i+3] = a
		}
	})
	return l
}

// ToImage converts back to the working format, un-premultiplying. like
// supplies Deep/Scale/Quality.
func (l *Linear) ToImage(like *Image) *Image {
	out := like.Sized(l.W, l.H)
	Rows(nil, l.H, func(y0, y1 int) {
		for i := 4 * y0 * l.W; i < 4*y1*l.W; i += 4 {
			a := l.Pix[i+3]
			if a <= 1.0/65536 {
				continue // fully transparent: colour is meaningless, keep zeros
			}
			inv := 1 / a
			out.Pix[i] = Enc(l.Pix[i] * inv)
			out.Pix[i+1] = Enc(l.Pix[i+1] * inv)
			out.Pix[i+2] = Enc(l.Pix[i+2] * inv)
			out.Pix[i+3] = Q(a)
		}
	})
	return out
}
