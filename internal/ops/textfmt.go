package ops

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// TextExts are the text-like extensions the text.* handlers accept.
var TextExts = []string{
	".txt", ".csv", ".tsv", ".srt", ".vtt", ".md", ".html", ".htm", ".xml", ".json",
	".yaml", ".yml", ".ini", ".log", ".ass", ".ssa", ".sbv", ".tex", ".rst", ".toml",
	".env", ".properties", ".sql", ".js", ".ts", ".css", ".go", ".py", ".java", ".c",
	".h", ".cpp", ".cs", ".rb", ".php", ".sh", ".bat", ".ps1",
}

func init() {
	register(Handler{Name: "text.utf8", Run: txtUTF8})
	register(Handler{Name: "text.lineEndings", Run: txtLineEndings})
	register(Handler{Name: "text.removeBOM", Run: txtRemoveBOM})
	register(Handler{Name: "text.checksum", Run: txtChecksum})
	register(Handler{Name: "text.base64Encode", Run: txtBase64Encode})
	register(Handler{Name: "text.base64Decode", Run: txtBase64Decode})
}

func txtRead(job Job) ([]byte, error) {
	info, err := os.Stat(job.Input())
	if err != nil {
		return nil, err
	}
	if info.Size() > maxDataBytes {
		return nil, Userf("%s is too large to convert in memory (limit 512 MB)", job.Name(0))
	}
	return os.ReadFile(job.Input())
}

// cp1252High maps bytes 0x80-0x9F of Windows-1252 to Unicode. The five
// undefined bytes (81, 8D, 8F, 90, 9D) map to the matching C1 control, as
// browsers do.
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

func txtCP1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/8)
	for _, c := range b {
		switch {
		case c >= 0x80 && c <= 0x9F:
			sb.WriteRune(cp1252High[c-0x80])
		default: // ASCII and 0xA0-0xFF (identical in ISO-8859-1)
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

func txtUTF16(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// txtGuessUTF16 recognises BOM-less UTF-16 of mostly-ASCII text: every other
// byte is zero.
func txtGuessUTF16(b []byte) binary.ByteOrder {
	if len(b) < 4 {
		return nil
	}
	n := min(len(b), 4096) &^ 1
	var even, odd int
	for i := 0; i < n; i += 2 {
		if b[i] == 0 {
			even++
		}
		if b[i+1] == 0 {
			odd++
		}
	}
	pairs := n / 2
	switch {
	case odd*3 > pairs && even*20 < odd:
		return binary.LittleEndian
	case even*3 > pairs && odd*20 < even:
		return binary.BigEndian
	}
	return nil
}

func txtIsUTF16(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.HasPrefix(b, []byte{0xFE, 0xFF}) || txtGuessUTF16(b) != nil
}

// txtDecode converts file bytes to a UTF-8 string without a BOM and names
// the encoding it detected. Detection order:
//  1. BOM: UTF-8, UTF-16 LE or UTF-16 BE.
//  2. BOM-less UTF-16 (mostly-ASCII text with a zero byte in every pair).
//  3. Valid UTF-8 (including plain ASCII) is kept as is.
//  4. Anything else is treated as Windows-1252. ISO-8859-1 is decoded the
//     same way: the two only differ in bytes 0x80-0x9F, which are control
//     characters in ISO-8859-1 and are essentially never intended there, but
//     are curly quotes, dashes, the euro sign and so on in Windows-1252.
func txtDecode(b []byte) (string, string) {
	var s, enc string
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		s, enc = string(b[3:]), "UTF-8"
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		s, enc = txtUTF16(b[2:], binary.LittleEndian), "UTF-16LE"
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		s, enc = txtUTF16(b[2:], binary.BigEndian), "UTF-16BE"
	default:
		if order := txtGuessUTF16(b); order != nil {
			s, enc = txtUTF16(b, order), "UTF-16"
		} else if utf8.Valid(b) {
			s, enc = string(b), "UTF-8"
		} else {
			s, enc = txtCP1252(b), "Windows-1252"
		}
	}
	return strings.TrimPrefix(s, "\ufeff"), enc
}

func txtDecodeString(b []byte) string {
	s, _ := txtDecode(b)
	return s
}

func txtUTF8(_ context.Context, job Job) error {
	b, err := txtRead(job)
	if err != nil {
		return err
	}
	s, _ := txtDecode(b)
	return os.WriteFile(job.Output, []byte(s), 0o644)
}

func txtLineEndings(_ context.Context, job Job) error {
	ending, err := job.Args.Choice("ending", "lf", []string{"lf", "crlf"}, map[string]string{"unix": "lf", "windows": "crlf", "cr lf": "crlf"})
	if err != nil {
		return err
	}
	b, err := txtRead(job)
	if err != nil {
		return err
	}
	if txtIsUTF16(b) {
		return Userf("%s is UTF-16 text; convert it to UTF-8 first", job.Name(0))
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	if ending == "crlf" {
		b = bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
	}
	return os.WriteFile(job.Output, b, 0o644)
}

func txtRemoveBOM(_ context.Context, job Job) error {
	b, err := txtRead(job)
	if err != nil {
		return err
	}
	if bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.HasPrefix(b, []byte{0xFE, 0xFF}) {
		return Userf("%s is UTF-16; use convert to UTF-8 to remove its BOM", job.Name(0))
	}
	return os.WriteFile(job.Output, bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}), 0o644)
}

// txtChecksum writes "<hex>  <filename>\n", the format sha256sum, md5sum,
// etc. print and verify with -c.
func txtChecksum(ctx context.Context, job Job) error {
	alg, err := job.Args.Choice("algorithm", "sha256", []string{"md5", "sha1", "sha256", "sha512"},
		map[string]string{"sha-1": "sha1", "sha-256": "sha256", "sha-512": "sha512", "sha2": "sha256", "sha": "sha1"})
	if err != nil {
		return err
	}
	var h hash.Hash
	switch alg {
	case "md5":
		h = md5.New()
	case "sha1":
		h = sha1.New()
	case "sha512":
		h = sha512.New()
	default:
		h = sha256.New()
	}
	f, err := os.Open(job.Input())
	if err != nil {
		return err
	}
	defer f.Close()
	var total int64
	if info, e := f.Stat(); e == nil {
		total = info.Size()
	}
	buf := make([]byte, 1<<20)
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := f.Read(buf)
		h.Write(buf[:n])
		done += int64(n)
		if total > 0 {
			job.Progress(float64(done) / float64(total))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	line := hex.EncodeToString(h.Sum(nil)) + "  " + job.Name(0) + "\n"
	return os.WriteFile(job.Output, []byte(line), 0o644)
}

// txtBase64Encode writes standard base64 wrapped at 76 columns.
func txtBase64Encode(ctx context.Context, job Job) (err error) {
	in, err := os.Open(job.Input())
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(job.Output)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	w := bufio.NewWriterSize(out, 1<<16)
	buf := make([]byte, 57*4096) // a multiple of 57 bytes = whole 76-column lines
	enc := make([]byte, base64.StdEncoding.EncodedLen(len(buf)))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			m := base64.StdEncoding.EncodedLen(n)
			base64.StdEncoding.Encode(enc[:m], buf[:n])
			for pos := 0; pos < m; pos += 76 {
				w.Write(enc[pos:min(pos+76, m)])
				w.WriteByte('\n')
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return w.Flush()
}

// txtBase64Decode accepts standard or URL-safe base64, with or without
// padding or line breaks, and an optional "data:...;base64," prefix. The
// output is always planned as .bin: the engine fixes the output extension
// before the handler runs, so the decoded type cannot be detected in time.
func txtBase64Decode(_ context.Context, job Job) error {
	raw, err := txtRead(job)
	if err != nil {
		return err
	}
	s := string(raw)
	if strings.HasPrefix(strings.TrimSpace(s), "data:") {
		if i := strings.Index(s, ";base64,"); i >= 0 {
			s = s[i+len(";base64,"):]
		}
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\ufeff' {
			return -1
		}
		return r
	}, s)
	trimmed := strings.TrimRight(clean, "=")
	var data []byte
	for _, try := range []struct {
		enc *base64.Encoding
		in  string
	}{{base64.RawStdEncoding, trimmed}, {base64.RawURLEncoding, trimmed}} {
		if d, e := try.enc.DecodeString(try.in); e == nil {
			data = d
			err = nil
			break
		} else {
			err = e
		}
	}
	if err != nil {
		return Userf("%s doesn't look like base64 text", job.Name(0))
	}
	return os.WriteFile(job.Output, data, 0o644)
}
