package ops

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"asteria/internal/img"
)

// Information and hygiene skills: strip metadata, show metadata, palette,
// sRGB normalisation, colour counting and image comparison.

func init() {
	register(Handler{Name: "info.strip", Run: infStrip, Concurrency: 3})
	register(Handler{Name: "info.metadata", Run: infMetadata, Concurrency: 3})
	register(Handler{Name: "info.palette", Run: infPaletteJSON, Concurrency: 3})
	register(Handler{Name: "info.paletteimage", Run: infPaletteImage, Concurrency: 3})
	register(Handler{Name: "info.srgb", Pixel: infSRGB})
	register(Handler{Name: "info.countcolors", Run: infCountColors, Concurrency: 3})
	register(Handler{Name: "info.compare", Run: infCompare, Combine: true})
	register(Handler{Name: "info.comparestats", Run: infCompareStats, Combine: true})
}

// infWriteJSON writes v as pretty JSON with a trailing newline.
func infWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func infFormat(head []byte) string {
	switch {
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return "JPEG"
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "WebP"
	case bytes.HasPrefix(head, []byte("GIF8")):
		return "GIF"
	case bytes.HasPrefix(head, []byte("BM")):
		return "BMP"
	case bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return "TIFF"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return "HEIC"
	}
	return ""
}

func infReadFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 1<<30 {
		return nil, Userf("%s is too large to process (limit 1 GB)", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// ---------------------------------------------------------------------------
// Strip metadata

func infStrip(ctx context.Context, job Job) error {
	in := job.Input()
	data, err := infReadFile(in)
	if err != nil {
		return err
	}
	job.Progress(0.2)
	var out []byte
	switch infFormat(data) {
	case "JPEG":
		// A non-trivial EXIF orientation is metadata that changes how the
		// picture looks: dropping it would leave the photo sideways. In that
		// case re-encode the already-oriented pixels (DecodeWorking applies
		// the orientation) at quality 95 instead of rewriting segments.
		if o := JPEGOrientation(in); o > 1 {
			return infReencode(in, job.Output, 95)
		}
		out, err = infStripJPEG(data)
	case "PNG":
		out, err = infStripPNG(data)
	case "WebP":
		out, err = infStripWebP(data)
	default:
		// HEIC, TIFF, BMP, GIF and anything else: decoding and re-encoding
		// writes a fresh file without metadata.
		return infReencode(in, job.Output, 0)
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(job.Output, out, 0o644)
}

func infReencode(in, out string, quality int) error {
	m, err := DecodeWorking(in)
	if err != nil {
		return err
	}
	if quality > 0 {
		return EncodeImage(m.Std(), out, quality, false)
	}
	return EncodeWorking(m, out)
}

// infStripJPEG drops EXIF/XMP (APP1), IPTC/Photoshop (APP13), other
// application segments and comments. APP0 (JFIF), the ICC profile (APP2),
// Adobe (APP14) and everything from the start of scan on stay byte for byte.
func infStripJPEG(d []byte) ([]byte, error) {
	if len(d) < 4 || d[0] != 0xFF || d[1] != 0xD8 {
		return nil, Userf("not a valid JPEG file")
	}
	out := make([]byte, 0, len(d))
	out = append(out, d[:2]...)
	i := 2
	for i < len(d) {
		if d[i] != 0xFF {
			return nil, Userf("damaged JPEG file")
		}
		if i+1 >= len(d) {
			break
		}
		marker := d[i+1]
		switch {
		case marker == 0xFF: // fill byte
			out = append(out, 0xFF)
			i++
			continue
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD8):
			out = append(out, d[i], d[i+1])
			i += 2
			continue
		case marker == 0xDA || marker == 0xD9:
			return append(out, d[i:]...), nil
		}
		if i+4 > len(d) {
			return nil, Userf("damaged JPEG file")
		}
		n := int(binary.BigEndian.Uint16(d[i+2:]))
		end := i + 2 + n
		if n < 2 || end > len(d) {
			return nil, Userf("damaged JPEG file")
		}
		seg := d[i+4 : end]
		keep := true
		switch {
		case marker == 0xFE:
			keep = false
		case marker == 0xE0:
			keep = !bytes.HasPrefix(seg, []byte("JFXX\x00"))
		case marker == 0xE2:
			keep = bytes.HasPrefix(seg, []byte("ICC_PROFILE\x00"))
		case marker == 0xEE:
			keep = true
		case marker >= 0xE1 && marker <= 0xEF:
			keep = false
		}
		if keep {
			out = append(out, d[i:end]...)
		}
		i = end
	}
	return out, nil
}

func infStripPNG(d []byte) ([]byte, error) {
	sig := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(d, sig) {
		return nil, Userf("not a valid PNG file")
	}
	out := make([]byte, 0, len(d))
	out = append(out, sig...)
	i := 8
	for i+12 <= len(d) {
		n := int(binary.BigEndian.Uint32(d[i:]))
		end := i + 12 + n
		if n < 0 || end > len(d) {
			return nil, Userf("damaged PNG file")
		}
		switch string(d[i+4 : i+8]) {
		case "tEXt", "iTXt", "zTXt", "eXIf", "tIME":
		default:
			out = append(out, d[i:end]...)
		}
		i = end
	}
	return append(out, d[i:]...), nil
}

func infStripWebP(d []byte) ([]byte, error) {
	if len(d) < 12 || string(d[:4]) != "RIFF" || string(d[8:12]) != "WEBP" {
		return nil, Userf("not a valid WebP file")
	}
	out := make([]byte, 0, len(d))
	out = append(out, d[:12]...)
	i := 12
	for i+8 <= len(d) {
		typ := string(d[i : i+4])
		n := int(binary.LittleEndian.Uint32(d[i+4:]))
		end := i + 8 + n + n%2
		if end > len(d) {
			// Final chunk may lack its padding byte.
			end = len(d)
			if i+8+n > len(d) {
				return nil, Userf("damaged WebP file")
			}
		}
		switch typ {
		case "EXIF", "XMP ":
		case "VP8X":
			c := append([]byte(nil), d[i:end]...)
			if n >= 1 {
				c[8] &^= 0x08 | 0x04 // EXIF and XMP flags
			}
			out = append(out, c...)
		default:
			out = append(out, d[i:end]...)
		}
		i = end
	}
	out = append(out, d[i:]...)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out, nil
}

// ---------------------------------------------------------------------------
// Show metadata

type infMeta struct {
	File         string   `json:"file"`
	Format       string   `json:"format"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	BitDepth     int      `json:"bitDepth"`
	HasAlpha     bool     `json:"hasAlpha"`
	ColorProfile string   `json:"colorProfile"`
	CameraMake   string   `json:"cameraMake,omitempty"`
	CameraModel  string   `json:"cameraModel,omitempty"`
	Lens         string   `json:"lens,omitempty"`
	DateTaken    string   `json:"dateTaken,omitempty"`
	ExposureTime string   `json:"exposureTime,omitempty"`
	FNumber      float64  `json:"fNumber,omitempty"`
	ISO          int      `json:"iso,omitempty"`
	FocalLength  float64  `json:"focalLengthMm,omitempty"`
	Orientation  int      `json:"orientation,omitempty"`
	Latitude     *float64 `json:"gpsLatitude,omitempty"`
	Longitude    *float64 `json:"gpsLongitude,omitempty"`
}

func infMetadata(ctx context.Context, job Job) error {
	in := job.Input()
	data, err := infReadFile(in)
	if err != nil {
		return err
	}
	m, err := DecodeWorking(in)
	if err != nil {
		return err
	}
	job.Progress(0.6)
	meta := infMeta{
		File:         job.Name(0),
		Format:       infFormat(data),
		Width:        m.W,
		Height:       m.H,
		BitDepth:     8,
		HasAlpha:     !m.Opaque(),
		ColorProfile: "sRGB (assumed)",
	}
	if meta.Format == "" {
		meta.Format = strings.ToUpper(strings.TrimPrefix(filepath.Ext(in), "."))
	}
	if m.Deep {
		meta.BitDepth = 16
	}
	if icc := img.ReadICC(in); icc != nil {
		meta.ColorProfile = "Embedded ICC profile"
		if p := img.ParseProfile(icc); p != nil && p.Desc != "" {
			meta.ColorProfile = p.Desc
		}
	}
	if t := infExifTIFF(data, meta.Format); t != nil {
		infFillExif(&meta, t)
	}
	return infWriteJSON(job.Output, meta)
}

// infExifTIFF returns the TIFF-structured EXIF block of a file, or nil.
func infExifTIFF(d []byte, format string) []byte {
	switch format {
	case "JPEG":
		for i := 2; i+4 <= len(d); {
			if d[i] != 0xFF {
				return nil
			}
			marker := d[i+1]
			if marker == 0xFF {
				i++
				continue
			}
			if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD8) {
				i += 2
				continue
			}
			if marker == 0xDA || marker == 0xD9 {
				return nil
			}
			n := int(binary.BigEndian.Uint16(d[i+2:]))
			end := i + 2 + n
			if n < 2 || end > len(d) {
				return nil
			}
			if marker == 0xE1 && bytes.HasPrefix(d[i+4:end], []byte("Exif\x00\x00")) {
				return d[i+10 : end]
			}
			i = end
		}
	case "PNG":
		for i := 8; i+12 <= len(d); {
			n := int(binary.BigEndian.Uint32(d[i:]))
			if n < 0 || i+12+n > len(d) {
				return nil
			}
			if string(d[i+4:i+8]) == "eXIf" {
				return d[i+8 : i+8+n]
			}
			i += 12 + n
		}
	case "WebP":
		for i := 12; i+8 <= len(d); {
			n := int(binary.LittleEndian.Uint32(d[i+4:]))
			if i+8+n > len(d) {
				return nil
			}
			if string(d[i:i+4]) == "EXIF" {
				return bytes.TrimPrefix(d[i+8:i+8+n], []byte("Exif\x00\x00"))
			}
			i += 8 + n + n%2
		}
	case "TIFF":
		return d
	case "HEIC":
		head := d
		if len(head) > 4<<20 {
			head = head[:4<<20]
		}
		for off := 0; ; {
			j := bytes.Index(head[off:], []byte("Exif\x00\x00"))
			if j < 0 {
				return nil
			}
			j += off + 6
			if j+4 <= len(d) && (string(d[j:j+2]) == "II" || string(d[j:j+2]) == "MM") {
				return d[j:]
			}
			off = j
		}
	}
	return nil
}

type infEntry struct {
	typ   uint16
	count uint32
	at    int // offset of the value bytes within the TIFF block
	size  int
}

type infTIFF struct {
	d  []byte
	bo binary.ByteOrder
}

func infOpenTIFF(d []byte) *infTIFF {
	if len(d) < 8 {
		return nil
	}
	t := &infTIFF{d: d}
	switch string(d[:2]) {
	case "II":
		t.bo = binary.LittleEndian
	case "MM":
		t.bo = binary.BigEndian
	default:
		return nil
	}
	if t.bo.Uint16(d[2:]) != 42 {
		return nil
	}
	return t
}

func (t *infTIFF) ifd(off int) map[uint16]infEntry {
	out := map[uint16]infEntry{}
	if off < 8 || off+2 > len(t.d) {
		return out
	}
	n := int(t.bo.Uint16(t.d[off:]))
	sizes := map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8}
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(t.d) {
			break
		}
		typ := t.bo.Uint16(t.d[e+2:])
		cnt := t.bo.Uint32(t.d[e+4:])
		sz, ok := sizes[typ]
		if !ok || cnt > 1<<20 {
			continue
		}
		total := sz * int(cnt)
		at := e + 8
		if total > 4 {
			at = int(t.bo.Uint32(t.d[e+8:]))
		}
		if at < 0 || at+total > len(t.d) {
			continue
		}
		out[t.bo.Uint16(t.d[e:])] = infEntry{typ: typ, count: cnt, at: at, size: sz}
	}
	return out
}

func (t *infTIFF) str(e infEntry, ok bool) string {
	if !ok || e.typ != 2 {
		return ""
	}
	s := string(t.d[e.at : e.at+int(e.count)])
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func (t *infTIFF) uint(e infEntry, ok bool) (int, bool) {
	if !ok || e.count < 1 {
		return 0, false
	}
	switch e.typ {
	case 1, 7:
		return int(t.d[e.at]), true
	case 3:
		return int(t.bo.Uint16(t.d[e.at:])), true
	case 4:
		return int(t.bo.Uint32(t.d[e.at:])), true
	}
	return 0, false
}

// rationals returns (numerator, denominator) pairs.
func (t *infTIFF) rationals(e infEntry, ok bool) [][2]float64 {
	if !ok || (e.typ != 5 && e.typ != 10) {
		return nil
	}
	out := make([][2]float64, 0, e.count)
	for i := 0; i < int(e.count); i++ {
		p := e.at + 8*i
		if e.typ == 5 {
			out = append(out, [2]float64{float64(t.bo.Uint32(t.d[p:])), float64(t.bo.Uint32(t.d[p+4:]))})
		} else {
			out = append(out, [2]float64{float64(int32(t.bo.Uint32(t.d[p:]))), float64(int32(t.bo.Uint32(t.d[p+4:])))})
		}
	}
	return out
}

func infRound(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func infFillExif(meta *infMeta, d []byte) {
	t := infOpenTIFF(d)
	if t == nil {
		return
	}
	ifd0 := t.ifd(int(t.bo.Uint32(d[4:])))
	get := func(m map[uint16]infEntry, tag uint16) (infEntry, bool) { e, ok := m[tag]; return e, ok }
	meta.CameraMake = t.str(get(ifd0, 0x010F))
	meta.CameraModel = t.str(get(ifd0, 0x0110))
	if o, ok := t.uint(get(ifd0, 0x0112)); ok && o >= 1 && o <= 8 {
		meta.Orientation = o
	}
	date := t.str(get(ifd0, 0x0132))
	if e, ok := get(ifd0, 0x8769); ok {
		if off, ok := t.uint(e, true); ok {
			x := t.ifd(off)
			if s := t.str(get(x, 0x9003)); s != "" {
				date = s
			}
			if r := t.rationals(get(x, 0x829A)); len(r) == 1 && r[0][1] != 0 {
				v := r[0][0] / r[0][1]
				switch {
				case v > 0 && v < 1:
					meta.ExposureTime = fmt.Sprintf("1/%d s", int(math.Round(1/v)))
				case v > 0:
					meta.ExposureTime = fmt.Sprintf("%g s", infRound(v, 3))
				}
			}
			if r := t.rationals(get(x, 0x829D)); len(r) == 1 && r[0][1] != 0 {
				meta.FNumber = infRound(r[0][0]/r[0][1], 2)
			}
			if v, ok := t.uint(get(x, 0x8827)); ok {
				meta.ISO = v
			}
			if r := t.rationals(get(x, 0x920A)); len(r) == 1 && r[0][1] != 0 {
				meta.FocalLength = infRound(r[0][0]/r[0][1], 1)
			}
			meta.Lens = t.str(get(x, 0xA434))
		}
	}
	// "2021:05:03 12:34:56" -> "2021-05-03 12:34:56"
	if len(date) >= 10 && date[4] == ':' && date[7] == ':' {
		date = date[:4] + "-" + date[5:7] + "-" + date[8:]
	}
	meta.DateTaken = date
	if e, ok := get(ifd0, 0x8825); ok {
		if off, ok := t.uint(e, true); ok {
			g := t.ifd(off)
			coord := func(valTag, refTag uint16, neg string) *float64 {
				r := t.rationals(get(g, valTag))
				if len(r) < 3 || r[0][1] == 0 || r[1][1] == 0 || r[2][1] == 0 {
					return nil
				}
				v := r[0][0]/r[0][1] + r[1][0]/r[1][1]/60 + r[2][0]/r[2][1]/3600
				if strings.EqualFold(t.str(get(g, refTag)), neg) {
					v = -v
				}
				v = infRound(v, 6)
				return &v
			}
			meta.Latitude = coord(2, 1, "S")
			meta.Longitude = coord(4, 3, "W")
		}
	}
}

// ---------------------------------------------------------------------------
// Palette

type infColor struct {
	Hex   string  `json:"hex"`
	Share float64 `json:"share"`
	RGB   [3]int  `json:"rgb"`
}

func infPaletteCount(args Args) (int, error) {
	return args.IntInRange("colors", 6, 2, 16)
}

// infPalette clusters the opaque pixels of a downsampled copy with k-means in
// OKLab (perceptual distance) and returns the cluster colours by share.
func infPalette(m *img.Image, k int) ([]infColor, error) {
	const maxSide = 256
	w, h := m.W, m.H
	if w > maxSide || h > maxSide {
		w, h = img.Fit(m.W, m.H, maxSide, maxSide)
	}
	w, h = max(w, 1), max(h, 1)
	type pt struct{ l, a, b float32 }
	pts := make([]pt, 0, w*h)
	for ty := 0; ty < h; ty++ {
		y := min(int((float64(ty)+0.5)*float64(m.H)/float64(h)), m.H-1)
		for tx := 0; tx < w; tx++ {
			x := min(int((float64(tx)+0.5)*float64(m.W)/float64(w)), m.W-1)
			r, g, b, a := m.At(x, y)
			if a < 0x8000 {
				continue
			}
			l, aa, bb := img.LinearToOKLab(img.Lin(r), img.Lin(g), img.Lin(b))
			pts = append(pts, pt{l, aa, bb})
		}
	}
	if len(pts) == 0 {
		return nil, Userf("this image has no opaque pixels to take colours from")
	}
	dist := func(p, c pt) float32 {
		dl, da, db := p.l-c.l, p.a-c.a, p.b-c.b
		return dl*dl + da*da + db*db
	}
	// k-means++ with a fixed seed: deterministic.
	rng := rand.New(rand.NewPCG(7, 11))
	cent := []pt{pts[rng.IntN(len(pts))]}
	d2 := make([]float64, len(pts))
	for len(cent) < k {
		var sum float64
		for i, p := range pts {
			best := float32(math.MaxFloat32)
			for _, c := range cent {
				best = min(best, dist(p, c))
			}
			d2[i] = float64(best)
			sum += d2[i]
		}
		if sum < 1e-12 {
			break // fewer distinct colours than k
		}
		target := rng.Float64() * sum
		pick := len(pts) - 1
		for i, v := range d2 {
			target -= v
			if target <= 0 {
				pick = i
				break
			}
		}
		cent = append(cent, pts[pick])
	}
	assign := make([]int, len(pts))
	for i := range assign {
		assign[i] = -1
	}
	for iter := 0; iter < 60; iter++ {
		changed := false
		for i, p := range pts {
			bi, bd := 0, float32(math.MaxFloat32)
			for ci, c := range cent {
				if d := dist(p, c); d < bd {
					bi, bd = ci, d
				}
			}
			if assign[i] != bi {
				assign[i], changed = bi, true
			}
		}
		if !changed {
			break
		}
		sums := make([][3]float64, len(cent))
		counts := make([]int, len(cent))
		for i, p := range pts {
			s := &sums[assign[i]]
			s[0] += float64(p.l)
			s[1] += float64(p.a)
			s[2] += float64(p.b)
			counts[assign[i]]++
		}
		for ci := range cent {
			if counts[ci] > 0 {
				n := float64(counts[ci])
				cent[ci] = pt{float32(sums[ci][0] / n), float32(sums[ci][1] / n), float32(sums[ci][2] / n)}
			}
		}
	}
	counts := make([]int, len(cent))
	for _, a := range assign {
		counts[a]++
	}
	type cl struct {
		c     infColor
		n     int
		light float32
	}
	var cls []cl
	for ci, n := range counts {
		if n == 0 {
			continue
		}
		r, g, b := img.OKLabToLinear(cent[ci].l, cent[ci].a, cent[ci].b)
		var rgb [3]int
		for i, v := range []float32{r, g, b} {
			rgb[i] = int(math.Round(float64(img.Enc(v)) / 257))
		}
		cls = append(cls, cl{
			c:     infColor{Hex: fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2]), Share: infRound(float64(n)/float64(len(pts)), 4), RGB: rgb},
			n:     n,
			light: cent[ci].l,
		})
	}
	sort.SliceStable(cls, func(i, j int) bool {
		if cls[i].n != cls[j].n {
			return cls[i].n > cls[j].n
		}
		return cls[i].light > cls[j].light
	})
	out := make([]infColor, len(cls))
	for i, c := range cls {
		out[i] = c.c
	}
	return out, nil
}

func infPaletteJSON(ctx context.Context, job Job) error {
	k, err := infPaletteCount(job.Args)
	if err != nil {
		return err
	}
	m, err := DecodeWorking(job.Input())
	if err != nil {
		return err
	}
	job.Progress(0.5)
	pal, err := infPalette(m, k)
	if err != nil {
		return err
	}
	return infWriteJSON(job.Output, pal)
}

func infPaletteImage(ctx context.Context, job Job) error {
	k, err := infPaletteCount(job.Args)
	if err != nil {
		return err
	}
	m, err := DecodeWorking(job.Input())
	if err != nil {
		return err
	}
	job.Progress(0.5)
	pal, err := infPalette(m, k)
	if err != nil {
		return err
	}
	const sw = 120
	out := img.New(sw*len(pal), sw)
	for i, c := range pal {
		r, g, b := uint16(c.RGB[0]*257), uint16(c.RGB[1]*257), uint16(c.RGB[2]*257)
		for y := 0; y < sw; y++ {
			for x := i * sw; x < (i+1)*sw; x++ {
				out.Set(x, y, r, g, b, 0xffff)
			}
		}
	}
	return EncodeWorking(out, job.Output)
}

// ---------------------------------------------------------------------------
// sRGB

// infSRGB: DecodeWorking has already converted any matrix/TRC profile to
// sRGB and encoders write untagged sRGB, so the working image is the result.
func infSRGB(ctx context.Context, m *img.Image, args Args) (*img.Image, error) {
	return m.Clone(), nil
}

// ---------------------------------------------------------------------------
// Count colours

type infTopColor struct {
	Hex   string `json:"hex"`
	Alpha int    `json:"alpha"`
	Count int    `json:"count"`
}

type infCountResult struct {
	UniqueColors      int           `json:"uniqueColors"`
	IsGreyscale       bool          `json:"isGreyscale"`
	UsesTransparency  bool          `json:"usesTransparency"`
	MostFrequentColor []infTopColor `json:"mostFrequentColors"`
}

func inf8(v uint16) uint32 { return (uint32(v)*255 + 32767) / 65535 }

func infCountColors(ctx context.Context, job Job) error {
	m, err := DecodeWorking(job.Input())
	if err != nil {
		return err
	}
	job.Progress(0.4)
	counts := map[uint32]int{}
	grey, alpha := true, false
	for i := 0; i < len(m.Pix); i += 4 {
		r, g, b, a := inf8(m.Pix[i]), inf8(m.Pix[i+1]), inf8(m.Pix[i+2]), inf8(m.Pix[i+3])
		counts[r<<24|g<<16|b<<8|a]++
		if r != g || g != b {
			grey = false
		}
		if a != 255 {
			alpha = true
		}
		if i&0xfffff == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	keys := make([]uint32, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	res := infCountResult{UniqueColors: len(counts), IsGreyscale: grey, UsesTransparency: alpha}
	for _, k := range keys[:min(10, len(keys))] {
		res.MostFrequentColor = append(res.MostFrequentColor, infTopColor{
			Hex:   fmt.Sprintf("#%02x%02x%02x", k>>24, k>>16&255, k>>8&255),
			Alpha: int(k & 255),
			Count: counts[k],
		})
	}
	return infWriteJSON(job.Output, res)
}

// ---------------------------------------------------------------------------
// Compare

func infPair(job Job) (a, b *img.Image, err error) {
	if len(job.Inputs) != 2 {
		return nil, nil, Userf("Compare needs exactly two images (got %d)", len(job.Inputs))
	}
	if a, err = DecodeWorking(job.Inputs[0]); err != nil {
		return nil, nil, err
	}
	if b, err = DecodeWorking(job.Inputs[1]); err != nil {
		return nil, nil, err
	}
	if a.W != b.W || a.H != b.H {
		return nil, nil, Userf("Compare needs two images of the same size (%dx%d vs %dx%d)", a.W, a.H, b.W, b.H)
	}
	return a, b, nil
}

// infDiff8 returns the largest channel difference (RGB and alpha) of pixel
// offset i on the 0-255 scale.
func infDiff8(a, b *img.Image, i int) float64 {
	d := 0.0
	for c := 0; c < 4; c++ {
		d = math.Max(d, math.Abs(float64(a.Pix[i+c])-float64(b.Pix[i+c]))/257)
	}
	return d
}

// infCompare shows the first image dimmed to 30% grey with differences in
// red (blend strength grows with the difference; differences of 2/255 or
// less are treated as noise and stay unmarked).
func infCompare(ctx context.Context, job Job) error {
	a, b, err := infPair(job)
	if err != nil {
		return err
	}
	job.Progress(0.5)
	out := a.NewLike()
	for i := 0; i < len(a.Pix); i += 4 {
		lum := img.Luma(img.Lin(a.Pix[i]), img.Lin(a.Pix[i+1]), img.Lin(a.Pix[i+2]))
		g := float64(img.Enc(lum)) * 0.3
		var t float64
		if d := infDiff8(a, b, i); d > 2 {
			t = math.Min(1, 0.4+d/255*4)
		}
		out.Pix[i] = uint16(math.Round(g*(1-t) + 65535*t))
		out.Pix[i+1] = uint16(math.Round(g * (1 - t)))
		out.Pix[i+2] = out.Pix[i+1]
		out.Pix[i+3] = 0xffff
	}
	return EncodeWorking(out, job.Output)
}

type infStats struct {
	MeanAbsoluteError float64 `json:"meanAbsoluteError"`
	MaxDifference     float64 `json:"maxDifference"`
	PSNR              any     `json:"psnrDb"`
	PercentDifferent  float64 `json:"percentPixelsDifferent"`
	SSIM              float64 `json:"ssim"`
}

func infCompareStats(ctx context.Context, job Job) error {
	a, b, err := infPair(job)
	if err != nil {
		return err
	}
	job.Progress(0.5)
	n := a.W * a.H
	var sumAbs, sumSq, maxD float64
	differing := 0
	for i := 0; i < len(a.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			d := math.Abs(float64(a.Pix[i+c])-float64(b.Pix[i+c])) / 257
			sumAbs += d
			sumSq += d * d
		}
		d := infDiff8(a, b, i)
		maxD = math.Max(maxD, d)
		if d > 2 {
			differing++
		}
	}
	st := infStats{
		MeanAbsoluteError: infRound(sumAbs/float64(3*n), 6),
		MaxDifference:     infRound(maxD, 4),
		PercentDifferent:  infRound(100*float64(differing)/float64(n), 6),
		SSIM:              infRound(infSSIM(a, b), 6),
	}
	if mse := sumSq / float64(3*n); mse == 0 {
		st.PSNR = "inf"
	} else {
		st.PSNR = infRound(10*math.Log10(255*255/mse), 4)
	}
	return infWriteJSON(job.Output, st)
}

// infSSIM is the mean structural similarity of the luminance (8-bit scale,
// Rec.601 weights) over every 8x8 window, via summed-area tables.
func infSSIM(a, b *img.Image) float64 {
	w, h := a.W, a.H
	win := min(8, w, h)
	stride := w + 1
	size := stride * (h + 1)
	sx, sy := make([]float64, size), make([]float64, size)
	sxx, syy, sxy := make([]float64, size), make([]float64, size), make([]float64, size)
	y8 := func(m *img.Image, i int) float64 {
		return (0.299*float64(m.Pix[i]) + 0.587*float64(m.Pix[i+1]) + 0.114*float64(m.Pix[i+2])) / 257
	}
	for y := 0; y < h; y++ {
		var rx, ry, rxx, ryy, rxy float64
		for x := 0; x < w; x++ {
			i := 4 * (y*w + x)
			p, q := y8(a, i), y8(b, i)
			rx, ry, rxx, ryy, rxy = rx+p, ry+q, rxx+p*p, ryy+q*q, rxy+p*q
			o, up := (y+1)*stride+x+1, y*stride+x+1
			sx[o], sy[o] = sx[up]+rx, sy[up]+ry
			sxx[o], syy[o], sxy[o] = sxx[up]+rxx, syy[up]+ryy, sxy[up]+rxy
		}
	}
	box := func(s []float64, x, y int) float64 {
		return s[(y+win)*stride+x+win] - s[y*stride+x+win] - s[(y+win)*stride+x] + s[y*stride+x]
	}
	const c1, c2 = (0.01 * 255) * (0.01 * 255), (0.03 * 255) * (0.03 * 255)
	nw := float64(win * win)
	var total float64
	var cnt int
	for y := 0; y+win <= h; y++ {
		for x := 0; x+win <= w; x++ {
			mx, my := box(sx, x, y)/nw, box(sy, x, y)/nw
			vx := box(sxx, x, y)/nw - mx*mx
			vy := box(syy, x, y)/nw - my*my
			cv := box(sxy, x, y)/nw - mx*my
			total += ((2*mx*my + c1) * (2*cv + c2)) / ((mx*mx + my*my + c1) * (vx + vy + c2))
			cnt++
		}
	}
	if cnt == 0 {
		return 1
	}
	return total / float64(cnt)
}
