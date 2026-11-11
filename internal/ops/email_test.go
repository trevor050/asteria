package ops

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tstWrap76(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	var out []string
	for len(s) > 76 {
		out = append(out, s[:76])
		s = s[76:]
	}
	out = append(out, s)
	return strings.Join(out, "\r\n")
}

func tstB64Word(s string) string {
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

var (
	tstAttachA = func() []byte {
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}()
	tstAttachB = []byte("second report, same file name\x00\xff\n")
	tstPNG     = []byte("\x89PNG\r\n\x1a\nnot-really-a-png")
)

// tstRichEML builds a multipart/related-ish message with RFC 2047 headers,
// quoted-printable ISO-8859-1 text, an HTML alternative full of hostile
// markup, two attachments sharing a name, an RFC 2047 file name and an inline
// image.
func tstRichEML() []byte {
	html := `<html><head><title>t</title><style>p{background:url(http://evil.example/x.png)}</style>` +
		`<script>alert(1)</script></head><body onload="boot()"><p onclick="x()">Hello <b>world</b></p>` +
		`<img src="http://tracker.example/pixel.gif" alt="pix"><img src="cid:logo1">` +
		`<a href="http://example.com/page">link</a><a href="javascript:alert(2)">bad</a></body></html>`
	lines := []string{
		"From: " + tstB64Word("Éléonore Dupont") + " <eleonore@example.com>",
		"To: =?ISO-8859-1?Q?Andr=E9_M=FCller?= <andre@example.com>",
		"Cc: cc@example.com",
		"Date: Tue, 11 Nov 2025 09:30:00 +0000",
		"Subject: " + tstB64Word("Réunion café ☕"),
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="mix"`,
		"",
		"--mix",
		`Content-Type: multipart/alternative; boundary="alt"`,
		"",
		"--alt",
		"Content-Type: text/plain; charset=ISO-8859-1",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Caf=E9 cr=E8me, tr=E8s bon=",
		"ne journ=E9e",
		"--alt",
		"Content-Type: text/html; charset=utf-8",
		"",
		html,
		"--alt--",
		"--mix",
		`Content-Type: application/octet-stream; name="report.bin"`,
		`Content-Disposition: attachment; filename="report.bin"`,
		"Content-Transfer-Encoding: base64",
		"",
		tstWrap76(tstAttachA),
		"--mix",
		`Content-Type: application/octet-stream`,
		`Content-Disposition: attachment; filename="report.bin"`,
		"Content-Transfer-Encoding: base64",
		"",
		tstWrap76(tstAttachB),
		"--mix",
		`Content-Type: text/plain`,
		`Content-Disposition: attachment; filename="` + tstB64Word("résumé.txt") + `"`,
		"",
		"cv contents",
		"--mix",
		`Content-Type: image/png`,
		`Content-Disposition: inline; filename="logo.png"`,
		"Content-ID: <logo1>",
		"Content-Transfer-Encoding: base64",
		"",
		tstWrap76(tstPNG),
		"--mix--",
		"",
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func tstEmailRun(t *testing.T, handler string, data []byte, name, outName string) (string, error) {
	t.Helper()
	h, ok := Lookup(handler)
	if !ok {
		t.Fatalf("%s not registered", handler)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "input.bin")
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, outName)
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{in}, Names: []string{name}, Output: out}))
	return out, err
}

func tstReadOut(t *testing.T, handler string, data []byte, name, outName string) string {
	t.Helper()
	out, err := tstEmailRun(t, handler, data, name, outName)
	if err != nil {
		t.Fatalf("%s: %v", handler, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEmlToText(t *testing.T) {
	got := tstReadOut(t, "email.toText", tstRichEML(), "mail.eml", "out.txt")
	for _, want := range []string{
		"From: Éléonore Dupont <eleonore@example.com>\n",
		"To: André Müller <andre@example.com>\n",
		"Cc: cc@example.com\n",
		"Date: Tue, 11 Nov 2025 09:30:00 +0000\n",
		"Subject: Réunion café ☕\n",
		"\nCafé crème, très bonne journée\n",
		"Attachments:\n- report.bin (256 B)\n- report.bin (",
		"- résumé.txt (11 B)\n",
		"- logo.png (",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Hello") {
		t.Errorf("the plain part should win over the HTML alternative:\n%s", got)
	}
}

func TestEmlToTextFromHTMLOnly(t *testing.T) {
	msg := "From: a@example.com\r\nSubject: S\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=windows-1252\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		tstWrap76([]byte("<p>Hello&nbsp;<b>w\xf6rld</b></p><script>x()</script><p>second</p>")) + "\r\n"
	got := tstReadOut(t, "email.toText", []byte(msg), "m.eml", "o.txt")
	want := "From: a@example.com\nSubject: S\n\nHello wörld\n\nsecond\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestEmlToTextBareLF(t *testing.T) {
	msg := "From: a@example.com\nSubject: plain\nContent-Type: text/plain; charset=utf-8\n\nline one\nline two\n"
	got := tstReadOut(t, "email.toText", []byte(msg), "m.eml", "o.txt")
	if want := "From: a@example.com\nSubject: plain\n\nline one\nline two\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestEmlToHTMLIsInertAndOffline(t *testing.T) {
	got := tstReadOut(t, "email.toHTML", tstRichEML(), "mail.eml", "out.html")
	lower := strings.ToLower(got)
	for _, bad := range []string{"<script", "onload", "onclick", "javascript:", "tracker.example", "evil.example", `src="http`, `src='http`, "<iframe", "<link"} {
		if strings.Contains(lower, bad) {
			t.Errorf("html output contains %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{
		"<title>Réunion café ☕</title>",
		"Hello <b>world</b>",
		"[pix]", // blocked image falls back to its alt text
		`src="data:image/png;base64,`,
		`href="http://example.com/page"`,
		"Content-Security-Policy",
		"<td>Éléonore Dupont &lt;eleonore@example.com&gt;</td>",
		"<li>résumé.txt (11 B)</li>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("html output missing %q:\n%s", want, got)
		}
	}
}

func TestEmlToHTMLPlainFallback(t *testing.T) {
	msg := "From: a@example.com\r\nSubject: <b>x</b>\r\nContent-Type: text/plain\r\n\r\n<script>alert(1)</script> & done\r\n"
	got := tstReadOut(t, "email.toHTML", []byte(msg), "m.eml", "o.html")
	if !strings.Contains(got, `<pre class="plain">&lt;script&gt;alert(1)&lt;/script&gt; &amp; done`) {
		t.Errorf("plain body not escaped inside <pre>:\n%s", got)
	}
	if strings.Contains(got, "<script") {
		t.Error("script leaked")
	}
}

func TestExtractAttachments(t *testing.T) {
	out, err := tstEmailRun(t, "email.attachments", tstRichEML(), "mail.eml", "atts")
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return b
	}
	if !bytes.Equal(read("report.bin"), tstAttachA) {
		t.Error("report.bin bytes differ")
	}
	if !bytes.Equal(read("report (2).bin"), tstAttachB) {
		t.Error("report (2).bin bytes differ")
	}
	if string(read("résumé.txt")) != "cv contents" {
		t.Errorf("résumé.txt = %q", read("résumé.txt"))
	}
	if !bytes.Equal(read("logo.png"), tstPNG) {
		t.Error("inline logo.png bytes differ")
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 4 {
		t.Errorf("want 4 files, got %d", len(entries))
	}

	_, err = tstEmailRun(t, "email.attachments", []byte("From: a@b.c\r\nSubject: none\r\n\r\nbody\r\n"), "m.eml", "atts")
	if err == nil || !strings.Contains(err.Error(), "no attachments") {
		t.Errorf("err = %v", err)
	}
}

func TestAttachmentNamesAreSanitized(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":                "passwd",
		`..\..\boot.ini`:                  "boot.ini",
		"a:b*c?.txt":                      "a_b_c_.txt",
		"  .hidden. ":                     "hidden",
		"":                                "attachment-3",
		"..":                              "attachment-3",
		"con.txt":                         "_con.txt",
		"tab\tnew\nline.txt":              "tabnewline.txt",
		strings.Repeat("é", 200) + ".pdf": strings.Repeat("é", 73) + ".pdf",
	} {
		got := emSafeName(in, 3)
		if got != want {
			t.Errorf("emSafeName(%q) = %q, want %q", in, got, want)
		}
	}

	msg := "From: a@b.c\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"../../evil.txt\"\r\n\r\npwn\r\n" +
		"--b--\r\n"
	out, err := tstEmailRun(t, "email.attachments", []byte(msg), "m.eml", "atts")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "evil.txt")); err != nil || !strings.HasPrefix(string(b), "pwn") {
		t.Errorf("evil.txt = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "evil.txt")); err == nil {
		t.Error("attachment escaped the output folder")
	}
}

func tstMbox() string {
	multipart := "From: carol@example.com\n" +
		"To: dave@example.com\n" +
		"Date: Wed, 03 Jan 2024 12:30:00 -0500\n" +
		"Subject: " + tstB64Word("Caf\u00e9 menu") + "\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: multipart/mixed; boundary=\"zz\"\n\n" +
		"--zz\nContent-Type: text/plain\n\nSee attached.\n" +
		"--zz\nContent-Type: text/plain; name=\"menu.txt\"\nContent-Disposition: attachment; filename=\"menu.txt\"\n\nsoup\n" +
		"--zz--\n"
	return "From alice@example.com Mon Jan  1 00:00:00 2024\n" +
		"From: alice@example.com\nTo: bob@example.com\nDate: Mon, 01 Jan 2024 00:00:00 +0000\nSubject: one\n\n" +
		"hello\n>From the top of the page\n>>From deeper\n\n" +
		"From carol@example.com Wed Jan  3 12:30:00 2024\n" + multipart + "\n" +
		"From erin@example.com Thu Jan  4 08:00:00 2024\n" +
		"From: erin@example.com\nTo: frank@example.com\nSubject: =1+1\n\nlast\n"
}

func TestMboxSplit(t *testing.T) {
	out, err := tstEmailRun(t, "email.mboxSplit", []byte(tstMbox()), "mail.mbox", "msgs")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 messages, got %d", len(entries))
	}
	for i, e := range entries {
		if want := fmt.Sprintf("%04d.eml", i+1); e.Name() != want {
			t.Errorf("entry %d = %s, want %s", i, e.Name(), want)
		}
	}
	first, _ := os.ReadFile(filepath.Join(out, "0001.eml"))
	want := "From: alice@example.com\nTo: bob@example.com\nDate: Mon, 01 Jan 2024 00:00:00 +0000\nSubject: one\n\n" +
		"hello\nFrom the top of the page\n>From deeper\n"
	if string(first) != want {
		t.Errorf("message 1 = %q\nwant %q", first, want)
	}
	second, _ := os.ReadFile(filepath.Join(out, "0002.eml"))
	if strings.HasPrefix(string(second), "From ") || !strings.HasSuffix(string(second), "--zz--\n") {
		t.Errorf("message 2 not cleanly separated: %q", second)
	}
	// Each split message is a valid, parseable email.
	m, err := emParse(second)
	if err != nil || len(m.atts) != 1 || m.atts[0].name != "menu.txt" {
		t.Errorf("message 2 parse: %v %+v", err, m)
	}

	if _, err := tstEmailRun(t, "email.mboxSplit", []byte("\n\n"), "empty.mbox", "msgs"); err == nil {
		t.Error("an empty mbox should be reported")
	}
}

func TestMboxToCSV(t *testing.T) {
	got := tstReadOut(t, "email.mboxCSV", []byte(tstMbox()), "mail.mbox", "out.csv")
	rows, err := csv.NewReader(strings.NewReader(got)).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v\n%s", err, got)
	}
	if len(rows) != 4 {
		t.Fatalf("want header + 3 rows, got %d:\n%s", len(rows), got)
	}
	if strings.Join(rows[0], ",") != "Date,From,To,Subject,Size,Attachments" {
		t.Errorf("header = %v", rows[0])
	}
	if r := rows[1]; r[0] != "2024-01-01 00:00:00 +0000" || r[1] != "alice@example.com" || r[2] != "bob@example.com" || r[3] != "one" || r[5] != "0" {
		t.Errorf("row 1 = %v", r)
	}
	if r := rows[2]; r[0] != "2024-01-03 12:30:00 -0500" || r[3] != "Café menu" || r[5] != "1" {
		t.Errorf("row 2 = %v", r)
	}
	// Formula-looking subjects are defused for spreadsheets.
	if r := rows[3]; r[3] != "'=1+1" || r[4] == "" || r[4] == "0" {
		t.Errorf("row 3 = %v", r)
	}
}

func TestEmlFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(formatsDir, "sample.eml"))
	if err != nil {
		t.Fatal(err)
	}
	got := tstReadOut(t, "email.toText", data, "sample.eml", "o.txt")
	for _, want := range []string{"Subject: Asteria matrix check", "Asteria matrix check: this is the plain text body.", "- note.txt (21 B)"} {
		if !strings.Contains(got, want) {
			t.Errorf("fixture text missing %q:\n%s", want, got)
		}
	}
	out, err := tstEmailRun(t, "email.attachments", data, "sample.eml", "atts")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "note.txt")); string(b) != "Asteria matrix check\n" {
		t.Errorf("note.txt = %q", b)
	}

	mb, err := os.ReadFile(filepath.Join(formatsDir, "sample.mbox"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := tstEmailRun(t, "email.mboxSplit", mb, "sample.mbox", "msgs")
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("sample.mbox should split into 2 messages, got %d", len(entries))
	}
	second, _ := os.ReadFile(filepath.Join(dir, "0002.eml"))
	if !strings.Contains(string(second), "\nFrom the desk of Charles, quoted line.") {
		t.Errorf(">From not restored: %q", second)
	}
}
