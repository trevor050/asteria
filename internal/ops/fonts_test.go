package ops

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

var fbe = binary.BigEndian

func fntTestWrite(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fntTestSameTables(t *testing.T, want, got *fntFont) {
	t.Helper()
	if want.Flavor != got.Flavor {
		t.Fatalf("flavor %08x, want %08x", got.Flavor, want.Flavor)
	}
	if len(want.Tables) != len(got.Tables) {
		t.Fatalf("%d tables, want %d", len(got.Tables), len(want.Tables))
	}
	for i, w := range want.Tables {
		g := got.Tables[i]
		if g.Tag != w.Tag {
			t.Fatalf("table %d is %q, want %q", i, g.Tag, w.Tag)
		}
		if !bytes.Equal(g.Data, w.Data) {
			t.Errorf("table %q differs (%d vs %d bytes)", w.Tag, len(g.Data), len(w.Data))
		}
	}
}

// fntTestParses checks the font with x/image's independent sfnt parser.
func fntTestParses(t *testing.T, data []byte, wantGlyphs int) {
	t.Helper()
	f, err := sfnt.Parse(data)
	if err != nil {
		t.Fatalf("sfnt.Parse: %v", err)
	}
	if f.NumGlyphs() != wantGlyphs {
		t.Fatalf("NumGlyphs = %d, want %d", f.NumGlyphs(), wantGlyphs)
	}
}

func fntTestGlyphCount(t *testing.T) int {
	t.Helper()
	f, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	return f.NumGlyphs()
}

func TestFontsBase128(t *testing.T) {
	for _, v := range []uint32{0, 1, 127, 128, 16383, 16384, 1 << 21, 1<<28 + 5, 0xFFFFFFFF} {
		b := fntBase128(v)
		got, pos, err := fntReadBase128(b, 0)
		if err != nil || got != v || pos != len(b) {
			t.Errorf("base128 %d: got %d pos %d err %v", v, got, pos, err)
		}
	}
}

func TestFontsWOFFRoundTrip(t *testing.T) {
	orig, err := fntParse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	in := fntTestWrite(t, "in.ttf", goregular.TTF)
	woffPath := mustRun(t, "fonts.woff", []string{in}, "out.woff", Args{})
	woff, _ := os.ReadFile(woffPath)

	n := len(orig.Tables)
	if fbe.Uint32(woff) != fntSigWOFF {
		t.Fatal("bad WOFF signature")
	}
	if fbe.Uint32(woff[4:]) != fntTTF {
		t.Errorf("flavor %08x", fbe.Uint32(woff[4:]))
	}
	if int(fbe.Uint32(woff[8:])) != len(woff) || len(woff)%4 != 0 {
		t.Errorf("length field %d, file %d", fbe.Uint32(woff[8:]), len(woff))
	}
	if int(fbe.Uint16(woff[12:])) != n || fbe.Uint16(woff[14:]) != 0 {
		t.Errorf("numTables/reserved wrong")
	}
	want := 12 + 16*n
	for _, tb := range orig.Tables {
		want += fntPad4(len(tb.Data))
	}
	if int(fbe.Uint32(woff[16:])) != want {
		t.Errorf("totalSfntSize %d, want %d", fbe.Uint32(woff[16:]), want)
	}
	for i, tb := range orig.Tables {
		e := woff[44+20*i:]
		off, comp, size := fbe.Uint32(e[4:]), fbe.Uint32(e[8:]), fbe.Uint32(e[12:])
		if string(e[:4]) != tb.Tag || off%4 != 0 || comp > size || int(size) != len(tb.Data) {
			t.Errorf("directory entry %d (%s) wrong: off %d comp %d orig %d", i, tb.Tag, off, comp, size)
		}
		if fbe.Uint32(e[16:]) != tb.Checksum {
			t.Errorf("table %s checksum not kept", tb.Tag)
		}
	}

	ttfPath := mustRun(t, "fonts.sfnt", []string{woffPath}, "back.ttf", Args{"flavor": "ttf"})
	back, _ := os.ReadFile(ttfPath)
	got, err := fntParse(back)
	if err != nil {
		t.Fatal(err)
	}
	fntTestSameTables(t, orig, got)
	fntTestParses(t, back, fntTestGlyphCount(t))
}

func TestFontsWOFF2NullTransform(t *testing.T) {
	orig, err := fntParse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	in := fntTestWrite(t, "in.ttf", goregular.TTF)
	p := mustRun(t, "fonts.woff2", []string{in}, "out.woff2", Args{})
	w2, _ := os.ReadFile(p)

	n := len(orig.Tables)
	if fbe.Uint32(w2) != fntSigWOFF2 || fbe.Uint32(w2[4:]) != fntTTF {
		t.Fatal("bad WOFF2 signature or flavor")
	}
	if int(fbe.Uint32(w2[8:])) != len(w2) {
		t.Errorf("length field %d, file %d", fbe.Uint32(w2[8:]), len(w2))
	}
	if int(fbe.Uint16(w2[12:])) != n {
		t.Errorf("numTables %d, want %d", fbe.Uint16(w2[12:]), n)
	}
	want := 12 + 16*n
	for _, tb := range orig.Tables {
		want += fntPad4(len(tb.Data))
	}
	if int(fbe.Uint32(w2[16:])) != want {
		t.Errorf("totalSfntSize %d, want %d", fbe.Uint32(w2[16:]), want)
	}
	// Directory: transform bits are 3 for glyf/loca and 0 for everything else
	// (including hmtx), and no transformLength follows any entry.
	pos := 48
	var tags []string
	for i := 0; i < n; i++ {
		flags := w2[pos]
		pos++
		idx := int(flags & 0x3f)
		tag := ""
		if idx == 63 {
			tag = string(w2[pos : pos+4])
			pos += 4
		} else {
			tag = fntW2Tags[idx]
		}
		tags = append(tags, tag)
		ver := flags >> 6
		wantVer := byte(0)
		if tag == "glyf" || tag == "loca" {
			wantVer = 3
		}
		if ver != wantVer {
			t.Errorf("table %s transform version %d, want %d", tag, ver, wantVer)
		}
		var l uint32
		var err error
		l, pos, err = fntReadBase128(w2, pos)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range orig.Tables {
			if tb.Tag == tag && int(l) != len(tb.Data) {
				t.Errorf("table %s origLength %d, want %d", tag, l, len(tb.Data))
			}
		}
	}
	if pos+int(fbe.Uint32(w2[20:])) > len(w2) {
		t.Error("totalCompressedSize runs past the file")
	}
	gi, li := fntIndexOf(tags, "glyf"), fntIndexOf(tags, "loca")
	if gi < 0 || li != gi+1 {
		t.Errorf("loca should follow glyf in the directory: glyf %d loca %d", gi, li)
	}

	got, err := fntParse(w2)
	if err != nil {
		t.Fatal(err)
	}
	fntTestSameTables(t, orig, got)

	ttfPath := mustRun(t, "fonts.sfnt", []string{p}, "back.ttf", Args{"flavor": "ttf"})
	back, _ := os.ReadFile(ttfPath)
	fntTestParses(t, back, fntTestGlyphCount(t))

	// WOFF2 -> WOFF -> TTF keeps the tables too.
	woff := mustRun(t, "fonts.woff", []string{p}, "again.woff", Args{})
	ttf2 := mustRun(t, "fonts.sfnt", []string{woff}, "again.ttf", Args{"flavor": "ttf"})
	b2, _ := os.ReadFile(ttf2)
	g2, err := fntParse(b2)
	if err != nil {
		t.Fatal(err)
	}
	fntTestSameTables(t, orig, g2)
}

func fntIndexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestFontsWOFF2TruncatedDoesNotPanic(t *testing.T) {
	orig, _ := fntParse(goregular.TTF)
	w2, err := fntEncodeWOFF2(orig)
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k < len(w2)-8; k += len(w2)/60 + 1 {
		if _, err := fntParse(w2[:k]); err == nil {
			t.Errorf("truncated to %d bytes parsed without error", k)
		}
	}
	// Corrupt bytes must not panic either.
	for k := 0; k < 200; k++ {
		c := append([]byte(nil), w2...)
		c[(k*7919)%len(c)] ^= 0xff
		_, _ = fntParse(c)
	}
}

func TestFontsCFFFlavorErrors(t *testing.T) {
	orig, _ := fntParse(goregular.TTF)
	orig.Flavor = fntOTTO
	in := fntTestWrite(t, "cff.woff", mustEncodeWOFF(t, orig))
	if _, err := run(t, "fonts.sfnt", []string{in}, "x.ttf", Args{"flavor": "ttf"}); err == nil || !strings.Contains(err.Error(), "OTF") {
		t.Fatalf("expected a CFF/OTF hint, got %v", err)
	}
	out := mustRun(t, "fonts.sfnt", []string{in}, "x.otf", Args{"flavor": "otf"})
	b, _ := os.ReadFile(out)
	if string(b[:4]) != "OTTO" {
		t.Error("otf output should start with OTTO")
	}
}

func mustEncodeWOFF(t *testing.T, f *fntFont) []byte {
	t.Helper()
	b, err := fntEncodeWOFF(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFontsInfo(t *testing.T) {
	in := fntTestWrite(t, "go.ttf", goregular.TTF)
	out := mustRun(t, "fonts.info", []string{in}, "go.json", Args{})
	b, _ := os.ReadFile(out)
	var doc fntInfoDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	f, _ := sfnt.Parse(goregular.TTF)
	if doc.GlyphCount != f.NumGlyphs() || doc.UnitsPerEm != int(f.UnitsPerEm()) {
		t.Errorf("glyphs %d / upem %d, want %d / %d", doc.GlyphCount, doc.UnitsPerEm, f.NumGlyphs(), f.UnitsPerEm())
	}
	if doc.Family == "" || doc.Container != "TrueType (TTF)" || doc.Outlines != "TrueType" {
		t.Errorf("unexpected info: %+v", doc)
	}
	if len(doc.Tables) < 8 || fntIndexOf(doc.Tables, "glyf") < 0 || fntIndexOf(doc.Tables, "cmap") < 0 {
		t.Errorf("tables = %v", doc.Tables)
	}
	if len(doc.UnicodeRanges) == 0 || doc.UnicodeRanges[0].Block != "Basic Latin" || doc.UnicodeRanges[0].Count < 90 {
		t.Errorf("ranges = %+v", doc.UnicodeRanges)
	}
}

// ---------------------------------------------------------------------------
// Transformed WOFF2 (glyf/loca/hmtx): the test builds a transformed file from
// goregular with a deliberately simple encoder (every point uses the 4-byte
// triplet form) and checks the decoder against the original outlines.
// ---------------------------------------------------------------------------

type fntTGlyph struct {
	nc     int
	bbox   [4]int16
	end    []int
	ins    []byte
	x, y   []int
	on     []bool
	comp   []byte
	hasIns bool
}

func fntTParseGlyph(g []byte) fntTGlyph {
	tg := fntTGlyph{nc: int(int16(fbe.Uint16(g)))}
	for i := range tg.bbox {
		tg.bbox[i] = int16(fbe.Uint16(g[2+2*i:]))
	}
	pos := 10
	if tg.nc < 0 {
		for more := true; more; {
			fl := int(fbe.Uint16(g[pos:]))
			pos += 4
			if fl&1 != 0 {
				pos += 4
			} else {
				pos += 2
			}
			switch {
			case fl&0x8 != 0:
				pos += 2
			case fl&0x40 != 0:
				pos += 4
			case fl&0x80 != 0:
				pos += 8
			}
			if fl&0x100 != 0 {
				tg.hasIns = true
			}
			more = fl&0x20 != 0
		}
		end := pos
		if tg.hasIns {
			il := int(fbe.Uint16(g[pos:]))
			tg.ins = g[pos+2 : pos+2+il]
			end = pos + 2 + il
		}
		tg.comp = g[10:end]
		return tg
	}
	for i := 0; i < tg.nc; i++ {
		tg.end = append(tg.end, int(fbe.Uint16(g[pos:])))
		pos += 2
	}
	il := int(fbe.Uint16(g[pos:]))
	tg.ins = g[pos+2 : pos+2+il]
	pos += 2 + il
	total := tg.end[tg.nc-1] + 1
	var flags []byte
	for len(flags) < total {
		f := g[pos]
		pos++
		flags = append(flags, f)
		if f&8 != 0 {
			r := int(g[pos])
			pos++
			for ; r > 0; r-- {
				flags = append(flags, f)
			}
		}
	}
	read := func(short, same byte) []int {
		var out []int
		cur := 0
		for _, f := range flags {
			v := 0
			switch {
			case f&short != 0:
				v = int(g[pos])
				pos++
				if f&same == 0 {
					v = -v
				}
			case f&same != 0:
			default:
				v = int(int16(fbe.Uint16(g[pos:])))
				pos += 2
			}
			cur += v
			out = append(out, cur)
		}
		return out
	}
	tg.x = read(2, 0x10)
	tg.y = read(4, 0x20)
	for _, f := range flags {
		tg.on = append(tg.on, f&1 != 0)
	}
	return tg
}

func fntTGlyphs(t *testing.T, f *fntFont) []*fntTGlyph {
	t.Helper()
	glyf, loca := f.table("glyf"), f.table("loca")
	numGlyphs := int(fbe.Uint16(f.table("maxp")[4:]))
	format := int(fbe.Uint16(f.table("head")[50:]))
	off := func(i int) int {
		if format == 0 {
			return 2 * int(fbe.Uint16(loca[2*i:]))
		}
		return int(fbe.Uint32(loca[4*i:]))
	}
	out := make([]*fntTGlyph, numGlyphs)
	for i := 0; i < numGlyphs; i++ {
		a, b := off(i), off(i+1)
		if b > a {
			g := fntTParseGlyph(glyf[a:b])
			out[i] = &g
		}
	}
	return out
}

func fntT255(n int) []byte {
	switch {
	case n < 253:
		return []byte{byte(n)}
	case n < 506:
		return []byte{255, byte(n - 253)}
	case n < 762:
		return []byte{254, byte(n - 506)}
	}
	return []byte{253, byte(n >> 8), byte(n)}
}

func fntTAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// tTransformGlyf builds a WOFF2 transformed glyf table from the font.
func tTransformGlyf(t *testing.T, f *fntFont) []byte {
	t.Helper()
	gl := fntTGlyphs(t, f)
	glyf, loca := f.table("glyf"), f.table("loca")
	format := int(fbe.Uint16(f.table("head")[50:]))
	off := func(i int) int {
		if format == 0 {
			return 2 * int(fbe.Uint16(loca[2*i:]))
		}
		return int(fbe.Uint32(loca[4*i:]))
	}
	var nContour, nPoints, flagS, glyphS, compS, bboxVals, instrS bytes.Buffer
	bits := make([]byte, 4*((len(gl)+31)/32))
	for g, tg := range gl {
		if tg == nil {
			nContour.Write([]byte{0, 0})
			continue
		}
		nContour.Write([]byte{byte(tg.nc >> 8), byte(tg.nc)})
		raw := glyf[off(g):off(g+1)]
		withBBox := true
		if tg.nc < 0 {
			compS.Write(tg.comp)
			if tg.hasIns {
				glyphS.Write(fntT255(len(tg.ins)))
				instrS.Write(tg.ins)
			}
		} else {
			prev := 0
			minX, maxX, minY, maxY := 1<<30, -(1 << 30), 1<<30, -(1 << 30)
			px, py := 0, 0
			for _, e := range tg.end {
				nPoints.Write(fntT255(e - prev + 1))
				prev = e + 1
			}
			for i := range tg.x {
				dx, dy := tg.x[i]-px, tg.y[i]-py
				px, py = tg.x[i], tg.y[i]
				fl := 124
				if dx >= 0 {
					fl |= 1
				}
				if dy >= 0 {
					fl |= 2
				}
				if !tg.on[i] {
					fl |= 0x80
				}
				flagS.WriteByte(byte(fl))
				glyphS.Write([]byte{byte(fntTAbs(dx) >> 8), byte(fntTAbs(dx)), byte(fntTAbs(dy) >> 8), byte(fntTAbs(dy))})
				minX, maxX = min(minX, px), max(maxX, px)
				minY, maxY = min(minY, py), max(maxY, py)
			}
			glyphS.Write(fntT255(len(tg.ins)))
			instrS.Write(tg.ins)
			// Odd glyphs leave the bbox to the decoder when it can be derived.
			if g%2 == 1 && int(tg.bbox[0]) == minX && int(tg.bbox[1]) == minY && int(tg.bbox[2]) == maxX && int(tg.bbox[3]) == maxY {
				withBBox = false
			}
		}
		if withBBox {
			bits[g>>3] |= 0x80 >> (g & 7)
			bboxVals.Write(raw[2:10])
		}
	}
	hdr := make([]byte, 36)
	fbe.PutUint16(hdr[4:], uint16(len(gl)))
	fbe.PutUint16(hdr[6:], uint16(format))
	sizes := []int{nContour.Len(), nPoints.Len(), flagS.Len(), glyphS.Len(), compS.Len(), len(bits) + bboxVals.Len(), instrS.Len()}
	for i, s := range sizes {
		fbe.PutUint32(hdr[8+4*i:], uint32(s))
	}
	var out bytes.Buffer
	out.Write(hdr)
	for _, b := range []*bytes.Buffer{&nContour, &nPoints, &flagS, &glyphS, &compS} {
		out.Write(b.Bytes())
	}
	out.Write(bits)
	out.Write(bboxVals.Bytes())
	out.Write(instrS.Bytes())
	return out.Bytes()
}

// tTransformHmtx builds the hmtx transform; lsbFromGlyf drops the lsb arrays
// (valid only when every lsb equals the glyph's xMin).
func tTransformHmtx(f *fntFont, gl []*fntTGlyph) (data []byte, lsbMatches bool) {
	hm := f.table("hmtx")
	nHM := int(fbe.Uint16(f.table("hhea")[34:]))
	numGlyphs := int(fbe.Uint16(f.table("maxp")[4:]))
	lsbMatches = true
	lsb := make([]int, numGlyphs)
	for i := 0; i < numGlyphs; i++ {
		if i < nHM {
			lsb[i] = int(int16(fbe.Uint16(hm[4*i+2:])))
		} else {
			lsb[i] = int(int16(fbe.Uint16(hm[4*nHM+2*(i-nHM):])))
		}
		xMin := 0
		if gl[i] != nil {
			xMin = int(gl[i].bbox[0])
		}
		if lsb[i] != xMin {
			lsbMatches = false
		}
	}
	return hm, lsbMatches
}

func tHmtxPayload(f *fntFont, gl []*fntTGlyph, omitLSB bool) []byte {
	hm := f.table("hmtx")
	nHM := int(fbe.Uint16(f.table("hhea")[34:]))
	numGlyphs := int(fbe.Uint16(f.table("maxp")[4:]))
	flags := byte(0)
	if omitLSB {
		flags = 3
	}
	out := []byte{flags}
	for i := 0; i < nHM; i++ {
		out = append(out, hm[4*i], hm[4*i+1])
	}
	if !omitLSB {
		for i := 0; i < nHM; i++ {
			out = append(out, hm[4*i+2], hm[4*i+3])
		}
		out = append(out, hm[4*nHM:4*nHM+2*(numGlyphs-nHM)]...)
	}
	return out
}

func TestFontsWOFF2TransformedDecode(t *testing.T) {
	orig, err := fntParse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	gl := fntTGlyphs(t, orig)
	xf := tTransformGlyf(t, orig)
	_, lsbMatches := tTransformHmtx(orig, gl)

	for _, omit := range []bool{false, true} {
		if omit && !lsbMatches {
			continue
		}
		var ents []fntW2Entry
		for _, tb := range orig.Tables {
			e := fntW2Entry{Tag: tb.Tag, OrigLen: uint32(len(tb.Data)), Data: tb.Data}
			switch tb.Tag {
			case "glyf":
				e.Data, e.HasXform = xf, true
			case "loca":
				continue
			case "hmtx":
				e.Version, e.HasXform, e.Data = 1, true, tHmtxPayload(orig, gl, omit)
			}
			ents = append(ents, e)
			if tb.Tag == "glyf" {
				ents = append(ents, fntW2Entry{Tag: "loca", OrigLen: uint32(len(orig.table("loca"))), HasXform: true})
			}
		}
		file, err := fntBuildWOFF2(orig.Flavor, 1, 0, uint32(orig.sfntSize()), ents)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fntParse(file)
		if err != nil {
			t.Fatalf("omitLSB=%v: %v", omit, err)
		}
		for _, tb := range orig.Tables {
			switch tb.Tag {
			case "glyf", "loca", "head":
				continue
			}
			if !bytes.Equal(got.table(tb.Tag), tb.Data) {
				t.Errorf("omitLSB=%v: table %q differs", omit, tb.Tag)
			}
		}
		og, gg := fntTGlyphs(t, orig), fntTGlyphs(t, got)
		if len(og) != len(gg) {
			t.Fatalf("glyph count %d, want %d", len(gg), len(og))
		}
		for i := range og {
			if !reflect.DeepEqual(og[i], gg[i]) {
				t.Fatalf("omitLSB=%v: glyph %d differs:\n got  %+v\n want %+v", omit, i, gg[i], og[i])
			}
		}
		// The whole-file checksum must make the font self-consistent again.
		whole := fntBuildSfnt(got)
		if fntChecksum(whole) != 0xB1B0AFBA {
			t.Errorf("omitLSB=%v: head.checkSumAdjustment not recomputed (sum %08x)", omit, fntChecksum(whole))
		}
		fntTestParses(t, whole, fntTestGlyphCount(t))
	}
}
