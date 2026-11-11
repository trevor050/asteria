package img

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ICC handling. Go's decoders ignore embedded profiles, so a Display P3
// screenshot or an Adobe RGB photo would be read as if it were sRGB and come
// out dull. ReadICC pulls the profile out of JPEG, PNG and WebP files and
// ToSRGB converts matrix/TRC RGB profiles (the kind cameras, phones and
// screenshots use) to sRGB. LUT-based profiles (CMYK, printer profiles) are
// left alone; the pixels are then treated as sRGB, which is what every other
// Go tool does.

// ReadICC returns the embedded ICC profile of a JPEG, PNG or WebP file, or
// nil when there is none.
func ReadICC(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return nil
	}
	switch {
	case head[0] == 0xFF && head[1] == 0xD8:
		return jpegICC(data)
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return pngICC(data)
	case bytes.HasPrefix(head, []byte("RIFF")) && string(head[8:12]) == "WEBP":
		return webpICC(data)
	}
	_ = filepath.Ext(path)
	return nil
}

func jpegICC(d []byte) []byte {
	type part struct {
		seq  byte
		data []byte
	}
	var parts []part
	for i := 2; i+4 <= len(d); {
		if d[i] != 0xFF {
			return nil
		}
		marker := d[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan / end
			break
		}
		n := int(binary.BigEndian.Uint16(d[i+2:]))
		if i+2+n > len(d) {
			break
		}
		seg := d[i+4 : i+2+n]
		if marker == 0xE2 && bytes.HasPrefix(seg, []byte("ICC_PROFILE\x00")) && len(seg) > 14 {
			parts = append(parts, part{seq: seg[12], data: seg[14:]})
		}
		i += 2 + n
	}
	if len(parts) == 0 {
		return nil
	}
	sort.Slice(parts, func(a, b int) bool { return parts[a].seq < parts[b].seq })
	var out []byte
	for _, p := range parts {
		out = append(out, p.data...)
	}
	return out
}

func pngICC(d []byte) []byte {
	for i := 8; i+12 <= len(d); {
		n := int(binary.BigEndian.Uint32(d[i:]))
		typ := string(d[i+4 : i+8])
		if i+12+n > len(d) {
			return nil
		}
		body := d[i+8 : i+8+n]
		if typ == "iCCP" {
			z := bytes.IndexByte(body, 0)
			if z < 0 || z+2 > len(body) {
				return nil
			}
			r, err := zlib.NewReader(bytes.NewReader(body[z+2:]))
			if err != nil {
				return nil
			}
			out, err := io.ReadAll(io.LimitReader(r, 8<<20))
			if err != nil {
				return nil
			}
			return out
		}
		if typ == "IDAT" {
			return nil // iCCP must come before image data
		}
		i += 12 + n
	}
	return nil
}

func webpICC(d []byte) []byte {
	for i := 12; i+8 <= len(d); {
		typ := string(d[i : i+4])
		n := int(binary.LittleEndian.Uint32(d[i+4:]))
		if i+8+n > len(d) {
			return nil
		}
		if typ == "ICCP" {
			return d[i+8 : i+8+n]
		}
		i += 8 + n + n%2
	}
	return nil
}

// Profile is a parsed matrix/TRC RGB profile.
type Profile struct {
	Desc string
	// toXYZ maps linear RGB to PCS XYZ (D50), columns are the colorants.
	toXYZ [3][3]float64
	trc   [3]curve
}

type curve struct {
	gamma  float64   // when table is nil and params is nil
	table  []float64 // sampled curve, 0..1
	params []float64 // parametric curve (ICC 'para')
	kind   int
}

func (c curve) eval(x float64) float64 {
	switch {
	case c.table != nil:
		n := len(c.table) - 1
		f := x * float64(n)
		i := int(f)
		if i >= n {
			return c.table[n]
		}
		t := f - float64(i)
		return c.table[i]*(1-t) + c.table[i+1]*t
	case c.params != nil:
		p := c.params
		g := p[0]
		switch c.kind {
		case 0:
			return math.Pow(x, g)
		case 1:
			if x >= -p[2]/p[1] {
				return math.Pow(p[1]*x+p[2], g)
			}
			return 0
		case 2:
			if x >= -p[2]/p[1] {
				return math.Pow(p[1]*x+p[2], g) + p[3]
			}
			return p[3]
		case 3:
			if x >= p[4] {
				return math.Pow(p[1]*x+p[2], g)
			}
			return p[3] * x
		case 4:
			if x >= p[4] {
				return math.Pow(p[1]*x+p[2], g) + p[5]
			}
			return p[3]*x + p[6]
		}
	}
	return math.Pow(x, c.gamma)
}

// ParseProfile reads a matrix/TRC RGB display profile. It returns nil for
// anything else (CMYK, LUT-based, grey, malformed).
func ParseProfile(b []byte) *Profile {
	if len(b) < 132 || string(b[16:20]) != "RGB " || string(b[36:40]) != "acsp" {
		return nil
	}
	count := int(binary.BigEndian.Uint32(b[128:]))
	tags := map[string][]byte{}
	for i := 0; i < count && 132+12*i+12 <= len(b); i++ {
		e := b[132+12*i:]
		sig := string(e[:4])
		off := int(binary.BigEndian.Uint32(e[4:]))
		n := int(binary.BigEndian.Uint32(e[8:]))
		if off < 0 || n < 0 || off+n > len(b) {
			return nil
		}
		tags[sig] = b[off : off+n]
	}
	p := &Profile{Desc: descText(tags["desc"])}
	for c, sig := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		t := tags[sig]
		if len(t) < 20 || string(t[:4]) != "XYZ " {
			return nil
		}
		for r := 0; r < 3; r++ {
			p.toXYZ[r][c] = s15f16(t[8+4*r:])
		}
	}
	for c, sig := range []string{"rTRC", "gTRC", "bTRC"} {
		cv, ok := parseCurve(tags[sig])
		if !ok {
			return nil
		}
		p.trc[c] = cv
	}
	return p
}

func s15f16(b []byte) float64 { return float64(int32(binary.BigEndian.Uint32(b))) / 65536 }

func parseCurve(t []byte) (curve, bool) {
	if len(t) < 12 {
		return curve{}, false
	}
	switch string(t[:4]) {
	case "curv":
		n := int(binary.BigEndian.Uint32(t[8:]))
		if n == 0 {
			return curve{gamma: 1}, true
		}
		if n == 1 && len(t) >= 14 {
			return curve{gamma: float64(binary.BigEndian.Uint16(t[12:])) / 256}, true
		}
		if len(t) < 12+2*n {
			return curve{}, false
		}
		tab := make([]float64, n)
		for i := range tab {
			tab[i] = float64(binary.BigEndian.Uint16(t[12+2*i:])) / 65535
		}
		return curve{table: tab}, true
	case "para":
		kind := int(binary.BigEndian.Uint16(t[8:]))
		need := []int{1, 3, 4, 5, 7}
		if kind < 0 || kind > 4 || len(t) < 12+4*need[kind] {
			return curve{}, false
		}
		ps := make([]float64, 7)
		for i := 0; i < need[kind]; i++ {
			ps[i] = s15f16(t[12+4*i:])
		}
		return curve{params: ps, kind: kind}, true
	}
	return curve{}, false
}

func descText(t []byte) string {
	if len(t) < 12 {
		return ""
	}
	switch string(t[:4]) {
	case "desc":
		n := int(binary.BigEndian.Uint32(t[8:]))
		if 12+n <= len(t) {
			return strings.TrimRight(string(t[12:12+n]), "\x00")
		}
	case "mluc":
		if len(t) >= 28 {
			n := int(binary.BigEndian.Uint32(t[20:]))
			off := int(binary.BigEndian.Uint32(t[24:]))
			if off+n <= len(t) {
				r := []rune{}
				for i := off; i+1 < off+n; i += 2 {
					r = append(r, rune(binary.BigEndian.Uint16(t[i:])))
				}
				return string(r)
			}
		}
	}
	return ""
}

// sRGB colorants adapted to D50, as in the ICC sRGB profile.
var srgbToXYZ = [3][3]float64{
	{0.4360747, 0.3850649, 0.1430804},
	{0.2225045, 0.7168786, 0.0606169},
	{0.0139322, 0.0971045, 0.7141733},
}

// IsSRGB reports whether the profile is (close enough to) sRGB, in which
// case no conversion is needed.
func (p *Profile) IsSRGB() bool {
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			if math.Abs(p.toXYZ[r][c]-srgbToXYZ[r][c]) > 0.003 {
				return false
			}
		}
	}
	for _, x := range []float64{0.02, 0.2, 0.5, 0.8} {
		for c := 0; c < 3; c++ {
			if math.Abs(p.trc[c].eval(x)-SRGBToLinear(x)) > 0.004 {
				return false
			}
		}
	}
	return true
}

func inv3(m [3][3]float64) [3][3]float64 {
	a, b, c := m[0][0], m[0][1], m[0][2]
	d, e, f := m[1][0], m[1][1], m[1][2]
	g, h, i := m[2][0], m[2][1], m[2][2]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	return [3][3]float64{
		{(e*i - f*h) / det, (c*h - b*i) / det, (b*f - c*e) / det},
		{(f*g - d*i) / det, (a*i - c*g) / det, (c*d - a*f) / det},
		{(d*h - e*g) / det, (b*g - a*h) / det, (a*e - b*d) / det},
	}
}

func mul3(a, b [3][3]float64) [3][3]float64 {
	var out [3][3]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			for k := 0; k < 3; k++ {
				out[r][c] += a[r][k] * b[k][c]
			}
		}
	}
	return out
}

// ToSRGB converts m from the profile's colour space to sRGB. Out-of-gamut
// colours are clipped per channel. Alpha is untouched.
func ToSRGB(m *Image, p *Profile) *Image {
	if p == nil || p.IsSRGB() {
		return m
	}
	mat := mul3(inv3(srgbToXYZ), p.toXYZ)
	var lut [3][4096]float32
	for c := 0; c < 3; c++ {
		for i := range lut[c] {
			lut[c][i] = float32(p.trc[c].eval(float64(i) / 4095))
		}
	}
	lin := func(c int, v uint16) float32 {
		f := float32(v) / 65535 * 4095
		i := int(f)
		if i >= 4095 {
			return lut[c][4095]
		}
		t := f - float32(i)
		return lut[c][i]*(1-t) + lut[c][i+1]*t
	}
	out := m.NewLike()
	Rows(nil, m.H, func(y0, y1 int) {
		for i := 4 * y0 * m.W; i < 4*y1*m.W; i += 4 {
			r, g, b := lin(0, m.Pix[i]), lin(1, m.Pix[i+1]), lin(2, m.Pix[i+2])
			R := float32(mat[0][0])*r + float32(mat[0][1])*g + float32(mat[0][2])*b
			G := float32(mat[1][0])*r + float32(mat[1][1])*g + float32(mat[1][2])*b
			B := float32(mat[2][0])*r + float32(mat[2][1])*g + float32(mat[2][2])*b
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = Enc(R), Enc(G), Enc(B), m.Pix[i+3]
		}
	})
	return out
}
