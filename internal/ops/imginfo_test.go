package ops

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
)

// ---- helpers ---------------------------------------------------------------

func infRun(t *testing.T, handler string, out string, args Args, inputs ...string) error {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok {
		t.Fatalf("no handler %s", handler)
	}
	names := make([]string, len(inputs))
	for i, in := range inputs {
		names[i] = filepath.Base(in)
	}
	return h.Run(context.Background(), Prepare(Job{Inputs: inputs, Names: names, Output: out, Args: args}))
}

func infMustRun(t *testing.T, handler, out string, args Args, inputs ...string) {
	t.Helper()
	if err := infRun(t, handler, out, args, inputs...); err != nil {
		t.Fatalf("%s: %v", handler, err)
	}
}

func infReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad json: %v\n%s", err, b)
	}
	return m
}

func infWritePNG(t *testing.T, path string, m image.Image) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func infGradientJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func infSegment(marker byte, payload []byte) []byte {
	out := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(out[2:], uint16(len(payload)+2))
	return append(out, payload...)
}

// infInsertJPEG puts segments right after SOI.
func infInsertJPEG(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

// infSOS returns the offset of the start-of-scan marker.
func infSOS(t *testing.T, d []byte) int {
	t.Helper()
	for i := 2; i+4 <= len(d); {
		if d[i] != 0xFF {
			t.Fatalf("bad marker at %d", i)
		}
		if d[i+1] == 0xDA {
			return i
		}
		i += 2 + int(binary.BigEndian.Uint16(d[i+2:]))
	}
	t.Fatal("no SOS")
	return 0
}

type infTE struct {
	tag, typ uint16
	cnt      uint32
	val      []byte // value bytes in file byte order
}

func infAscii(tag uint16, s string) infTE {
	return infTE{tag, 2, uint32(len(s) + 1), append([]byte(s), 0)}
}

func infShort(bo binary.ByteOrder, tag uint16, v uint16) infTE {
	b := make([]byte, 2)
	bo.PutUint16(b, v)
	return infTE{tag, 3, 1, b}
}

func infLong(bo binary.ByteOrder, tag uint16, v uint32) infTE {
	b := make([]byte, 4)
	bo.PutUint32(b, v)
	return infTE{tag, 4, 1, b}
}

func infRat(bo binary.ByteOrder, tag uint16, pairs ...[2]uint32) infTE {
	b := make([]byte, 8*len(pairs))
	for i, p := range pairs {
		bo.PutUint32(b[8*i:], p[0])
		bo.PutUint32(b[8*i+4:], p[1])
	}
	return infTE{tag, 5, uint32(len(pairs)), b}
}

func infIFDSize(es []infTE) int {
	n := 2 + 12*len(es) + 4
	for _, e := range es {
		if len(e.val) > 4 {
			n += (len(e.val) + 1) &^ 1
		}
	}
	return n
}

func infLayout(bo binary.ByteOrder, start int, es []infTE) []byte {
	out := make([]byte, 2+12*len(es)+4)
	bo.PutUint16(out, uint16(len(es)))
	var extra []byte
	dataAt := start + len(out)
	for i, e := range es {
		p := out[2+12*i:]
		bo.PutUint16(p, e.tag)
		bo.PutUint16(p[2:], e.typ)
		bo.PutUint32(p[4:], e.cnt)
		if len(e.val) <= 4 {
			copy(p[8:12], e.val)
		} else {
			bo.PutUint32(p[8:], uint32(dataAt+len(extra)))
			extra = append(extra, e.val...)
			if len(extra)%2 == 1 {
				extra = append(extra, 0)
			}
		}
	}
	return append(out, extra...)
}

// infExifBlob builds a TIFF block: IFD0, Exif IFD and GPS IFD.
func infExifBlob(order string, ifd0, exif, gps []infTE) []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	if order == "MM" {
		bo = binary.BigEndian
	}
	ifd0 = append(append([]infTE{}, ifd0...), infLong(bo, 0x8769, 0), infLong(bo, 0x8825, 0))
	off0 := 8
	off1 := off0 + infIFDSize(ifd0)
	off2 := off1 + infIFDSize(exif)
	bo.PutUint32(ifd0[len(ifd0)-2].val, uint32(off1))
	bo.PutUint32(ifd0[len(ifd0)-1].val, uint32(off2))
	hdr := []byte(order + "\x00\x00\x00\x00\x00\x00")
	bo.PutUint16(hdr[2:], 42)
	bo.PutUint32(hdr[4:], 8)
	out := append(hdr, infLayout(bo, off0, ifd0)...)
	out = append(out, infLayout(bo, off1, exif)...)
	return append(out, infLayout(bo, off2, gps)...)
}

func infAPP1(tiff []byte) []byte { return infSegment(0xE1, append([]byte("Exif\x00\x00"), tiff...)) }

func infTestExif(order string, orientation uint16) []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	if order == "MM" {
		bo = binary.BigEndian
	}
	ifd0 := []infTE{
		infAscii(0x010F, "TestMake"),
		infAscii(0x0110, "TestModel"),
		infShort(bo, 0x0112, orientation),
	}
	exif := []infTE{
		infRat(bo, 0x829A, [2]uint32{1, 250}),
		infRat(bo, 0x829D, [2]uint32{28, 10}),
		infShort(bo, 0x8827, 400),
		infAscii(0x9003, "2021:05:03 12:34:56"),
		infRat(bo, 0x920A, [2]uint32{35, 1}),
		infAscii(0xA434, "TestLens 35mm"),
	}
	gps := []infTE{
		infAscii(1, "N"),
		infRat(bo, 2, [2]uint32{40, 1}, [2]uint32{30, 1}, [2]uint32{0, 1}),
		infAscii(3, "W"),
		infRat(bo, 4, [2]uint32{74, 1}, [2]uint32{15, 1}, [2]uint32{36, 1}),
	}
	return infExifBlob(order, ifd0, exif, gps)
}

// ---- strip -----------------------------------------------------------------

func TestStripJPEG(t *testing.T) {
	dir := t.TempDir()
	base := infGradientJPEG(t, 24, 16)
	icc := append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 40)...)
	withMeta := infInsertJPEG(base,
		infSegment(0xE2, icc),
		infAPP1(infTestExif("II", 1)),
		infSegment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>XMPSECRET</x:xmpmeta>")),
		infSegment(0xED, []byte("Photoshop 3.0\x008BIMIPTCSECRET")),
		infSegment(0xFE, []byte("hello comment")),
		infSegment(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01")),
	)
	if !bytes.Contains(withMeta, []byte("TestMake")) {
		t.Fatal("fixture lacks exif")
	}
	in := filepath.Join(dir, "in.jpg")
	os.WriteFile(in, withMeta, 0o644)
	out := filepath.Join(dir, "out.jpg")
	infMustRun(t, "info.strip", out, nil, in)
	got, _ := os.ReadFile(out)
	for _, s := range []string{"Exif", "TestMake", "XMPSECRET", "xmpmeta", "Photoshop", "IPTCSECRET", "hello comment"} {
		if bytes.Contains(got, []byte(s)) {
			t.Errorf("output still contains %q", s)
		}
	}
	if !bytes.Contains(got, icc) {
		t.Error("ICC profile segment was lost")
	}
	if !bytes.Contains(got, []byte("Adobe\x00\x64")) {
		t.Error("Adobe segment was lost")
	}
	if !bytes.Equal(got[infSOS(t, got):], withMeta[infSOS(t, withMeta):]) {
		t.Error("scan data changed")
	}
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Errorf("output does not decode: %v", err)
	}
	if len(got) >= len(withMeta) {
		t.Error("output is not smaller")
	}
}

func TestStripJPEGOrientationReencodes(t *testing.T) {
	dir := t.TempDir()
	src := infInsertJPEG(infGradientJPEG(t, 16, 8), infAPP1(infTestExif("MM", 6)))
	in := filepath.Join(dir, "in.jpg")
	os.WriteFile(in, src, 0o644)
	out := filepath.Join(dir, "out.jpg")
	infMustRun(t, "info.strip", out, nil, in)
	got, _ := os.ReadFile(out)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 8 || cfg.Height != 16 {
		t.Errorf("expected upright 8x16 pixels, got %dx%d", cfg.Width, cfg.Height)
	}
	if bytes.Contains(got, []byte("Exif")) || JPEGOrientation(out) != 0 {
		t.Error("orientation/EXIF survived")
	}
}

func infChunk(typ string, data []byte) []byte {
	out := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	copy(out[4:], typ)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[4:]))
}

func infPNGChunks(d []byte) map[string][]byte {
	out := map[string][]byte{}
	for i := 8; i+12 <= len(d); {
		n := int(binary.BigEndian.Uint32(d[i:]))
		out[string(d[i+4:i+8])] = d[i+8 : i+8+n]
		i += 12 + n
	}
	return out
}

func TestStripPNG(t *testing.T) {
	dir := t.TempDir()
	m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range m.Pix {
		m.Pix[i] = uint8(i * 7)
	}
	var buf bytes.Buffer
	png.Encode(&buf, m)
	orig := buf.Bytes()
	ihdrEnd := 8 + 12 + 13
	var injected []byte
	injected = append(injected, orig[:ihdrEnd]...)
	injected = append(injected, infChunk("tEXt", []byte("Comment\x00PNGSECRET"))...)
	injected = append(injected, infChunk("eXIf", infTestExif("II", 1))...)
	injected = append(injected, infChunk("tIME", []byte{7, 0xE5, 1, 2, 3, 4, 5})...)
	injected = append(injected, infChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00XMPSECRET"))...)
	injected = append(injected, infChunk("zTXt", []byte("k\x00\x00\x78\x9c"))...)
	injected = append(injected, orig[ihdrEnd:]...)
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, injected, 0o644)
	out := filepath.Join(dir, "out.png")
	infMustRun(t, "info.strip", out, nil, in)
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, orig) {
		t.Errorf("stripped PNG differs from the metadata-free original (%d vs %d bytes)", len(got), len(orig))
	}
	for _, s := range []string{"PNGSECRET", "TestMake", "XMPSECRET"} {
		if bytes.Contains(got, []byte(s)) {
			t.Errorf("still contains %q", s)
		}
	}
	if _, err := png.Decode(bytes.NewReader(got)); err != nil {
		t.Error(err)
	}
}

func TestStripWebP(t *testing.T) {
	dir := t.TempDir()
	le := func(n int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(n)) }
	chunk := func(typ string, data []byte) []byte {
		c := append([]byte(typ), le(len(data))...)
		c = append(c, data...)
		if len(data)%2 == 1 {
			c = append(c, 0)
		}
		return c
	}
	vp8x := func(flags byte) []byte {
		return chunk("VP8X", []byte{flags, 0, 0, 0, 15, 0, 0, 7, 0, 0})
	}
	body := func(flags byte, withMeta bool) []byte {
		b := []byte("WEBP")
		b = append(b, vp8x(flags)...)
		b = append(b, chunk("ICCP", bytes.Repeat([]byte{3}, 10))...)
		b = append(b, chunk("VP8L", []byte("fakebitstream!"))...) // odd length: padded
		if withMeta {
			b = append(b, chunk("EXIF", append([]byte("Exif\x00\x00"), []byte("WEBPEXIFSECRET")...))...)
			b = append(b, chunk("XMP ", []byte("WEBPXMPSECRET"))...)
		}
		return b
	}
	file := func(b []byte) []byte { return append(append([]byte("RIFF"), le(len(b))...), b...) }
	in := filepath.Join(dir, "in.webp")
	os.WriteFile(in, file(body(0x20|0x08|0x04, true)), 0o644)
	out := filepath.Join(dir, "out.webp")
	infMustRun(t, "info.strip", out, nil, in)
	got, _ := os.ReadFile(out)
	want := file(body(0x20, false))
	if !bytes.Equal(got, want) {
		t.Errorf("webp mismatch\n got %x\nwant %x", got, want)
	}
}

func TestStripBMPReencodes(t *testing.T) {
	dir := t.TempDir()
	m := image.NewRGBA(image.Rect(0, 0, 6, 4))
	m.Set(2, 1, color.RGBA{200, 10, 30, 255})
	var buf bytes.Buffer
	bmp.Encode(&buf, m)
	in := filepath.Join(dir, "in.bmp")
	os.WriteFile(in, buf.Bytes(), 0o644)
	out := filepath.Join(dir, "out.bmp")
	infMustRun(t, "info.strip", out, nil, in)
	f, _ := os.Open(out)
	defer f.Close()
	dec, err := bmp.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := dec.At(2, 1).RGBA()
	if r>>8 != 200 || g>>8 != 10 || b>>8 != 30 || dec.Bounds().Dx() != 6 {
		t.Errorf("pixel/size changed: %v %v", dec.Bounds(), dec.At(2, 1))
	}
}

// ---- metadata ----------------------------------------------------------------

func TestShowMetadata(t *testing.T) {
	for _, order := range []string{"II", "MM"} {
		t.Run(order, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "photo.jpg")
			os.WriteFile(in, infInsertJPEG(infGradientJPEG(t, 16, 8), infAPP1(infTestExif(order, 6))), 0o644)
			out := filepath.Join(dir, "meta.json")
			infMustRun(t, "info.metadata", out, nil, in)
			m := infReadJSON(t, out)
			checks := map[string]any{
				"file": "photo.jpg", "format": "JPEG", "width": 8.0, "height": 16.0, "bitDepth": 8.0, "hasAlpha": false,
				"colorProfile": "sRGB (assumed)", "cameraMake": "TestMake", "cameraModel": "TestModel",
				"lens": "TestLens 35mm", "dateTaken": "2021-05-03 12:34:56", "exposureTime": "1/250 s",
				"fNumber": 2.8, "iso": 400.0, "focalLengthMm": 35.0, "orientation": 6.0,
			}
			for k, want := range checks {
				if m[k] != want {
					t.Errorf("%s = %v, want %v", k, m[k], want)
				}
			}
			if lat, _ := m["gpsLatitude"].(float64); math.Abs(lat-40.5) > 1e-6 {
				t.Errorf("latitude %v", m["gpsLatitude"])
			}
			if lon, _ := m["gpsLongitude"].(float64); math.Abs(lon-(-(74 + 15.0/60 + 36.0/3600))) > 1e-6 {
				t.Errorf("longitude %v", m["gpsLongitude"])
			}
		})
	}
}

func TestShowMetadataPlainPNG(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "a.png")
	m := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	m.SetNRGBA(0, 0, color.NRGBA{1, 2, 3, 100})
	infWritePNG(t, in, m)
	out := filepath.Join(dir, "a.json")
	infMustRun(t, "info.metadata", out, nil, in)
	j := infReadJSON(t, out)
	if j["format"] != "PNG" || j["hasAlpha"] != true || j["width"] != 3.0 {
		t.Errorf("unexpected %v", j)
	}
	if _, ok := j["cameraMake"]; ok {
		t.Error("camera info invented")
	}
}

// ---- palette -----------------------------------------------------------------

func infThreeColors(t *testing.T, path string) {
	t.Helper()
	// 40x10: 20 columns red, 12 green, 8 blue-ish => 50% / 30% / 20%
	m := image.NewNRGBA(image.Rect(0, 0, 40, 10))
	cols := []color.NRGBA{{200, 30, 40, 255}, {20, 180, 60, 255}, {30, 60, 220, 255}}
	for y := 0; y < 10; y++ {
		for x := 0; x < 40; x++ {
			c := cols[2]
			if x < 20 {
				c = cols[0]
			} else if x < 32 {
				c = cols[1]
			}
			m.SetNRGBA(x, y, c)
		}
	}
	infWritePNG(t, path, m)
}

func TestExtractPalette(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "three.png")
	infThreeColors(t, in)
	out := filepath.Join(dir, "p.json")
	infMustRun(t, "info.palette", out, nil, in)
	var pal []infColor
	b, _ := os.ReadFile(out)
	if err := json.Unmarshal(b, &pal); err != nil {
		t.Fatal(err)
	}
	if len(pal) != 3 {
		t.Fatalf("want 3 colours, got %d: %s", len(pal), b)
	}
	want := []struct {
		rgb   [3]int
		share float64
	}{{[3]int{200, 30, 40}, 0.5}, {[3]int{20, 180, 60}, 0.3}, {[3]int{30, 60, 220}, 0.2}}
	for i, w := range want {
		for c := 0; c < 3; c++ {
			if d := pal[i].RGB[c] - w.rgb[c]; d < -1 || d > 1 {
				t.Errorf("colour %d = %v, want %v", i, pal[i].RGB, w.rgb)
			}
		}
		if math.Abs(pal[i].Share-w.share) > 1e-9 {
			t.Errorf("share %d = %v, want %v", i, pal[i].Share, w.share)
		}
		if pal[i].Hex != "#"+infHex(pal[i].RGB) {
			t.Errorf("hex %q does not match rgb %v", pal[i].Hex, pal[i].RGB)
		}
	}
	// A bad count is rejected.
	if err := infRun(t, "info.palette", filepath.Join(dir, "x.json"), Args{"colors": 40}, in); err == nil {
		t.Error("colors=40 accepted")
	}
}

func infHex(rgb [3]int) string {
	const d = "0123456789abcdef"
	s := make([]byte, 0, 6)
	for _, v := range rgb {
		s = append(s, d[v>>4], d[v&15])
	}
	return string(s)
}

func TestPaletteSwatches(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "three.png")
	infThreeColors(t, in)
	out := filepath.Join(dir, "s.png")
	infMustRun(t, "info.paletteimage", out, nil, in)
	f, _ := os.Open(out)
	defer f.Close()
	dec, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := dec.Bounds(); b.Dx() != 360 || b.Dy() != 120 {
		t.Fatalf("strip is %v, want 360x120", b)
	}
	want := [][3]uint32{{200, 30, 40}, {20, 180, 60}, {30, 60, 220}}
	for i, w := range want {
		r, g, b, _ := dec.At(i*120+60, 60).RGBA()
		for c, got := range []uint32{r >> 8, g >> 8, b >> 8} {
			if d := int(got) - int(w[c]); d < -1 || d > 1 {
				t.Errorf("swatch %d channel %d = %d, want %d", i, c, got, w[c])
			}
		}
	}
}

// ---- sRGB --------------------------------------------------------------------

// infP3Profile builds a minimal matrix/linear-TRC Display P3 profile
// (D50-adapted colorants).
func infP3Profile() []byte {
	cols := [3][3]float64{{0.5151, 0.2920, 0.1571}, {0.2412, 0.6922, 0.0666}, {-0.0011, 0.0419, 0.7841}}
	type tag struct {
		sig  string
		data []byte
	}
	xyz := func(v [3]float64) []byte {
		b := make([]byte, 20)
		copy(b, "XYZ ")
		for i, x := range v {
			binary.BigEndian.PutUint32(b[8+4*i:], uint32(int32(math.Round(x*65536))))
		}
		return b
	}
	curv := make([]byte, 12) // zero entries: identity (linear) curve
	copy(curv, "curv")
	var tags []tag
	for c, sig := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		tags = append(tags, tag{sig, xyz([3]float64{cols[0][c], cols[1][c], cols[2][c]})})
	}
	for _, sig := range []string{"rTRC", "gTRC", "bTRC"} {
		tags = append(tags, tag{sig, curv})
	}
	desc := append([]byte("desc\x00\x00\x00\x00\x00\x00\x00\x0aTest P3\x00"), 0, 0)
	tags = append(tags, tag{"desc", desc})
	b := make([]byte, 132+12*len(tags))
	copy(b[16:], "RGB ")
	copy(b[36:], "acsp")
	binary.BigEndian.PutUint32(b[128:], uint32(len(tags)))
	for i, tg := range tags {
		e := b[132+12*i:]
		copy(e, tg.sig)
		binary.BigEndian.PutUint32(e[4:], uint32(len(b)))
		binary.BigEndian.PutUint32(e[8:], uint32(len(tg.data)))
		b = append(b, tg.data...)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
	}
	binary.BigEndian.PutUint32(b[0:], uint32(len(b)))
	return b
}

func TestConvertToSRGB(t *testing.T) {
	dir := t.TempDir()
	m := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	m.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	m.SetNRGBA(1, 0, color.NRGBA{128, 128, 128, 255})
	m.SetNRGBA(2, 0, color.NRGBA{255, 255, 255, 255})
	var buf bytes.Buffer
	png.Encode(&buf, m)
	orig := buf.Bytes()
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(infP3Profile())
	zw.Close()
	ihdrEnd := 8 + 12 + 13
	file := append([]byte{}, orig[:ihdrEnd]...)
	file = append(file, infChunk("iCCP", append([]byte("P3\x00\x00"), z.Bytes()...))...)
	file = append(file, orig[ihdrEnd:]...)
	in := filepath.Join(dir, "p3.png")
	os.WriteFile(in, file, 0o644)

	src, err := DecodeWorking(in)
	if err != nil {
		t.Fatal(err)
	}
	out := apply(t, "info.srgb", src, nil)
	if out == src {
		t.Error("handler returned its input; it must return a copy")
	}
	// P3 red is outside sRGB: clips to pure red.
	if p := px(out, 0, 0); !closePx(p, [4]float64{1, 0, 0, 1}, 0.01) {
		t.Errorf("P3 red -> %v, want sRGB red", p)
	}
	// Linear-TRC profile: mid grey (encoded 0.5) is treated as linear 0.5, and
	// must stay neutral.
	g := px(out, 1, 0)
	if math.Abs(g[0]-g[1]) > 0.005 || math.Abs(g[1]-g[2]) > 0.005 {
		t.Errorf("grey became coloured: %v", g)
	}
	if w := px(out, 2, 0); !closePx(w, [4]float64{1, 1, 1, 1}, 0.01) {
		t.Errorf("white -> %v", w)
	}
}

// ---- count colours -------------------------------------------------------------

func TestCountColors(t *testing.T) {
	dir := t.TempDir()
	m := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	fill := func(x0, x1 int, c color.NRGBA) {
		for y := 0; y < 10; y++ {
			for x := x0; x < x1; x++ {
				m.SetNRGBA(x, y, c)
			}
		}
	}
	fill(0, 5, color.NRGBA{255, 0, 0, 255})     // 50
	fill(5, 8, color.NRGBA{0, 255, 0, 255})     // 30
	fill(8, 10, color.NRGBA{0, 0, 255, 255})    // 20
	m.SetNRGBA(9, 9, color.NRGBA{9, 9, 9, 255}) // -1 blue, 1 grey
	in := filepath.Join(dir, "c.png")
	infWritePNG(t, in, m)
	out := filepath.Join(dir, "c.json")
	infMustRun(t, "info.countcolors", out, nil, in)
	j := infReadJSON(t, out)
	if j["uniqueColors"] != 4.0 || j["isGreyscale"] != false || j["usesTransparency"] != false {
		t.Errorf("unexpected %v", j)
	}
	top := j["mostFrequentColors"].([]any)
	first := top[0].(map[string]any)
	if first["hex"] != "#ff0000" || first["count"] != 50.0 {
		t.Errorf("top colour %v", first)
	}
	if len(top) != 4 || top[3].(map[string]any)["count"] != 1.0 {
		t.Errorf("top list %v", top)
	}

	// Greyscale with transparency.
	g := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := 0; x < 4; x++ {
		g.SetNRGBA(x, 0, color.NRGBA{uint8(x * 40), uint8(x * 40), uint8(x * 40), 255})
	}
	g.SetNRGBA(3, 0, color.NRGBA{120, 120, 120, 128})
	in2 := filepath.Join(dir, "g.png")
	infWritePNG(t, in2, g)
	out2 := filepath.Join(dir, "g.json")
	infMustRun(t, "info.countcolors", out2, nil, in2)
	j = infReadJSON(t, out2)
	if j["uniqueColors"] != 4.0 || j["isGreyscale"] != true || j["usesTransparency"] != true {
		t.Errorf("unexpected %v", j)
	}
}

// ---- compare -------------------------------------------------------------------

func infFlat(t *testing.T, path string, w, h int, c color.NRGBA, tweak func(*image.NRGBA)) {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
	if tweak != nil {
		tweak(m)
	}
	infWritePNG(t, path, m)
}

func TestCompareSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	infFlat(t, a, 8, 6, color.NRGBA{1, 2, 3, 255}, nil)
	infFlat(t, b, 6, 4, color.NRGBA{1, 2, 3, 255}, nil)
	for _, h := range []string{"info.compare", "info.comparestats"} {
		err := infRun(t, h, filepath.Join(dir, h+".out"), nil, a, b)
		want := "Compare needs two images of the same size (8x6 vs 6x4)"
		if err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", h, err, want)
		}
	}
	if err := infRun(t, "info.compare", filepath.Join(dir, "x.png"), nil, a); err == nil {
		t.Error("one image accepted")
	}
}

func TestCompareImages(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")
	infFlat(t, a, 10, 10, color.NRGBA{100, 100, 100, 255}, nil)
	infFlat(t, b, 10, 10, color.NRGBA{100, 100, 100, 255}, func(m *image.NRGBA) {
		m.SetNRGBA(4, 5, color.NRGBA{255, 100, 100, 255})
	})
	out := filepath.Join(dir, "cmp.png")
	infMustRun(t, "info.compare", out, nil, a, b)
	f, _ := os.Open(out)
	defer f.Close()
	dec, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r, g, bl, _ := dec.At(1, 1).RGBA()
	// Unchanged pixel: neutral grey at 30% of the (encoded) luminance of 100/255.
	wantGrey := 100.0 * 0.3
	for _, v := range []uint32{r >> 8, g >> 8, bl >> 8} {
		if math.Abs(float64(v)-wantGrey) > 1.5 {
			t.Errorf("unchanged pixel channel %d, want about %.1f", v, wantGrey)
		}
	}
	r, g, bl, _ = dec.At(4, 5).RGBA()
	if r>>8 < 200 || g>>8 > 30 || bl>>8 > 30 {
		t.Errorf("changed pixel not red: %d %d %d", r>>8, g>>8, bl>>8)
	}
}

func TestImageDifference(t *testing.T) {
	dir := t.TempDir()
	base := color.NRGBA{100, 100, 100, 255}
	a := filepath.Join(dir, "a.png")
	infFlat(t, a, 10, 10, base, nil)

	run := func(name string, tweak func(*image.NRGBA)) map[string]any {
		b := filepath.Join(dir, name+".png")
		infFlat(t, b, 10, 10, base, tweak)
		out := filepath.Join(dir, name+".json")
		infMustRun(t, "info.comparestats", out, nil, a, b)
		return infReadJSON(t, out)
	}

	same := run("same", nil)
	if same["psnrDb"] != "inf" || same["ssim"] != 1.0 || same["meanAbsoluteError"] != 0.0 || same["maxDifference"] != 0.0 || same["percentPixelsDifferent"] != 0.0 {
		t.Errorf("identical images: %v", same)
	}

	one := run("one", func(m *image.NRGBA) { m.SetNRGBA(3, 3, color.NRGBA{130, 100, 100, 255}) })
	// 30 off on one of 300 channel samples; 1 of 100 pixels differs.
	if v := one["meanAbsoluteError"].(float64); math.Abs(v-0.1) > 1e-9 {
		t.Errorf("MAE %v, want 0.1", v)
	}
	if one["maxDifference"] != 30.0 {
		t.Errorf("max %v", one["maxDifference"])
	}
	if v := one["percentPixelsDifferent"].(float64); math.Abs(v-1) > 1e-9 {
		t.Errorf("percent %v, want 1", v)
	}
	wantPSNR := 10 * math.Log10(255*255/(900.0/300))
	if v := one["psnrDb"].(float64); math.Abs(v-wantPSNR) > 1e-3 {
		t.Errorf("PSNR %v, want %v", v, wantPSNR)
	}
	if v := one["ssim"].(float64); v >= 1 || v < 0.9 {
		t.Errorf("SSIM %v should be just under 1", v)
	}

	off := run("off", func(m *image.NRGBA) {
		for i := range m.Pix {
			if i%4 != 3 {
				m.Pix[i] += 10
			}
		}
	})
	if v := off["psnrDb"].(float64); math.Abs(v-20*math.Log10(255.0/10)) > 1e-3 {
		t.Errorf("PSNR %v, want %v", v, 20*math.Log10(255.0/10))
	}
	if v := off["meanAbsoluteError"].(float64); math.Abs(v-10) > 1e-9 {
		t.Errorf("MAE %v, want 10", v)
	}
	if off["percentPixelsDifferent"] != 100.0 {
		t.Errorf("percent %v", off["percentPixelsDifferent"])
	}
	// Flat images: SSIM reduces to the luminance term (2*100*110+C1)/(100^2+110^2+C1) with C1 = 6.5025.
	lx, ly := 100.0, 110.0
	wantSSIM := (2*lx*ly + 6.5025) / (lx*lx + ly*ly + 6.5025) // contrast term is exactly 1 when variances are 0
	if v := off["ssim"].(float64); math.Abs(v-wantSSIM) > 1e-5 {
		t.Errorf("SSIM %v, want %v", v, wantSSIM)
	}
}
