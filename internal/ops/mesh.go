package ops

// 3D triangle meshes: OBJ, STL (ascii and binary), PLY (ascii and binary),
// glTF 2.0 / GLB and 3MF on one small indexed mesh model, plus a mesh report.
//
// The model is a list of objects, each with float32 positions, optional
// normals and UVs (one per vertex) and a triangle index list. Units are
// unknown, except that 3MF input is converted to millimetres (its own unit
// declaration) so a 3MF written afterwards, which is always millimetres, keeps
// its real size.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

func init() {
	register(Handler{Name: "mesh.convert", Run: mshConvert})
	register(Handler{Name: "mesh.info", Run: mshInfoRun})
}

const (
	mshMaxTriangles = 50_000_000
	mshMaxVertices  = 100_000_000
)

type mshObject struct {
	Name     string
	Material string
	Pos      []float32 // x y z per vertex
	Nrm      []float32 // empty, or x y z per vertex
	UV       []float32 // empty, or u v per vertex
	Idx      []uint32  // three per triangle
}

type mshMesh struct {
	Objects []*mshObject
	tris    int
}

func (o *mshObject) triCount() int { return len(o.Idx) / 3 }

func (m *mshMesh) addTris(n int) error {
	m.tris += n
	if m.tris > mshMaxTriangles {
		return Userf("this model has more than 50 million triangles, which is more than Asteria converts")
	}
	return nil
}

func (m *mshMesh) triangles() int {
	n := 0
	for _, o := range m.Objects {
		n += o.triCount()
	}
	return n
}

// keep drops empty objects and checks that something is left.
func (m *mshMesh) finish() error {
	var keep []*mshObject
	for _, o := range m.Objects {
		if o.triCount() > 0 {
			keep = append(keep, o)
		}
	}
	m.Objects = keep
	if len(keep) == 0 {
		return Userf("no triangles found in this model")
	}
	return nil
}

func mshCleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == 0 {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func mshInExt(job Job) string {
	e := strings.ToLower(filepath.Ext(job.Name(0)))
	if e == "" {
		e = strings.ToLower(filepath.Ext(job.Input()))
	}
	return e
}

func mshRead(path, ext string) (*mshMesh, error) {
	var m *mshMesh
	var err error
	switch ext {
	case ".obj", ".ply", ".stl":
		var f *os.File
		if f, err = os.Open(path); err != nil {
			return nil, err
		}
		defer f.Close()
		switch ext {
		case ".obj":
			m, err = mshReadOBJ(f)
		case ".ply":
			m, err = mshReadPLY(f)
		default:
			m, err = mshReadSTL(f)
		}
	case ".gltf", ".glb":
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			m, err = mshReadGLTF(data)
		}
	case ".3mf":
		m, err = mshRead3MF(path)
	default:
		return nil, Userf("%s is not a 3D format Asteria reads (OBJ, STL, PLY, glTF/GLB, 3MF)", ext)
	}
	if err != nil {
		return nil, err
	}
	if err := m.finish(); err != nil {
		return nil, err
	}
	return m, nil
}

func mshConvert(ctx context.Context, job Job) error {
	m, err := mshRead(job.Input(), mshInExt(job))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	job.Progress(0.5)
	outExt := strings.ToLower(filepath.Ext(job.Output))
	f, err := os.Create(job.Output)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	switch outExt {
	case ".obj":
		err = mshWriteOBJ(w, m)
	case ".stl":
		err = mshWriteSTL(w, m)
	case ".ply":
		err = mshWritePLY(w, m)
	case ".glb":
		err = mshWriteGLB(w, m)
	case ".3mf":
		err = mshWrite3MF(w, m)
	default:
		err = Userf("cannot write %s 3D files", outExt)
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ---------------------------------------------------------------- shared helpers

// mshWeld builds an object's vertices while merging identical positions.
type mshWeld struct {
	o *mshObject
	m map[[3]float32]uint32
}

func newMshWeld(name string) *mshWeld {
	return &mshWeld{o: &mshObject{Name: name}, m: map[[3]float32]uint32{}}
}

func (w *mshWeld) vertex(x, y, z float32) uint32 {
	if x == 0 {
		x = 0
	}
	if y == 0 {
		y = 0
	}
	if z == 0 {
		z = 0
	}
	k := [3]float32{x, y, z}
	if id, ok := w.m[k]; ok {
		return id
	}
	id := uint32(len(w.o.Pos) / 3)
	w.o.Pos = append(w.o.Pos, x, y, z)
	w.m[k] = id
	return id
}

func mshAppendF32(dst []byte, v float32) []byte {
	return strconv.AppendFloat(dst, float64(v), 'g', -1, 32)
}

func mshParseF32(s string, line int) (float32, error) {
	f, err := strconv.ParseFloat(s, 32)
	if err != nil && !math.IsInf(f, 0) {
		return 0, Userf("line %d: %q is not a number", line, s)
	}
	return float32(f), nil
}

// mshFaceNormal returns the unit normal of triangle abc (zero when degenerate).
func mshFaceNormal(a, b, c [3]float64) [3]float64 {
	ux, uy, uz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
	vx, vy, vz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
	n := [3]float64{uy*vz - uz*vy, uz*vx - ux*vz, ux*vy - uy*vx}
	l := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])
	if l == 0 {
		return [3]float64{}
	}
	return [3]float64{n[0] / l, n[1] / l, n[2] / l}
}

func (o *mshObject) vert(i uint32) [3]float64 {
	return [3]float64{float64(o.Pos[3*i]), float64(o.Pos[3*i+1]), float64(o.Pos[3*i+2])}
}

func mshObjName(o *mshObject, i int) string {
	if n := mshCleanName(o.Name); n != "" {
		return n
	}
	return "object" + strconv.Itoa(i+1)
}

// ---------------------------------------------------------------- OBJ

func mshReadOBJ(r io.Reader) (*mshMesh, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	type key struct{ v, vt, vn int32 }
	type state struct {
		o            *mshObject
		ids          map[key]uint32
		hasUV, hasNr bool
	}
	var vs, vts, vns []float32
	m := &mshMesh{}
	var states []*state
	var cur *state
	var oName, gName, curMat string
	lineNo := 0
	resolve := func(s string, n int) (int32, error) {
		if s == "" {
			return 0, nil
		}
		i, err := strconv.Atoi(s)
		if err != nil || i == 0 {
			return 0, Userf("line %d: %q is not a valid OBJ index", lineNo, s)
		}
		if i < 0 {
			i = n + i + 1
		}
		if i < 1 || i > n {
			return 0, Userf("line %d: face refers to element %s that doesn't exist", lineNo, s)
		}
		return int32(i), nil
	}
	pending := ""
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if strings.HasSuffix(line, "\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		if pending != "" {
			line = pending + line
			pending = ""
		}
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "v", "vn", "vt":
			need := 3
			if f[0] == "vt" {
				need = 2
			}
			if len(f) < need+1 {
				return nil, Userf("line %d: %s needs %d numbers", lineNo, f[0], need)
			}
			for i := 1; i <= need; i++ {
				x, err := mshParseF32(f[i], lineNo)
				if err != nil {
					return nil, err
				}
				switch f[0] {
				case "v":
					vs = append(vs, x)
				case "vn":
					vns = append(vns, x)
				default:
					vts = append(vts, x)
				}
			}
			if len(vs)/3 > mshMaxVertices {
				return nil, Userf("this model has more than 100 million vertices, which is more than Asteria converts")
			}
		case "o", "g":
			name := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
			if f[0] == "o" {
				oName, gName = name, ""
			} else {
				if strings.EqualFold(name, "default") {
					name = ""
				}
				gName = name
			}
			cur = nil
		case "usemtl":
			curMat = strings.TrimSpace(strings.TrimPrefix(line, "usemtl"))
			if cur != nil && cur.o.Material == "" {
				cur.o.Material = curMat
			}
		case "f":
			if len(f) < 4 {
				continue
			}
			if cur == nil {
				name := oName
				if gName != "" {
					if name == "" {
						name = gName
					} else {
						name += "/" + gName
					}
				}
				cur = &state{o: &mshObject{Name: name, Material: curMat}, ids: map[key]uint32{}}
				states = append(states, cur)
				m.Objects = append(m.Objects, cur.o)
			}
			ids := make([]uint32, 0, len(f)-1)
			for _, tok := range f[1:] {
				parts := strings.Split(tok, "/")
				v, err := resolve(parts[0], len(vs)/3)
				if err != nil {
					return nil, err
				}
				if v == 0 {
					return nil, Userf("line %d: face vertex has no position index", lineNo)
				}
				var vt, vn int32
				if len(parts) > 1 {
					if vt, err = resolve(parts[1], len(vts)/2); err != nil {
						return nil, err
					}
				}
				if len(parts) > 2 {
					if vn, err = resolve(parts[2], len(vns)/3); err != nil {
						return nil, err
					}
				}
				k := key{v, vt, vn}
				id, ok := cur.ids[k]
				if !ok {
					id = uint32(len(cur.o.Pos) / 3)
					cur.ids[k] = id
					cur.o.Pos = append(cur.o.Pos, vs[3*(v-1)], vs[3*(v-1)+1], vs[3*(v-1)+2])
					if vt != 0 {
						cur.hasUV = true
						cur.o.UV = append(cur.o.UV, vts[2*(vt-1)], vts[2*(vt-1)+1])
					} else {
						cur.o.UV = append(cur.o.UV, 0, 0)
					}
					if vn != 0 {
						cur.hasNr = true
						cur.o.Nrm = append(cur.o.Nrm, vns[3*(vn-1)], vns[3*(vn-1)+1], vns[3*(vn-1)+2])
					} else {
						cur.o.Nrm = append(cur.o.Nrm, 0, 0, 0)
					}
				}
				ids = append(ids, id)
			}
			if err := m.addTris(len(ids) - 2); err != nil {
				return nil, err
			}
			for i := 1; i+1 < len(ids); i++ {
				cur.o.Idx = append(cur.o.Idx, ids[0], ids[i], ids[i+1])
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, Userf("could not read this OBJ file: %v", err)
	}
	for _, s := range states {
		if !s.hasUV {
			s.o.UV = nil
		}
		if !s.hasNr {
			s.o.Nrm = nil
		}
	}
	return m, nil
}

func mshWriteOBJ(w io.Writer, m *mshMesh) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	bw.WriteString("# Exported by Asteria\n")
	vOff, tOff, nOff := 1, 1, 1
	var buf []byte
	for oi, o := range m.Objects {
		bw.WriteString("o " + mshObjName(o, oi) + "\n")
		nv := len(o.Pos) / 3
		for i := 0; i < nv; i++ {
			buf = append(buf[:0], 'v', ' ')
			buf = mshAppendF32(buf, o.Pos[3*i])
			buf = append(buf, ' ')
			buf = mshAppendF32(buf, o.Pos[3*i+1])
			buf = append(buf, ' ')
			buf = mshAppendF32(buf, o.Pos[3*i+2])
			buf = append(buf, '\n')
			bw.Write(buf)
		}
		hasUV, hasN := len(o.UV) == 2*nv, len(o.Nrm) == 3*nv
		if hasUV {
			for i := 0; i < nv; i++ {
				buf = append(buf[:0], 'v', 't', ' ')
				buf = mshAppendF32(buf, o.UV[2*i])
				buf = append(buf, ' ')
				buf = mshAppendF32(buf, o.UV[2*i+1])
				buf = append(buf, '\n')
				bw.Write(buf)
			}
		}
		if hasN {
			for i := 0; i < nv; i++ {
				buf = append(buf[:0], 'v', 'n', ' ')
				buf = mshAppendF32(buf, o.Nrm[3*i])
				buf = append(buf, ' ')
				buf = mshAppendF32(buf, o.Nrm[3*i+1])
				buf = append(buf, ' ')
				buf = mshAppendF32(buf, o.Nrm[3*i+2])
				buf = append(buf, '\n')
				bw.Write(buf)
			}
		}
		if o.Material != "" {
			bw.WriteString("usemtl " + mshCleanName(o.Material) + "\n")
		}
		for t := 0; t < o.triCount(); t++ {
			buf = append(buf[:0], 'f')
			for k := 0; k < 3; k++ {
				id := int(o.Idx[3*t+k])
				buf = append(buf, ' ')
				buf = strconv.AppendInt(buf, int64(id+vOff), 10)
				switch {
				case hasUV && hasN:
					buf = append(buf, '/')
					buf = strconv.AppendInt(buf, int64(id+tOff), 10)
					buf = append(buf, '/')
					buf = strconv.AppendInt(buf, int64(id+nOff), 10)
				case hasUV:
					buf = append(buf, '/')
					buf = strconv.AppendInt(buf, int64(id+tOff), 10)
				case hasN:
					buf = append(buf, '/', '/')
					buf = strconv.AppendInt(buf, int64(id+nOff), 10)
				}
			}
			buf = append(buf, '\n')
			bw.Write(buf)
		}
		vOff += nv
		if hasUV {
			tOff += nv
		}
		if hasN {
			nOff += nv
		}
	}
	return bw.Flush()
}

// ---------------------------------------------------------------- STL

func mshReadSTL(f *os.File) (*mshMesh, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	head := make([]byte, 84)
	n, _ := io.ReadFull(f, head)
	count := int64(0)
	if n == 84 {
		count = int64(binary.LittleEndian.Uint32(head[80:]))
	}
	isBinary := n == 84 && 84+count*50 == size
	startsSolid := n >= 5 && strings.EqualFold(string(head[:5]), "solid")
	if !isBinary && !startsSolid {
		// Binary files with trailing bytes (or a header-only count) still parse.
		if n == 84 && count > 0 && 84+count*50 <= size {
			isBinary = true
		} else {
			return nil, Userf("this doesn't look like an STL file")
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if isBinary {
		return mshReadBinarySTL(f, head, count)
	}
	return mshReadASCIISTL(f)
}

func mshReadBinarySTL(r io.Reader, head []byte, count int64) (*mshMesh, error) {
	if count > mshMaxTriangles {
		return nil, Userf("this STL has %d triangles, which is more than the 50 million Asteria converts", count)
	}
	br := bufio.NewReaderSize(r, 1<<20)
	if _, err := io.CopyN(io.Discard, br, 84); err != nil {
		return nil, Userf("this STL file is truncated")
	}
	name := strings.TrimSpace(strings.Trim(string(head[:80]), "\x00"))
	if len(name) >= 6 && strings.EqualFold(name[:6], "solid ") {
		name = strings.TrimSpace(name[6:])
	}
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\x01\x02\x03\x04\x05\x06\x07\x08") {
		name = ""
	}
	w := newMshWeld(name)
	w.o.Idx = make([]uint32, 0, 3*count)
	var rec [50]byte
	le := binary.LittleEndian
	for i := int64(0); i < count; i++ {
		if _, err := io.ReadFull(br, rec[:]); err != nil {
			return nil, Userf("this STL file is truncated")
		}
		for k := 0; k < 3; k++ {
			o := 12 + 12*k
			x := math.Float32frombits(le.Uint32(rec[o:]))
			y := math.Float32frombits(le.Uint32(rec[o+4:]))
			z := math.Float32frombits(le.Uint32(rec[o+8:]))
			w.o.Idx = append(w.o.Idx, w.vertex(x, y, z))
		}
	}
	return &mshMesh{Objects: []*mshObject{w.o}, tris: int(count)}, nil
}

func mshReadASCIISTL(r io.Reader) (*mshMesh, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	m := &mshMesh{}
	var w *mshWeld
	var tri [3]uint32
	nv := 0
	lineNo := 0
	for sc.Scan() {
		lineNo++
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "solid":
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), f[0]))
			w = newMshWeld(name)
			m.Objects = append(m.Objects, w.o)
		case "vertex":
			if len(f) < 4 {
				return nil, Userf("line %d: vertex needs three numbers", lineNo)
			}
			if w == nil {
				w = newMshWeld("")
				m.Objects = append(m.Objects, w.o)
			}
			var c [3]float32
			for i := 0; i < 3; i++ {
				v, err := mshParseF32(f[i+1], lineNo)
				if err != nil {
					return nil, err
				}
				c[i] = v
			}
			tri[nv] = w.vertex(c[0], c[1], c[2])
			nv++
			if nv == 3 {
				nv = 0
				if err := m.addTris(1); err != nil {
					return nil, err
				}
				w.o.Idx = append(w.o.Idx, tri[0], tri[1], tri[2])
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, Userf("could not read this STL file: %v", err)
	}
	if nv != 0 {
		return nil, Userf("this STL file ends in the middle of a triangle")
	}
	return m, nil
}

func mshWriteSTL(w io.Writer, m *mshMesh) error {
	total := m.triangles()
	if int64(total) > math.MaxUint32 {
		return Userf("too many triangles for an STL file")
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	header := make([]byte, 80)
	name := "Asteria"
	if len(m.Objects) > 0 {
		if n := mshCleanName(m.Objects[0].Name); n != "" {
			name = n
		}
	}
	// Binary files must not start with "solid": many readers then assume ASCII.
	if len(name) >= 5 && strings.EqualFold(name[:5], "solid") {
		name = "_" + name
	}
	copy(header, name)
	bw.Write(header)
	var cnt [4]byte
	binary.LittleEndian.PutUint32(cnt[:], uint32(total))
	bw.Write(cnt[:])
	var rec [50]byte
	le := binary.LittleEndian
	for _, o := range m.Objects {
		for t := 0; t < o.triCount(); t++ {
			a, b, c := o.vert(o.Idx[3*t]), o.vert(o.Idx[3*t+1]), o.vert(o.Idx[3*t+2])
			n := mshFaceNormal(a, b, c)
			for k := 0; k < 3; k++ {
				le.PutUint32(rec[4*k:], math.Float32bits(float32(n[k])))
			}
			for vi, v := range [3][3]float64{a, b, c} {
				for k := 0; k < 3; k++ {
					le.PutUint32(rec[12+12*vi+4*k:], math.Float32bits(float32(v[k])))
				}
			}
			rec[48], rec[49] = 0, 0
			bw.Write(rec[:])
		}
	}
	return bw.Flush()
}

// ---------------------------------------------------------------- PLY

type plyProp struct {
	name     string
	typ      string
	list     bool
	countTyp string
}

type plyElem struct {
	name  string
	count int
	props []plyProp
}

type plyReader struct {
	br      *bufio.Reader
	ascii   bool
	order   binary.ByteOrder
	scratch [8]byte
}

func plySize(t string) int {
	switch t {
	case "char", "int8", "uchar", "uint8":
		return 1
	case "short", "int16", "ushort", "uint16":
		return 2
	case "int", "int32", "uint", "uint32", "float", "float32":
		return 4
	case "double", "float64":
		return 8
	}
	return 0
}

func (p *plyReader) read(typ string) (float64, error) {
	if p.ascii {
		var sb []byte
		for {
			c, err := p.br.ReadByte()
			if err != nil {
				if err == io.EOF && len(sb) > 0 {
					break
				}
				return 0, Userf("this PLY file is truncated")
			}
			if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				if len(sb) == 0 {
					continue
				}
				break
			}
			sb = append(sb, c)
		}
		v, err := strconv.ParseFloat(string(sb), 64)
		if err != nil {
			return 0, Userf("this PLY file has %q where a number was expected", string(sb))
		}
		return v, nil
	}
	size := plySize(typ)
	if size == 0 {
		return 0, Userf("this PLY file uses an unknown data type %q", typ)
	}
	b := p.scratch[:size]
	if _, err := io.ReadFull(p.br, b); err != nil {
		return 0, Userf("this PLY file is truncated")
	}
	switch typ {
	case "char", "int8":
		return float64(int8(b[0])), nil
	case "uchar", "uint8":
		return float64(b[0]), nil
	case "short", "int16":
		return float64(int16(p.order.Uint16(b))), nil
	case "ushort", "uint16":
		return float64(p.order.Uint16(b)), nil
	case "int", "int32":
		return float64(int32(p.order.Uint32(b))), nil
	case "uint", "uint32":
		return float64(p.order.Uint32(b)), nil
	case "float", "float32":
		return float64(math.Float32frombits(p.order.Uint32(b))), nil
	}
	return math.Float64frombits(p.order.Uint64(b)), nil
}

func mshReadPLY(r io.Reader) (*mshMesh, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	readLine := func() (string, error) {
		s, err := br.ReadString('\n')
		if err != nil && s == "" {
			return "", Userf("this PLY file ends inside its header")
		}
		return strings.TrimSpace(s), nil
	}
	first, err := readLine()
	if err != nil || first != "ply" {
		return nil, Userf("this is not a PLY file")
	}
	pr := &plyReader{br: br}
	var elems []*plyElem
	name := ""
	formatSeen := false
header:
	for n := 0; ; n++ {
		if n > 10000 {
			return nil, Userf("this PLY header is too long")
		}
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "format":
			if len(f) < 2 {
				return nil, Userf("bad PLY format line")
			}
			formatSeen = true
			switch f[1] {
			case "ascii":
				pr.ascii = true
			case "binary_little_endian":
				pr.order = binary.LittleEndian
			case "binary_big_endian":
				pr.order = binary.BigEndian
			default:
				return nil, Userf("unknown PLY format %q", f[1])
			}
		case "comment":
			raw := strings.TrimSpace(strings.TrimPrefix(line, "comment"))
			if strings.HasPrefix(raw, "object ") && name == "" {
				name = strings.TrimSpace(strings.TrimPrefix(raw, "object "))
			}
		case "element":
			if len(f) < 3 {
				return nil, Userf("bad PLY element line")
			}
			c, err := strconv.Atoi(f[2])
			if err != nil || c < 0 {
				return nil, Userf("bad PLY element count %q", f[2])
			}
			elems = append(elems, &plyElem{name: f[1], count: c})
		case "property":
			if len(elems) == 0 {
				return nil, Userf("PLY property before any element")
			}
			e := elems[len(elems)-1]
			if len(f) >= 5 && f[1] == "list" {
				e.props = append(e.props, plyProp{name: f[4], typ: f[3], list: true, countTyp: f[2]})
			} else if len(f) >= 3 {
				e.props = append(e.props, plyProp{name: f[2], typ: f[1]})
			}
		case "end_header":
			break header
		}
	}
	if !formatSeen {
		return nil, Userf("this PLY file has no format line")
	}
	m := &mshMesh{}
	o := &mshObject{Name: name}
	nverts := 0
	for _, e := range elems {
		switch e.name {
		case "vertex":
			if e.count > mshMaxVertices {
				return nil, Userf("this model has more than 100 million vertices, which is more than Asteria converts")
			}
			nverts = e.count
			o.Pos = make([]float32, 0, 3*e.count)
			hasN, hasUV := false, false
			col := map[string]int{}
			for i, p := range e.props {
				if !p.list {
					col[p.name] = i
				}
			}
			_, hasN = col["nx"]
			_, hasUV = col["s"]
			uName, vName := "s", "t"
			if !hasUV {
				for _, pair := range [][2]string{{"u", "v"}, {"texture_u", "texture_v"}, {"texture_s", "texture_t"}} {
					if _, ok := col[pair[0]]; ok {
						hasUV, uName, vName = true, pair[0], pair[1]
						break
					}
				}
			}
			_, okx := col["x"]
			_, oky := col["y"]
			_, okz := col["z"]
			if !okx || !oky || !okz {
				return nil, Userf("this PLY file has no x, y, z vertex coordinates")
			}
			vals := make([]float64, len(e.props))
			for i := 0; i < e.count; i++ {
				for pi, p := range e.props {
					if p.list {
						c, err := pr.read(p.countTyp)
						if err != nil {
							return nil, err
						}
						for k := 0; k < int(c); k++ {
							if _, err := pr.read(p.typ); err != nil {
								return nil, err
							}
						}
						continue
					}
					v, err := pr.read(p.typ)
					if err != nil {
						return nil, err
					}
					vals[pi] = v
				}
				o.Pos = append(o.Pos, float32(vals[col["x"]]), float32(vals[col["y"]]), float32(vals[col["z"]]))
				if hasN {
					o.Nrm = append(o.Nrm, float32(vals[col["nx"]]), float32(vals[col["ny"]]), float32(vals[col["nz"]]))
				}
				if hasUV {
					o.UV = append(o.UV, float32(vals[col[uName]]), float32(vals[col[vName]]))
				}
			}
			if hasN {
				if _, ok := col["ny"]; !ok {
					o.Nrm = nil
				} else if _, ok := col["nz"]; !ok {
					o.Nrm = nil
				}
			}
		case "face":
			var ids []uint32
			for i := 0; i < e.count; i++ {
				for _, p := range e.props {
					if !p.list {
						if _, err := pr.read(p.typ); err != nil {
							return nil, err
						}
						continue
					}
					c, err := pr.read(p.countTyp)
					if err != nil {
						return nil, err
					}
					isIdx := p.name == "vertex_indices" || p.name == "vertex_index"
					ids = ids[:0]
					for k := 0; k < int(c); k++ {
						v, err := pr.read(p.typ)
						if err != nil {
							return nil, err
						}
						if isIdx {
							if v < 0 || int(v) >= nverts {
								return nil, Userf("this PLY face refers to vertex %d, which doesn't exist", int(v))
							}
							ids = append(ids, uint32(v))
						}
					}
					if isIdx && len(ids) >= 3 {
						if err := m.addTris(len(ids) - 2); err != nil {
							return nil, err
						}
						for k := 1; k+1 < len(ids); k++ {
							o.Idx = append(o.Idx, ids[0], ids[k], ids[k+1])
						}
					}
				}
			}
		default:
			for i := 0; i < e.count; i++ {
				for _, p := range e.props {
					if p.list {
						c, err := pr.read(p.countTyp)
						if err != nil {
							return nil, err
						}
						for k := 0; k < int(c); k++ {
							if _, err := pr.read(p.typ); err != nil {
								return nil, err
							}
						}
					} else if _, err := pr.read(p.typ); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	m.Objects = []*mshObject{o}
	return m, nil
}

func mshWritePLY(w io.Writer, m *mshMesh) error {
	nv, nt := 0, 0
	allN, allUV := true, true
	for _, o := range m.Objects {
		nv += len(o.Pos) / 3
		nt += o.triCount()
		if len(o.Nrm) != len(o.Pos) {
			allN = false
		}
		if len(o.UV)*3 != len(o.Pos)*2 {
			allUV = false
		}
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	bw.WriteString("ply\nformat binary_little_endian 1.0\ncomment Exported by Asteria\n")
	if len(m.Objects) > 0 {
		if n := mshCleanName(m.Objects[0].Name); n != "" {
			bw.WriteString("comment object " + n + "\n")
		}
	}
	bw.WriteString("element vertex " + strconv.Itoa(nv) + "\nproperty float x\nproperty float y\nproperty float z\n")
	if allN {
		bw.WriteString("property float nx\nproperty float ny\nproperty float nz\n")
	}
	if allUV {
		bw.WriteString("property float s\nproperty float t\n")
	}
	bw.WriteString("element face " + strconv.Itoa(nt) + "\nproperty list uchar int vertex_indices\nend_header\n")
	le := binary.LittleEndian
	var b4 [4]byte
	putF := func(v float32) {
		le.PutUint32(b4[:], math.Float32bits(v))
		bw.Write(b4[:])
	}
	for _, o := range m.Objects {
		for i := 0; i < len(o.Pos)/3; i++ {
			putF(o.Pos[3*i])
			putF(o.Pos[3*i+1])
			putF(o.Pos[3*i+2])
			if allN {
				putF(o.Nrm[3*i])
				putF(o.Nrm[3*i+1])
				putF(o.Nrm[3*i+2])
			}
			if allUV {
				putF(o.UV[2*i])
				putF(o.UV[2*i+1])
			}
		}
	}
	off := uint32(0)
	for _, o := range m.Objects {
		for t := 0; t < o.triCount(); t++ {
			bw.WriteByte(3)
			for k := 0; k < 3; k++ {
				le.PutUint32(b4[:], o.Idx[3*t+k]+off)
				bw.Write(b4[:])
			}
		}
		off += uint32(len(o.Pos) / 3)
	}
	return bw.Flush()
}

// ---------------------------------------------------------------- glTF / GLB

type gltfDoc struct {
	Asset struct {
		Version string `json:"version"`
	} `json:"asset"`
	ExtensionsRequired []string `json:"extensionsRequired"`
	Scene              *int     `json:"scene"`
	Scenes             []struct {
		Nodes []int `json:"nodes"`
	} `json:"scenes"`
	Nodes       []gltfNode     `json:"nodes"`
	Meshes      []gltfMesh     `json:"meshes"`
	Accessors   []gltfAccessor `json:"accessors"`
	BufferViews []gltfView     `json:"bufferViews"`
	Buffers     []gltfBuffer   `json:"buffers"`
	Materials   []struct {
		Name string `json:"name"`
	} `json:"materials"`
}

type gltfNode struct {
	Name        string    `json:"name"`
	Mesh        *int      `json:"mesh"`
	Children    []int     `json:"children"`
	Matrix      []float64 `json:"matrix"`
	Translation []float64 `json:"translation"`
	Rotation    []float64 `json:"rotation"`
	Scale       []float64 `json:"scale"`
}

type gltfMesh struct {
	Name       string `json:"name"`
	Primitives []struct {
		Attributes map[string]int `json:"attributes"`
		Indices    *int           `json:"indices"`
		Mode       *int           `json:"mode"`
		Material   *int           `json:"material"`
	} `json:"primitives"`
}

type gltfAccessor struct {
	BufferView    *int            `json:"bufferView"`
	ByteOffset    int             `json:"byteOffset"`
	ComponentType int             `json:"componentType"`
	Normalized    bool            `json:"normalized"`
	Count         int             `json:"count"`
	Type          string          `json:"type"`
	Sparse        json.RawMessage `json:"sparse"`
}

type gltfView struct {
	Buffer     int `json:"buffer"`
	ByteOffset int `json:"byteOffset"`
	ByteLength int `json:"byteLength"`
	ByteStride int `json:"byteStride"`
}

type gltfBuffer struct {
	URI        string `json:"uri"`
	ByteLength int    `json:"byteLength"`
}

type gltfCtx struct {
	doc  gltfDoc
	bufs [][]byte
}

func mshSplitGLB(data []byte) (jsonChunk, bin []byte, err error) {
	if len(data) < 20 {
		return nil, nil, Userf("this GLB file is truncated")
	}
	le := binary.LittleEndian
	if le.Uint32(data[0:]) != 0x46546C67 {
		return nil, nil, Userf("this is not a GLB file")
	}
	if v := le.Uint32(data[4:]); v != 2 {
		return nil, nil, Userf("this GLB is glTF version %d; Asteria reads glTF 2.0", v)
	}
	total := int(le.Uint32(data[8:]))
	if total > len(data) || total < 20 {
		return nil, nil, Userf("this GLB file is truncated")
	}
	pos := 12
	for pos+8 <= total {
		clen := int(le.Uint32(data[pos:]))
		ctype := le.Uint32(data[pos+4:])
		pos += 8
		if clen < 0 || pos+clen > total {
			return nil, nil, Userf("this GLB file has a damaged chunk")
		}
		chunk := data[pos : pos+clen]
		pos += clen
		switch ctype {
		case 0x4E4F534A:
			if jsonChunk == nil {
				jsonChunk = chunk
			}
		case 0x004E4942:
			if bin == nil {
				bin = chunk
			}
		}
	}
	if jsonChunk == nil {
		return nil, nil, Userf("this GLB file has no JSON chunk")
	}
	return jsonChunk, bin, nil
}

func mshReadGLTF(data []byte) (*mshMesh, error) {
	var jsonData, glbBin []byte
	if len(data) >= 4 && string(data[:4]) == "glTF" {
		var err error
		if jsonData, glbBin, err = mshSplitGLB(data); err != nil {
			return nil, err
		}
	} else {
		jsonData = data
	}
	g := &gltfCtx{}
	if err := json.Unmarshal(bytes.TrimPrefix(jsonData, []byte{0xEF, 0xBB, 0xBF}), &g.doc); err != nil {
		return nil, Userf("this glTF file isn't valid JSON: %v", err)
	}
	if v := g.doc.Asset.Version; v != "" && !strings.HasPrefix(v, "2") {
		return nil, Userf("this is glTF version %s; Asteria reads glTF 2.0", v)
	}
	for _, ext := range g.doc.ExtensionsRequired {
		if ext == "KHR_draco_mesh_compression" || ext == "EXT_meshopt_compression" {
			return nil, Userf("this glTF uses %s compression, which Asteria can't read", ext)
		}
	}
	for i, b := range g.doc.Buffers {
		switch {
		case b.URI == "":
			if i != 0 || glbBin == nil {
				return nil, Userf("this glTF buffer %d has no data", i)
			}
			g.bufs = append(g.bufs, glbBin)
		case strings.HasPrefix(b.URI, "data:"):
			comma := strings.IndexByte(b.URI, ',')
			if comma < 0 {
				return nil, Userf("this glTF has a malformed data: buffer")
			}
			var raw []byte
			var err error
			if strings.Contains(b.URI[:comma], ";base64") {
				raw, err = base64.StdEncoding.DecodeString(b.URI[comma+1:])
			} else {
				var s string
				if s, err = url.PathUnescape(b.URI[comma+1:]); err == nil {
					raw = []byte(s)
				}
			}
			if err != nil {
				return nil, Userf("this glTF has an unreadable data: buffer: %v", err)
			}
			g.bufs = append(g.bufs, raw)
		default:
			return nil, Userf("this glTF points to an external buffer file (%s). Asteria reads only .glb files or .gltf files with embedded data; export again with the buffers embedded", b.URI)
		}
	}
	return g.build()
}

func gltfCompSize(ct int) int {
	switch ct {
	case 5120, 5121:
		return 1
	case 5122, 5123:
		return 2
	case 5125, 5126:
		return 4
	}
	return 0
}

func gltfTypeComps(t string) int {
	switch t {
	case "SCALAR":
		return 1
	case "VEC2":
		return 2
	case "VEC3":
		return 3
	case "VEC4":
		return 4
	}
	return 0
}

// access validates accessor ai and returns a function reading component c of
// element i as a float64, plus the element count and components per element.
func (g *gltfCtx) access(ai int) (func(i, c int) float64, int, int, error) {
	if ai < 0 || ai >= len(g.doc.Accessors) {
		return nil, 0, 0, Userf("this glTF refers to accessor %d, which doesn't exist", ai)
	}
	a := g.doc.Accessors[ai]
	if len(a.Sparse) > 0 && string(a.Sparse) != "null" {
		return nil, 0, 0, Userf("this glTF uses sparse accessors, which Asteria can't read")
	}
	comps := gltfTypeComps(a.Type)
	csize := gltfCompSize(a.ComponentType)
	if comps == 0 || csize == 0 || a.Count < 0 {
		return nil, 0, 0, Userf("this glTF has an unsupported accessor (%s, type %d)", a.Type, a.ComponentType)
	}
	if a.BufferView == nil {
		return func(int, int) float64 { return 0 }, a.Count, comps, nil
	}
	vi := *a.BufferView
	if vi < 0 || vi >= len(g.doc.BufferViews) {
		return nil, 0, 0, Userf("this glTF refers to buffer view %d, which doesn't exist", vi)
	}
	v := g.doc.BufferViews[vi]
	if v.Buffer < 0 || v.Buffer >= len(g.bufs) {
		return nil, 0, 0, Userf("this glTF refers to a missing buffer")
	}
	buf := g.bufs[v.Buffer]
	stride := v.ByteStride
	if stride == 0 {
		stride = csize * comps
	}
	base := v.ByteOffset + a.ByteOffset
	if a.Count > 0 && (base < 0 || base+stride*(a.Count-1)+csize*comps > len(buf)) {
		return nil, 0, 0, Userf("this glTF accessor reads past the end of its buffer")
	}
	le := binary.LittleEndian
	ct, norm := a.ComponentType, a.Normalized
	return func(i, c int) float64 {
		b := buf[base+i*stride+c*csize:]
		switch ct {
		case 5120:
			x := float64(int8(b[0]))
			if norm {
				return math.Max(x/127, -1)
			}
			return x
		case 5121:
			x := float64(b[0])
			if norm {
				return x / 255
			}
			return x
		case 5122:
			x := float64(int16(le.Uint16(b)))
			if norm {
				return math.Max(x/32767, -1)
			}
			return x
		case 5123:
			x := float64(le.Uint16(b))
			if norm {
				return x / 65535
			}
			return x
		case 5125:
			return float64(le.Uint32(b))
		}
		return float64(math.Float32frombits(le.Uint32(b)))
	}, a.Count, comps, nil
}

func (g *gltfCtx) floats(ai, wantComps int) ([]float32, error) {
	get, n, comps, err := g.access(ai)
	if err != nil {
		return nil, err
	}
	if comps != wantComps {
		return nil, Userf("this glTF has an attribute with %d components where %d were expected", comps, wantComps)
	}
	if n > mshMaxVertices {
		return nil, Userf("this model has more than 100 million vertices, which is more than Asteria converts")
	}
	out := make([]float32, n*comps)
	for i := 0; i < n; i++ {
		for c := 0; c < comps; c++ {
			out[i*comps+c] = float32(get(i, c))
		}
	}
	return out, nil
}

func (g *gltfCtx) indices(ai int) ([]uint32, error) {
	get, n, comps, err := g.access(ai)
	if err != nil {
		return nil, err
	}
	if comps != 1 {
		return nil, Userf("this glTF has a malformed index accessor")
	}
	if n > 3*mshMaxTriangles {
		return nil, Userf("this model has more than 50 million triangles, which is more than Asteria converts")
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = uint32(get(i, 0))
	}
	return out, nil
}

type gltfMat [16]float64 // column-major

func gltfMatMul(a, b gltfMat) gltfMat {
	var r gltfMat
	for c := 0; c < 4; c++ {
		for row := 0; row < 4; row++ {
			s := 0.0
			for k := 0; k < 4; k++ {
				s += a[k*4+row] * b[c*4+k]
			}
			r[c*4+row] = s
		}
	}
	return r
}

var gltfIdentity = gltfMat{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

func gltfLocal(n gltfNode) gltfMat {
	if len(n.Matrix) == 16 {
		var m gltfMat
		copy(m[:], n.Matrix)
		return m
	}
	t, r, s := []float64{0, 0, 0}, []float64{0, 0, 0, 1}, []float64{1, 1, 1}
	if len(n.Translation) == 3 {
		t = n.Translation
	}
	if len(n.Rotation) == 4 {
		r = n.Rotation
	}
	if len(n.Scale) == 3 {
		s = n.Scale
	}
	x, y, z, w := r[0], r[1], r[2], r[3]
	l := math.Sqrt(x*x + y*y + z*z + w*w)
	if l > 0 {
		x, y, z, w = x/l, y/l, z/l, w/l
	}
	m := gltfMat{
		1 - 2*(y*y+z*z), 2 * (x*y + z*w), 2 * (x*z - y*w), 0,
		2 * (x*y - z*w), 1 - 2*(x*x+z*z), 2 * (y*z + x*w), 0,
		2 * (x*z + y*w), 2 * (y*z - x*w), 1 - 2*(x*x+y*y), 0,
		0, 0, 0, 1,
	}
	for c := 0; c < 3; c++ {
		for row := 0; row < 3; row++ {
			m[c*4+row] *= s[c]
		}
	}
	m[12], m[13], m[14] = t[0], t[1], t[2]
	return m
}

func (g *gltfCtx) build() (*mshMesh, error) {
	d := &g.doc
	var roots []int
	switch {
	case len(d.Scenes) > 0:
		si := 0
		if d.Scene != nil && *d.Scene >= 0 && *d.Scene < len(d.Scenes) {
			si = *d.Scene
		}
		roots = d.Scenes[si].Nodes
	default:
		child := make([]bool, len(d.Nodes))
		for _, n := range d.Nodes {
			for _, c := range n.Children {
				if c >= 0 && c < len(child) {
					child[c] = true
				}
			}
		}
		for i := range d.Nodes {
			if !child[i] {
				roots = append(roots, i)
			}
		}
	}
	m := &mshMesh{}
	visited := 0
	var walk func(ni int, parent gltfMat, depth int) error
	walk = func(ni int, parent gltfMat, depth int) error {
		if ni < 0 || ni >= len(d.Nodes) {
			return Userf("this glTF refers to node %d, which doesn't exist", ni)
		}
		if depth > 64 {
			return Userf("this glTF's node tree is too deep")
		}
		visited++
		if visited > 1_000_000 {
			return Userf("this glTF has too many node instances")
		}
		n := d.Nodes[ni]
		world := gltfMatMul(parent, gltfLocal(n))
		if n.Mesh != nil {
			o, err := g.meshObject(*n.Mesh, world)
			if err != nil {
				return err
			}
			if o != nil {
				if n.Name != "" {
					o.Name = n.Name
				}
				if err := m.addTris(o.triCount()); err != nil {
					return err
				}
				m.Objects = append(m.Objects, o)
			}
		}
		for _, c := range n.Children {
			if err := walk(c, world, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range roots {
		if err := walk(r, gltfIdentity, 0); err != nil {
			return nil, err
		}
	}
	if len(d.Nodes) == 0 {
		// A file with meshes but no nodes: take the meshes as they are.
		for mi := range d.Meshes {
			o, err := g.meshObject(mi, gltfIdentity)
			if err != nil {
				return nil, err
			}
			if o != nil {
				if err := m.addTris(o.triCount()); err != nil {
					return nil, err
				}
				m.Objects = append(m.Objects, o)
			}
		}
	}
	return m, nil
}

func (g *gltfCtx) meshObject(mi int, world gltfMat) (*mshObject, error) {
	d := &g.doc
	if mi < 0 || mi >= len(d.Meshes) {
		return nil, Userf("this glTF refers to mesh %d, which doesn't exist", mi)
	}
	gm := d.Meshes[mi]
	o := &mshObject{Name: gm.Name}
	if o.Name == "" {
		o.Name = "mesh" + strconv.Itoa(mi)
	}
	allN, allUV := true, true
	for _, p := range gm.Primitives {
		mode := 4
		if p.Mode != nil {
			mode = *p.Mode
		}
		if mode < 4 || mode > 6 {
			continue
		}
		pa, ok := p.Attributes["POSITION"]
		if !ok {
			continue
		}
		pos, err := g.floats(pa, 3)
		if err != nil {
			return nil, err
		}
		nv := len(pos) / 3
		var nrm, uv []float32
		if na, ok := p.Attributes["NORMAL"]; ok {
			if nrm, err = g.floats(na, 3); err != nil {
				return nil, err
			}
			if len(nrm) != len(pos) {
				nrm = nil
			}
		}
		if ua, ok := p.Attributes["TEXCOORD_0"]; ok {
			if uv, err = g.floats(ua, 2); err != nil {
				return nil, err
			}
			if len(uv)*3 != len(pos)*2 {
				uv = nil
			}
		}
		var idx []uint32
		if p.Indices != nil {
			if idx, err = g.indices(*p.Indices); err != nil {
				return nil, err
			}
		} else {
			idx = make([]uint32, nv)
			for i := range idx {
				idx[i] = uint32(i)
			}
		}
		for _, i := range idx {
			if int(i) >= nv {
				return nil, Userf("this glTF has a triangle that refers to vertex %d, which doesn't exist", i)
			}
		}
		var tris []uint32
		switch mode {
		case 4:
			for i := 0; i+2 < len(idx); i += 3 {
				tris = append(tris, idx[i], idx[i+1], idx[i+2])
			}
		case 5:
			for i := 0; i+2 < len(idx); i++ {
				if i%2 == 0 {
					tris = append(tris, idx[i], idx[i+1], idx[i+2])
				} else {
					tris = append(tris, idx[i+1], idx[i], idx[i+2])
				}
			}
		case 6:
			for i := 1; i+1 < len(idx); i++ {
				tris = append(tris, idx[0], idx[i], idx[i+1])
			}
		}
		if len(tris) == 0 {
			continue
		}
		if nrm == nil {
			allN = false
		}
		if uv == nil {
			allUV = false
		}
		base := uint32(len(o.Pos) / 3)
		o.Pos = append(o.Pos, pos...)
		o.Nrm = append(o.Nrm, nrm...)
		o.UV = append(o.UV, uv...)
		for _, t := range tris {
			o.Idx = append(o.Idx, t+base)
		}
		if o.Material == "" && p.Material != nil && *p.Material >= 0 && *p.Material < len(d.Materials) {
			o.Material = d.Materials[*p.Material].Name
		}
	}
	if o.triCount() == 0 {
		return nil, nil
	}
	if !allN || len(o.Nrm) != len(o.Pos) {
		o.Nrm = nil
	}
	if !allUV || len(o.UV)*3 != len(o.Pos)*2 {
		o.UV = nil
	}
	if world != gltfIdentity {
		gltfTransform(o, world)
	}
	return o, nil
}

func gltfTransform(o *mshObject, w gltfMat) {
	for i := 0; i+2 < len(o.Pos); i += 3 {
		x, y, z := float64(o.Pos[i]), float64(o.Pos[i+1]), float64(o.Pos[i+2])
		o.Pos[i] = float32(w[0]*x + w[4]*y + w[8]*z + w[12])
		o.Pos[i+1] = float32(w[1]*x + w[5]*y + w[9]*z + w[13])
		o.Pos[i+2] = float32(w[2]*x + w[6]*y + w[10]*z + w[14])
	}
	// Normals use the cofactor matrix of the upper 3x3 (the inverse
	// transpose, up to the sign of the determinant).
	a, b, c := w[0], w[4], w[8]
	d, e, f := w[1], w[5], w[9]
	g, h, k := w[2], w[6], w[10]
	cof := [9]float64{
		e*k - f*h, f*g - d*k, d*h - e*g,
		c*h - b*k, a*k - c*g, b*g - a*h,
		b*f - c*e, c*d - a*f, a*e - b*d,
	}
	det := a*cof[0] + b*cof[1] + c*cof[2]
	sign := 1.0
	if det < 0 {
		sign = -1
	}
	for i := 0; i+2 < len(o.Nrm); i += 3 {
		x, y, z := float64(o.Nrm[i]), float64(o.Nrm[i+1]), float64(o.Nrm[i+2])
		nx := sign * (cof[0]*x + cof[1]*y + cof[2]*z)
		ny := sign * (cof[3]*x + cof[4]*y + cof[5]*z)
		nz := sign * (cof[6]*x + cof[7]*y + cof[8]*z)
		if l := math.Sqrt(nx*nx + ny*ny + nz*nz); l > 0 {
			nx, ny, nz = nx/l, ny/l, nz/l
		}
		o.Nrm[i], o.Nrm[i+1], o.Nrm[i+2] = float32(nx), float32(ny), float32(nz)
	}
	if det < 0 {
		for i := 0; i+2 < len(o.Idx); i += 3 {
			o.Idx[i+1], o.Idx[i+2] = o.Idx[i+2], o.Idx[i+1]
		}
	}
}

type mshBlob struct {
	f32 []float32
	u32 []uint32
}

func (b mshBlob) size() int { return 4 * (len(b.f32) + len(b.u32)) }

func mshNum(v float32) json.Number {
	return json.Number(strconv.FormatFloat(float64(v), 'g', -1, 32))
}

// mshWriteGLB writes a binary glTF 2.0 with a single buffer: per object
// positions, normals, optional UVs and indices. Objects without normals are
// written flat shaded (each triangle gets its own vertices and face normal).
func mshWriteGLB(w io.Writer, m *mshMesh) error {
	var blobs []mshBlob
	var views, accessors, meshes, nodes, nodeIdx []any
	offset := 0
	addView := func(b mshBlob, target int) int {
		views = append(views, map[string]any{"buffer": 0, "byteOffset": offset, "byteLength": b.size(), "target": target})
		blobs = append(blobs, b)
		offset += b.size()
		return len(views) - 1
	}
	for oi, o := range m.Objects {
		pos, nrm, uv, idx := o.Pos, o.Nrm, o.UV, o.Idx
		nv := len(pos) / 3
		if len(nrm) != len(pos) {
			nt := o.triCount()
			pos = make([]float32, 0, 9*nt)
			nrm = make([]float32, 0, 9*nt)
			var nuv []float32
			if len(uv) == 2*nv {
				nuv = make([]float32, 0, 6*nt)
			}
			idx = make([]uint32, 0, 3*nt)
			for t := 0; t < nt; t++ {
				a, b, c := o.Idx[3*t], o.Idx[3*t+1], o.Idx[3*t+2]
				n := mshFaceNormal(o.vert(a), o.vert(b), o.vert(c))
				for k, v := range [3]uint32{a, b, c} {
					pos = append(pos, o.Pos[3*v], o.Pos[3*v+1], o.Pos[3*v+2])
					nrm = append(nrm, float32(n[0]), float32(n[1]), float32(n[2]))
					if nuv != nil {
						nuv = append(nuv, o.UV[2*v], o.UV[2*v+1])
					}
					idx = append(idx, uint32(3*t+k))
				}
			}
			uv = nuv
			nv = len(pos) / 3
		}
		if len(uv) != 2*nv {
			uv = nil
		}
		mn := [3]float32{float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(1))}
		mx := [3]float32{float32(math.Inf(-1)), float32(math.Inf(-1)), float32(math.Inf(-1))}
		for i := 0; i < nv; i++ {
			for k := 0; k < 3; k++ {
				mn[k] = min(mn[k], pos[3*i+k])
				mx[k] = max(mx[k], pos[3*i+k])
			}
		}
		attrs := map[string]any{}
		accessors = append(accessors, map[string]any{
			"bufferView": addView(mshBlob{f32: pos}, 34962), "componentType": 5126, "count": nv, "type": "VEC3",
			"min": []json.Number{mshNum(mn[0]), mshNum(mn[1]), mshNum(mn[2])},
			"max": []json.Number{mshNum(mx[0]), mshNum(mx[1]), mshNum(mx[2])},
		})
		attrs["POSITION"] = len(accessors) - 1
		accessors = append(accessors, map[string]any{"bufferView": addView(mshBlob{f32: nrm}, 34962), "componentType": 5126, "count": nv, "type": "VEC3"})
		attrs["NORMAL"] = len(accessors) - 1
		if uv != nil {
			accessors = append(accessors, map[string]any{"bufferView": addView(mshBlob{f32: uv}, 34962), "componentType": 5126, "count": nv, "type": "VEC2"})
			attrs["TEXCOORD_0"] = len(accessors) - 1
		}
		accessors = append(accessors, map[string]any{"bufferView": addView(mshBlob{u32: idx}, 34963), "componentType": 5125, "count": len(idx), "type": "SCALAR"})
		name := mshObjName(o, oi)
		meshes = append(meshes, map[string]any{"name": name, "primitives": []any{map[string]any{"attributes": attrs, "indices": len(accessors) - 1, "mode": 4}}})
		nodes = append(nodes, map[string]any{"name": name, "mesh": len(meshes) - 1})
		nodeIdx = append(nodeIdx, len(nodes)-1)
	}
	if offset > math.MaxUint32-1024*1024 {
		return Userf("this model is too big for a single .glb file (4 GB limit)")
	}
	doc := map[string]any{
		"asset":       map[string]any{"version": "2.0", "generator": "Asteria"},
		"scene":       0,
		"scenes":      []any{map[string]any{"nodes": nodeIdx}},
		"nodes":       nodes,
		"meshes":      meshes,
		"accessors":   accessors,
		"bufferViews": views,
		"buffers":     []any{map[string]any{"byteLength": offset}},
	}
	js, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	binLen := offset
	binPad := (4 - binLen%4) % 4
	total := 12 + 8 + len(js) + 8 + binLen + binPad
	le := binary.LittleEndian
	var h [12]byte
	le.PutUint32(h[0:], 0x46546C67)
	le.PutUint32(h[4:], 2)
	le.PutUint32(h[8:], uint32(total))
	bw := bufio.NewWriterSize(w, 1<<20)
	bw.Write(h[:])
	var ch [8]byte
	le.PutUint32(ch[0:], uint32(len(js)))
	le.PutUint32(ch[4:], 0x4E4F534A)
	bw.Write(ch[:])
	bw.Write(js)
	le.PutUint32(ch[0:], uint32(binLen+binPad))
	le.PutUint32(ch[4:], 0x004E4942)
	bw.Write(ch[:])
	var b4 [4]byte
	for _, b := range blobs {
		for _, v := range b.f32 {
			le.PutUint32(b4[:], math.Float32bits(v))
			bw.Write(b4[:])
		}
		for _, v := range b.u32 {
			le.PutUint32(b4[:], v)
			bw.Write(b4[:])
		}
	}
	bw.Write(make([]byte, binPad))
	return bw.Flush()
}

// ---------------------------------------------------------------- 3MF

type tmXform [12]float64 // m00 m01 m02 m10 m11 m12 m20 m21 m22 m30 m31 m32

var tmIdentity = tmXform{1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}

func (t tmXform) linear(x, y, z float64) (float64, float64, float64) {
	return x*t[0] + y*t[3] + z*t[6], x*t[1] + y*t[4] + z*t[7], x*t[2] + y*t[5] + z*t[8]
}

func (t tmXform) apply(x, y, z float64) (float64, float64, float64) {
	a, b, c := t.linear(x, y, z)
	return a + t[9], b + t[10], c + t[11]
}

// then returns the transform that applies t first and u second.
func (t tmXform) then(u tmXform) tmXform {
	var r tmXform
	for i := 0; i < 3; i++ {
		r[3*i], r[3*i+1], r[3*i+2] = u.linear(t[3*i], t[3*i+1], t[3*i+2])
	}
	r[9], r[10], r[11] = u.apply(t[9], t[10], t[11])
	return r
}

func (t tmXform) det() float64 {
	return t[0]*(t[4]*t[8]-t[5]*t[7]) - t[1]*(t[3]*t[8]-t[5]*t[6]) + t[2]*(t[3]*t[7]-t[4]*t[6])
}

func tmParseXform(s string) tmXform {
	f := strings.Fields(s)
	if len(f) != 12 {
		return tmIdentity
	}
	var t tmXform
	for i, x := range f {
		v, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return tmIdentity
		}
		t[i] = v
	}
	return t
}

type tmComp struct {
	id   int
	path string
	xf   tmXform
}

type tmObject struct {
	name  string
	verts []float32
	tris  []uint32
	comps []tmComp
}

type tmItem struct {
	id int
	xf tmXform
}

type tmModel struct {
	scale   float64
	objects map[int]*tmObject
	items   []tmItem
	order   []int
}

func tmAttr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func tmUnitScale(u string) float64 {
	switch strings.ToLower(u) {
	case "micron":
		return 0.001
	case "centimeter":
		return 10
	case "inch":
		return 25.4
	case "foot":
		return 304.8
	case "meter":
		return 1000
	}
	return 1
}

func mshParse3MFModel(r io.Reader, budget *int) (*tmModel, error) {
	dec := xml.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	dec.Strict = false
	md := &tmModel{scale: 1, objects: map[int]*tmObject{}}
	var cur *tmObject
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Userf("this 3MF model part isn't valid XML: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "model":
				md.scale = tmUnitScale(tmAttr(t, "unit"))
			case "object":
				id, _ := strconv.Atoi(tmAttr(t, "id"))
				cur = &tmObject{name: tmAttr(t, "name")}
				md.objects[id] = cur
				md.order = append(md.order, id)
			case "vertex":
				if cur == nil {
					continue
				}
				var c [3]float32
				for i, k := range []string{"x", "y", "z"} {
					v, err := strconv.ParseFloat(strings.TrimSpace(tmAttr(t, k)), 32)
					if err != nil && !math.IsInf(v, 0) {
						return nil, Userf("this 3MF has a vertex with a bad %s coordinate", k)
					}
					c[i] = float32(v)
				}
				cur.verts = append(cur.verts, c[0], c[1], c[2])
				if len(cur.verts)/3 > mshMaxVertices {
					return nil, Userf("this model has more than 100 million vertices, which is more than Asteria converts")
				}
			case "triangle":
				if cur == nil {
					continue
				}
				a, e1 := strconv.Atoi(tmAttr(t, "v1"))
				b, e2 := strconv.Atoi(tmAttr(t, "v2"))
				c, e3 := strconv.Atoi(tmAttr(t, "v3"))
				if e1 != nil || e2 != nil || e3 != nil || a < 0 || b < 0 || c < 0 {
					return nil, Userf("this 3MF has a triangle with bad vertex numbers")
				}
				cur.tris = append(cur.tris, uint32(a), uint32(b), uint32(c))
				*budget--
				if *budget < 0 {
					return nil, Userf("this model has more than 50 million triangles, which is more than Asteria converts")
				}
			case "component":
				if cur == nil {
					continue
				}
				id, _ := strconv.Atoi(tmAttr(t, "objectid"))
				cur.comps = append(cur.comps, tmComp{id: id, path: tmAttr(t, "path"), xf: tmParseXform(tmAttr(t, "transform"))})
			case "item":
				id, _ := strconv.Atoi(tmAttr(t, "objectid"))
				md.items = append(md.items, tmItem{id: id, xf: tmParseXform(tmAttr(t, "transform"))})
			}
		case xml.EndElement:
			if t.Name.Local == "object" {
				cur = nil
			}
		}
	}
	return md, nil
}

func mshRead3MF(path string) (*mshMesh, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, Userf("this isn't a readable 3MF (zip) file: %v", err)
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "/"))] = f
	}
	rootPart := "3d/3dmodel.model"
	if rel := files["_rels/.rels"]; rel != nil {
		if data, err := gisZipRead(rel); err == nil {
			if tree, err := gisParseXML(data); err == nil {
				for _, r := range tree.childrenNamed("Relationship") {
					if strings.HasSuffix(strings.ToLower(r.Attrs["Type"]), "/3dmodel") && r.Attrs["Target"] != "" {
						rootPart = strings.ToLower(strings.TrimPrefix(r.Attrs["Target"], "/"))
						break
					}
				}
			}
		}
	}
	budget := mshMaxTriangles
	models := map[string]*tmModel{}
	load := func(part string) (*tmModel, error) {
		key := strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(part, "\\", "/"), "/"))
		if md, ok := models[key]; ok {
			return md, nil
		}
		f := files[key]
		if f == nil {
			return nil, Userf("this 3MF refers to %s, which isn't in the package", part)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		md, err := mshParse3MFModel(rc, &budget)
		if err != nil {
			return nil, err
		}
		models[key] = md
		return md, nil
	}
	root, err := load(rootPart)
	if err != nil {
		return nil, err
	}
	m := &mshMesh{}
	unit := root.scale
	var flatten func(o *mshObject, md *tmModel, partKey string, id int, xf tmXform, depth int) error
	flatten = func(o *mshObject, md *tmModel, partKey string, id int, xf tmXform, depth int) error {
		if depth > 16 {
			return Userf("this 3MF nests components too deeply")
		}
		ob := md.objects[id]
		if ob == nil {
			return Userf("this 3MF refers to object %d, which doesn't exist", id)
		}
		if len(ob.tris) > 0 {
			base := uint32(len(o.Pos) / 3)
			nv := len(ob.verts) / 3
			for i := 0; i < nv; i++ {
				x, y, z := xf.apply(float64(ob.verts[3*i]), float64(ob.verts[3*i+1]), float64(ob.verts[3*i+2]))
				o.Pos = append(o.Pos, float32(x*unit), float32(y*unit), float32(z*unit))
			}
			flip := xf.det() < 0
			for i := 0; i+2 < len(ob.tris); i += 3 {
				a, b, c := ob.tris[i], ob.tris[i+1], ob.tris[i+2]
				if int(a) >= nv || int(b) >= nv || int(c) >= nv {
					return Userf("this 3MF has a triangle that refers to a vertex that doesn't exist")
				}
				if flip {
					b, c = c, b
				}
				o.Idx = append(o.Idx, a+base, b+base, c+base)
			}
		}
		for _, c := range ob.comps {
			target, key := md, partKey
			if c.path != "" {
				pm, err := load(c.path)
				if err != nil {
					return err
				}
				target, key = pm, strings.ToLower(c.path)
			}
			if err := flatten(o, target, key, c.id, c.xf.then(xf), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	items := root.items
	if len(items) == 0 {
		for _, id := range root.order {
			if ob := root.objects[id]; len(ob.tris) > 0 || len(ob.comps) > 0 {
				items = append(items, tmItem{id: id, xf: tmIdentity})
			}
		}
	}
	for _, it := range items {
		o := &mshObject{}
		if ob := root.objects[it.id]; ob != nil {
			o.Name = ob.name
		}
		if err := flatten(o, root, rootPart, it.id, it.xf, 0); err != nil {
			return nil, err
		}
		if err := m.addTris(o.triCount()); err != nil {
			return nil, err
		}
		m.Objects = append(m.Objects, o)
	}
	return m, nil
}

func mshWrite3MF(w io.Writer, m *mshMesh) error {
	zw := zip.NewWriter(w)
	add := func(name, content string) error {
		ew, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(ew, content)
		return err
	}
	if err := add("[Content_Types].xml", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
		"<Types xmlns=\"http://schemas.openxmlformats.org/package/2006/content-types\">"+
		"<Default Extension=\"rels\" ContentType=\"application/vnd.openxmlformats-package.relationships+xml\"/>"+
		"<Default Extension=\"model\" ContentType=\"application/vnd.ms-package.3dmanufacturing-3dmodel+xml\"/>"+
		"</Types>\n"); err != nil {
		return err
	}
	if err := add("_rels/.rels", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
		"<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\">"+
		"<Relationship Target=\"/3D/3dmodel.model\" Id=\"rel0\" Type=\"http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel\"/>"+
		"</Relationships>\n"); err != nil {
		return err
	}
	mw, err := zw.Create("3D/3dmodel.model")
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(mw, 1<<20)
	bw.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	bw.WriteString("<model unit=\"millimeter\" xml:lang=\"en-US\" xmlns=\"http://schemas.microsoft.com/3dmanufacturing/core/2015/02\">\n")
	bw.WriteString("<metadata name=\"Application\">Asteria</metadata>\n<resources>\n")
	var buf []byte
	var ids []int
	n := 0
	for oi, o := range m.Objects {
		// Weld by position: 3MF wants vertices shared between triangles.
		wd := newMshWeld("")
		remap := make([]uint32, len(o.Pos)/3)
		for i := range remap {
			remap[i] = wd.vertex(o.Pos[3*i], o.Pos[3*i+1], o.Pos[3*i+2])
		}
		n++
		ids = append(ids, n)
		bw.WriteString("<object id=\"" + strconv.Itoa(n) + "\" type=\"model\" name=\"" + gisEsc(mshObjName(o, oi)) + "\"><mesh><vertices>\n")
		for i := 0; i < len(wd.o.Pos)/3; i++ {
			buf = append(buf[:0], "<vertex x=\""...)
			buf = mshAppendF32(buf, wd.o.Pos[3*i])
			buf = append(buf, "\" y=\""...)
			buf = mshAppendF32(buf, wd.o.Pos[3*i+1])
			buf = append(buf, "\" z=\""...)
			buf = mshAppendF32(buf, wd.o.Pos[3*i+2])
			buf = append(buf, "\"/>\n"...)
			bw.Write(buf)
		}
		bw.WriteString("</vertices><triangles>\n")
		for t := 0; t < o.triCount(); t++ {
			a, b, c := remap[o.Idx[3*t]], remap[o.Idx[3*t+1]], remap[o.Idx[3*t+2]]
			if a == b || b == c || a == c {
				continue
			}
			buf = append(buf[:0], "<triangle v1=\""...)
			buf = strconv.AppendUint(buf, uint64(a), 10)
			buf = append(buf, "\" v2=\""...)
			buf = strconv.AppendUint(buf, uint64(b), 10)
			buf = append(buf, "\" v3=\""...)
			buf = strconv.AppendUint(buf, uint64(c), 10)
			buf = append(buf, "\"/>\n"...)
			bw.Write(buf)
		}
		bw.WriteString("</triangles></mesh></object>\n")
	}
	bw.WriteString("</resources>\n<build>\n")
	for _, id := range ids {
		bw.WriteString("<item objectid=\"" + strconv.Itoa(id) + "\"/>\n")
	}
	bw.WriteString("</build>\n</model>\n")
	if err := bw.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

// ---------------------------------------------------------------- info

type mshBBox struct {
	Min  [3]float64 `json:"min"`
	Max  [3]float64 `json:"max"`
	Size [3]float64 `json:"size"`
}

type mshObjInfo struct {
	Name        string  `json:"name"`
	Triangles   int     `json:"triangles"`
	Vertices    int     `json:"vertices"`
	SurfaceArea float64 `json:"surfaceArea"`
	Watertight  bool    `json:"watertight"`
	Material    string  `json:"material,omitempty"`
}

type mshInfo struct {
	Triangles        int          `json:"triangles"`
	Vertices         int          `json:"vertices"`
	Objects          int          `json:"objects"`
	BoundingBox      mshBBox      `json:"boundingBox"`
	SurfaceArea      float64      `json:"surfaceArea"`
	Volume           *float64     `json:"volume"`
	Watertight       bool         `json:"watertight"`
	OpenEdges        int          `json:"openEdges"`
	NonManifoldEdges int          `json:"nonManifoldEdges"`
	WindingInverted  bool         `json:"windingInverted,omitempty"`
	Units            string       `json:"units"`
	ObjectDetails    []mshObjInfo `json:"objectDetails"`
}

type mshStats struct {
	area, vol           float64
	open, nonManifold   int
	vertices, triangles int
	watertight          bool
}

// mshAnalyse welds an object's vertices by position, then measures it.
func mshAnalyse(o *mshObject) mshStats {
	nv := len(o.Pos) / 3
	weld := map[[3]float32]uint32{}
	ids := make([]uint32, nv)
	for i := 0; i < nv; i++ {
		k := [3]float32{o.Pos[3*i], o.Pos[3*i+1], o.Pos[3*i+2]}
		for j := range k {
			if k[j] == 0 {
				k[j] = 0
			}
		}
		id, ok := weld[k]
		if !ok {
			id = uint32(len(weld))
			weld[k] = id
		}
		ids[i] = id
	}
	used := make([]bool, len(weld))
	edges := make([]uint64, 0, o.triCount()*3)
	var st mshStats
	st.triangles = o.triCount()
	for t := 0; t < st.triangles; t++ {
		ia, ib, ic := o.Idx[3*t], o.Idx[3*t+1], o.Idx[3*t+2]
		a, b, c := o.vert(ia), o.vert(ib), o.vert(ic)
		ux, uy, uz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
		vx, vy, vz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
		cx, cy, cz := uy*vz-uz*vy, uz*vx-ux*vz, ux*vy-uy*vx
		st.area += 0.5 * math.Sqrt(cx*cx+cy*cy+cz*cz)
		st.vol += (a[0]*(b[1]*c[2]-b[2]*c[1]) - a[1]*(b[0]*c[2]-b[2]*c[0]) + a[2]*(b[0]*c[1]-b[1]*c[0])) / 6
		wa, wb, wc := ids[ia], ids[ib], ids[ic]
		used[wa], used[wb], used[wc] = true, true, true
		if wa == wb || wb == wc || wa == wc {
			continue
		}
		for _, e := range [3][2]uint32{{wa, wb}, {wb, wc}, {wc, wa}} {
			lo, hi := e[0], e[1]
			if lo > hi {
				lo, hi = hi, lo
			}
			edges = append(edges, uint64(lo)<<32|uint64(hi))
		}
	}
	for _, u := range used {
		if u {
			st.vertices++
		}
	}
	slices.Sort(edges)
	for i := 0; i < len(edges); {
		j := i
		for j < len(edges) && edges[j] == edges[i] {
			j++
		}
		switch j - i {
		case 1:
			st.open++
		case 2:
		default:
			st.nonManifold++
		}
		i = j
	}
	st.watertight = len(edges) > 0 && st.open == 0 && st.nonManifold == 0
	return st
}

func mshInfoOf(m *mshMesh) mshInfo {
	info := mshInfo{Objects: len(m.Objects), Units: "unknown", Watertight: len(m.Objects) > 0}
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	vol := 0.0
	for oi, o := range m.Objects {
		st := mshAnalyse(o)
		info.Triangles += st.triangles
		info.Vertices += st.vertices
		info.SurfaceArea += st.area
		info.OpenEdges += st.open
		info.NonManifoldEdges += st.nonManifold
		vol += st.vol
		if !st.watertight {
			info.Watertight = false
		}
		for _, i := range o.Idx {
			for k := 0; k < 3; k++ {
				v := float64(o.Pos[3*i+uint32(k)])
				lo[k] = math.Min(lo[k], v)
				hi[k] = math.Max(hi[k], v)
			}
		}
		info.ObjectDetails = append(info.ObjectDetails, mshObjInfo{
			Name: mshObjName(o, oi), Triangles: st.triangles, Vertices: st.vertices,
			SurfaceArea: mshRound(st.area), Watertight: st.watertight, Material: o.Material,
		})
	}
	if info.Triangles > 0 {
		for k := 0; k < 3; k++ {
			info.BoundingBox.Min[k] = mshRound(lo[k])
			info.BoundingBox.Max[k] = mshRound(hi[k])
			info.BoundingBox.Size[k] = mshRound(hi[k] - lo[k])
		}
	}
	info.SurfaceArea = mshRound(info.SurfaceArea)
	if info.Watertight {
		if vol < 0 {
			info.WindingInverted = true
		}
		v := mshRound(math.Abs(vol))
		info.Volume = &v
	}
	return info
}

func mshRound(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func mshInfoRun(ctx context.Context, job Job) error {
	m, err := mshRead(job.Input(), mshInExt(job))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(mshInfoOf(m), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(job.Output, append(b, '\n'), 0o644)
}
