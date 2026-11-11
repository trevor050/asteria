package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	stdhtml "html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/encoding/htmlindex"
)

// Email handlers: .eml to text / HTML, attachment extraction, and .mbox
// splitting or summarizing. Pure Go (net/mail, mime, golang.org/x/net/html,
// golang.org/x/text charsets); nothing is fetched or executed.

// EmailExts and MboxExts list the input types of the email skills.
var (
	EmailExts = []string{".eml"}
	MboxExts  = []string{".mbox", ".mbx"}
)

func init() {
	register(Handler{Name: "email.toText", Run: emToText})
	register(Handler{Name: "email.toHTML", Run: emToHTML})
	register(Handler{Name: "email.attachments", Run: emExtractAttachments})
	register(Handler{Name: "email.mboxSplit", Run: emMboxSplit})
	register(Handler{Name: "email.mboxCSV", Run: emMboxCSV})
}

// ---- parsing ----

type emAttachment struct {
	name   string
	ctype  string
	data   []byte
	cid    string
	inline bool // an image referenced from the HTML body by Content-ID
}

type emMessage struct {
	header mail.Header
	texts  []string
	htmls  []string
	atts   []emAttachment
}

func emReadFile(job Job) ([]byte, error) {
	info, err := os.Stat(job.Input())
	if err != nil {
		return nil, err
	}
	if info.Size() > maxDataBytes {
		return nil, Userf("%s is too large to read in memory (limit 512 MB)", job.Name(0))
	}
	return os.ReadFile(job.Input())
}

// emStripEnvelope drops a leading mbox "From " line and a UTF-8 BOM.
func emStripEnvelope(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if bytes.HasPrefix(raw, []byte("From ")) {
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			return raw[i+1:]
		}
	}
	return raw
}

func emParse(raw []byte) (*emMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(emStripEnvelope(raw)))
	if err != nil {
		return nil, Userf("not a readable email message: %v", err)
	}
	m := &emMessage{header: msg.Header}
	m.walk(textproto.MIMEHeader(msg.Header), msg.Body, 0)
	return m, nil
}

var emParamRe = regexp.MustCompile(`(?i)(boundary|charset|filename|name)\s*=\s*(?:"([^"]*)"|([^;\s]+))`)

// emMediaType parses a Content-Type or Content-Disposition value, tolerating
// the malformed parameters real mail contains.
func emMediaType(v string) (string, map[string]string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", map[string]string{}
	}
	t, p, err := mime.ParseMediaType(v)
	if err == nil {
		return t, p
	}
	if p == nil {
		p = map[string]string{}
	}
	if t == "" {
		t = strings.ToLower(strings.TrimSpace(strings.SplitN(v, ";", 2)[0]))
	}
	for _, m := range emParamRe.FindAllStringSubmatch(v, -1) {
		key := strings.ToLower(m[1])
		if _, ok := p[key]; ok {
			continue
		}
		val := m[2]
		if val == "" {
			val = m[3]
		}
		p[key] = val
	}
	return t, p
}

func (m *emMessage) walk(h textproto.MIMEHeader, body io.Reader, depth int) {
	ctype, params := emMediaType(h.Get("Content-Type"))
	if ctype == "" {
		ctype = "text/plain"
	}
	if strings.HasPrefix(ctype, "multipart/") && params["boundary"] != "" && depth < 16 {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				break
			}
			m.walk(p.Header, p, depth+1)
		}
		return
	}
	data := emReadBody(body, h.Get("Content-Transfer-Encoding"))
	m.leaf(h, ctype, params, data)
}

// emReadBody undoes the Content-Transfer-Encoding, keeping whatever decoded
// before an error.
func emReadBody(r io.Reader, cte string) []byte {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		raw, _ := io.ReadAll(io.LimitReader(r, maxDataBytes*4/3+1024))
		clean := make([]byte, 0, len(raw))
		for _, c := range raw {
			switch c {
			case ' ', '\t', '\r', '\n':
			default:
				clean = append(clean, c)
			}
		}
		clean = bytes.TrimRight(clean, "=")
		out := make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
		n, _ := base64.RawStdEncoding.Decode(out, clean)
		return out[:n]
	case "quoted-printable":
		b, _ := io.ReadAll(io.LimitReader(quotedprintable.NewReader(r), maxDataBytes))
		return b
	}
	b, _ := io.ReadAll(io.LimitReader(r, maxDataBytes))
	return b
}

var emExt = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp",
	"image/svg+xml": ".svg", "image/bmp": ".bmp", "application/pdf": ".pdf", "text/calendar": ".ics",
	"message/rfc822": ".eml", "text/plain": ".txt", "text/html": ".html", "application/zip": ".zip",
}

func (m *emMessage) leaf(h textproto.MIMEHeader, ctype string, params map[string]string, data []byte) {
	disp, dparams := emMediaType(h.Get("Content-Disposition"))
	filename := dparams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	filename = emHeader(filename)
	cid := strings.Trim(strings.TrimSpace(h.Get("Content-Id")), "<>")

	if (ctype == "text/plain" || ctype == "text/html") && disp != "attachment" && filename == "" {
		s := emDecodeCharset(data, params["charset"])
		if ctype == "text/html" {
			m.htmls = append(m.htmls, s)
		} else {
			m.texts = append(m.texts, s)
		}
		return
	}
	if len(data) == 0 && filename == "" {
		return
	}
	if filename == "" {
		filename = fmt.Sprintf("attachment-%d%s", len(m.atts)+1, emExt[ctype])
	}
	m.atts = append(m.atts, emAttachment{
		name:   filename,
		ctype:  ctype,
		data:   data,
		cid:    cid,
		inline: cid != "" && disp != "attachment" && strings.HasPrefix(ctype, "image/"),
	})
}

// emDecodeCharset converts body bytes to UTF-8 using the declared charset.
func emDecodeCharset(data []byte, charset string) string {
	charset = strings.TrimSpace(strings.Trim(charset, `"'`))
	lower := strings.ToLower(charset)
	if lower == "" || lower == "utf-8" || lower == "utf8" || lower == "us-ascii" {
		if utf8.Valid(data) {
			return strings.TrimPrefix(string(data), "\uFEFF")
		}
		charset = "windows-1252"
	}
	enc, err := htmlindex.Get(charset)
	if err != nil {
		if utf8.Valid(data) {
			return string(data)
		}
		enc, err = htmlindex.Get("windows-1252")
		if err != nil {
			return strings.ToValidUTF8(string(data), "\uFFFD")
		}
	}
	out, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return strings.ToValidUTF8(string(data), "\uFFFD")
	}
	return strings.TrimPrefix(string(out), "\uFEFF")
}

var emWordDecoder = &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	enc, err := htmlindex.Get(charset)
	if err != nil {
		return nil, err
	}
	return enc.NewDecoder().Reader(input), nil
}}

// emHeader decodes RFC 2047 encoded words and flattens whitespace.
func emHeader(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	d, err := emWordDecoder.DecodeHeader(v)
	if err != nil {
		d = v
	}
	d = strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, d)
	return strings.TrimSpace(d)
}

func (m *emMessage) hdr(key string) string {
	return emHeader(strings.Join(m.header[key], ", "))
}

func (m *emMessage) plainBody() string {
	if len(m.texts) > 0 {
		parts := make([]string, 0, len(m.texts))
		for _, t := range m.texts {
			parts = append(parts, strings.TrimRight(strings.ReplaceAll(t, "\r\n", "\n"), " \t\r\n"))
		}
		return strings.Join(parts, "\n\n")
	}
	if len(m.htmls) > 0 {
		return emHTMLToText(strings.Join(m.htmls, "\n"))
	}
	return ""
}

func emSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---- HTML helpers ----

var (
	emSpaceRe = regexp.MustCompile(`[ \t\r\n\f]+`)
	emBlankRe = regexp.MustCompile(`\n{3,}`)
)

var emBlockAtoms = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Tr: true, atom.Table: true, atom.H1: true, atom.H2: true,
	atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Ul: true, atom.Ol: true,
	atom.Blockquote: true, atom.Pre: true, atom.Hr: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true,
}

// emHTMLToText flattens an HTML body into readable plain text.
func emHTMLToText(s string) string {
	doc, err := xhtml.Parse(strings.NewReader(s))
	if err != nil {
		return strings.TrimSpace(emSpaceRe.ReplaceAllString(s, " "))
	}
	var b strings.Builder
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		switch n.Type {
		case xhtml.TextNode:
			b.WriteString(emSpaceRe.ReplaceAllString(n.Data, " "))
			return
		case xhtml.CommentNode, xhtml.DoctypeNode:
			return
		case xhtml.ElementNode:
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Title, atom.Template, atom.Noscript:
				return
			case atom.Br:
				b.WriteString("\n")
				return
			case atom.Li:
				b.WriteString("\n- ")
			case atom.Td, atom.Th:
				b.WriteString(" ")
			}
			if emBlockAtoms[n.DataAtom] {
				b.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == xhtml.ElementNode && emBlockAtoms[n.DataAtom] {
			b.WriteString("\n")
		}
	}
	walk(doc)
	lines := strings.Split(strings.ReplaceAll(b.String(), "\u00a0", " "), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(emBlankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var (
	emCSSURL    = regexp.MustCompile(`(?i)url\s*\([^)]*\)`)
	emCSSImport = regexp.MustCompile(`(?i)@import[^;]*;?`)
	emCSSEval   = regexp.MustCompile(`(?i)expression\s*\([^)]*\)|behavior\s*:[^;}]*|-moz-binding\s*:[^;}]*`)
)

func emCleanCSS(s string) string {
	s = emCSSImport.ReplaceAllString(s, "")
	s = emCSSEval.ReplaceAllString(s, "")
	return emCSSURL.ReplaceAllString(s, "none")
}

var emDropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Iframe: true, atom.Frame: true, atom.Frameset: true, atom.Object: true,
	atom.Embed: true, atom.Applet: true, atom.Link: true, atom.Meta: true, atom.Base: true,
	atom.Noscript: true, atom.Template: true, atom.Title: true, atom.Svg: true, atom.Math: true,
	atom.Form: true, atom.Input: true, atom.Button: true, atom.Select: true, atom.Textarea: true,
	atom.Audio: true, atom.Video: true, atom.Source: true, atom.Track: true, atom.Picture: true,
	atom.Dialog: true,
}

var emSafeImage = regexp.MustCompile(`(?i)^data:image/(png|jpe?g|gif|webp|bmp);base64,[a-z0-9+/=\s]+$`)

// emResolveSrc keeps only data: images and cid: references to attached
// images (inlined as data URIs). Everything else, including every http(s)
// URL, is dropped so opening the page fetches nothing.
func emResolveSrc(v string, cids map[string]*emAttachment) string {
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "cid:"):
		key := strings.Trim(strings.TrimSpace(v[4:]), "<>")
		a, ok := cids[key]
		if !ok || a.ctype == "image/svg+xml" || !strings.HasPrefix(a.ctype, "image/") {
			return ""
		}
		return "data:" + a.ctype + ";base64," + base64.StdEncoding.EncodeToString(a.data)
	case emSafeImage.MatchString(v):
		return v
	}
	return ""
}

func emLinkOK(v string) bool {
	lower := strings.ToLower(strings.TrimSpace(v))
	for _, p := range []string{"http://", "https://", "mailto:", "tel:", "#"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func emSanitizeAttrs(n *xhtml.Node, cids map[string]*emAttachment) {
	kept := make([]xhtml.Attribute, 0, len(n.Attr))
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if strings.HasPrefix(key, "on") || a.Namespace != "" {
			continue
		}
		switch key {
		case "style":
			a.Val = emCleanCSS(a.Val)
		case "src", "background", "poster":
			v := emResolveSrc(a.Val, cids)
			if v == "" {
				continue
			}
			a.Val = v
		case "srcset", "action", "formaction", "ping", "data", "codebase", "dynsrc", "lowsrc", "manifest", "usemap", "target", "rel":
			continue
		case "href":
			if n.DataAtom != atom.A && n.DataAtom != atom.Area || !emLinkOK(a.Val) {
				continue
			}
		}
		kept = append(kept, a)
	}
	if n.DataAtom == atom.A || n.DataAtom == atom.Area {
		for _, a := range kept {
			if a.Key == "href" {
				kept = append(kept,
					xhtml.Attribute{Key: "rel", Val: "noopener noreferrer nofollow"},
					xhtml.Attribute{Key: "target", Val: "_blank"})
				break
			}
		}
	}
	n.Attr = kept
}

// emSanitizeTree strips active and external content below n, collecting the
// cleaned CSS of any <style> element into styles.
func emSanitizeTree(n *xhtml.Node, cids map[string]*emAttachment, styles *[]string) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case c.Type == xhtml.CommentNode || c.Type == xhtml.DoctypeNode:
			n.RemoveChild(c)
		case c.Type != xhtml.ElementNode:
		case c.DataAtom == atom.Style:
			var css strings.Builder
			for t := c.FirstChild; t != nil; t = t.NextSibling {
				css.WriteString(t.Data)
			}
			*styles = append(*styles, strings.ReplaceAll(emCleanCSS(css.String()), "</", "<\\/"))
			n.RemoveChild(c)
		case emDropTags[c.DataAtom]:
			n.RemoveChild(c)
		default:
			emSanitizeAttrs(c, cids)
			emSanitizeTree(c, cids, styles)
			if c.DataAtom == atom.Img {
				hasSrc := false
				for _, a := range c.Attr {
					if a.Key == "src" {
						hasSrc = true
					}
				}
				if !hasSrc {
					for _, a := range c.Attr {
						if a.Key == "alt" && strings.TrimSpace(a.Val) != "" {
							n.InsertBefore(&xhtml.Node{Type: xhtml.TextNode, Data: "[" + a.Val + "]"}, c)
						}
					}
					n.RemoveChild(c)
				}
			}
		}
		c = next
	}
}

func emFindAtom(n *xhtml.Node, a atom.Atom) *xhtml.Node {
	if n.Type == xhtml.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := emFindAtom(c, a); f != nil {
			return f
		}
	}
	return nil
}

// emSanitizeHTML returns a safe body fragment and the cleaned CSS.
func emSanitizeHTML(src string, atts []emAttachment) (string, string) {
	cids := map[string]*emAttachment{}
	for i := range atts {
		if atts[i].cid != "" {
			cids[atts[i].cid] = &atts[i]
		}
	}
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return "<pre>" + stdhtml.EscapeString(emHTMLToText(src)) + "</pre>", ""
	}
	var styles []string
	// Styles usually sit in <head>; collect them before dropping it.
	if head := emFindAtom(doc, atom.Head); head != nil {
		emSanitizeTree(head, cids, &styles)
	}
	body := emFindAtom(doc, atom.Body)
	if body == nil {
		return "", strings.Join(styles, "\n")
	}
	emSanitizeTree(body, cids, &styles)
	var b bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		_ = xhtml.Render(&b, c)
	}
	return b.String(), strings.Join(styles, "\n")
}

// ---- handlers: single messages ----

func emToText(ctx context.Context, job Job) error {
	raw, err := emReadFile(job)
	if err != nil {
		return err
	}
	m, err := emParse(raw)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, k := range []string{"From", "To", "Cc", "Date", "Subject"} {
		v := m.hdr(k)
		if v == "" && k != "Subject" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}
	b.WriteString("\n")
	body := m.plainBody()
	if body == "" {
		body = "(no text content)"
	}
	b.WriteString(body)
	b.WriteString("\n")
	if len(m.atts) > 0 {
		b.WriteString("\nAttachments:\n")
		for _, a := range m.atts {
			note := ""
			if a.inline {
				note = ", inline"
			}
			fmt.Fprintf(&b, "- %s (%s%s)\n", a.name, emSize(len(a.data)), note)
		}
	}
	return os.WriteFile(job.Output, []byte(b.String()), 0o644)
}

const emPageCSS = `:root{color-scheme:light}
body{margin:0;background:#f4f4f5;color:#18181b;font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:24px 16px}
table.hdr{border-collapse:collapse;width:100%;margin-bottom:16px}
table.hdr th{text-align:left;color:#71717a;font-weight:500;padding:2px 12px 2px 0;white-space:nowrap;vertical-align:top;width:1%}
table.hdr td{padding:2px 0;overflow-wrap:anywhere}
table.hdr tr.subject td{font-size:18px;font-weight:600}
.msg{background:#fff;border:1px solid #e4e4e7;border-radius:8px;padding:16px;overflow:auto}
pre.plain{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
.atts{margin-top:16px;color:#52525b}
.atts ul{margin:4px 0 0;padding-left:20px}
`

func emToHTML(ctx context.Context, job Job) error {
	raw, err := emReadFile(job)
	if err != nil {
		return err
	}
	m, err := emParse(raw)
	if err != nil {
		return err
	}
	esc := stdhtml.EscapeString
	subject := m.hdr("Subject")
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; img-src data:; style-src 'unsafe-inline'\">\n")
	b.WriteString("<meta name=\"referrer\" content=\"no-referrer\">\n")
	title := subject
	if title == "" {
		title = "(no subject)"
	}
	b.WriteString("<title>" + esc(title) + "</title>\n<style>" + emPageCSS + "</style>\n")

	var bodyHTML, css string
	if len(m.htmls) > 0 {
		bodyHTML, css = emSanitizeHTML(strings.Join(m.htmls, "\n<hr>\n"), m.atts)
	}
	if strings.TrimSpace(bodyHTML) == "" {
		bodyHTML = "<pre class=\"plain\">" + esc(m.plainBody()) + "</pre>"
		css = ""
	}
	if css != "" {
		b.WriteString("<style>" + css + "</style>\n")
	}
	b.WriteString("</head>\n<body>\n<div class=\"wrap\">\n<table class=\"hdr\">\n")
	for _, k := range []string{"From", "To", "Cc", "Date", "Subject"} {
		v := m.hdr(k)
		if v == "" && k != "Subject" {
			continue
		}
		class := ""
		if k == "Subject" {
			class = " class=\"subject\""
			if v == "" {
				v = "(no subject)"
			}
		}
		fmt.Fprintf(&b, "<tr%s><th>%s</th><td>%s</td></tr>\n", class, k, esc(v))
	}
	b.WriteString("</table>\n<div class=\"msg\">\n")
	b.WriteString(bodyHTML)
	b.WriteString("\n</div>\n")
	if len(m.atts) > 0 {
		b.WriteString("<div class=\"atts\">Attachments<ul>\n")
		for _, a := range m.atts {
			fmt.Fprintf(&b, "<li>%s (%s)</li>\n", esc(a.name), emSize(len(a.data)))
		}
		b.WriteString("</ul></div>\n")
	}
	b.WriteString("</div>\n</body>\n</html>\n")
	return os.WriteFile(job.Output, []byte(b.String()), 0o644)
}

var emBadNameChars = strings.NewReplacer(":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")

var emReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// emSafeName turns an attachment name into a plain file name: no directories,
// no characters Windows rejects, no control characters, bounded length.
func emSafeName(name string, idx int) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(emBadNameChars.Replace(name), " .")
	if name == "" {
		return fmt.Sprintf("attachment-%d", idx)
	}
	if len(name) > 150 {
		ext := path.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		stem := name[:len(name)-len(ext)]
		for len(stem) > 150-len(ext) {
			_, size := utf8.DecodeLastRuneInString(stem)
			stem = stem[:len(stem)-size]
		}
		name = stem + ext
	}
	stem := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	if emReserved[stem] {
		name = "_" + name
	}
	return name
}

func emExtractAttachments(ctx context.Context, job Job) error {
	raw, err := emReadFile(job)
	if err != nil {
		return err
	}
	m, err := emParse(raw)
	if err != nil {
		return err
	}
	if len(m.atts) == 0 {
		return Userf("this email has no attachments")
	}
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	seen := map[string]int{}
	for i, a := range m.atts {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := uniqueName(seen, emSafeName(a.name, i+1))
		dst, err := safeJoin(job.Output, name)
		if err != nil {
			return Userf("%v", err)
		}
		if err := os.WriteFile(dst, a.data, 0o644); err != nil {
			return err
		}
		job.Progress(float64(i+1) / float64(len(m.atts)))
	}
	return nil
}

// ---- mbox ----

var (
	emFromPrefix  = []byte("From ")
	emEnvelopeRe  = regexp.MustCompile(`^From \S+\s+\w{3}\s+\w{3}\s+\d{1,2}\s+\d\d:\d\d`)
	emEscapedFrom = regexp.MustCompile(`^>+From `)
)

// emEachMbox calls fn with each message of an mbox stream, without its
// "From " separator line, with ">From " quoting undone, and without the blank
// line mbox adds after every message. A "From " line starts a new message
// when it follows a blank line or looks like a real envelope line.
func emEachMbox(ctx context.Context, r io.Reader, fn func(idx int, msg []byte) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var cur bytes.Buffer
	started := false
	prevBlank := true
	count := 0
	flush := func() error {
		if !started {
			return nil
		}
		b := cur.Bytes()
		switch {
		case bytes.HasSuffix(b, []byte("\r\n\r\n")):
			b = b[:len(b)-2]
		case bytes.HasSuffix(b, []byte("\n\n")):
			b = b[:len(b)-1]
		}
		count++
		if count > maxExtractEntries {
			return Userf("mbox has more than %d messages", maxExtractEntries)
		}
		err := fn(count, b)
		cur.Reset()
		return err
	}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			trimmed := bytes.TrimRight(line, "\r\n")
			blank := len(trimmed) == 0
			switch {
			case bytes.HasPrefix(trimmed, emFromPrefix) && (prevBlank || emEnvelopeRe.Match(trimmed)):
				if ferr := flush(); ferr != nil {
					return ferr
				}
				started = true
			case !started && blank:
			default:
				started = true
				if len(line) > 0 && line[0] == '>' && emEscapedFrom.Match(trimmed) {
					line = line[1:]
				}
				cur.Write(line)
				if cur.Len() > maxDataBytes {
					return Userf("a message in this mbox is larger than 512 MB")
				}
			}
			prevBlank = blank
		}
		if err != nil {
			if err == io.EOF {
				return flush()
			}
			return err
		}
	}
}

func emMboxSplit(ctx context.Context, job Job) error {
	f, err := os.Open(job.Input())
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	n := 0
	err = emEachMbox(ctx, f, func(idx int, msg []byte) error {
		n = idx
		return os.WriteFile(filepath.Join(job.Output, fmt.Sprintf("%04d.eml", idx)), msg, 0o644)
	})
	if err == nil && n == 0 {
		err = Userf("no messages found in %s; is it an mbox file?", job.Name(0))
	}
	if err != nil {
		_ = os.RemoveAll(job.Output)
	}
	return err
}

// emCSVCell defuses spreadsheet formulas in untrusted text.
func emCSVCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func emMboxCSV(ctx context.Context, job Job) (err error) {
	f, err := os.Open(job.Input())
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.OpenFile(job.Output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(job.Output)
		}
	}()
	w := csv.NewWriter(out)
	if err = w.Write([]string{"Date", "From", "To", "Subject", "Size", "Attachments"}); err != nil {
		return err
	}
	n := 0
	err = emEachMbox(ctx, f, func(idx int, msg []byte) error {
		n = idx
		row := []string{"", "", "", "", fmt.Sprint(len(msg)), "0"}
		if m, perr := emParse(msg); perr == nil {
			date := m.hdr("Date")
			if t, derr := mail.ParseDate(strings.TrimSpace(strings.Join(m.header["Date"], " "))); derr == nil {
				date = t.Format("2006-01-02 15:04:05 -0700")
			}
			count := 0
			for _, a := range m.atts {
				if !a.inline {
					count++
				}
			}
			row[0], row[1], row[2] = date, emCSVCell(m.hdr("From")), emCSVCell(m.hdr("To"))
			row[3], row[5] = emCSVCell(m.hdr("Subject")), fmt.Sprint(count)
		}
		return w.Write(row)
	})
	if err == nil && n == 0 {
		err = Userf("no messages found in %s; is it an mbox file?", job.Name(0))
	}
	if err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}
