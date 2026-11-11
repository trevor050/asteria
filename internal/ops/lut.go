package ops

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"asteria/internal/img"
)

// Colour lookup tables.
//
// Formats read:
//
//   - .cube (Adobe/Iridas): LUT_1D_SIZE or LUT_3D_SIZE (3D up to 128), TITLE,
//     DOMAIN_MIN/DOMAIN_MAX, LUT_*_INPUT_RANGE, # comments. 3D data is red
//     fastest, then green, then blue.
//   - .3dl (Autodesk Lustre/Flame): an optional first line listing the input
//     steps (its length is the cube size), then integer RGB triples with blue
//     varying fastest. The file does not say its bit depth, so it is read as
//     10-bit, 12-bit or 16-bit from the largest value: above 4095 means
//     16-bit (0-65535), above 1023 means 12-bit (0-4095), otherwise 10-bit
//     (0-1023); data with decimals that never exceeds 1 is taken as already
//     normalised. A non-linear input shaper line is used only for its size.
//     Written files use a 10-bit shaper and 12-bit output.
//   - Hald CLUT PNG: a square image of side level^3 (level 2-11). Pixel index
//     x + y*side counts red fastest, then green, then blue, on a grid of
//     level^2 points per channel.
//
// A LUT maps encoded (gamma) RGB to encoded RGB, which is how .cube files are
// defined for display-referred images, so apply_lut works on the working
// image's sRGB-encoded values directly and never touches alpha. 3D LUTs are
// interpolated tetrahedrally (the standard choice: exact on the lattice,
// fewer colour artefacts on neutral ramps than trilinear), 1D LUTs linearly.

func init() {
	register(Handler{Name: "lut.apply", Pixel: pixApplyLUT, Live: true})
	register(Handler{Name: "lut.toCube", Run: lutToCube})
	register(Handler{Name: "lut.to3dl", Run: lutTo3DL})
	register(Handler{Name: "lut.toHald", Run: lutToHald})
}

const lutMaxSize3D = 128

// lutData is a parsed LUT. 1D data holds Size entries of r,g,b. 3D data holds
// Size^3 entries with red fastest: entry (r,g,b) is at ((b*Size+g)*Size+r)*3.
type lutData struct {
	Title    string
	Dim      int // 1 or 3
	Size     int
	Min, Max [3]float32
	Data     []float32
}

func lutClamp01(t float32) float32 {
	if !(t > 0) {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// lutSplit turns a lattice coordinate into a cell index and fraction.
func lutSplit(p float32, n int) (int, float32) {
	i := int(p)
	if i > n-2 {
		i = n - 2
	}
	if i < 0 {
		i = 0
	}
	return i, p - float32(i)
}

// eval maps image values (encoded, 0..1) through the LUT.
func (l *lutData) eval(r, g, b float32) (float32, float32, float32) {
	in := [3]float32{r, g, b}
	for c := range in {
		in[c] = lutClamp01((in[c] - l.Min[c]) / (l.Max[c] - l.Min[c]))
	}
	if l.Dim == 1 {
		var out [3]float32
		m := float32(l.Size - 1)
		for c := 0; c < 3; c++ {
			i, f := lutSplit(in[c]*m, l.Size)
			out[c] = l.Data[3*i+c]*(1-f) + l.Data[3*(i+1)+c]*f
		}
		return out[0], out[1], out[2]
	}
	n := l.Size
	m := float32(n - 1)
	ir, fr := lutSplit(in[0]*m, n)
	ig, fg := lutSplit(in[1]*m, n)
	ib, fb := lutSplit(in[2]*m, n)
	base := ((ib*n+ig)*n + ir) * 3
	sr, sg, sb := 3, 3*n, 3*n*n
	d := l.Data
	var out [3]float32
	for c := 0; c < 3; c++ {
		c000 := d[base+c]
		c100 := d[base+sr+c]
		c010 := d[base+sg+c]
		c001 := d[base+sb+c]
		c110 := d[base+sr+sg+c]
		c101 := d[base+sr+sb+c]
		c011 := d[base+sg+sb+c]
		c111 := d[base+sr+sg+sb+c]
		// Tetrahedral: walk from c000 to c111 along the axes in order of
		// decreasing fraction.
		var v float32
		if fr >= fg {
			switch {
			case fg >= fb: // r >= g >= b
				v = c000 + fr*(c100-c000) + fg*(c110-c100) + fb*(c111-c110)
			case fr >= fb: // r >= b > g
				v = c000 + fr*(c100-c000) + fb*(c101-c100) + fg*(c111-c101)
			default: // b > r >= g
				v = c000 + fb*(c001-c000) + fr*(c101-c001) + fg*(c111-c101)
			}
		} else {
			switch {
			case fb >= fg: // b >= g > r
				v = c000 + fb*(c001-c000) + fg*(c011-c001) + fr*(c111-c011)
			case fb >= fr: // g > b >= r
				v = c000 + fg*(c010-c000) + fb*(c011-c010) + fr*(c111-c011)
			default: // g > r > b
				v = c000 + fg*(c010-c000) + fr*(c110-c010) + fb*(c111-c110)
			}
		}
		out[c] = v
	}
	return out[0], out[1], out[2]
}

// lutIdentity3D returns an identity cube of the given size.
func lutIdentity3D(n int) *lutData {
	l := &lutData{Dim: 3, Size: n, Max: [3]float32{1, 1, 1}, Data: make([]float32, 3*n*n*n)}
	k := 0
	for b := 0; b < n; b++ {
		for g := 0; g < n; g++ {
			for r := 0; r < n; r++ {
				l.Data[k], l.Data[k+1], l.Data[k+2] = float32(r)/float32(n-1), float32(g)/float32(n-1), float32(b)/float32(n-1)
				k += 3
			}
		}
	}
	return l
}

// resample3D samples the LUT on an n-point cube over the 0..1 image range, so
// 1D LUTs, other sizes and non-default domains all become a plain cube.
func (l *lutData) resample3D(n int) *lutData {
	if l.Dim == 3 && l.Size == n && l.Min == [3]float32{} && l.Max == [3]float32{1, 1, 1} {
		return l
	}
	out := lutIdentity3D(n)
	out.Title = l.Title
	for k := 0; k < len(out.Data); k += 3 {
		out.Data[k], out.Data[k+1], out.Data[k+2] = l.eval(out.Data[k], out.Data[k+1], out.Data[k+2])
	}
	return out
}

// ---------------------------------------------------------------------------
// .cube
// ---------------------------------------------------------------------------

func lutParseCube(b []byte) (*lutData, error) {
	l := &lutData{Max: [3]float32{1, 1, 1}}
	size1, size3 := 0, 0
	var want int
	lineNo := 0
	floats := func(f []string, n int) ([]float32, bool) {
		if len(f) < n {
			return nil, false
		}
		out := make([]float32, n)
		for i := 0; i < n; i++ {
			v, err := strconv.ParseFloat(f[i], 32)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, false
			}
			out[i] = float32(v)
		}
		return out, true
	}
	for len(b) > 0 {
		var line []byte
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			line, b = b, nil
		}
		lineNo++
		if i := bytes.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		s := strings.TrimSpace(string(line))
		if s == "" {
			continue
		}
		f := strings.Fields(s)
		c := f[0][0]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' {
			if want == 0 {
				switch {
				case size1 > 0 && size3 > 0:
					return nil, Userf("this .cube file declares both LUT_1D_SIZE and LUT_3D_SIZE, which is not supported")
				case size1 == 0 && size3 == 0:
					return nil, Userf("this .cube file has colour data before LUT_3D_SIZE or LUT_1D_SIZE")
				case size3 > 0:
					l.Dim, l.Size = 3, size3
					want = size3 * size3 * size3
				default:
					l.Dim, l.Size = 1, size1
					want = size1
				}
				l.Data = make([]float32, 0, 3*want)
			}
			v, ok := floats(f, 3)
			if !ok || len(f) != 3 {
				return nil, Userf("line %d of this .cube file is not three numbers", lineNo)
			}
			if len(l.Data) >= 3*want {
				return nil, Userf("this .cube file has more colour entries than its declared size")
			}
			l.Data = append(l.Data, v...)
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "TITLE":
			l.Title = strings.Trim(strings.TrimSpace(s[len(f[0]):]), `"`)
		case "LUT_1D_SIZE", "LUT_3D_SIZE":
			if len(f) < 2 {
				return nil, Userf("line %d: %s needs a size", lineNo, f[0])
			}
			n, err := strconv.Atoi(f[1])
			if err != nil {
				return nil, Userf("line %d: %q is not a valid size", lineNo, f[1])
			}
			if strings.EqualFold(f[0], "LUT_3D_SIZE") {
				if n < 2 || n > lutMaxSize3D {
					return nil, Userf("3D LUT size %d is not supported (2 to %d)", n, lutMaxSize3D)
				}
				size3 = n
			} else {
				if n < 2 || n > 1<<16 {
					return nil, Userf("1D LUT size %d is not supported (2 to 65536)", n)
				}
				size1 = n
			}
		case "DOMAIN_MIN", "DOMAIN_MAX":
			v, ok := floats(f[1:], 3)
			if !ok {
				return nil, Userf("line %d: %s needs three numbers", lineNo, f[0])
			}
			if strings.EqualFold(f[0], "DOMAIN_MIN") {
				copy(l.Min[:], v)
			} else {
				copy(l.Max[:], v)
			}
		case "LUT_1D_INPUT_RANGE", "LUT_3D_INPUT_RANGE":
			v, ok := floats(f[1:], 2)
			if !ok {
				return nil, Userf("line %d: %s needs two numbers", lineNo, f[0])
			}
			l.Min, l.Max = [3]float32{v[0], v[0], v[0]}, [3]float32{v[1], v[1], v[1]}
		}
	}
	if want == 0 {
		return nil, Userf("this .cube file has no colour data")
	}
	if len(l.Data) != 3*want {
		return nil, Userf("this .cube file has %d colour entries but its size needs %d", len(l.Data)/3, want)
	}
	for c := 0; c < 3; c++ {
		if !(l.Max[c] > l.Min[c]) {
			return nil, Userf("this .cube file's DOMAIN_MAX must be larger than DOMAIN_MIN")
		}
	}
	return l, nil
}

func lutWriteCube(l *lutData) []byte {
	var b bytes.Buffer
	if l.Title != "" {
		fmt.Fprintf(&b, "TITLE %q\n", l.Title)
	}
	if l.Dim == 1 {
		fmt.Fprintf(&b, "LUT_1D_SIZE %d\n", l.Size)
	} else {
		fmt.Fprintf(&b, "LUT_3D_SIZE %d\n", l.Size)
	}
	if l.Min != [3]float32{} || l.Max != [3]float32{1, 1, 1} {
		fmt.Fprintf(&b, "DOMAIN_MIN %g %g %g\nDOMAIN_MAX %g %g %g\n", l.Min[0], l.Min[1], l.Min[2], l.Max[0], l.Max[1], l.Max[2])
	}
	b.WriteByte('\n')
	var tmp []byte
	for i := 0; i+2 < len(l.Data); i += 3 {
		for c := 0; c < 3; c++ {
			tmp = strconv.AppendFloat(tmp[:0], float64(l.Data[i+c]), 'f', 6, 32)
			b.Write(tmp)
			if c < 2 {
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// ---------------------------------------------------------------------------
// .3dl
// ---------------------------------------------------------------------------

func lutParse3DL(b []byte) (*lutData, error) {
	var shaper []float64
	var triples []float64
	decimals := false
	for _, raw := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			raw = raw[:i]
		}
		f := strings.Fields(raw)
		if len(f) == 0 {
			continue
		}
		vals := make([]float64, 0, len(f))
		numeric := true
		for _, tok := range f {
			v, err := strconv.ParseFloat(tok, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				numeric = false
				break
			}
			if strings.ContainsAny(tok, ".eE") {
				decimals = true
			}
			vals = append(vals, v)
		}
		if !numeric {
			continue // header words such as 3DMESH, Mesh 1 12, LUT8, gamma 1.0
		}
		switch {
		case len(vals) == 3:
			triples = append(triples, vals...)
		case len(triples) == 0 && shaper == nil && len(vals) >= 2:
			shaper = vals
		default:
			return nil, Userf("this .3dl file has a line with %d numbers where RGB triples are expected", len(vals))
		}
	}
	count := len(triples) / 3
	n := len(shaper)
	if n == 0 {
		n = int(math.Round(math.Cbrt(float64(count))))
	}
	if n < 2 || n > lutMaxSize3D || n*n*n != count {
		return nil, Userf("this .3dl file has %d colour entries, which is not a cube of 2 to %d points per channel", count, lutMaxSize3D)
	}
	maxV := 0.0
	for _, v := range triples {
		maxV = math.Max(maxV, v)
	}
	scale := 1023.0
	switch {
	case decimals && maxV <= 1.0001:
		scale = 1
	case maxV > 4095:
		scale = 65535
	case maxV > 1023:
		scale = 4095
	}
	l := &lutData{Dim: 3, Size: n, Max: [3]float32{1, 1, 1}, Data: make([]float32, 3*count)}
	k := 0
	for r := 0; r < n; r++ { // file order: blue varies fastest
		for g := 0; g < n; g++ {
			for b := 0; b < n; b++ {
				o := 3 * ((b*n+g)*n + r)
				l.Data[o] = float32(triples[k] / scale)
				l.Data[o+1] = float32(triples[k+1] / scale)
				l.Data[o+2] = float32(triples[k+2] / scale)
				k += 3
			}
		}
	}
	return l, nil
}

func lutWrite3DL(l *lutData) []byte {
	if l.Dim != 3 || l.Size < 2 || l.Size > lutMaxSize3D || l.Min != [3]float32{} || l.Max != [3]float32{1, 1, 1} {
		l = l.resample3D(33)
	}
	n := l.Size
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Itoa(int(math.Round(float64(i) * 1023 / float64(n-1)))))
	}
	b.WriteByte('\n')
	q := func(v float32) int {
		return int(math.Round(math.Max(0, math.Min(1, float64(v))) * 4095))
	}
	for r := 0; r < n; r++ {
		for g := 0; g < n; g++ {
			for bl := 0; bl < n; bl++ {
				o := 3 * ((bl*n+g)*n + r)
				fmt.Fprintf(&b, "%d %d %d\n", q(l.Data[o]), q(l.Data[o+1]), q(l.Data[o+2]))
			}
		}
	}
	return b.Bytes()
}

// ---------------------------------------------------------------------------
// Hald CLUT
// ---------------------------------------------------------------------------

func lutFromHald(m *img.Image) (*lutData, error) {
	side := m.W
	level := int(math.Round(math.Cbrt(float64(side))))
	if m.W != m.H || level < 2 || level > 11 || level*level*level != side {
		return nil, Userf("this image is not a Hald CLUT: it must be square with a side of level^3 pixels, such as 512x512 for level 8 (levels 2 to 11)")
	}
	n := level * level
	l := &lutData{Dim: 3, Size: n, Max: [3]float32{1, 1, 1}, Data: make([]float32, 3*n*n*n)}
	for i := 0; i < n*n*n; i++ {
		r, g, b, _ := m.At(i%side, i/side)
		l.Data[3*i], l.Data[3*i+1], l.Data[3*i+2] = img.F(r), img.F(g), img.F(b)
	}
	return l, nil
}

// lutToHaldImage renders the LUT as a Hald CLUT image of the given level.
func lutToHaldImage(l *lutData, level int) *img.Image {
	n := level * level
	side := level * level * level
	m := img.New(side, side)
	den := float32(n - 1)
	for i := 0; i < n*n*n; i++ {
		r, g, b := l.eval(float32(i%n)/den, float32((i/n)%n)/den, float32(i/(n*n))/den)
		m.Set(i%side, i/side, img.Q(r), img.Q(g), img.Q(b), 0xffff)
	}
	return m
}

// ---------------------------------------------------------------------------
// Loading by path, with a small cache for live previews
// ---------------------------------------------------------------------------

type lutCacheKey struct {
	path string
	size int64
	mod  int64
}

var (
	lutCacheMu sync.Mutex
	lutCache   = map[lutCacheKey]*lutData{}
)

// lutResolve turns what the user typed into an existing file: a path (with
// quotes, ~ or file:// tolerated), or the name of a file in the luts folder
// next to the user's skills (".../asteria/luts").
func lutResolve(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, `"'`)
	if strings.HasPrefix(ref, "file://") {
		if u, err := url.Parse(ref); err == nil {
			ref = u.Path
		}
	}
	if ref == "~" || strings.HasPrefix(ref, "~/") || strings.HasPrefix(ref, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			ref = filepath.Join(home, ref[1:])
		}
	}
	exts := []string{"", ".cube", ".3dl", ".png"}
	try := func(base string) string {
		for _, e := range exts {
			if st, err := os.Stat(base + e); err == nil && !st.IsDir() {
				return base + e
			}
		}
		return ""
	}
	if p := try(ref); p != "" {
		return p, nil
	}
	if !strings.ContainsAny(ref, `/\`) {
		if dir, err := os.UserConfigDir(); err == nil {
			if p := try(filepath.Join(dir, "asteria", "luts", ref)); p != "" {
				return p, nil
			}
		}
	}
	return "", Userf("can't find the LUT %q. Give a path to a .cube, .3dl or Hald .png file, or put it in the luts folder", ref)
}

func lutLoad(ref string) (*lutData, error) {
	path, err := lutResolve(ref)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := lutCacheKey{path, st.Size(), st.ModTime().UnixNano()}
	lutCacheMu.Lock()
	l := lutCache[key]
	lutCacheMu.Unlock()
	if l != nil {
		return l, nil
	}
	if l, err = lutParseFile(path); err != nil {
		return nil, err
	}
	lutCacheMu.Lock()
	if len(lutCache) >= 6 {
		lutCache = map[lutCacheKey]*lutData{}
	}
	lutCache[key] = l
	lutCacheMu.Unlock()
	return l, nil
}

// lutParseFile reads a .cube, .3dl or Hald PNG by extension.
func lutParseFile(path string) (*lutData, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > 256<<20 {
		return nil, Userf("%s is too large to be a LUT", filepath.Base(path))
	}
	var l *lutData
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".cube", ".3dl":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if ext == ".cube" {
			l, err = lutParseCube(b)
		} else {
			l, err = lutParse3DL(b)
		}
		if err != nil {
			return nil, err
		}
	case ".png":
		m, err := DecodeWorking(path)
		if err != nil {
			return nil, err
		}
		if l, err = lutFromHald(m); err != nil {
			return nil, err
		}
	default:
		return nil, Userf("%s is not a LUT: use a .cube, .3dl or Hald CLUT .png", filepath.Base(path))
	}
	if l.Title == "" {
		l.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return l, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// pixApplyLUT runs every pixel's encoded RGB through the LUT named by the
// "lut" argument (a path, or a file name in the luts folder). Encoded space
// because LUTs are authored for display-referred pictures; alpha is kept.
func pixApplyLUT(_ context.Context, m *img.Image, args Args) (*img.Image, error) {
	ref := args.String("lut", "")
	if ref == "" {
		return nil, Userf("Say which LUT to use, e.g. apply lut ~/luts/film.cube (a .cube, .3dl or Hald .png)")
	}
	l, err := lutLoad(ref)
	if err != nil {
		return nil, err
	}
	return img.MapColor(m, img.Encoded, l.eval), nil
}

func lutRead(job Job) (*lutData, error) {
	return lutParseFile(job.Input())
}

func lutToCube(_ context.Context, job Job) error {
	l, err := lutRead(job)
	if err != nil {
		return err
	}
	if err := os.WriteFile(job.Output, lutWriteCube(l), 0o644); err != nil {
		return err
	}
	job.Progress(1)
	return nil
}

func lutTo3DL(_ context.Context, job Job) error {
	l, err := lutRead(job)
	if err != nil {
		return err
	}
	if err := os.WriteFile(job.Output, lutWrite3DL(l), 0o644); err != nil {
		return err
	}
	job.Progress(1)
	return nil
}

// lutToHald writes a Hald CLUT PNG; the "level" argument (2-11, default 8)
// sets the image side to level^3 and the grid to level^2 points per channel.
func lutToHald(_ context.Context, job Job) error {
	level, err := job.Args.IntInRange("level", 8, 2, 11)
	if err != nil {
		return err
	}
	l, err := lutRead(job)
	if err != nil {
		return err
	}
	job.Progress(0.3)
	return EncodeWorking(lutToHaldImage(l, level), job.Output)
}
