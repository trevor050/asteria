package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"go.yaml.in/yaml/v3"
)

// DataExts are the text formats data.convert reads.
var DataExts = []string{".csv", ".tsv", ".json", ".yaml", ".yml", ".md", ".markdown"}

const maxDataBytes = 512 << 20

func init() {
	register(Handler{Name: "data.convert", Run: dataConvert})
}

func dataKind(ext string) string {
	switch strings.ToLower(ext) {
	case ".csv", ".tsv":
		return "csv"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md", ".markdown":
		return "md"
	case ".html", ".htm":
		return "html"
	}
	return ""
}

func readData(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxDataBytes {
		return nil, Userf("%s is too large to convert in memory (limit 512 MB)", filepath.Base(path))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), nil
}

func dataConvert(_ context.Context, job Job) error {
	inExt := strings.ToLower(filepath.Ext(job.Name(0)))
	if inExt == "" {
		inExt = strings.ToLower(filepath.Ext(job.Input()))
	}
	from, to := dataKind(inExt), dataKind(filepath.Ext(job.Output))
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	var out []byte
	switch {
	case from == "csv" && to == "json":
		out, err = csvToJSON(src, inExt)
	case from == "csv" && to == "md":
		out, err = csvToMarkdown(src, inExt)
	case from == "json" && to == "csv":
		out, err = jsonToCSV(src)
	case from == "json" && to == "yaml":
		out, err = jsonToYAML(src)
	case from == "yaml" && to == "json":
		out, err = yamlToJSON(src)
	case from == "md" && to == "html":
		out, err = markdownToHTML(src, strings.TrimSuffix(job.Name(0), filepath.Ext(job.Name(0))))
	default:
		return Userf("cannot convert %s to %s", inExt, filepath.Ext(job.Output))
	}
	if err != nil {
		return err
	}
	return os.WriteFile(job.Output, out, 0o644)
}

func readCSV(src []byte, ext string) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(src))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.Comma = sniffDelimiter(src, ext)
	rows, err := r.ReadAll()
	if err != nil {
		return nil, Userf("could not parse CSV: %v", err)
	}
	if len(rows) == 0 {
		return nil, Userf("the CSV file is empty")
	}
	return rows, nil
}

// sniffDelimiter picks tab for .tsv and otherwise chooses between comma and
// semicolon (Excel in many locales) by looking at the header line.
func sniffDelimiter(src []byte, ext string) rune {
	if ext == ".tsv" {
		return '\t'
	}
	line := src
	if i := bytes.IndexByte(src, '\n'); i >= 0 {
		line = src[:i]
	}
	if bytes.Count(line, []byte(";")) > bytes.Count(line, []byte(",")) {
		return ';'
	}
	if bytes.Count(line, []byte("\t")) > bytes.Count(line, []byte(",")) {
		return '\t'
	}
	return ','
}

func headerNames(header []string) []string {
	names := make([]string, len(header))
	seen := map[string]int{}
	for i, h := range header {
		h = strings.TrimSpace(h)
		if h == "" {
			h = fmt.Sprintf("column%d", i+1)
		}
		seen[h]++
		if seen[h] > 1 {
			h = fmt.Sprintf("%s_%d", h, seen[h])
		}
		names[i] = h
	}
	return names
}

// csvToJSON emits an array of objects keyed by the header row. Values stay
// strings so IDs like "007" survive the round trip.
func csvToJSON(src []byte, ext string) ([]byte, error) {
	rows, err := readCSV(src, ext)
	if err != nil {
		return nil, err
	}
	names := headerNames(rows[0])
	var buf bytes.Buffer
	buf.WriteString("[")
	for ri, row := range rows[1:] {
		if ri > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  {")
		for i, name := range names {
			if i > 0 {
				buf.WriteString(", ")
			}
			k, _ := json.Marshal(name)
			val := ""
			if i < len(row) {
				val = row[i]
			}
			v, _ := json.Marshal(val)
			buf.Write(k)
			buf.WriteString(": ")
			buf.Write(v)
		}
		buf.WriteString("}")
	}
	if len(rows) > 1 {
		buf.WriteString("\n")
	}
	buf.WriteString("]\n")
	return buf.Bytes(), nil
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r\n", "<br>")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return strings.TrimSpace(s)
}

func csvToMarkdown(src []byte, ext string) ([]byte, error) {
	rows, err := readCSV(src, ext)
	if err != nil {
		return nil, err
	}
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	var buf bytes.Buffer
	writeRow := func(r []string) {
		buf.WriteString("|")
		for i := 0; i < width; i++ {
			cell := ""
			if i < len(r) {
				cell = mdCell(r[i])
			}
			buf.WriteString(" " + cell + " |")
		}
		buf.WriteString("\n")
	}
	writeRow(rows[0])
	buf.WriteString("|")
	for i := 0; i < width; i++ {
		buf.WriteString(" --- |")
	}
	buf.WriteString("\n")
	for _, r := range rows[1:] {
		writeRow(r)
	}
	return buf.Bytes(), nil
}

// orderedValue is a JSON value that remembers object key order.
type orderedValue struct {
	kind   byte // 'o' object, 'a' array, 's' scalar
	keys   []string
	fields map[string]*orderedValue
	items  []*orderedValue
	raw    json.RawMessage
}

func decodeOrdered(dec *json.Decoder) (*orderedValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			v := &orderedValue{kind: 'o', fields: map[string]*orderedValue{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				child, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := v.fields[key]; !dup {
					v.keys = append(v.keys, key)
				}
				v.fields[key] = child
			}
			_, err := dec.Token()
			return v, err
		case '[':
			v := &orderedValue{kind: 'a'}
			for dec.More() {
				child, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				v.items = append(v.items, child)
			}
			_, err := dec.Token()
			return v, err
		}
	}
	raw, err := json.Marshal(tok)
	if err != nil {
		return nil, err
	}
	if n, ok := tok.(json.Number); ok {
		raw = json.RawMessage(n.String())
	}
	return &orderedValue{kind: 's', raw: raw}, nil
}

func parseOrderedJSON(src []byte) (*orderedValue, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	v, err := decodeOrdered(dec)
	if err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			line := 1 + bytes.Count(src[:min(int(se.Offset), len(src))], []byte("\n"))
			return nil, Userf("invalid JSON on line %d: %v", line, se)
		}
		return nil, Userf("invalid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, Userf("invalid JSON: unexpected data after the first value")
	}
	return v, nil
}

func (v *orderedValue) cellString() string {
	switch v.kind {
	case 's':
		var s string
		if json.Unmarshal(v.raw, &s) == nil {
			return s
		}
		if string(v.raw) == "null" {
			return ""
		}
		return string(v.raw)
	default:
		b, _ := v.MarshalJSON()
		return string(b)
	}
}

// MarshalJSON writes compact JSON preserving key order.
func (v *orderedValue) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	switch v.kind {
	case 'o':
		buf.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			cb, err := v.fields[k].MarshalJSON()
			if err != nil {
				return nil, err
			}
			buf.Write(cb)
		}
		buf.WriteByte('}')
	case 'a':
		buf.WriteByte('[')
		for i, it := range v.items {
			if i > 0 {
				buf.WriteByte(',')
			}
			cb, err := it.MarshalJSON()
			if err != nil {
				return nil, err
			}
			buf.Write(cb)
		}
		buf.WriteByte(']')
	default:
		buf.Write(v.raw)
	}
	return buf.Bytes(), nil
}

func jsonToCSV(src []byte) ([]byte, error) {
	root, err := parseOrderedJSON(src)
	if err != nil {
		return nil, err
	}
	var rows []*orderedValue
	switch root.kind {
	case 'a':
		rows = root.items
	case 'o':
		rows = []*orderedValue{root}
	default:
		return nil, Userf("JSON to CSV needs an array of objects")
	}
	cols := []string{}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.kind != 'o' {
			return nil, Userf("JSON to CSV needs an array of objects (found a %s)", kindName(r.kind))
		}
		for _, k := range r.keys {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(cols); err != nil {
		return nil, err
	}
	for _, r := range rows {
		rec := make([]string, len(cols))
		for i, c := range cols {
			if f, ok := r.fields[c]; ok {
				rec[i] = f.cellString()
			}
		}
		if err := w.Write(rec); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func kindName(k byte) string {
	switch k {
	case 'a':
		return "list"
	case 'o':
		return "object"
	}
	return "plain value"
}

func jsonToYAML(src []byte) ([]byte, error) {
	if _, err := parseOrderedJSON(src); err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(src, &node); err != nil {
		return nil, Userf("invalid JSON: %v", err)
	}
	blockStyle(&node)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

// blockStyle converts JSON-flavoured flow nodes into block YAML while keeping
// every scalar's resolved type (a JSON string "123" stays a string).
func blockStyle(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		n.Style = 0
	case yaml.ScalarNode:
		if n.Tag == "!!str" {
			switch {
			case strings.Contains(n.Value, "\n"):
				n.Style = yaml.LiteralStyle
			case plainStringSafe(n.Value):
				n.Style = 0
			default:
				n.Style = yaml.DoubleQuotedStyle
			}
		} else {
			n.Style = 0
		}
	}
	for _, c := range n.Content {
		blockStyle(c)
	}
}

// plainStringSafe reports whether s, written as a plain YAML scalar, reads
// back as the same string (so "123", "true" and "null" get quoted).
func plainStringSafe(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	var probe any
	if err := yaml.Unmarshal([]byte("v: "+s), &probe); err != nil {
		return false
	}
	m, ok := probe.(map[string]any)
	if !ok {
		return false
	}
	got, ok := m["v"].(string)
	return ok && got == s
}

func yamlToJSON(src []byte) ([]byte, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(src, &node); err != nil {
		return nil, Userf("invalid YAML: %v", err)
	}
	if node.Kind == 0 {
		return []byte("null\n"), nil
	}
	var buf bytes.Buffer
	if err := writeYAMLAsJSON(&buf, &node, 0, 0); err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

type yamlPair struct {
	key   string
	value *yaml.Node
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < 64; i++ {
		n = n.Alias
	}
	return n
}

// mappingPairs flattens a mapping in document order, expanding "<<" merge
// keys in place. Explicit keys always win over merged ones.
func mappingPairs(n *yaml.Node, depth int) []yamlPair {
	explicit := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Tag != "!!merge" {
			explicit[n.Content[i].Value] = true
		}
	}
	out := []yamlPair{}
	added := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag != "!!merge" {
			if !added[k.Value] {
				added[k.Value] = true
				out = append(out, yamlPair{k.Value, v})
			} else {
				for j := range out {
					if out[j].key == k.Value {
						out[j].value = v
					}
				}
			}
			continue
		}
		if depth > 32 {
			continue
		}
		sources := []*yaml.Node{resolveAlias(v)}
		if s := sources[0]; s != nil && s.Kind == yaml.SequenceNode {
			sources = sources[:0]
			for _, c := range s.Content {
				sources = append(sources, resolveAlias(c))
			}
		}
		for _, src := range sources {
			if src == nil || src.Kind != yaml.MappingNode {
				continue
			}
			for _, p := range mappingPairs(src, depth+1) {
				if explicit[p.key] || added[p.key] {
					continue
				}
				added[p.key] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func writeYAMLAsJSON(buf *bytes.Buffer, n *yaml.Node, depth, aliasDepth int) error {
	if depth > 10000 || aliasDepth > 64 {
		return Userf("YAML is nested too deeply")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			buf.WriteString("null")
			return nil
		}
		return writeYAMLAsJSON(buf, n.Content[0], depth+1, aliasDepth)
	case yaml.AliasNode:
		return writeYAMLAsJSON(buf, n.Alias, depth+1, aliasDepth+1)
	case yaml.MappingNode:
		buf.WriteByte('{')
		for i, p := range mappingPairs(n, 0) {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(p.key)
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeYAMLAsJSON(buf, p.value, depth+1, aliasDepth); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeYAMLAsJSON(buf, c, depth+1, aliasDepth); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			buf.WriteString("null")
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return err
			}
			buf.WriteString(strconv.FormatBool(b))
		case "!!int", "!!float":
			var f any
			if err := n.Decode(&f); err != nil {
				return err
			}
			b, err := json.Marshal(f)
			if err != nil {
				// NaN/Inf have no JSON form; keep them as strings.
				b, _ = json.Marshal(n.Value)
			}
			buf.Write(b)
		default:
			b, _ := json.Marshal(n.Value)
			buf.Write(b)
		}
	}
	return nil
}

func markdownToHTML(src []byte, title string) ([]byte, error) {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	var body bytes.Buffer
	if err := md.Convert(src, &body); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	buf.WriteString("<title>" + html.EscapeString(title) + "</title>\n")
	buf.WriteString("<style>body{font:16px/1.6 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;color:#1d1d1f}pre,code{font-family:ui-monospace,monospace;background:#f3f3f5;border-radius:4px}pre{padding:1rem;overflow:auto}table{border-collapse:collapse}td,th{border:1px solid #ddd;padding:.3rem .6rem}img{max-width:100%}</style>\n")
	buf.WriteString("</head>\n<body>\n")
	buf.Write(body.Bytes())
	buf.WriteString("</body>\n</html>\n")
	return buf.Bytes(), nil
}
