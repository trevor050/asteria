package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"asteria/internal/inspect"
	"asteria/internal/skills"
	"asteria/internal/tools"
)

// The format matrix: every conversion skill runs on a real fixture of every
// format it accepts (testdata/formats/sample.<ext>, then testdata/fixtures),
// and its output is checked by a verifier that does not share code with the
// writer (ImageMagick, poppler, ffprobe, Python's parsers, or a structural
// check of the container plus the fixture's marker text). Only conversions
// that pass are published.
//
//	ASTERIA_MATRIX=1 [ASTERIA_MATRIX_OUT=docs/formats.json] go test -run Matrix ./internal/engine
//
// It runs in CI without engines (the pure-Go part) and on a machine with the
// engines installed for the published list.

const marker = "Asteria matrix check"

type matrixRow struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Skill      string `json:"skill"`
	SkillName  string `json:"skillName"`
	Category   string `json:"category"`
	Engine     string `json:"engine,omitempty"`
	VerifiedBy string `json:"verifiedBy"`
}

type matrixReport struct {
	Generated   string            `json:"generated"`
	Platform    string            `json:"platform"`
	Engines     map[string]string `json:"engines"`
	Conversions []matrixRow       `json:"conversions"`
	Failed      []string          `json:"failed"`
	Untested    []string          `json:"untested"`
	Summary     map[string]int    `json:"summary"`
}

func TestMatrix(t *testing.T) {
	if os.Getenv("ASTERIA_MATRIX") == "" {
		t.Skip("set ASTERIA_MATRIX=1")
	}
	e := newEnv(t, "")
	finder := tools.NewFinder()
	e.eng.opts.Tools = finder
	fixtures := map[string]string{}
	for _, dir := range []string{"../../testdata/fixtures", "../../testdata/formats"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		for _, f := range files {
			if st, err := os.Stat(f); err != nil || st.IsDir() {
				continue
			}
			fixtures[skills.FileType(f, false)] = f // formats/ wins (read second)
		}
	}
	// Other spellings of the same format read the canonical fixture under
	// their own extension (the copy made for each run carries it).
	for alias, canon := range formatAliases {
		if _, ok := fixtures[alias]; !ok && fixtures[canon] != "" {
			fixtures[alias] = fixtures[canon]
		}
	}
	// Skills that take any file are run once on a plain note.
	fixtures["*"] = "../../testdata/formats/any-file.txt"
	rep := matrixReport{Generated: time.Now().UTC().Format(time.RFC3339), Platform: runtime.GOOS + "/" + runtime.GOARCH, Engines: map[string]string{}, Summary: map[string]int{}}
	seenUntested := map[string]bool{}
	var pureFailed []string
	list := e.reg.List()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, sk := range list {
		if sk.IsMeta || sk.Combine || sk.OutputType == "" || sk.OutputType == "none" {
			continue
		}
		if os.Getenv("ASTERIA_MATRIX_PURE") != "" && len(sk.Requires) > 0 {
			continue // built-in conversions only (quick local check)
		}
		missing := ""
		for _, tool := range sk.Requires {
			if _, ok := finder.Lookup(tool); !ok {
				missing = tool
			}
		}
		for _, in := range sk.InputTypes {
			if in == sk.OutputType {
				continue
			}
			key := in + " -> " + sk.OutputType + " (" + sk.ID + ")"
			src, ok := fixtures[in]
			// A skill may need particular content (OCR needs text):
			// testdata/formats/<skill>.<ext> overrides the generic fixture.
			hasSpecial := false
			for _, ext := range []string{in, formatAliases[in]} {
				if special := filepath.Join("../../testdata/formats", sk.ID+ext); ext != "" && fileExists(special) {
					src, ok, hasSpecial = special, true, true
					break
				}
			}
			// A skill with content-specific fixtures (OCR needs a picture
			// of text) is only run where such a fixture exists.
			if !hasSpecial && contentSpecific[sk.ID] {
				ok = false
			}
			if !ok || missing != "" {
				if !seenUntested[key] {
					seenUntested[key] = true
					why := "no fixture"
					if missing != "" {
						why = "needs " + missing
					}
					rep.Untested = append(rep.Untested, key+": "+why)
				}
				continue
			}
			by, err := runAndVerify(t, e, sk, src, in)
			var nv noVerifier
			if errors.As(err, &nv) {
				rep.Untested = append(rep.Untested, key+": "+nv.Error())
				continue
			}
			if err != nil {
				rep.Failed = append(rep.Failed, key+": "+err.Error())
				if len(sk.Requires) == 0 {
					pureFailed = append(pureFailed, key+": "+err.Error())
				}
				continue
			}
			eng := ""
			if len(sk.Requires) > 0 {
				eng = sk.Requires[0]
				if _, ok := rep.Engines[eng]; !ok {
					rep.Engines[eng] = toolVersion(finder, eng)
				}
			}
			rep.Conversions = append(rep.Conversions, matrixRow{From: in, To: sk.OutputType, Skill: sk.ID, SkillName: sk.Name, Category: sk.Category, Engine: eng, VerifiedBy: by})
		}
	}
	readable, writable, pairs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range rep.Conversions {
		if r.From != "*" {
			readable[r.From] = true
		}
		writable[r.To] = true
		pairs[r.From+">"+r.To] = true
	}
	rep.Summary["readFormats"] = len(readable)
	rep.Summary["writeFormats"] = len(writable)
	rep.Summary["conversionPairs"] = len(pairs)
	rep.Summary["failed"] = len(rep.Failed)
	t.Logf("matrix: %d readable formats, %d writable, %d from->to pairs verified, %d failed, %d untested",
		len(readable), len(writable), len(pairs), len(rep.Failed), len(rep.Untested))
	for _, f := range rep.Failed {
		t.Log("FAILED " + f)
	}
	if out := os.Getenv("ASTERIA_MATRIX_OUT"); out != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Built-in (pure Go) conversions must all pass wherever this runs;
	// engine conversions depend on what is installed.
	if len(pureFailed) > 0 {
		t.Fatalf("%d built-in conversions failed:\n%s", len(pureFailed), strings.Join(pureFailed, "\n"))
	}
}

// contentSpecific skills need fixtures with particular content.
var contentSpecific = map[string]bool{"ocr_to_text": true, "ocr_to_searchable_pdf": true}

// formatAliases are other spellings of a format with its own fixture.
var formatAliases = map[string]string{
	".jpeg": ".jpg", ".tiff": ".tif", ".heif": ".heic", ".yml": ".yaml", ".ndjson": ".jsonl",
	".tgz": ".tar.gz", ".txz": ".tar.xz", ".tzst": ".tar.zst", ".tbz2": ".tar.bz2", ".tbz": ".tar.bz2",
	".mbx": ".mbox", ".sqlite3": ".sqlite", ".db": ".sqlite", ".markdown": ".md", ".dfxp": ".ttml",
	".cfg": ".ini", ".conf": ".ini", ".htm": ".html",
}

func runAndVerify(t *testing.T, e *env, sk skills.Skill, src, in string) (string, error) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	ext := in
	if in == "*" {
		ext = skills.FileType(src, false)
	}
	p := filepath.Join(e.src, fmt.Sprintf("m-%s-%d%s", sk.ID, time.Now().UnixNano(), ext))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	f, err := e.store.Add(p, func(path, typ string) inspect.Info { return inspect.Info{} })
	if err != nil {
		return "", err
	}
	defer e.store.Remove([]string{f.ID()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := e.eng.Apply(ctx, []string{f.ID()}, sk.ID, nil, nil)
	if err != nil {
		return "", err
	}
	if len(out.Failed) > 0 {
		return "", fmt.Errorf("%s", out.Failed[0].Reason)
	}
	if len(out.Updated) != 1 {
		return "", fmt.Errorf("no output (%d skipped)", len(out.Skipped))
	}
	cur := f.Current()
	if check, ok := skillChecks[sk.ID]; ok {
		if _, err := verify(cur.Path, sk.OutputType, false); err != nil {
			return "", err
		}
		return check(p, cur.Path)
	}
	return verify(cur.Path, sk.OutputType, sourceHasMarker(src, ext))
}

// skillChecks verify skills whose output is about the input rather than a
// copy of its content, each against Python's own implementation.
var skillChecks = map[string]func(in, out string) (string, error){
	"base64_encode": pyCompare("import base64,sys;a=open(sys.argv[1],'rb').read();b=base64.b64decode(open(sys.argv[2]).read());assert a==b", "Python base64"),
	"checksum":      pyCompare("import hashlib,sys;h=hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest();assert h in open(sys.argv[2]).read().lower()", "Python hashlib"),
	"file_info":     pyCompare("import hashlib,json,sys;h=hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest();assert json.load(open(sys.argv[2]))['sha256']==h", "Python hashlib + json"),
	"list_archive_contents": func(in, out string) (string, error) {
		b, _ := os.ReadFile(out)
		switch filepath.Ext(in) {
		case ".gz", ".xz", ".zst", ".bz2":
			if strings.Contains(in, ".tar.") {
				break
			}
			// A single compressed file: the listing gives its real size.
			if !strings.Contains(string(b), fmt.Sprint(len(marker)+1)) {
				return "", fmt.Errorf("listing doesn't give the %d-byte size", len(marker)+1)
			}
			return "listing gives the uncompressed size", nil
		}
		if !strings.Contains(string(b), "note.txt") {
			return "", fmt.Errorf("listing doesn't name note.txt")
		}
		return "listing names the archived file", nil
	},
}

func pyCompare(script, by string) func(in, out string) (string, error) {
	return func(in, out string) (string, error) {
		if _, err := exec.LookPath("python3"); err != nil {
			return "", errNoVerifier("python3")
		}
		if b, err := exec.Command("python3", "-c", script, in, out).CombinedOutput(); err != nil {
			return "", fmt.Errorf("%s: %s", by, lastLine(string(b)))
		}
		return by, nil
	}
}

// sourceHasMarker: text-bearing fixtures carry the marker; outputs of
// text-preserving conversions must still contain it.
func sourceHasMarker(src, ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".mp4", ".mov", ".webm", ".mkv",
		".mp3", ".wav", ".flac", ".m4a", ".ogg", ".opus":
		return false
	}
	switch ext {
	// Every compressed fixture holds a note with the marker.
	case ".gz", ".xz", ".zst", ".bz2", ".tar.gz", ".tgz", ".tar.xz", ".tar.zst", ".tar.bz2", ".7z", ".zip":
		return true
	}
	b, _ := os.ReadFile(src)
	if bytes.Contains(b, []byte(marker)) {
		return true
	}
	return strings.Contains(zipText(src), marker)
}

// folderHasMarker looks for the marker in any file under dir.
func folderHasMarker(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		b, _ := os.ReadFile(p)
		found = bytes.Contains(b, []byte(marker)) || strings.Contains(zipText(p), marker)
		return nil
	})
	return found
}

func verify(path, ext string, wantMarker bool) (string, error) {
	have := func(name string) bool { _, err := exec.LookPath(name); return err == nil }
	run := func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return string(out), err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		n := 0
		_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		if n == 0 {
			return "", fmt.Errorf("empty folder")
		}
		if wantMarker && !folderHasMarker(path) {
			return "", fmt.Errorf("marker text missing from the extracted files")
		}
		return "folder contents", nil
	}
	if st.Size() == 0 {
		return "", fmt.Errorf("empty output")
	}
	text := func() string { b, _ := os.ReadFile(path); return string(b) }
	needMarker := func(s, by string) (string, error) {
		if wantMarker && !strings.Contains(s, marker) {
			return "", fmt.Errorf("marker text missing (%s)", by)
		}
		return by, nil
	}
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".avif", ".ico":
		if have("magick") {
			out, err := run("magick", "identify", "-format", "%wx%h", path+"[0]")
			if err == nil && strings.Contains(out, "x") {
				return "ImageMagick identify", nil
			}
			// Builds without a HEIC/AVIF delegate: fall through to ffprobe.
			if !strings.Contains(out, "delegate") && !strings.Contains(out, "module") && !strings.Contains(out, "plugin") {
				return "", fmt.Errorf("ImageMagick can't read it: %s", strings.TrimSpace(out))
			}
		}
		if have("ffprobe") {
			if out, err := run("ffprobe", "-v", "error", "-show_entries", "stream=width,height", "-of", "csv=p=0", path); err == nil && strings.Contains(out, ",") {
				return "ffprobe", nil
			}
		}
		if _, _, err := inspect.Thumbnail(path, 64); err != nil {
			return "", err
		}
		return "image decoder", nil
	case ".pdf":
		if have("pdfinfo") {
			out, err := run("pdfinfo", path)
			if err != nil || !strings.Contains(out, "Pages:") {
				return "", fmt.Errorf("pdfinfo: %s", strings.TrimSpace(out))
			}
			if wantMarker && have("pdftotext") {
				txt, _ := run("pdftotext", path, "-")
				return needMarker(strings.Join(strings.Fields(txt), " "), "poppler pdfinfo + pdftotext")
			}
			return "poppler pdfinfo", nil
		}
		if !strings.HasPrefix(text(), "%PDF-") {
			return "", fmt.Errorf("not a PDF")
		}
		return "PDF header", nil
	case ".mp4", ".mov", ".m4v", ".mkv", ".webm", ".avi", ".mp3", ".m4a", ".wav", ".flac", ".ogg", ".opus", ".aac", ".gif2":
		if !have("ffprobe") {
			return "", fmt.Errorf("no ffprobe to verify")
		}
		out, err := run("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
		if err != nil || strings.TrimSpace(out) == "" || strings.HasPrefix(strings.TrimSpace(out), "N/A") {
			return "", fmt.Errorf("ffprobe: %s", strings.TrimSpace(out))
		}
		return "ffprobe", nil
	case ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".epub", ".3mf":
		s := zipText(path)
		if s == "" {
			return "", fmt.Errorf("not a valid package")
		}
		return needMarker(s, "zip package + XML text")
	case ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".xml", ".plist", ".ttml", ".html", ".htm", ".svg":
		if have("python3") {
			if out, err := run("python3", "-c", pyCheck(ext), path); err != nil {
				return "", fmt.Errorf("python: %s", strings.TrimSpace(out))
			}
			return needMarker(text(), "Python parser")
		}
		return needMarker(text(), "text")
	case ".sqlite", ".db", ".sqlite3":
		if have("python3") {
			out, err := run("python3", "-c", "import sqlite3,sys;c=sqlite3.connect(sys.argv[1]);print(c.execute(\"select count(*) from sqlite_master where type='table'\").fetchone()[0])", path)
			if err != nil || strings.TrimSpace(out) == "0" {
				return "", fmt.Errorf("sqlite: %s", strings.TrimSpace(out))
			}
			return "Python sqlite3", nil
		}
		return "", errNoVerifier("python3")
	case ".mobi", ".azw3", ".azw":
		b := []byte(text())
		if len(b) < 68 || string(b[60:68]) != "BOOKMOBI" {
			return "", fmt.Errorf("not a Mobipocket file")
		}
		return "MOBI header", nil
	case ".7z":
		sz := ""
		for _, n := range []string{"7zz", "7z", "7za"} {
			if have(n) {
				sz = n
				break
			}
		}
		if sz != "" {
			if out, err := run(sz, "l", path); err != nil {
				return "", fmt.Errorf("7-Zip: %s", strings.TrimSpace(out))
			}
			return "7-Zip list", nil
		}
		return "", errNoVerifier("7-Zip")
	case ".zip":
		if _, err := zip.OpenReader(path); err != nil {
			return "", err
		}
		return "zip reader", nil
	case ".gz", ".tgz", ".tar.gz", ".xz", ".txz", ".tar.xz", ".bz2", ".tar.bz2":
		// Python's own decompressors; a tar inside is checked by tarfile.
		if !have("python3") {
			return "", errNoVerifier("python3")
		}
		out, err := run("python3", "-c", pyDecompress, path)
		if err != nil {
			return "", fmt.Errorf("python: %s", strings.TrimSpace(out))
		}
		return needMarker(out, "Python gzip/lzma/bz2")
	case ".zst", ".tzst", ".tar.zst":
		if !have("zstd") {
			return "", errNoVerifier("zstd")
		}
		out, err := run("zstd", "-dc", "--", path)
		if err != nil {
			return "", fmt.Errorf("zstd: %s", strings.TrimSpace(out))
		}
		return needMarker(out, "zstd CLI")
	case ".ttf", ".otf", ".woff", ".woff2":
		if !have("python3") {
			return "", errNoVerifier("python3")
		}
		if _, err := run("python3", "-c", "import fontTools, brotli"); err != nil {
			return "", errNoVerifier("Python fontTools and brotli")
		}
		out, err := run("python3", "-c", "import sys;from fontTools.ttLib import TTFont;f=TTFont(sys.argv[1]);n=len(f.getGlyphOrder());assert n>1;f['cmap'];print(n)", path)
		if err != nil {
			return "", fmt.Errorf("fontTools: %s", lastLine(out))
		}
		return "fontTools", nil
	case ".cube", ".3dl":
		if !have("python3") {
			return "", errNoVerifier("python3")
		}
		out, err := run("python3", "-c", pyLUT, path)
		if err != nil {
			return "", fmt.Errorf("python: %s", lastLine(out))
		}
		return "Python LUT parse", nil
	}
	// Plain text formats (csv, tsv, md, txt, srt, vtt, ass, sbv, ini, env, rst, tex...).
	s := text()
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("not UTF-8 text")
	}
	return needMarker(s, "UTF-8 text")
}

// noVerifier means no independent checker is installed here: the
// conversion is reported as untested, not passed or failed.
type noVerifier string

func (n noVerifier) Error() string { return "no " + string(n) + " here to verify the output" }

func errNoVerifier(what string) error { return noVerifier(what) }

// pyDecompress prints the decompressed text, or for a tarball the text of
// its members.
const pyDecompress = `import sys,gzip,lzma,bz2,tarfile,io
p=sys.argv[1]
raw=open(p,'rb').read()
if raw[:2]==b'\x1f\x8b': data=gzip.decompress(raw)
elif raw[:6]==b'\xfd7zXZ\x00': data=lzma.decompress(raw)
elif raw[:3]==b'BZh': data=bz2.decompress(raw)
else: raise SystemExit('unknown compression')
if len(data)>262 and data[257:262]==b'ustar':
  t=tarfile.open(fileobj=io.BytesIO(data))
  data=b''.join(t.extractfile(m).read() for m in t.getmembers() if m.isfile())
sys.stdout.write(data.decode('utf-8','replace'))`

// pyLUT checks a .cube or .3dl has a full grid of in-range entries.
const pyLUT = `import sys,re
p=sys.argv[1];L=[l.strip() for l in open(p,encoding='utf-8') if l.strip() and not l.startswith('#')]
if p.lower().endswith('.cube'):
  n=int([l.split()[1] for l in L if l.startswith('LUT_3D_SIZE')][0])
  rows=[l for l in L if re.match(r'^[-0-9.eE]+\s+[-0-9.eE]+\s+[-0-9.eE]+$',l)]
  assert len(rows)==n**3,(len(rows),n)
  assert all(-0.01<=float(v)<=1.01 for r in rows for v in r.split())
else:
  n=len(L[0].split());rows=L[1:]
  assert len(rows)==n**3,(len(rows),n)
  mx=max(int(v) for r in rows for v in r.split());assert 0<mx<65536
print(n)`

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func pyCheck(ext string) string {
	switch ext {
	case ".json":
		return "import json,sys;json.load(open(sys.argv[1],encoding='utf-8'))"
	case ".jsonl", ".ndjson":
		return "import json,sys;[json.loads(l) for l in open(sys.argv[1],encoding='utf-8') if l.strip()]"
	case ".yaml", ".yml":
		return "import yaml,sys;yaml.safe_load(open(sys.argv[1],encoding='utf-8'))"
	case ".toml":
		return "import tomllib,sys;tomllib.load(open(sys.argv[1],'rb'))"
	case ".plist":
		return "import plistlib,sys;plistlib.load(open(sys.argv[1],'rb'))"
	case ".html", ".htm":
		return "import html.parser,sys;p=html.parser.HTMLParser();p.feed(open(sys.argv[1],encoding='utf-8').read())"
	}
	return "import xml.dom.minidom,sys;xml.dom.minidom.parse(sys.argv[1])"
}

// zipText concatenates the text of XML parts in a zip package.
func zipText(path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()
	var b strings.Builder
	for _, f := range zr.File {
		if !(strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".xhtml") || strings.HasSuffix(f.Name, ".html") || strings.HasSuffix(f.Name, ".model")) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(rc, 8<<20))
		rc.Close()
		// strip tags so marker words split across runs still match
		s := string(data)
		var out strings.Builder
		in := false
		for _, r := range s {
			switch {
			case r == '<':
				in = true
			case r == '>':
				in = false
			case !in:
				out.WriteRune(r)
			}
		}
		b.WriteString(out.String())
		b.WriteByte(' ')
	}
	if b.Len() == 0 {
		return "pkg"
	}
	return b.String()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func toolVersion(f *tools.Finder, tool string) string {
	p, ok := f.Lookup(tool)
	if !ok {
		return ""
	}
	flag := "--version"
	switch tool {
	case "ffmpeg", "ffprobe":
		flag = "-version"
	case "7zz", "7za", "7z":
		flag = "i"
	}
	out, _ := exec.Command(p, flag).CombinedOutput()
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}
