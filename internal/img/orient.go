package img

// Rotate90 turns the image a quarter turn clockwise.
func Rotate90(m *Image) *Image {
	out := m.Sized(m.H, m.W)
	Rows(nil, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < m.W; x++ {
				s, d := m.Off(x, y), out.Off(m.H-1-y, x)
				copy(out.Pix[d:d+4], m.Pix[s:s+4])
			}
		}
	})
	return out
}

// Rotate180 turns the image half a turn.
func Rotate180(m *Image) *Image {
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < m.W; x++ {
				s, d := m.Off(x, y), out.Off(m.W-1-x, m.H-1-y)
				copy(out.Pix[d:d+4], m.Pix[s:s+4])
			}
		}
	})
	return out
}

// Rotate270 turns the image a quarter turn counter-clockwise.
func Rotate270(m *Image) *Image {
	out := m.Sized(m.H, m.W)
	Rows(nil, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < m.W; x++ {
				s, d := m.Off(x, y), out.Off(y, m.W-1-x)
				copy(out.Pix[d:d+4], m.Pix[s:s+4])
			}
		}
	})
	return out
}

// FlipH mirrors left-right; FlipV mirrors top-bottom.
func FlipH(m *Image) *Image {
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < m.W; x++ {
				s, d := m.Off(x, y), out.Off(m.W-1-x, y)
				copy(out.Pix[d:d+4], m.Pix[s:s+4])
			}
		}
	})
	return out
}

func FlipV(m *Image) *Image {
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			copy(out.Pix[4*(m.H-1-y)*m.W:4*(m.H-y)*m.W], m.Pix[4*y*m.W:4*(y+1)*m.W])
		}
	})
	return out
}
