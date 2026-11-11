package ops

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/andybalholm/brotli"
)

// Web font containers. A font is read into its sfnt tables (fntFont) from
// TTF/OTF, WOFF 1.0 or WOFF2 and written back out as any of them, so every
// conversion is "parse, then serialise". Tables are never edited except where
// WOFF2's optional glyf/loca/hmtx transform has to be undone on read.
//
//   - WOFF 1.0: zlib per table, spec-exact header and directory, original
//     checksums kept, tables padded to 4 bytes.
//   - WOFF2 write: the "null" transform for every table (glyf/loca use
//     transform version 3, everything else version 0), one brotli stream.
//     Lossless and byte-exact per table, but not as small as a transformed
//     glyf. Very old WOFF2 decoders (pre-2018) do not know version 3.
//   - WOFF2 read: also reverses the standard glyf/loca transform and the hmtx
//     transform that most WOFF2 files in the wild use. Glyph data is
//     rebuilt, so glyf/loca/head.checkSumAdjustment may differ in bytes from
//     the original while describing identical outlines.
//   - Font collections (.ttc) and WOFF/WOFF2 metadata/private blocks are not
//     carried over.

func init() {
	register(Handler{Name: "fonts.woff", Run: fntToWOFF})
	register(Handler{Name: "fonts.woff2", Run: fntToWOFF2})
	register(Handler{Name: "fonts.sfnt", Run: fntToSfnt})
	register(Handler{Name: "fonts.info", Run: fntInfo})
}

const (
	fntMaxFile  = 256 << 20
	fntMaxTotal = 512 << 20

	fntSigWOFF  = 0x774F4646 // wOFF
	fntSigWOFF2 = 0x774F4632 // wOF2
	fntTTF      = 0x00010000
	fntOTTO     = 0x4F54544F
	fntTrue     = 0x74727565
	fntTTCF     = 0x74746366
)

type fntTable struct {
	Tag      string
	Checksum uint32
	Data     []byte
}

type fntFont struct {
	Flavor       uint32
	Tables       []fntTable // sorted by tag
	Major, Minor uint16     // version carried by the source WOFF, if any
}

func (f *fntFont) table(tag string) []byte {
	for _, t := range f.Tables {
		if t.Tag == tag {
			return t.Data
		}
	}
	return nil
}

func fntPad4(n int) int { return (n + 3) &^ 3 }

func fntChecksum(d []byte) uint32 {
	var s uint32
	n := len(d) &^ 3
	for i := 0; i < n; i += 4 {
		s += binary.BigEndian.Uint32(d[i:])
	}
	if n < len(d) {
		var w [4]byte
		copy(w[:], d[n:])
		s += binary.BigEndian.Uint32(w[:])
	}
	return s
}

// fntTableChecksum is the directory checksum of a table; head is summed with
// checkSumAdjustment zeroed.
func fntTableChecksum(tag string, d []byte) uint32 {
	if tag == "head" && len(d) >= 12 {
		c := append([]byte(nil), d...)
		clear(c[8:12])
		return fntChecksum(c)
	}
	return fntChecksum(d)
}

func (f *fntFont) sortTables() {
	sort.Slice(f.Tables, func(i, j int) bool { return f.Tables[i].Tag < f.Tables[j].Tag })
}

func (f *fntFont) sfntSize() int {
	n := 12 + 16*len(f.Tables)
	for _, t := range f.Tables {
		n += fntPad4(len(t.Data))
	}
	return n
}

// woffVersion returns the major/minor version for a WOFF header: the font's
// head.fontRevision (Fixed 16.16) when present.
func (f *fntFont) woffVersion() (uint16, uint16) {
	if h := f.table("head"); len(h) >= 8 {
		return binary.BigEndian.Uint16(h[4:]), binary.BigEndian.Uint16(h[6:])
	}
	return f.Major, f.Minor
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

func fntLoad(path string) (*fntFont, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > fntMaxFile {
		return nil, Userf("this font file is too large to convert (limit 256 MB)")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return fntParse(b)
}

func fntParse(b []byte) (*fntFont, error) {
	if len(b) < 12 {
		return nil, Userf("this is not a font file (too short)")
	}
	switch binary.BigEndian.Uint32(b) {
	case fntSigWOFF:
		return fntParseWOFF(b)
	case fntSigWOFF2:
		return fntParseWOFF2(b)
	case fntTTCF:
		return nil, Userf("font collections (.ttc) are not supported; extract a single font first")
	case fntTTF, fntOTTO, fntTrue:
		return fntParseSfnt(b)
	}
	return nil, Userf("this is not a TTF, OTF, WOFF or WOFF2 font")
}

func fntParseSfnt(b []byte) (*fntFont, error) {
	n := int(binary.BigEndian.Uint16(b[4:]))
	if n == 0 || len(b) < 12+16*n {
		return nil, Userf("the font's table directory is damaged")
	}
	f := &fntFont{Flavor: binary.BigEndian.Uint32(b)}
	for i := 0; i < n; i++ {
		e := b[12+16*i:]
		tag := string(e[:4])
		sum := binary.BigEndian.Uint32(e[4:])
		off := int(binary.BigEndian.Uint32(e[8:]))
		ln := int(binary.BigEndian.Uint32(e[12:]))
		if off+ln > len(b) {
			return nil, Userf("the font is truncated (table %q runs past the end of the file)", tag)
		}
		f.Tables = append(f.Tables, fntTable{Tag: tag, Checksum: sum, Data: b[off : off+ln]})
	}
	f.sortTables()
	return f, nil
}

func fntParseWOFF(b []byte) (*fntFont, error) {
	if len(b) < 44 {
		return nil, Userf("the WOFF file is truncated")
	}
	f := &fntFont{Flavor: binary.BigEndian.Uint32(b[4:])}
	length := int(binary.BigEndian.Uint32(b[8:]))
	n := int(binary.BigEndian.Uint16(b[12:]))
	f.Major, f.Minor = binary.BigEndian.Uint16(b[20:]), binary.BigEndian.Uint16(b[22:])
	if length > len(b) || n == 0 || 44+20*n > len(b) {
		return nil, Userf("the WOFF file is truncated or damaged")
	}
	total := 0
	for i := 0; i < n; i++ {
		e := b[44+20*i:]
		tag := string(e[:4])
		off := int(binary.BigEndian.Uint32(e[4:]))
		comp := int(binary.BigEndian.Uint32(e[8:]))
		orig := int(binary.BigEndian.Uint32(e[12:]))
		sum := binary.BigEndian.Uint32(e[16:])
		total += orig
		if off+comp > len(b) || comp > orig || orig > fntMaxFile || total > fntMaxTotal {
			return nil, Userf("the WOFF file is damaged (table %q has bad sizes)", tag)
		}
		raw := b[off : off+comp]
		data := raw
		if comp < orig {
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, Userf("the WOFF file is damaged (table %q will not decompress)", tag)
			}
			data, err = io.ReadAll(io.LimitReader(zr, int64(orig)+1))
			if err != nil || len(data) != orig {
				return nil, Userf("the WOFF file is damaged (table %q has the wrong size after decompression)", tag)
			}
		}
		f.Tables = append(f.Tables, fntTable{Tag: tag, Checksum: sum, Data: data})
	}
	f.sortTables()
	return f, nil
}

// fntW2Tags are the WOFF2 "known table" tags, indexed by the low 6 bits of a
// directory entry's flags byte (63 means an explicit tag follows).
var fntW2Tags = [63]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post", "cvt ", "fpgm",
	"glyf", "loca", "prep", "CFF ", "VORG", "EBDT", "EBLC", "gasp", "hdmx", "kern",
	"LTSH", "PCLT", "VDMX", "vhea", "vmtx", "BASE", "GDEF", "GPOS", "GSUB", "EBSC",
	"JSTF", "MATH", "CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt", "avar",
	"bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar", "gvar", "hsty",
	"just", "lcar", "mort", "morx", "opbd", "prop", "trak", "Zapf", "Silf", "Glat",
	"Gloc", "Feat", "Sill",
}

var fntW2Index = func() map[string]int {
	m := make(map[string]int, len(fntW2Tags))
	for i, t := range fntW2Tags {
		m[t] = i
	}
	return m
}()

// fntReadBase128 reads a WOFF2 UIntBase128 value.
func fntReadBase128(b []byte, pos int) (uint32, int, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		if pos >= len(b) {
			return 0, pos, Userf("the WOFF2 table directory is truncated")
		}
		c := b[pos]
		pos++
		if i == 0 && c == 0x80 {
			return 0, pos, Userf("the WOFF2 table directory is damaged")
		}
		if v&0xFE000000 != 0 {
			return 0, pos, Userf("the WOFF2 table directory is damaged")
		}
		v = v<<7 | uint32(c&0x7f)
		if c&0x80 == 0 {
			return v, pos, nil
		}
	}
	return 0, pos, Userf("the WOFF2 table directory is damaged")
}

func fntBase128(v uint32) []byte {
	var buf [5]byte
	i := 4
	buf[i] = byte(v & 0x7f)
	v >>= 7
	for v > 0 {
		i--
		buf[i] = byte(v&0x7f) | 0x80
		v >>= 7
	}
	return buf[i:]
}

func fntParseWOFF2(b []byte) (*fntFont, error) {
	if len(b) < 48 {
		return nil, Userf("the WOFF2 file is truncated")
	}
	flavor := binary.BigEndian.Uint32(b[4:])
	if flavor == fntTTCF {
		return nil, Userf("WOFF2 font collections are not supported")
	}
	n := int(binary.BigEndian.Uint16(b[12:]))
	compSize := int(binary.BigEndian.Uint32(b[20:]))
	f := &fntFont{Flavor: flavor, Major: binary.BigEndian.Uint16(b[24:]), Minor: binary.BigEndian.Uint16(b[26:])}
	if n == 0 {
		return nil, Userf("the WOFF2 file has no tables")
	}
	type entry struct {
		tag          string
		orig, stream int
		xform        bool
	}
	ents := make([]entry, n)
	pos := 48
	sum := 0
	for i := range ents {
		if pos >= len(b) {
			return nil, Userf("the WOFF2 table directory is truncated")
		}
		flags := b[pos]
		pos++
		idx := int(flags & 0x3f)
		var tag string
		if idx == 63 {
			if pos+4 > len(b) {
				return nil, Userf("the WOFF2 table directory is truncated")
			}
			tag = string(b[pos : pos+4])
			pos += 4
		} else {
			tag = fntW2Tags[idx]
		}
		ver := flags >> 6
		var xform bool
		switch tag {
		case "glyf", "loca":
			switch ver {
			case 0:
				xform = true
			case 3:
			default:
				return nil, Userf("the WOFF2 %s table uses an unknown transform (version %d)", tag, ver)
			}
		case "hmtx":
			switch ver {
			case 0:
			case 1:
				xform = true
			default:
				return nil, Userf("the WOFF2 hmtx table uses an unknown transform (version %d)", ver)
			}
		default:
			if ver != 0 {
				return nil, Userf("the WOFF2 %s table uses an unknown transform (version %d)", tag, ver)
			}
		}
		orig, np, err := fntReadBase128(b, pos)
		if err != nil {
			return nil, err
		}
		pos = np
		stream := int(orig)
		if xform {
			s, np, err := fntReadBase128(b, pos)
			if err != nil {
				return nil, err
			}
			pos, stream = np, int(s)
		}
		if int(orig) > fntMaxFile || stream > fntMaxFile {
			return nil, Userf("the WOFF2 file is damaged (table %q is implausibly large)", tag)
		}
		sum += stream
		if sum > fntMaxTotal {
			return nil, Userf("the WOFF2 font is too large to convert")
		}
		ents[i] = entry{tag: tag, orig: int(orig), stream: stream, xform: xform}
	}
	if compSize < 0 || pos+compSize > len(b) {
		return nil, Userf("the WOFF2 file is truncated")
	}
	raw, err := io.ReadAll(io.LimitReader(brotli.NewReader(bytes.NewReader(b[pos:pos+compSize])), int64(sum)+1))
	if err != nil {
		return nil, Userf("the WOFF2 font data is damaged (brotli: %v)", err)
	}
	if len(raw) != sum {
		return nil, Userf("the WOFF2 font data is damaged (unexpected size after decompression)")
	}

	data := map[string][]byte{}
	xformed := map[string]bool{}
	off := 0
	for _, e := range ents {
		data[e.tag] = raw[off : off+e.stream]
		xformed[e.tag] = e.xform
		off += e.stream
	}

	rebuilt := false
	if xformed["glyf"] || xformed["loca"] {
		if !xformed["glyf"] || !xformed["loca"] {
			return nil, Userf("the WOFF2 glyf and loca tables must both be transformed or both be plain")
		}
		glyf, loca, xMins, err := fntReconstructGlyf(data["glyf"])
		if err != nil {
			return nil, err
		}
		data["glyf"], data["loca"] = glyf, loca
		rebuilt = true
		if xformed["hmtx"] {
			hm, err := fntReconstructHmtx(data["hmtx"], data["hhea"], data["maxp"], xMins)
			if err != nil {
				return nil, err
			}
			data["hmtx"] = hm
		}
	} else if xformed["hmtx"] {
		return nil, Userf("the WOFF2 hmtx transform needs the glyf transform, which this file does not use")
	}

	for _, e := range ents {
		d := data[e.tag]
		f.Tables = append(f.Tables, fntTable{Tag: e.tag, Checksum: fntTableChecksum(e.tag, d), Data: d})
	}
	f.sortTables()

	if rebuilt {
		// Glyph data changed bytes, so the whole-file checksum stored in head
		// must be recomputed (it is not in the WOFF2 container).
		for i, t := range f.Tables {
			if t.Tag == "head" && len(t.Data) >= 12 {
				h := append([]byte(nil), t.Data...)
				clear(h[8:12])
				f.Tables[i].Data = h
				sfnt := fntBuildSfnt(f)
				binary.BigEndian.PutUint32(h[8:], 0xB1B0AFBA-fntChecksum(sfnt))
			}
		}
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// WOFF2 glyf / loca / hmtx transform (decode)
// ---------------------------------------------------------------------------

type fntStream struct {
	b   []byte
	pos int
	bad bool
}

func (s *fntStream) take(n int) []byte {
	if s.bad || n < 0 || s.pos+n > len(s.b) {
		s.bad = true
		return nil
	}
	v := s.b[s.pos : s.pos+n]
	s.pos += n
	return v
}

func (s *fntStream) u8() int {
	v := s.take(1)
	if s.bad {
		return 0
	}
	return int(v[0])
}

func (s *fntStream) u16() int {
	v := s.take(2)
	if s.bad {
		return 0
	}
	return int(v[0])<<8 | int(v[1])
}

// u255 reads a WOFF2 255UInt16.
func (s *fntStream) u255() int {
	switch c := s.u8(); c {
	case 253:
		return s.u16()
	case 255:
		return 253 + s.u8()
	case 254:
		return 506 + s.u8()
	default:
		return c
	}
}

func fntTriplet(f int, s *fntStream) (dx, dy int) {
	var n int
	switch {
	case f < 84:
		n = 1
	case f < 120:
		n = 2
	case f < 124:
		n = 3
	default:
		n = 4
	}
	in := s.take(n)
	if s.bad {
		return 0, 0
	}
	sign := func(flag, v int) int {
		if flag&1 != 0 {
			return v
		}
		return -v
	}
	switch {
	case f < 10:
		dy = sign(f, ((f&14)<<7)+int(in[0]))
	case f < 20:
		dx = sign(f, (((f-10)&14)<<7)+int(in[0]))
	case f < 84:
		b0 := f - 20
		b1 := int(in[0])
		dx = sign(f, 1+(b0&0x30)+(b1>>4))
		dy = sign(f>>1, 1+((b0&0x0c)<<2)+(b1&0x0f))
	case f < 120:
		b0 := f - 84
		dx = sign(f, 1+((b0/12)<<8)+int(in[0]))
		dy = sign(f>>1, 1+(((b0%12)>>2)<<8)+int(in[1]))
	case f < 124:
		b2 := int(in[1])
		dx = sign(f, (int(in[0])<<4)+(b2>>4))
		dy = sign(f>>1, ((b2&0x0f)<<8)+int(in[2]))
	default:
		dx = sign(f, (int(in[0])<<8)+int(in[1]))
		dy = sign(f>>1, (int(in[2])<<8)+int(in[3]))
	}
	return dx, dy
}

var errFntGlyf = Userf("this WOFF2 font has a damaged glyph table")

// fntReconstructGlyf undoes the WOFF2 glyf transform and returns the glyf and
// loca tables plus each glyph's xMin (for the hmtx transform).
func fntReconstructGlyf(t []byte) (glyf, loca []byte, xMins []int16, err error) {
	if len(t) < 36 {
		return nil, nil, nil, errFntGlyf
	}
	optFlags := int(binary.BigEndian.Uint16(t[2:]))
	numGlyphs := int(binary.BigEndian.Uint16(t[4:]))
	indexFormat := int(binary.BigEndian.Uint16(t[6:]))
	if indexFormat > 1 {
		return nil, nil, nil, errFntGlyf
	}
	var streams [7][]byte
	pos := 36
	for i := range streams {
		sz := int(binary.BigEndian.Uint32(t[8+4*i:]))
		if sz < 0 || pos+sz > len(t) {
			return nil, nil, nil, errFntGlyf
		}
		streams[i] = t[pos : pos+sz]
		pos += sz
	}
	var overlap []byte
	if optFlags&1 != 0 {
		overlap = t[pos:]
		if len(overlap) < (numGlyphs+7)/8 {
			return nil, nil, nil, errFntGlyf
		}
	}
	nContour := &fntStream{b: streams[0]}
	nPoints := &fntStream{b: streams[1]}
	flagS := &fntStream{b: streams[2]}
	glyphS := &fntStream{b: streams[3]}
	compS := &fntStream{b: streams[4]}
	bboxS := &fntStream{b: streams[5]}
	instrS := &fntStream{b: streams[6]}
	bboxBits := bboxS.take(4 * ((numGlyphs + 31) / 32))
	if bboxS.bad {
		return nil, nil, nil, errFntGlyf
	}

	var out bytes.Buffer
	offsets := make([]int, numGlyphs+1)
	xMins = make([]int16, numGlyphs)
	put16 := func(v int) { out.Write([]byte{byte(v >> 8), byte(v)}) }
	for g := 0; g < numGlyphs; g++ {
		offsets[g] = out.Len()
		nc := int(int16(nContour.u16()))
		hasBBox := bboxBits[g>>3]&(0x80>>(g&7)) != 0
		var bbox []byte
		if hasBBox {
			bbox = bboxS.take(8)
		}
		if nContour.bad || bboxS.bad {
			return nil, nil, nil, errFntGlyf
		}
		switch {
		case nc == 0:
			if hasBBox {
				return nil, nil, nil, errFntGlyf
			}
			continue
		case nc == -1:
			if !hasBBox {
				return nil, nil, nil, errFntGlyf
			}
			start := compS.pos
			haveInstr := false
			for more := true; more; {
				flags := compS.u16()
				compS.u16()
				if flags&0x0001 != 0 {
					compS.take(4)
				} else {
					compS.take(2)
				}
				switch {
				case flags&0x0008 != 0:
					compS.take(2)
				case flags&0x0040 != 0:
					compS.take(4)
				case flags&0x0080 != 0:
					compS.take(8)
				}
				if flags&0x0100 != 0 {
					haveInstr = true
				}
				more = flags&0x0020 != 0
				if compS.bad {
					return nil, nil, nil, errFntGlyf
				}
			}
			put16(0xFFFF)
			out.Write(bbox)
			out.Write(compS.b[start:compS.pos])
			if haveInstr {
				il := glyphS.u255()
				ins := instrS.take(il)
				if glyphS.bad || instrS.bad {
					return nil, nil, nil, errFntGlyf
				}
				put16(il)
				out.Write(ins)
			}
		case nc > 0:
			endPts := make([]int, nc)
			total := 0
			for i := range endPts {
				total += nPoints.u255()
				endPts[i] = total - 1
			}
			if nPoints.bad || total == 0 || total > 0xFFFF {
				return nil, nil, nil, errFntGlyf
			}
			flags := flagS.take(total)
			if flagS.bad {
				return nil, nil, nil, errFntGlyf
			}
			dxs, dys := make([]int, total), make([]int, total)
			on := make([]bool, total)
			x, y := 0, 0
			minX, minY, maxX, maxY := 1<<30, 1<<30, -(1 << 30), -(1 << 30)
			for i := 0; i < total; i++ {
				f := int(flags[i])
				on[i] = f&0x80 == 0
				dx, dy := fntTriplet(f&0x7f, glyphS)
				if glyphS.bad {
					return nil, nil, nil, errFntGlyf
				}
				dxs[i], dys[i] = dx, dy
				x += dx
				y += dy
				minX, maxX = min(minX, x), max(maxX, x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
			il := glyphS.u255()
			ins := instrS.take(il)
			if glyphS.bad || instrS.bad {
				return nil, nil, nil, errFntGlyf
			}
			put16(nc)
			if hasBBox {
				out.Write(bbox)
			} else {
				put16(minX)
				put16(minY)
				put16(maxX)
				put16(maxY)
			}
			for _, e := range endPts {
				put16(e)
			}
			put16(il)
			out.Write(ins)
			isOverlap := overlap != nil && overlap[g>>3]&(0x80>>(g&7)) != 0
			body, ok := fntSimpleGlyphPoints(dxs, dys, on, isOverlap)
			if !ok {
				return nil, nil, nil, errFntGlyf
			}
			out.Write(body)
		default:
			return nil, nil, nil, errFntGlyf
		}
		// bbox xMin is the second field after nContours.
		gb := out.Bytes()[offsets[g]:]
		xMins[g] = int16(binary.BigEndian.Uint16(gb[2:]))
		for out.Len()%4 != 0 {
			out.WriteByte(0)
		}
	}
	offsets[numGlyphs] = out.Len()

	if indexFormat == 0 {
		loca = make([]byte, 2*(numGlyphs+1))
		for i, o := range offsets {
			if o/2 > 0xFFFF {
				return nil, nil, nil, Userf("this WOFF2 font's glyph data is too large for the short loca format")
			}
			binary.BigEndian.PutUint16(loca[2*i:], uint16(o/2))
		}
	} else {
		loca = make([]byte, 4*(numGlyphs+1))
		for i, o := range offsets {
			binary.BigEndian.PutUint32(loca[4*i:], uint32(o))
		}
	}
	return out.Bytes(), loca, xMins, nil
}

// fntSimpleGlyphPoints encodes the flag and coordinate arrays of a TrueType
// simple glyph from per-point deltas (flags use repeat runs).
func fntSimpleGlyphPoints(dxs, dys []int, on []bool, overlap bool) ([]byte, bool) {
	flags := make([]byte, len(dxs))
	var xb, yb []byte
	for i := range dxs {
		var f byte
		if on[i] {
			f |= 0x01
		}
		if overlap && i == 0 {
			f |= 0x40
		}
		dx, dy := dxs[i], dys[i]
		if dx > 32767 || dx < -32768 || dy > 32767 || dy < -32768 {
			return nil, false
		}
		switch {
		case dx == 0:
			f |= 0x10
		case dx > -256 && dx < 256:
			f |= 0x02
			if dx > 0 {
				f |= 0x10
				xb = append(xb, byte(dx))
			} else {
				xb = append(xb, byte(-dx))
			}
		default:
			xb = append(xb, byte(dx>>8), byte(dx))
		}
		switch {
		case dy == 0:
			f |= 0x20
		case dy > -256 && dy < 256:
			f |= 0x04
			if dy > 0 {
				f |= 0x20
				yb = append(yb, byte(dy))
			} else {
				yb = append(yb, byte(-dy))
			}
		default:
			yb = append(yb, byte(dy>>8), byte(dy))
		}
		flags[i] = f
	}
	var out []byte
	for i := 0; i < len(flags); {
		j := i + 1
		for j < len(flags) && flags[j] == flags[i] && j-i < 256 {
			j++
		}
		if j-i > 1 {
			out = append(out, flags[i]|0x08, byte(j-i-1))
		} else {
			out = append(out, flags[i])
		}
		i = j
	}
	out = append(out, xb...)
	out = append(out, yb...)
	return out, true
}

// fntReconstructHmtx undoes the WOFF2 hmtx transform (version 1).
func fntReconstructHmtx(t, hhea, maxp []byte, xMins []int16) ([]byte, error) {
	bad := Userf("this WOFF2 font has a damaged horizontal metrics table")
	if len(hhea) < 36 || len(maxp) < 6 {
		return nil, bad
	}
	nHM := int(binary.BigEndian.Uint16(hhea[34:]))
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:]))
	if nHM == 0 || nHM > numGlyphs || numGlyphs > len(xMins) {
		return nil, bad
	}
	s := &fntStream{b: t}
	flags := s.u8()
	adv := make([]int, nHM)
	for i := range adv {
		adv[i] = s.u16()
	}
	lsb := make([]int16, numGlyphs)
	for i := 0; i < numGlyphs; i++ {
		omitted := flags&1 != 0
		if i >= nHM {
			omitted = flags&2 != 0
		}
		if omitted {
			lsb[i] = xMins[i]
		} else {
			lsb[i] = int16(s.u16())
		}
	}
	if s.bad {
		return nil, bad
	}
	out := make([]byte, 0, 4*nHM+2*(numGlyphs-nHM))
	for i := 0; i < numGlyphs; i++ {
		if i < nHM {
			out = binary.BigEndian.AppendUint16(out, uint16(adv[i]))
		}
		out = binary.BigEndian.AppendUint16(out, uint16(lsb[i]))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// fntBuildSfnt serialises the tables as a TTF/OTF file.
func fntBuildSfnt(f *fntFont) []byte {
	f.sortTables()
	n := len(f.Tables)
	out := make([]byte, f.sfntSize())
	binary.BigEndian.PutUint32(out, f.Flavor)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	pow, lg := 1, 0
	for pow*2 <= n {
		pow *= 2
		lg++
	}
	binary.BigEndian.PutUint16(out[6:], uint16(pow*16))
	binary.BigEndian.PutUint16(out[8:], uint16(lg))
	binary.BigEndian.PutUint16(out[10:], uint16(n*16-pow*16))
	off := 12 + 16*n
	for i, t := range f.Tables {
		e := out[12+16*i:]
		copy(e, t.Tag)
		binary.BigEndian.PutUint32(e[4:], t.Checksum)
		binary.BigEndian.PutUint32(e[8:], uint32(off))
		binary.BigEndian.PutUint32(e[12:], uint32(len(t.Data)))
		copy(out[off:], t.Data)
		off += fntPad4(len(t.Data))
	}
	return out
}

func fntEncodeWOFF(f *fntFont) ([]byte, error) {
	f.sortTables()
	n := len(f.Tables)
	comps := make([][]byte, n)
	for i, t := range f.Tables {
		var buf bytes.Buffer
		zw, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
		if err != nil {
			return nil, err
		}
		if _, err := zw.Write(t.Data); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		if buf.Len() < len(t.Data) {
			comps[i] = buf.Bytes()
		} else {
			comps[i] = t.Data
		}
	}
	total := 44 + 20*n
	for _, c := range comps {
		total += fntPad4(len(c))
	}
	out := make([]byte, total)
	major, minor := f.woffVersion()
	binary.BigEndian.PutUint32(out[0:], fntSigWOFF)
	binary.BigEndian.PutUint32(out[4:], f.Flavor)
	binary.BigEndian.PutUint32(out[8:], uint32(total))
	binary.BigEndian.PutUint16(out[12:], uint16(n))
	binary.BigEndian.PutUint32(out[16:], uint32(f.sfntSize()))
	binary.BigEndian.PutUint16(out[20:], major)
	binary.BigEndian.PutUint16(out[22:], minor)
	off := 44 + 20*n
	for i, t := range f.Tables {
		e := out[44+20*i:]
		copy(e, t.Tag)
		binary.BigEndian.PutUint32(e[4:], uint32(off))
		binary.BigEndian.PutUint32(e[8:], uint32(len(comps[i])))
		binary.BigEndian.PutUint32(e[12:], uint32(len(t.Data)))
		binary.BigEndian.PutUint32(e[16:], t.Checksum)
		copy(out[off:], comps[i])
		off += fntPad4(len(comps[i]))
	}
	return out, nil
}

// fntW2Entry is one table as it goes into a WOFF2 file.
type fntW2Entry struct {
	Tag      string
	Version  byte   // transform version bits (0-3)
	OrigLen  uint32 // length of the original (untransformed) table
	Data     []byte // bytes placed in the compressed stream
	HasXform bool   // a transformLength field follows (non-null transform)
}

func fntBuildWOFF2(flavor uint32, major, minor uint16, sfntSize uint32, ents []fntW2Entry) ([]byte, error) {
	var dir, stream bytes.Buffer
	for _, e := range ents {
		idx, known := fntW2Index[e.Tag]
		flags := byte(63)
		if known {
			flags = byte(idx)
		}
		dir.WriteByte(flags | e.Version<<6)
		if !known {
			dir.WriteString(e.Tag)
		}
		dir.Write(fntBase128(e.OrigLen))
		if e.HasXform {
			dir.Write(fntBase128(uint32(len(e.Data))))
		}
		stream.Write(e.Data)
	}
	level := 11
	if stream.Len() > 8<<20 {
		level = 9
	}
	var comp bytes.Buffer
	bw := brotli.NewWriterLevel(&comp, level)
	if _, err := bw.Write(stream.Bytes()); err != nil {
		return nil, err
	}
	if err := bw.Close(); err != nil {
		return nil, err
	}
	hdr := 48 + dir.Len()
	total := fntPad4(hdr + comp.Len())
	out := make([]byte, total)
	binary.BigEndian.PutUint32(out[0:], fntSigWOFF2)
	binary.BigEndian.PutUint32(out[4:], flavor)
	binary.BigEndian.PutUint32(out[8:], uint32(total))
	binary.BigEndian.PutUint16(out[12:], uint16(len(ents)))
	binary.BigEndian.PutUint32(out[16:], sfntSize)
	binary.BigEndian.PutUint32(out[20:], uint32(comp.Len()))
	binary.BigEndian.PutUint16(out[24:], major)
	binary.BigEndian.PutUint16(out[26:], minor)
	copy(out[48:], dir.Bytes())
	copy(out[hdr:], comp.Bytes())
	return out, nil
}

// fntEncodeWOFF2 writes every table with the null transform.
func fntEncodeWOFF2(f *fntFont) ([]byte, error) {
	f.sortTables()
	ents := make([]fntW2Entry, 0, len(f.Tables))
	var loca *fntW2Entry
	for _, t := range f.Tables {
		e := fntW2Entry{Tag: t.Tag, OrigLen: uint32(len(t.Data)), Data: t.Data}
		if t.Tag == "glyf" || t.Tag == "loca" {
			e.Version = 3
		}
		if t.Tag == "loca" {
			c := e
			loca = &c
			continue
		}
		ents = append(ents, e)
	}
	if loca != nil {
		// Keep loca right after glyf, as the format expects for transformed fonts.
		at := len(ents)
		for i, e := range ents {
			if e.Tag == "glyf" {
				at = i + 1
			}
		}
		ents = append(ents, fntW2Entry{})
		copy(ents[at+1:], ents[at:])
		ents[at] = *loca
	}
	major, minor := f.woffVersion()
	return fntBuildWOFF2(f.Flavor, major, minor, uint32(f.sfntSize()), ents)
}

func fntWriteOut(job Job, data []byte) error {
	if err := os.WriteFile(job.Output, data, 0o644); err != nil {
		return err
	}
	job.Progress(1)
	return nil
}

func fntToWOFF(_ context.Context, job Job) error {
	f, err := fntLoad(job.Input())
	if err != nil {
		return err
	}
	out, err := fntEncodeWOFF(f)
	if err != nil {
		return err
	}
	return fntWriteOut(job, out)
}

func fntToWOFF2(_ context.Context, job Job) error {
	f, err := fntLoad(job.Input())
	if err != nil {
		return err
	}
	out, err := fntEncodeWOFF2(f)
	if err != nil {
		return err
	}
	return fntWriteOut(job, out)
}

// fntToSfnt unpacks a WOFF/WOFF2 into a plain font. Argument "flavor" is
// "ttf" (TrueType outlines) or "otf" (CFF outlines).
func fntToSfnt(_ context.Context, job Job) error {
	f, err := fntLoad(job.Input())
	if err != nil {
		return err
	}
	want := strings.ToLower(job.Args.String("flavor", "ttf"))
	isCFF := f.Flavor == fntOTTO
	switch {
	case want == "ttf" && isCFF:
		return Userf("%s has PostScript (CFF) outlines, so it converts to OTF, not TTF", job.Name(0))
	case want == "otf" && !isCFF:
		return Userf("%s has TrueType outlines, so it converts to TTF, not OTF", job.Name(0))
	}
	return fntWriteOut(job, fntBuildSfnt(f))
}

// ---------------------------------------------------------------------------
// Font info
// ---------------------------------------------------------------------------

type fntRange struct {
	Block string `json:"block"`
	Range string `json:"range"`
	Count int    `json:"characters"`
}

type fntInfoDoc struct {
	File           string     `json:"file"`
	Container      string     `json:"container"`
	Outlines       string     `json:"outlines"`
	Family         string     `json:"family,omitempty"`
	Style          string     `json:"style,omitempty"`
	FullName       string     `json:"fullName,omitempty"`
	PostScriptName string     `json:"postScriptName,omitempty"`
	Version        string     `json:"version,omitempty"`
	Designer       string     `json:"designer,omitempty"`
	Manufacturer   string     `json:"manufacturer,omitempty"`
	License        string     `json:"license,omitempty"`
	LicenseURL     string     `json:"licenseUrl,omitempty"`
	Copyright      string     `json:"copyright,omitempty"`
	GlyphCount     int        `json:"glyphCount"`
	UnitsPerEm     int        `json:"unitsPerEm"`
	Weight         int        `json:"weight,omitempty"`
	Italic         bool       `json:"italic"`
	Variable       bool       `json:"variable"`
	Tables         []string   `json:"tables"`
	CharacterCount int        `json:"characterCount"`
	UnicodeRanges  []fntRange `json:"unicodeRanges"`
}

func fntInfo(_ context.Context, job Job) error {
	st, err := os.Stat(job.Input())
	if err != nil {
		return err
	}
	if st.Size() > fntMaxFile {
		return Userf("this font file is too large to inspect (limit 256 MB)")
	}
	raw, err := os.ReadFile(job.Input())
	if err != nil {
		return err
	}
	f, err := fntParse(raw)
	if err != nil {
		return err
	}
	doc := fntInfoDoc{File: job.Name(0), Tables: []string{}, UnicodeRanges: []fntRange{}}
	switch binary.BigEndian.Uint32(raw) {
	case fntSigWOFF:
		doc.Container = "WOFF"
	case fntSigWOFF2:
		doc.Container = "WOFF2"
	case fntOTTO:
		doc.Container = "OpenType (OTF)"
	default:
		doc.Container = "TrueType (TTF)"
	}
	if f.Flavor == fntOTTO {
		doc.Outlines = "CFF (PostScript)"
	} else {
		doc.Outlines = "TrueType"
	}
	for _, t := range f.Tables {
		doc.Tables = append(doc.Tables, strings.TrimSpace(t.Tag))
		if t.Tag == "fvar" {
			doc.Variable = true
		}
	}
	if h := f.table("head"); len(h) >= 20 {
		doc.UnitsPerEm = int(binary.BigEndian.Uint16(h[18:]))
		if len(h) >= 46 && binary.BigEndian.Uint16(h[44:])&2 != 0 {
			doc.Italic = true
		}
	}
	if m := f.table("maxp"); len(m) >= 6 {
		doc.GlyphCount = int(binary.BigEndian.Uint16(m[4:]))
	}
	if o := f.table("OS/2"); len(o) >= 6 {
		doc.Weight = int(binary.BigEndian.Uint16(o[4:]))
		if len(o) >= 64 && binary.BigEndian.Uint16(o[62:])&1 != 0 {
			doc.Italic = true
		}
	}
	names := fntNames(f.table("name"))
	pick := func(ids ...int) string {
		for _, id := range ids {
			if s := names[id]; s != "" {
				return s
			}
		}
		return ""
	}
	doc.Family = pick(16, 1)
	doc.Style = pick(17, 2)
	doc.FullName = pick(4)
	doc.PostScriptName = pick(6)
	doc.Version = pick(5)
	doc.Designer = pick(9)
	doc.Manufacturer = pick(8)
	doc.License = pick(13)
	doc.LicenseURL = pick(14)
	doc.Copyright = pick(0)

	bits := fntCmap(f.table("cmap"))
	for _, blk := range fntBlocks {
		c := 0
		for cp := blk.lo; cp <= blk.hi; cp++ {
			if bits[cp>>6]&(1<<(cp&63)) != 0 {
				c++
			}
		}
		if c > 0 {
			doc.CharacterCount += c
			doc.UnicodeRanges = append(doc.UnicodeRanges, fntRange{
				Block: blk.name, Range: fmt.Sprintf("U+%04X-U+%04X", blk.lo, blk.hi), Count: c,
			})
		}
	}
	// Characters outside the named blocks.
	total := 0
	for _, w := range bits {
		total += popcount64(w)
	}
	if other := total - doc.CharacterCount; other > 0 {
		doc.CharacterCount = total
		doc.UnicodeRanges = append(doc.UnicodeRanges, fntRange{Block: "Other", Range: "various", Count: other})
	}
	if doc.Family == "" {
		doc.Family = strings.TrimSuffix(filepath.Base(job.Name(0)), filepath.Ext(job.Name(0)))
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return fntWriteOut(job, append(out, '\n'))
}

func popcount64(x uint64) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

// fntNames reads the name table: for each name ID the best record (Windows
// English, then Windows any, then Unicode, then Macintosh).
func fntNames(t []byte) map[int]string {
	out := map[int]string{}
	if len(t) < 6 {
		return out
	}
	count := int(binary.BigEndian.Uint16(t[2:]))
	strOff := int(binary.BigEndian.Uint16(t[4:]))
	score := map[int]int{}
	for i := 0; i < count; i++ {
		r := 6 + 12*i
		if r+12 > len(t) {
			break
		}
		plat := int(binary.BigEndian.Uint16(t[r:]))
		lang := int(binary.BigEndian.Uint16(t[r+4:]))
		id := int(binary.BigEndian.Uint16(t[r+6:]))
		ln := int(binary.BigEndian.Uint16(t[r+8:]))
		off := strOff + int(binary.BigEndian.Uint16(t[r+10:]))
		if off+ln > len(t) || ln == 0 {
			continue
		}
		sc := 0
		switch {
		case plat == 3 && lang == 0x409:
			sc = 4
		case plat == 3:
			sc = 3
		case plat == 0:
			sc = 2
		case plat == 1 && lang == 0:
			sc = 1
		}
		if sc == 0 || sc <= score[id] {
			continue
		}
		b := t[off : off+ln]
		var s string
		if plat == 1 {
			rs := make([]rune, len(b))
			for j, c := range b {
				rs[j] = rune(c)
			}
			s = string(rs)
		} else {
			u := make([]uint16, len(b)/2)
			for j := range u {
				u[j] = binary.BigEndian.Uint16(b[2*j:])
			}
			s = string(utf16.Decode(u))
		}
		out[id] = strings.TrimSpace(s)
		score[id] = sc
	}
	return out
}

type fntBlock struct {
	name   string
	lo, hi int
}

var fntBlocks = []fntBlock{
	{"Basic Latin", 0x0000, 0x007F}, {"Latin-1 Supplement", 0x0080, 0x00FF},
	{"Latin Extended-A", 0x0100, 0x017F}, {"Latin Extended-B", 0x0180, 0x024F},
	{"IPA Extensions", 0x0250, 0x02AF}, {"Spacing Modifier Letters", 0x02B0, 0x02FF},
	{"Combining Diacritical Marks", 0x0300, 0x036F}, {"Greek and Coptic", 0x0370, 0x03FF},
	{"Cyrillic", 0x0400, 0x04FF}, {"Cyrillic Supplement", 0x0500, 0x052F},
	{"Armenian", 0x0530, 0x058F}, {"Hebrew", 0x0590, 0x05FF}, {"Arabic", 0x0600, 0x06FF},
	{"Devanagari", 0x0900, 0x097F}, {"Bengali", 0x0980, 0x09FF}, {"Thai", 0x0E00, 0x0E7F},
	{"Georgian", 0x10A0, 0x10FF}, {"Hangul Jamo", 0x1100, 0x11FF},
	{"Latin Extended Additional", 0x1E00, 0x1EFF}, {"Greek Extended", 0x1F00, 0x1FFF},
	{"General Punctuation", 0x2000, 0x206F}, {"Superscripts and Subscripts", 0x2070, 0x209F},
	{"Currency Symbols", 0x20A0, 0x20CF}, {"Letterlike Symbols", 0x2100, 0x214F},
	{"Number Forms", 0x2150, 0x218F}, {"Arrows", 0x2190, 0x21FF},
	{"Mathematical Operators", 0x2200, 0x22FF}, {"Miscellaneous Technical", 0x2300, 0x23FF},
	{"Box Drawing", 0x2500, 0x257F}, {"Block Elements", 0x2580, 0x259F},
	{"Geometric Shapes", 0x25A0, 0x25FF}, {"Miscellaneous Symbols", 0x2600, 0x26FF},
	{"Dingbats", 0x2700, 0x27BF}, {"CJK Symbols and Punctuation", 0x3000, 0x303F},
	{"Hiragana", 0x3040, 0x309F}, {"Katakana", 0x30A0, 0x30FF},
	{"CJK Unified Ideographs", 0x4E00, 0x9FFF}, {"Hangul Syllables", 0xAC00, 0xD7AF},
	{"Private Use Area", 0xE000, 0xF8FF}, {"Alphabetic Presentation Forms", 0xFB00, 0xFB4F},
	{"Arabic Presentation Forms-A", 0xFB50, 0xFDFF}, {"Halfwidth and Fullwidth Forms", 0xFF00, 0xFFEF},
	{"Specials", 0xFFF0, 0xFFFF}, {"Miscellaneous Symbols and Pictographs", 0x1F300, 0x1F5FF},
	{"Emoticons", 0x1F600, 0x1F64F},
}

// fntCmap returns a bitset (index cp>>6, bit cp&63) of the Unicode code
// points the font maps to a real glyph, from its format 4 and format 12
// Unicode subtables.
func fntCmap(t []byte) []uint64 {
	bits := make([]uint64, 0x110000/64)
	set := func(cp int) {
		if cp >= 0 && cp < 0x110000 {
			bits[cp>>6] |= 1 << (cp & 63)
		}
	}
	if len(t) < 4 {
		return bits
	}
	n := int(binary.BigEndian.Uint16(t[2:]))
	for i := 0; i < n; i++ {
		r := 4 + 8*i
		if r+8 > len(t) {
			break
		}
		plat := binary.BigEndian.Uint16(t[r:])
		enc := binary.BigEndian.Uint16(t[r+2:])
		off := int(binary.BigEndian.Uint32(t[r+4:]))
		if !(plat == 0 || (plat == 3 && (enc == 1 || enc == 10))) || off+4 > len(t) {
			continue
		}
		s := t[off:]
		switch binary.BigEndian.Uint16(s) {
		case 4:
			if len(s) < 16 {
				continue
			}
			segX2 := int(binary.BigEndian.Uint16(s[6:]))
			segs := segX2 / 2
			endO, startO, deltaO, rangeO := 14, 16+segX2, 16+2*segX2, 16+3*segX2
			if rangeO+segX2 > len(s) {
				continue
			}
			for k := 0; k < segs; k++ {
				end := int(binary.BigEndian.Uint16(s[endO+2*k:]))
				start := int(binary.BigEndian.Uint16(s[startO+2*k:]))
				delta := int(binary.BigEndian.Uint16(s[deltaO+2*k:]))
				ro := int(binary.BigEndian.Uint16(s[rangeO+2*k:]))
				for c := start; c <= end && c < 0xFFFF; c++ {
					gid := 0
					if ro == 0 {
						gid = (c + delta) & 0xFFFF
					} else {
						a := rangeO + 2*k + ro + 2*(c-start)
						if a+2 > len(s) {
							continue
						}
						if v := int(binary.BigEndian.Uint16(s[a:])); v != 0 {
							gid = (v + delta) & 0xFFFF
						}
					}
					if gid != 0 {
						set(c)
					}
				}
			}
		case 12:
			if len(s) < 16 {
				continue
			}
			ng := int(binary.BigEndian.Uint32(s[12:]))
			for k := 0; k < ng && 16+12*k+12 <= len(s); k++ {
				g := s[16+12*k:]
				start := int(binary.BigEndian.Uint32(g))
				end := int(binary.BigEndian.Uint32(g[4:]))
				gid0 := int(binary.BigEndian.Uint32(g[8:]))
				for c := start; c <= end && c < 0x110000; c++ {
					if gid0+(c-start) != 0 {
						set(c)
					}
				}
			}
		}
	}
	return bits
}
