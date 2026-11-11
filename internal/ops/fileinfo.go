package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// file.info describes any file, including types Asteria has no skills for:
// what its content says it is (by signature, not by name), its size, dates,
// a SHA-256, and for text, the encoding and line count.

func init() {
	register(Handler{Name: "file.info", Run: runFileInfo})
}

type magic struct {
	offset int
	sig    string
	kind   string
	mime   string
	ext    string
}

// Signatures checked in order; the first match wins.
var magics = []magic{
	{0, "\x89PNG\r\n\x1a\n", "PNG image", "image/png", ".png"},
	{0, "\xff\xd8\xff", "JPEG image", "image/jpeg", ".jpg"},
	{0, "GIF87a", "GIF image", "image/gif", ".gif"},
	{0, "GIF89a", "GIF image", "image/gif", ".gif"},
	{0, "RIFF", "", "", ""}, // refined below
	{0, "II*\x00", "TIFF image (or camera RAW)", "image/tiff", ".tif"},
	{0, "MM\x00*", "TIFF image (or camera RAW)", "image/tiff", ".tif"},
	{0, "BM", "BMP image", "image/bmp", ".bmp"},
	{0, "8BPS", "Photoshop document", "image/vnd.adobe.photoshop", ".psd"},
	{0, "\x00\x00\x01\x00", "Windows icon", "image/x-icon", ".ico"},
	{0, "%PDF-", "PDF document", "application/pdf", ".pdf"},
	{0, "%!PS", "PostScript document", "application/postscript", ".ps"},
	{0, "PK\x03\x04", "", "", ""}, // refined below
	{0, "PK\x05\x06", "ZIP archive (empty)", "application/zip", ".zip"},
	{0, "7z\xbc\xaf\x27\x1c", "7-Zip archive", "application/x-7z-compressed", ".7z"},
	{0, "Rar!\x1a\x07", "RAR archive", "application/vnd.rar", ".rar"},
	{0, "\x1f\x8b", "gzip-compressed file", "application/gzip", ".gz"},
	{0, "\xfd7zXZ\x00", "xz-compressed file", "application/x-xz", ".xz"},
	{0, "\x28\xb5\x2f\xfd", "Zstandard-compressed file", "application/zstd", ".zst"},
	{0, "BZh", "bzip2-compressed file", "application/x-bzip2", ".bz2"},
	{257, "ustar", "tar archive", "application/x-tar", ".tar"},
	{0, "MSWIM\x00\x00\x00", "Windows imaging archive", "application/x-ms-wim", ".wim"},
	{0, "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1", "Legacy Microsoft Office document (or MSI)", "application/x-ole-storage", ".doc"},
	{0, "{\\rtf", "Rich Text document", "application/rtf", ".rtf"},
	{0, "SQLite format 3\x00", "SQLite database", "application/vnd.sqlite3", ".sqlite"},
	{0, "PAR1", "Parquet data file", "application/vnd.apache.parquet", ".parquet"},
	{0, "ID3", "MP3 audio", "audio/mpeg", ".mp3"},
	{0, "fLaC", "FLAC audio", "audio/flac", ".flac"},
	{0, "OggS", "Ogg audio or video", "audio/ogg", ".ogg"},
	{0, "\x1a\x45\xdf\xa3", "Matroska or WebM video", "video/x-matroska", ".mkv"},
	{0, "FORM", "AIFF audio", "audio/aiff", ".aiff"},
	{0, "wOFF", "WOFF web font", "font/woff", ".woff"},
	{0, "wOF2", "WOFF2 web font", "font/woff2", ".woff2"},
	{0, "\x00\x01\x00\x00", "TrueType font", "font/ttf", ".ttf"},
	{0, "OTTO", "OpenType font", "font/otf", ".otf"},
	{0, "glTF", "glTF binary 3D model", "model/gltf-binary", ".glb"},
	{0, "\x7fELF", "Linux program", "application/x-elf", ""},
	{0, "MZ", "Windows program or library", "application/vnd.microsoft.portable-executable", ".exe"},
	{0, "\xcf\xfa\xed\xfe", "macOS program", "application/x-mach-binary", ""},
	{0, "\xca\xfe\xba\xbe", "macOS universal program (or Java class)", "application/x-mach-binary", ""},
	{0, "-----BEGIN PGP", "PGP data", "application/pgp-encrypted", ".asc"},
	{0, "age-encryption.org/", "age-encrypted file", "application/x-age", ".age"},
	{0, "From ", "Mailbox (mbox)", "application/mbox", ".mbox"},
	{0, "BEGIN:VCARD", "Contact card (vCard)", "text/vcard", ".vcf"},
	{0, "BEGIN:VCALENDAR", "Calendar (iCalendar)", "text/calendar", ".ics"},
	{0, "WEBVTT", "WebVTT subtitles", "text/vtt", ".vtt"},
}

type fileInfo struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SizeText      string `json:"sizeText"`
	Modified      string `json:"modified"`
	Kind          string `json:"kind"`
	MIME          string `json:"mime,omitempty"`
	TypicalExt    string `json:"typicalExtension,omitempty"`
	NameMatches   *bool  `json:"extensionMatchesContent,omitempty"`
	Text          bool   `json:"text"`
	Encoding      string `json:"encoding,omitempty"`
	Lines         int    `json:"lines,omitempty"`
	LineEndings   string `json:"lineEndings,omitempty"`
	SHA256        string `json:"sha256"`
	FirstBytesHex string `json:"firstBytes"`
}

func runFileInfo(ctx context.Context, job Job) error {
	f, err := os.Open(job.Input())
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	// Hash and, for text, count lines in one pass.
	var crlf, lf, cr int
	var prev byte
	buf := make([]byte, 1<<20)
	textSoFar := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		k, rerr := f.Read(buf)
		chunk := buf[:k]
		h.Write(chunk)
		if textSoFar && bytes.IndexByte(chunk, 0) >= 0 && !utf16BOM(head) {
			textSoFar = false
		}
		for _, c := range chunk {
			switch {
			case c == '\n' && prev == '\r':
				crlf++
				cr--
			case c == '\n':
				lf++
			case c == '\r':
				cr++
			}
			prev = c
		}
		if rerr == io.EOF || k == 0 {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	info := fileInfo{
		Name:          job.Name(0),
		Size:          st.Size(),
		SizeText:      humanSize(st.Size()),
		Modified:      st.ModTime().Format(time.RFC3339),
		SHA256:        hex.EncodeToString(h.Sum(nil)),
		FirstBytesHex: hex.EncodeToString(head[:min(16, len(head))]),
	}
	info.Kind, info.MIME, info.TypicalExt = sniff(head)
	if info.Kind == "" {
		if enc, ok := textEncoding(head); ok {
			info.Kind, info.MIME = "Text", "text/plain"
			info.Text, info.Encoding = true, enc
		} else if len(head) == 0 {
			info.Kind = "Empty file"
		} else {
			info.Kind = "Unrecognized binary data"
			info.MIME = "application/octet-stream"
		}
	} else if strings.HasPrefix(info.MIME, "text/") || info.MIME == "application/mbox" || info.MIME == "application/rtf" {
		if enc, ok := textEncoding(head); ok {
			info.Text, info.Encoding = true, enc
		}
	}
	if info.Text {
		info.Lines = crlf + lf + cr
		if len(head) > 0 && prev != '\n' && prev != '\r' {
			info.Lines++
		}
		switch {
		case crlf > 0 && lf == 0 && cr == 0:
			info.LineEndings = "Windows (CRLF)"
		case lf > 0 && crlf == 0 && cr == 0:
			info.LineEndings = "Unix (LF)"
		case cr > 0 && lf == 0 && crlf == 0:
			info.LineEndings = "Classic Mac (CR)"
		case crlf+lf+cr > 0:
			info.LineEndings = "Mixed"
		}
	}
	if info.TypicalExt != "" {
		ext := strings.ToLower(filepath.Ext(info.Name))
		match := ext == info.TypicalExt || sameFamilyExt(ext, info.TypicalExt)
		info.NameMatches = &match
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(job.Output, append(b, '\n'), 0o644)
}

func sniff(head []byte) (kind, mime, ext string) {
	for _, m := range magics {
		if len(head) < m.offset+len(m.sig) || string(head[m.offset:m.offset+len(m.sig)]) != m.sig {
			continue
		}
		switch m.sig {
		case "RIFF":
			if len(head) >= 12 {
				switch string(head[8:12]) {
				case "WEBP":
					return "WebP image", "image/webp", ".webp"
				case "WAVE":
					return "WAV audio", "audio/wav", ".wav"
				case "AVI ":
					return "AVI video", "video/x-msvideo", ".avi"
				}
			}
			return "RIFF container", "application/octet-stream", ""
		case "PK\x03\x04":
			return sniffZip(head)
		}
		return m.kind, m.mime, m.ext
	}
	// ISO base media (MP4, MOV, HEIC, AVIF, 3GP): "ftyp" at offset 4.
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		switch brand := string(head[8:12]); {
		case brand == "heic" || brand == "heix" || brand == "mif1" || brand == "heim":
			return "HEIC image", "image/heic", ".heic"
		case brand == "avif" || brand == "avis":
			return "AVIF image", "image/avif", ".avif"
		case brand == "qt  ":
			return "QuickTime video", "video/quicktime", ".mov"
		case strings.HasPrefix(brand, "M4A"):
			return "MPEG-4 audio", "audio/mp4", ".m4a"
		default:
			return "MPEG-4 video", "video/mp4", ".mp4"
		}
	}
	if len(head) >= 2 && head[0] == 0xff && head[1]&0xe0 == 0xe0 {
		return "MP3 audio", "audio/mpeg", ".mp3"
	}
	trim := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
	switch {
	case bytes.HasPrefix(trim, []byte("<svg")) || bytes.HasPrefix(trim, []byte("<?xml")) && bytes.Contains(head, []byte("<svg")):
		return "SVG image", "image/svg+xml", ".svg"
	case bytes.HasPrefix(bytes.ToLower(trim), []byte("<!doctype html")) || bytes.HasPrefix(bytes.ToLower(trim), []byte("<html")):
		return "HTML page", "text/html", ".html"
	case bytes.HasPrefix(trim, []byte("<?xml")):
		return "XML document", "application/xml", ".xml"
	case (bytes.HasPrefix(trim, []byte("{")) || bytes.HasPrefix(trim, []byte("["))) && json.Valid(head) && len(head) < 4096:
		return "JSON data", "application/json", ".json"
	}
	return "", "", ""
}

// sniffZip tells apart packages that are zip files inside: Office, OpenDocument
// and EPUB name their type in the first entries.
func sniffZip(head []byte) (string, string, string) {
	s := string(head)
	switch {
	case strings.Contains(s, "mimetypeapplication/epub+zip"):
		return "EPUB ebook", "application/epub+zip", ".epub"
	case strings.Contains(s, "mimetypeapplication/vnd.oasis.opendocument.text"):
		return "OpenDocument text", "application/vnd.oasis.opendocument.text", ".odt"
	case strings.Contains(s, "mimetypeapplication/vnd.oasis.opendocument.spreadsheet"):
		return "OpenDocument spreadsheet", "application/vnd.oasis.opendocument.spreadsheet", ".ods"
	case strings.Contains(s, "mimetypeapplication/vnd.oasis.opendocument.presentation"):
		return "OpenDocument presentation", "application/vnd.oasis.opendocument.presentation", ".odp"
	case strings.Contains(s, "word/"):
		return "Word document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"
	case strings.Contains(s, "xl/"):
		return "Excel workbook", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"
	case strings.Contains(s, "ppt/"):
		return "PowerPoint presentation", "application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"
	case strings.Contains(s, "[Content_Types].xml"):
		return "Office Open XML package", "application/zip", ".zip"
	case strings.Contains(s, "AndroidManifest.xml"):
		return "Android app package", "application/vnd.android.package-archive", ".apk"
	case strings.Contains(s, "META-INF/MANIFEST.MF"):
		return "Java archive", "application/java-archive", ".jar"
	}
	return "ZIP archive", "application/zip", ".zip"
}

func sameFamilyExt(a, b string) bool {
	groups := [][]string{
		{".jpg", ".jpeg", ".jpe", ".jfif"}, {".tif", ".tiff", ".dng", ".cr2", ".nef", ".arw", ".orf", ".rw2", ".pef", ".srw"},
		{".mp4", ".m4v", ".3gp"}, {".heic", ".heif"}, {".mkv", ".webm"}, {".ogg", ".oga", ".ogv", ".opus"},
		{".aiff", ".aif"}, {".html", ".htm"}, {".doc", ".xls", ".ppt", ".msi", ".msg"}, {".sqlite", ".db", ".sqlite3"},
		{".exe", ".dll"}, {".zip", ".docx", ".xlsx", ".pptx", ".jar", ".apk", ".epub", ".odt", ".ods", ".odp"},
		{".xml", ".svg", ".kml", ".gpx", ".plist"}, {".gz", ".tgz"}, {".xz", ".txz"}, {".ttf", ".ttc"},
		{".json", ".geojson", ".ipynb", ".gltf"},
	}
	for _, g := range groups {
		ina, inb := false, false
		for _, x := range g {
			ina = ina || x == a
			inb = inb || x == b
		}
		if ina && inb {
			return true
		}
	}
	return false
}

func utf16BOM(head []byte) bool {
	return bytes.HasPrefix(head, []byte{0xff, 0xfe}) || bytes.HasPrefix(head, []byte{0xfe, 0xff})
}

// textEncoding guesses whether the start of a file is text, and how it is
// encoded.
func textEncoding(head []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(head, []byte("\xef\xbb\xbf")):
		return "UTF-8 with BOM", true
	case bytes.HasPrefix(head, []byte{0xff, 0xfe}):
		return "UTF-16 little-endian", true
	case bytes.HasPrefix(head, []byte{0xfe, 0xff}):
		return "UTF-16 big-endian", true
	}
	if len(head) == 0 || bytes.IndexByte(head, 0) >= 0 {
		return "", false
	}
	// A multi-byte character may be cut at the end of the sample.
	sample := head
	for i := 0; i < 3 && len(sample) > 0 && !utf8.Valid(sample); i++ {
		sample = sample[:len(sample)-1]
	}
	control := 0
	for _, c := range head {
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' && c != '\f' {
			control++
		}
	}
	if control*100 > len(head) {
		return "", false
	}
	if utf8.Valid(sample) {
		ascii := true
		for _, c := range head {
			if c >= 0x80 {
				ascii = false
				break
			}
		}
		if ascii {
			return "ASCII", true
		}
		return "UTF-8", true
	}
	return "Windows-1252 or another 8-bit encoding", true
}

func humanSize(n int64) string {
	switch {
	case n == 1:
		return "1 byte"
	case n < 1000:
		return fmt.Sprintf("%d bytes", n)
	case n < 1e6:
		return fmt.Sprintf("%.1f KB", float64(n)/1e3)
	case n < 1e9:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}
