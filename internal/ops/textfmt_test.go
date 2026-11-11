package ops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func txtTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func txtRun(t *testing.T, handler, name string, content []byte, outName string, args Args) []byte {
	t.Helper()
	out := mustRun(t, handler, []string{txtTempFile(t, name, content)}, outName, args)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTextUTF16LEWithBOMToUTF8(t *testing.T) {
	in := []byte{0xFF, 0xFE}
	for _, r := range "héllo ✓ 𝄞\r\n" {
		if r > 0xFFFF {
			// surrogate pair for U+1D11E
			in = append(in, 0x34, 0xD8, 0x1E, 0xDD)
			continue
		}
		in = append(in, byte(r), byte(r>>8))
	}
	got := txtRun(t, "text.utf8", "in.txt", in, "out.txt", nil)
	if want := "héllo ✓ 𝄞\r\n"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTextUTF16BEWithBOMAndNoBOM(t *testing.T) {
	be := []byte{0xFE, 0xFF, 0x00, 'h', 0x00, 'i', 0x00, '\n'}
	if got := txtRun(t, "text.utf8", "be.txt", be, "o.txt", nil); string(got) != "hi\n" {
		t.Fatalf("BE: %q", got)
	}
	noBOM := []byte{'h', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0}
	if got := txtRun(t, "text.utf8", "le.txt", noBOM, "o.txt", nil); string(got) != "hello" {
		t.Fatalf("BOM-less LE: %q", got)
	}
}

func TestTextWindows1252ToUTF8(t *testing.T) {
	got := txtRun(t, "text.utf8", "in.txt", []byte("caf\xe9 \x93quote\x94 \x80 \x96"), "o.txt", nil)
	if want := "café “quote” € –"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTextValidUTF8KeptAndBOMDropped(t *testing.T) {
	got := txtRun(t, "text.utf8", "in.txt", []byte("\xef\xbb\xbfcafé ✓"), "o.txt", nil)
	if string(got) != "café ✓" {
		t.Fatalf("got %q", got)
	}
	// a file that is already clean passes through byte for byte
	clean := []byte("日本語\nplain\n")
	if got := txtRun(t, "text.utf8", "c.txt", clean, "o.txt", nil); !bytes.Equal(got, clean) {
		t.Fatalf("got %q", got)
	}
}

func TestTextLineEndings(t *testing.T) {
	in := []byte("a\r\nb\rc\nd\r\n\r\ne")
	lf := txtRun(t, "text.lineEndings", "in.txt", in, "o.txt", Args{"ending": "lf"})
	if string(lf) != "a\nb\nc\nd\n\ne" {
		t.Fatalf("lf: %q", lf)
	}
	crlf := txtRun(t, "text.lineEndings", "in.txt", in, "o.txt", Args{"ending": "crlf"})
	if string(crlf) != "a\r\nb\r\nc\r\nd\r\n\r\ne" {
		t.Fatalf("crlf: %q", crlf)
	}
	// converting twice changes nothing
	if again := txtRun(t, "text.lineEndings", "x.txt", crlf, "o.txt", Args{"ending": "crlf"}); !bytes.Equal(again, crlf) {
		t.Fatalf("not idempotent: %q", again)
	}
	// UTF-8 content and a BOM are left alone
	bom := []byte("\xef\xbb\xbfé\r\n")
	if got := txtRun(t, "text.lineEndings", "b.txt", bom, "o.txt", Args{"ending": "lf"}); string(got) != "\xef\xbb\xbfé\n" {
		t.Fatalf("bom: %q", got)
	}
	if _, err := run(t, "text.lineEndings", []string{txtTempFile(t, "u.txt", []byte{0xFF, 0xFE, 'a', 0})}, "o.txt", Args{"ending": "lf"}); err == nil {
		t.Fatal("expected error for UTF-16")
	}
}

func TestTextRemoveBOM(t *testing.T) {
	if got := txtRun(t, "text.removeBOM", "in.txt", []byte("\xef\xbb\xbfhello"), "o.txt", nil); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	if got := txtRun(t, "text.removeBOM", "in.txt", []byte("no bom"), "o.txt", nil); string(got) != "no bom" {
		t.Fatalf("got %q", got)
	}
}

func TestTextChecksumKnownVectors(t *testing.T) {
	abc := []byte("abc")
	vectors := map[string]string{
		"":       "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", // default is sha256
		"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"md5":    "900150983cd24fb0d6963f7d28e17f72",
		"sha1":   "a9993e364706816aba3e25717850c26c9cd0d89d",
		"sha512": "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f",
	}
	for alg, hexSum := range vectors {
		args := Args{}
		if alg != "" {
			args["algorithm"] = alg
		}
		got := txtRun(t, "text.checksum", "abc.bin", abc, "abc.txt", args)
		if want := hexSum + "  abc.bin\n"; string(got) != want {
			t.Errorf("%q: got %q want %q", alg, got, want)
		}
	}
	// and computed with the standard library for a larger input
	big := bytes.Repeat([]byte("asteria"), 300000)
	sum := sha256.Sum256(big)
	got := txtRun(t, "text.checksum", "big.dat", big, "big.txt", nil)
	if want := hex.EncodeToString(sum[:]) + "  big.dat\n"; string(got) != want {
		t.Fatalf("got %q", got)
	}
	if _, err := run(t, "text.checksum", []string{txtTempFile(t, "a", abc)}, "o.txt", Args{"algorithm": "crc32"}); err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

func TestTextBase64Known(t *testing.T) {
	if got := txtRun(t, "text.base64Encode", "m.bin", []byte("Man"), "o.txt", nil); string(got) != "TWFu\n" {
		t.Fatalf("got %q", got)
	}
	if got := txtRun(t, "text.base64Encode", "m.bin", []byte("Ma"), "o.txt", nil); string(got) != "TWE=\n" {
		t.Fatalf("got %q", got)
	}
	if got := txtRun(t, "text.base64Encode", "e.bin", nil, "o.txt", nil); len(got) != 0 {
		t.Fatalf("empty input gave %q", got)
	}
}

func TestTextBase64RoundTripAndLineWidth(t *testing.T) {
	data := make([]byte, 57*4096*2+123) // crosses the internal buffer boundary
	x := uint32(1)
	for i := range data {
		x = x*1664525 + 1013904223
		data[i] = byte(x >> 24)
	}
	enc := txtRun(t, "text.base64Encode", "blob.bin", data, "blob.txt", nil)
	lines := strings.Split(strings.TrimSuffix(string(enc), "\n"), "\n")
	for i, l := range lines {
		if len(l) > 76 || (i < len(lines)-1 && len(l) != 76) {
			t.Fatalf("line %d has %d columns", i, len(l))
		}
	}
	dec := txtRun(t, "text.base64Decode", "blob.txt", enc, "blob.bin", nil)
	if !bytes.Equal(dec, data) {
		t.Fatal("round trip changed the bytes")
	}
}

func TestTextBase64DecodeTolerance(t *testing.T) {
	want := "hello?>>world"
	cases := map[string]string{
		"std":        "aGVsbG8/Pj53b3JsZA==",
		"unpadded":   "aGVsbG8/Pj53b3JsZA",
		"urlsafe":    "aGVsbG8_Pj53b3JsZA",
		"wrapped":    "aGVsbG8/\r\nPj53b3Js\r\nZA==\r\n",
		"data uri":   "data:text/plain;base64,aGVsbG8/Pj53b3JsZA==",
		"leading ws": "  \n aGVsbG8/Pj53b3JsZA==  ",
	}
	for name, in := range cases {
		if got := txtRun(t, "text.base64Decode", "in.b64", []byte(in), "o.bin", nil); string(got) != want {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if _, err := run(t, "text.base64Decode", []string{txtTempFile(t, "bad.txt", []byte("this is not base64!"))}, "o.bin", nil); err == nil {
		t.Fatal("expected error for non-base64 text")
	}
}
