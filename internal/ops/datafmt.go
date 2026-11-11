package ops

// Data and config formats on one generic value tree.
//
// Every tree format (JSON, JSON Lines, YAML, TOML, XML, INI, .env, plist, CSV)
// is parsed into the same small value model and written back out from it, so
// any reader pairs with any writer. The model is: *dfMap (object, key order
// kept where the source has an order), []any, string, bool, nil, int64,
// float64 and dfNum (a number literal kept verbatim, used by JSON).
//
// XML maps to JSON like this: attributes become "@name", text that sits next
// to attributes or child elements becomes "#text", repeated child elements
// become an array, and an element with only text becomes a plain string.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/parquet-go/parquet-go"
	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
	"howett.net/plist"
	_ "modernc.org/sqlite"
)

func init() {
	register(Handler{Name: "data.tree", Run: dfTreeConvert})
	register(Handler{Name: "data.table", Run: dfTableConvert})
	register(Handler{Name: "data.json", Run: dfJSONFormat})
	register(Handler{Name: "data.xmlFormat", Run: dfXMLFormat})
	register(Handler{Name: "data.sqlite", Run: dfSQLite})
	register(Handler{Name: "data.parquet", Run: dfParquet})
}

// ---------------------------------------------------------------- value model

type dfMap struct {
	keys []string
	vals map[string]any
}

func newDfMap() *dfMap { return &dfMap{vals: map[string]any{}} }

func (m *dfMap) set(k string, v any) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

// dfNum is a number literal kept exactly as written in the source.
type dfNum string

var (
	dfIntRe   = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,17})$`)
	dfFloatRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

const dfMaxDepth = 2000

func dfInputExt(job Job) string {
	e := strings.ToLower(filepath.Ext(job.Name(0)))
	if e == "" {
		e = strings.ToLower(filepath.Ext(job.Input()))
	}
	return e
}

func dfFormat(ext string) string {
	switch strings.ToLower(ext) {
	case ".json":
		return "json"
	case ".jsonl", ".ndjson":
		return "jsonl"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".ini", ".cfg", ".conf":
		return "ini"
	case ".env":
		return "env"
	case ".plist":
		return "plist"
	case ".csv", ".tsv":
		return "csv"
	case ".sqlite", ".sqlite3", ".db":
		return "sqlite"
	case ".parquet":
		return "parquet"
	}
	return ""
}

// dfNormalize converts values produced by third-party decoders into the model.
func dfNormalize(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case string:
		return t
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case uint:
		return dfUint(uint64(t))
	case uint8:
		return int64(t)
	case uint16:
		return int64(t)
	case uint32:
		return int64(t)
	case uint64:
		return dfUint(t)
	case float32:
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(t), 'g', -1, 32), 64)
		return f
	case float64:
		return t
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case []byte:
		return base64.StdEncoding.EncodeToString(t)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := newDfMap()
		for _, k := range keys {
			m.set(k, dfNormalize(t[k]))
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = dfNormalize(x)
		}
		return out
	case *dfMap:
		return t
	case dfNum:
		return t
	case fmt.Stringer:
		return t.String()
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return dfUint(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return fmt.Sprint(v)
}

func dfUint(u uint64) any {
	if u <= math.MaxInt64 {
		return int64(u)
	}
	return dfNum(strconv.FormatUint(u, 10))
}

// dfScalarString renders a scalar as plain text. ok is false for objects and lists.
func dfScalarString(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", true
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case dfNum:
		return string(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return dfFloatText(t), true
	}
	return "", false
}

func dfFloatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func dfKindName(v any) string {
	switch v.(type) {
	case *dfMap:
		return "an object"
	case []any:
		return "a list"
	}
	return "a value"
}

// ---------------------------------------------------------------- dispatch

func dfReadTree(format string, src []byte, ext string) (any, error) {
	switch format {
	case "json":
		return dfParseJSON(src)
	case "jsonl":
		return dfParseJSONL(src)
	case "yaml":
		return dfParseYAML(src)
	case "toml":
		return dfParseTOML(src)
	case "xml":
		doc, err := dfParseXML(src)
		if err != nil {
			return nil, err
		}
		return dfXMLTree(doc), nil
	case "ini":
		return dfParseINI(src)
	case "env":
		return dfParseEnv(src)
	case "plist":
		return dfParsePlist(src)
	case "csv":
		rows, err := readCSV(src, ext)
		if err != nil {
			return nil, err
		}
		names := headerNames(rows[0])
		out := make([]any, 0, len(rows)-1)
		for _, row := range rows[1:] {
			m := newDfMap()
			for i, n := range names {
				val := ""
				if i < len(row) {
					val = row[i]
				}
				m.set(n, val)
			}
			out = append(out, m)
		}
		return out, nil
	}
	return nil, Userf("cannot read %s files", ext)
}

func dfWriteTree(format string, v any, name string) ([]byte, error) {
	switch format {
	case "json":
		return dfJSONBytes(v, true), nil
	case "jsonl":
		return dfWriteJSONL(v), nil
	case "yaml":
		return dfWriteYAML(v)
	case "toml":
		return dfWriteTOML(v)
	case "xml":
		return dfWriteXML(v)
	case "ini":
		return dfWriteINI(v)
	case "env":
		return dfWriteEnv(v)
	case "plist":
		return dfWritePlist(v)
	case "csv":
		return jsonToCSV(dfJSONBytes(v, false))
	}
	return nil, Userf("cannot write %s files", name)
}

func dfTreeConvert(_ context.Context, job Job) error {
	inExt := dfInputExt(job)
	outExt := strings.ToLower(filepath.Ext(job.Output))
	from, to := dfFormat(inExt), dfFormat(outExt)
	if from == "" || to == "" || from == "sqlite" || from == "parquet" || to == "sqlite" || to == "parquet" {
		return Userf("cannot convert %s to %s", inExt, outExt)
	}
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	tree, err := dfReadTree(from, src, inExt)
	if err != nil {
		return err
	}
	out, err := dfWriteTree(to, tree, outExt)
	if err != nil {
		return err
	}
	return os.WriteFile(job.Output, out, 0o644)
}

// ---------------------------------------------------------------- JSON

func dfJSONErr(src []byte, err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		line := 1 + bytes.Count(src[:min(int(se.Offset), len(src))], []byte("\n"))
		return Userf("invalid JSON on line %d: %v", line, se)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return Userf("invalid JSON: the data ends too early")
	}
	return Userf("invalid JSON: %v", err)
}

func dfParseJSON(src []byte) (any, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, Userf("the JSON file is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	v, err := dfDecodeJSON(dec, 0)
	if err != nil {
		return nil, dfJSONErr(src, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, Userf("invalid JSON: unexpected data after the first value")
	}
	return v, nil
}

func dfDecodeJSON(dec *json.Decoder, depth int) (any, error) {
	if depth > dfMaxDepth {
		return nil, Userf("JSON is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := newDfMap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				child, err := dfDecodeJSON(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m.set(key, child)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			arr := []any{}
			for dec.More() {
				child, err := dfDecodeJSON(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, child)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, Userf("invalid JSON: unexpected %q", string(t))
	case json.Number:
		return dfNum(t.String()), nil
	case string:
		return t, nil
	case bool:
		return t, nil
	case nil:
		return nil, nil
	}
	return nil, Userf("invalid JSON")
}

func dfParseJSONL(src []byte) (any, error) {
	out := []any{}
	for i, line := range bytes.Split(src, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		v, err := dfParseJSON(line)
		if err != nil {
			return nil, Userf("line %d: %v", i+1, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func dfJSONString(buf *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
}

func dfWriteJSON(buf *bytes.Buffer, v any) {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case string:
		dfJSONString(buf, t)
	case dfNum:
		buf.WriteString(string(t))
	case int64:
		buf.WriteString(strconv.FormatInt(t, 10))
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			dfJSONString(buf, dfFloatText(t))
		} else {
			b, _ := json.Marshal(t)
			buf.Write(b)
		}
	case *dfMap:
		buf.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			dfJSONString(buf, k)
			buf.WriteByte(':')
			dfWriteJSON(buf, t.vals[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			dfWriteJSON(buf, x)
		}
		buf.WriteByte(']')
	default:
		dfJSONString(buf, fmt.Sprint(t))
	}
}

// dfJSONBytes serialises v as JSON with a trailing newline, pretty-printed
// with two spaces or compact.
func dfJSONBytes(v any, pretty bool) []byte {
	var buf bytes.Buffer
	dfWriteJSON(&buf, v)
	if pretty {
		var out bytes.Buffer
		if err := json.Indent(&out, buf.Bytes(), "", "  "); err == nil {
			buf = out
		}
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

func dfWriteJSONL(v any) []byte {
	var buf bytes.Buffer
	if arr, ok := v.([]any); ok {
		for _, it := range arr {
			dfWriteJSON(&buf, it)
			buf.WriteByte('\n')
		}
		return buf.Bytes()
	}
	dfWriteJSON(&buf, v)
	buf.WriteByte('\n')
	return buf.Bytes()
}

// dfJSONFormat backs format_json (args mode=pretty) and minify_json (mode=minify).
func dfJSONFormat(_ context.Context, job Job) error {
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	v, err := dfParseJSON(src)
	if err != nil {
		return err
	}
	pretty := job.Args.String("mode", "pretty") != "minify"
	return os.WriteFile(job.Output, dfJSONBytes(v, pretty), 0o644)
}

// ---------------------------------------------------------------- YAML

func dfParseYAML(src []byte) (any, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(src, &node); err != nil {
		return nil, Userf("invalid YAML: %v", err)
	}
	if node.Kind == 0 {
		return nil, nil
	}
	return dfYAMLValue(&node, 0)
}

func dfYAMLValue(n *yaml.Node, depth int) (any, error) {
	if n == nil {
		return nil, nil
	}
	if depth > dfMaxDepth {
		return nil, Userf("YAML is nested too deeply")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return dfYAMLValue(n.Content[0], depth+1)
	case yaml.AliasNode:
		return dfYAMLValue(n.Alias, depth+1)
	case yaml.MappingNode:
		m := newDfMap()
		for _, p := range mappingPairs(n, 0) {
			v, err := dfYAMLValue(p.value, depth+1)
			if err != nil {
				return nil, err
			}
			m.set(p.key, v)
		}
		return m, nil
	case yaml.SequenceNode:
		arr := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := dfYAMLValue(c, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return arr, nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return nil, nil
		case "!!bool", "!!int", "!!float":
			var f any
			if err := n.Decode(&f); err != nil {
				return n.Value, nil
			}
			return dfNormalize(f), nil
		}
		return n.Value, nil
	}
	return nil, nil
}

func dfYAMLNode(v any) *yaml.Node {
	switch t := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(t, 10)}
	case dfNum:
		tag := "!!float"
		if dfIntRe.MatchString(string(t)) {
			tag = "!!int"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(t)}
	case float64:
		switch {
		case math.IsNaN(t):
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: ".nan"}
		case math.IsInf(t, 1):
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: ".inf"}
		case math.IsInf(t, -1):
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: "-.inf"}
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(t, 'g', -1, 64)}
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}
	case *dfMap:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range t.keys {
			n.Content = append(n.Content, dfYAMLNode(k), dfYAMLNode(t.vals[k]))
		}
		return n
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, x := range t {
			n.Content = append(n.Content, dfYAMLNode(x))
		}
		return n
	}
	return dfYAMLNode(fmt.Sprint(v))
}

func dfWriteYAML(v any) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{dfYAMLNode(v)}}
	blockStyle(doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------- TOML

func dfParseTOML(src []byte) (any, error) {
	var m map[string]any
	if err := toml.Unmarshal(src, &m); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, Userf("invalid TOML on line %d, column %d: %s", row, col, de.Error())
		}
		return nil, Userf("invalid TOML: %v", err)
	}
	return dfNormalize(m), nil
}

func dfTOMLNative(v any, path string) (any, error) {
	where := path
	if where == "" {
		where = "the top level"
	}
	switch t := v.(type) {
	case nil:
		return nil, Userf("TOML cannot store null (found at %s)", where)
	case bool, string, int64, float64:
		return t, nil
	case dfNum:
		s := string(t)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, Userf("TOML cannot store the number %s (at %s)", s, where)
		}
		return f, nil
	case *dfMap:
		out := make(map[string]any, len(t.keys))
		for _, k := range t.keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			c, err := dfTOMLNative(t.vals[k], p)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			c, err := dfTOMLNative(x, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return nil, Userf("TOML cannot store this value (at %s)", where)
}

func dfWriteTOML(v any) ([]byte, error) {
	if _, ok := v.(*dfMap); !ok {
		return nil, Userf("TOML needs an object at the top level, but this file holds %s. Wrap it in a key first", dfKindName(v))
	}
	native, err := dfTOMLNative(v, "")
	if err != nil {
		return nil, err
	}
	out, err := toml.Marshal(native)
	if err != nil {
		return nil, Userf("could not write TOML: %v", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- XML

type dfXAttr struct{ name, value string }

type dfXItem struct {
	kind  byte // 't' text, 'e' element, 'c' comment, 'p' processing instruction, 'd' directive
	text  string
	extra string
	el    *dfXNode
}

type dfXNode struct {
	name    string
	attrs   []dfXAttr
	content []dfXItem
}

func dfQName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func dfCharset(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		for _, c := range b {
			out.WriteRune(rune(c))
		}
		return &out, nil
	}
	return nil, Userf("XML encoding %q is not supported (use UTF-8)", charset)
}

func dfParseXML(src []byte) (*dfXNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(src))
	dec.Strict = true
	dec.CharsetReader = dfCharset
	doc := &dfXNode{}
	stack := []*dfXNode{doc}
	roots := 0
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var se *xml.SyntaxError
			if errors.As(err, &se) {
				return nil, Userf("invalid XML on line %d: %s", se.Line, se.Msg)
			}
			var ue *UserError
			if errors.As(err, &ue) {
				return nil, ue
			}
			return nil, Userf("invalid XML: %v", err)
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > dfMaxDepth {
				return nil, Userf("XML is nested too deeply")
			}
			n := &dfXNode{name: dfQName(t.Name)}
			for _, a := range t.Attr {
				n.attrs = append(n.attrs, dfXAttr{dfQName(a.Name), a.Value})
			}
			if len(stack) == 1 {
				roots++
				if roots > 1 {
					return nil, Userf("invalid XML: more than one root element")
				}
			}
			top.content = append(top.content, dfXItem{kind: 'e', el: n})
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) <= 1 || top.name != dfQName(t.Name) {
				return nil, Userf("invalid XML: </%s> does not match an open element", dfQName(t.Name))
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if n := len(top.content); n > 0 && top.content[n-1].kind == 't' {
				top.content[n-1].text += string(t)
			} else {
				top.content = append(top.content, dfXItem{kind: 't', text: string(t)})
			}
		case xml.Comment:
			top.content = append(top.content, dfXItem{kind: 'c', text: string(t)})
		case xml.ProcInst:
			top.content = append(top.content, dfXItem{kind: 'p', text: t.Target, extra: string(t.Inst)})
		case xml.Directive:
			top.content = append(top.content, dfXItem{kind: 'd', text: string(t)})
		}
	}
	if len(stack) != 1 {
		return nil, Userf("invalid XML: <%s> is never closed", stack[len(stack)-1].name)
	}
	if roots == 0 {
		return nil, Userf("invalid XML: no root element")
	}
	return doc, nil
}

func (n *dfXNode) text() string {
	var sb strings.Builder
	for _, it := range n.content {
		if it.kind == 't' {
			sb.WriteString(it.text)
		}
	}
	return sb.String()
}

func (n *dfXNode) elements() []*dfXNode {
	var out []*dfXNode
	for _, it := range n.content {
		if it.kind == 'e' {
			out = append(out, it.el)
		}
	}
	return out
}

func dfXMLTree(doc *dfXNode) any {
	kids := doc.elements()
	m := newDfMap()
	m.set(kids[0].name, dfXMLElementTree(kids[0]))
	return m
}

func dfXMLElementTree(n *dfXNode) any {
	kids := n.elements()
	text := strings.TrimSpace(n.text())
	if len(n.attrs) == 0 && len(kids) == 0 {
		return text
	}
	m := newDfMap()
	for _, a := range n.attrs {
		m.set("@"+a.name, a.value)
	}
	if text != "" {
		m.set("#text", text)
	}
	for _, k := range kids {
		child := dfXMLElementTree(k)
		existing, ok := m.vals[k.name]
		if !ok {
			m.set(k.name, child)
			continue
		}
		if arr, isArr := existing.([]any); isArr {
			m.vals[k.name] = append(arr, child)
		} else {
			m.vals[k.name] = []any{existing, child}
		}
	}
	return m
}

func dfXMLName(s string) string {
	var sb strings.Builder
	for i, r := range s {
		ok := unicode.IsLetter(r) || r == '_' || r == ':'
		if i > 0 {
			ok = ok || unicode.IsDigit(r) || r == '-' || r == '.'
		}
		if ok {
			sb.WriteRune(r)
		} else if i == 0 && (unicode.IsDigit(r) || r == '-' || r == '.') {
			sb.WriteRune('_')
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	if sb.Len() == 0 {
		return "_"
	}
	return sb.String()
}

func dfXMLEscText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;")
	return r.Replace(s)
}

func dfXMLEscAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", "&#xA;", "\r", "&#xD;", "\t", "&#x9;")
	return r.Replace(s)
}

func dfWriteXML(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	rootName := "root"
	if m, ok := v.(*dfMap); ok && len(m.keys) == 1 {
		k := m.keys[0]
		if _, isArr := m.vals[k].([]any); !isArr && !strings.HasPrefix(k, "@") && k != "#text" {
			rootName = dfXMLName(k)
			v = m.vals[k]
		}
	}
	if err := dfXMLElem(&buf, rootName, v, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func dfXMLElem(buf *bytes.Buffer, name string, v any, depth int) error {
	pad := strings.Repeat("  ", depth)
	switch t := v.(type) {
	case *dfMap:
		var attrs, children []string
		text := ""
		for _, k := range t.keys {
			switch {
			case strings.HasPrefix(k, "@") && len(k) > 1:
				s, ok := dfScalarString(t.vals[k])
				if !ok {
					return Userf("XML attribute %q must hold a plain value", k)
				}
				attrs = append(attrs, fmt.Sprintf(` %s="%s"`, dfXMLName(k[1:]), dfXMLEscAttr(s)))
			case k == "#text":
				s, ok := dfScalarString(t.vals[k])
				if !ok {
					return Userf("XML #text must hold a plain value")
				}
				text = s
			default:
				children = append(children, k)
			}
		}
		open := pad + "<" + name + strings.Join(attrs, "")
		if len(children) == 0 {
			if text == "" {
				buf.WriteString(open + "/>\n")
			} else {
				buf.WriteString(open + ">" + dfXMLEscText(text) + "</" + name + ">\n")
			}
			return nil
		}
		buf.WriteString(open + ">\n")
		if text != "" {
			buf.WriteString(pad + "  " + dfXMLEscText(text) + "\n")
		}
		for _, k := range children {
			cn := dfXMLName(k)
			if arr, ok := t.vals[k].([]any); ok {
				for _, it := range arr {
					if err := dfXMLElem(buf, cn, it, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if err := dfXMLElem(buf, cn, t.vals[k], depth+1); err != nil {
				return err
			}
		}
		buf.WriteString(pad + "</" + name + ">\n")
	case []any:
		if len(t) == 0 {
			buf.WriteString(pad + "<" + name + "/>\n")
			return nil
		}
		buf.WriteString(pad + "<" + name + ">\n")
		for _, it := range t {
			if err := dfXMLElem(buf, "item", it, depth+1); err != nil {
				return err
			}
		}
		buf.WriteString(pad + "</" + name + ">\n")
	default:
		s, _ := dfScalarString(v)
		if v == nil || s == "" {
			buf.WriteString(pad + "<" + name + "/>\n")
			return nil
		}
		buf.WriteString(pad + "<" + name + ">" + dfXMLEscText(s) + "</" + name + ">\n")
	}
	return nil
}

var dfXMLEncodingRe = regexp.MustCompile(`encoding\s*=\s*("[^"]*"|'[^']*')`)

// dfXMLFormat re-indents an XML document without changing its content.
func dfXMLFormat(_ context.Context, job Job) error {
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	doc, err := dfParseXML(src)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, it := range doc.content {
		switch it.kind {
		case 'p':
			inst := it.extra
			if strings.EqualFold(it.text, "xml") {
				inst = dfXMLEncodingRe.ReplaceAllString(inst, `encoding="UTF-8"`)
			}
			buf.WriteString("<?" + strings.TrimSpace(it.text+" "+inst) + "?>\n")
		case 'c':
			buf.WriteString("<!--" + it.text + "-->\n")
		case 'd':
			buf.WriteString("<!" + it.text + ">\n")
		case 'e':
			dfXMLPrint(&buf, it.el, 0)
		}
	}
	return os.WriteFile(job.Output, buf.Bytes(), 0o644)
}

func dfXMLPrint(buf *bytes.Buffer, n *dfXNode, depth int) {
	pad := strings.Repeat("  ", depth)
	var open strings.Builder
	open.WriteString(pad + "<" + n.name)
	for _, a := range n.attrs {
		open.WriteString(fmt.Sprintf(` %s="%s"`, a.name, dfXMLEscAttr(a.value)))
	}
	structural := false
	for _, it := range n.content {
		if it.kind != 't' {
			structural = true
			break
		}
	}
	if !structural {
		text := n.text()
		if strings.TrimSpace(text) == "" {
			buf.WriteString(open.String() + "/>\n")
		} else {
			buf.WriteString(open.String() + ">" + dfXMLEscText(text) + "</" + n.name + ">\n")
		}
		return
	}
	buf.WriteString(open.String() + ">\n")
	for _, it := range n.content {
		switch it.kind {
		case 't':
			if s := strings.TrimSpace(it.text); s != "" {
				buf.WriteString(pad + "  " + dfXMLEscText(s) + "\n")
			}
		case 'e':
			dfXMLPrint(buf, it.el, depth+1)
		case 'c':
			buf.WriteString(pad + "  <!--" + it.text + "-->\n")
		case 'p':
			buf.WriteString(pad + "  <?" + strings.TrimSpace(it.text+" "+it.extra) + "?>\n")
		}
	}
	buf.WriteString(pad + "</" + n.name + ">\n")
}

// ---------------------------------------------------------------- INI

func dfUnescape(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case '$':
				sb.WriteByte('$')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(s[i])
			}
			continue
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

func dfEscapeQuoted(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "$", `\$`)
	return r.Replace(s)
}

func dfParseINI(src []byte) (any, error) {
	root := newDfMap()
	cur := root
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, Userf("invalid INI on line %d: section header is missing ]", i+1)
			}
			name := strings.TrimSpace(line[1:end])
			if existing, ok := root.vals[name].(*dfMap); ok {
				cur = existing
			} else {
				cur = newDfMap()
				root.set(name, cur)
			}
			continue
		}
		cut := strings.IndexAny(line, "=:")
		key, val := line, ""
		if cut >= 0 {
			key, val = strings.TrimSpace(line[:cut]), strings.TrimSpace(line[cut+1:])
		}
		if key == "" {
			return nil, Userf("invalid INI on line %d: missing key", i+1)
		}
		if n := len(val); n >= 2 && val[0] == '"' && val[n-1] == '"' {
			val = dfUnescape(val[1 : n-1])
		} else if n >= 2 && val[0] == '\'' && val[n-1] == '\'' {
			val = val[1 : n-1]
		}
		cur.set(key, val)
	}
	return root, nil
}

func dfINIValue(v any, where string) (string, error) {
	s, ok := dfScalarString(v)
	if !ok {
		return "", Userf("INI cannot hold %s (at %s). INI supports one level of sections with plain values", dfKindName(v), where)
	}
	if s != strings.TrimSpace(s) || strings.ContainsAny(s, "\n\r") || strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'") {
		s = `"` + dfEscapeQuoted(s) + `"`
		s = strings.ReplaceAll(s, `\$`, "$")
	}
	return s, nil
}

func dfWriteINI(v any) ([]byte, error) {
	root, ok := v.(*dfMap)
	if !ok {
		return nil, Userf("INI needs an object at the top level, but this file holds %s", dfKindName(v))
	}
	var buf bytes.Buffer
	for _, k := range root.keys {
		if _, isMap := root.vals[k].(*dfMap); isMap {
			continue
		}
		val, err := dfINIValue(root.vals[k], k)
		if err != nil {
			return nil, err
		}
		buf.WriteString(k + " = " + val + "\n")
	}
	for _, k := range root.keys {
		sec, isMap := root.vals[k].(*dfMap)
		if !isMap {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString("[" + k + "]\n")
		for _, sk := range sec.keys {
			val, err := dfINIValue(sec.vals[sk], k+"."+sk)
			if err != nil {
				return nil, err
			}
			buf.WriteString(sk + " = " + val + "\n")
		}
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------- .env

// dfEnvQuoted scans a quoted value starting right after the opening quote.
func dfEnvQuoted(s string, quote byte) (string, bool) {
	if quote == '\'' {
		if i := strings.IndexByte(s, '\''); i >= 0 {
			return s[:i], true
		}
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			return dfUnescape(s[:i]), true
		}
	}
	return "", false
}

func dfParseEnv(src []byte) (any, error) {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	m := newDfMap()
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[len("export"):])
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, Userf("invalid .env on line %d: expected KEY=value", i+1)
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			return nil, Userf("invalid .env on line %d: missing key", i+1)
		}
		rest := strings.TrimLeft(line[eq+1:], " \t")
		var val string
		if rest != "" && (rest[0] == '"' || rest[0] == '\'') {
			quote := rest[0]
			body := rest[1:]
			j := i
			for {
				v, ok := dfEnvQuoted(body, quote)
				if ok {
					val = v
					break
				}
				j++
				if j >= len(lines) {
					return nil, Userf("invalid .env on line %d: the quoted value is never closed", i+1)
				}
				body += "\n" + lines[j]
			}
			i = j
		} else {
			if c := strings.Index(rest, " #"); c >= 0 {
				rest = rest[:c]
			} else if c := strings.Index(rest, "\t#"); c >= 0 {
				rest = rest[:c]
			}
			val = strings.TrimSpace(rest)
		}
		m.set(key, val)
	}
	return m, nil
}

var dfEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

func dfEnvValue(s string) string {
	if s == "" {
		return ""
	}
	special := strings.ContainsAny(s, " \t\n\r\"'#$\\`") || s != strings.TrimSpace(s)
	if !special {
		return s
	}
	if !strings.ContainsAny(s, "'\n\r") {
		return "'" + s + "'"
	}
	return `"` + dfEscapeQuoted(s) + `"`
}

func dfWriteEnv(v any) ([]byte, error) {
	root, ok := v.(*dfMap)
	if !ok {
		return nil, Userf(".env needs a flat object at the top level, but this file holds %s", dfKindName(v))
	}
	var buf bytes.Buffer
	for _, k := range root.keys {
		if !dfEnvKeyRe.MatchString(k) {
			return nil, Userf(".env keys use letters, digits and _ only (found %q)", k)
		}
		s, ok := dfScalarString(root.vals[k])
		if !ok {
			return nil, Userf(".env needs flat key=value pairs, but %q holds %s", k, dfKindName(root.vals[k]))
		}
		buf.WriteString(k + "=" + dfEnvValue(s) + "\n")
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------- plist

func dfParsePlist(src []byte) (any, error) {
	if bytes.HasPrefix(src, []byte("bplist")) {
		var v any
		if _, err := plist.Unmarshal(src, &v); err != nil {
			return nil, Userf("could not read the binary plist: %v", err)
		}
		return dfNormalize(v), nil
	}
	doc, err := dfParseXML(src)
	if err != nil {
		return nil, err
	}
	root := doc.elements()[0]
	if root.name != "plist" {
		return nil, Userf("this is not a property list (expected <plist>, found <%s>)", root.name)
	}
	kids := root.elements()
	if len(kids) == 0 {
		return nil, nil
	}
	return dfPlistValue(kids[0], 0)
}

func dfPlistValue(n *dfXNode, depth int) (any, error) {
	if depth > dfMaxDepth {
		return nil, Userf("plist is nested too deeply")
	}
	switch n.name {
	case "dict":
		kids := n.elements()
		if len(kids)%2 != 0 {
			return nil, Userf("invalid plist: a <dict> has a key without a value")
		}
		m := newDfMap()
		for i := 0; i < len(kids); i += 2 {
			if kids[i].name != "key" {
				return nil, Userf("invalid plist: expected <key> in a <dict>, found <%s>", kids[i].name)
			}
			v, err := dfPlistValue(kids[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			m.set(kids[i].text(), v)
		}
		return m, nil
	case "array":
		arr := []any{}
		for _, k := range n.elements() {
			v, err := dfPlistValue(k, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return arr, nil
	case "string":
		return n.text(), nil
	case "integer":
		s := strings.TrimSpace(n.text())
		var i int64
		var err error
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "-0x") {
			i, err = strconv.ParseInt(s, 0, 64)
		} else {
			i, err = strconv.ParseInt(s, 10, 64)
		}
		if err != nil {
			if u, uerr := strconv.ParseUint(s, 10, 64); uerr == nil {
				return dfUint(u), nil
			}
			return nil, Userf("invalid plist integer %q", s)
		}
		return i, nil
	case "real":
		s := strings.TrimSpace(n.text())
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, Userf("invalid plist real %q", s)
		}
		return f, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "date":
		return strings.TrimSpace(n.text()), nil
	case "data":
		return strings.Join(strings.Fields(n.text()), ""), nil
	}
	return nil, Userf("unsupported plist element <%s>", n.name)
}

func dfWritePlist(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0">` + "\n")
	if err := dfPlistElem(&buf, v, 0, ""); err != nil {
		return nil, err
	}
	buf.WriteString("</plist>\n")
	return buf.Bytes(), nil
}

func dfPlistElem(buf *bytes.Buffer, v any, depth int, path string) error {
	pad := strings.Repeat("\t", depth)
	switch t := v.(type) {
	case nil:
		return Userf("a property list cannot store null (found at %s)", dfPathName(path))
	case bool:
		if t {
			buf.WriteString(pad + "<true/>\n")
		} else {
			buf.WriteString(pad + "<false/>\n")
		}
	case string:
		buf.WriteString(pad + "<string>" + dfXMLEscText(t) + "</string>\n")
	case int64:
		buf.WriteString(pad + "<integer>" + strconv.FormatInt(t, 10) + "</integer>\n")
	case dfNum:
		if dfIntRe.MatchString(string(t)) {
			buf.WriteString(pad + "<integer>" + string(t) + "</integer>\n")
		} else {
			buf.WriteString(pad + "<real>" + string(t) + "</real>\n")
		}
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return Userf("a property list cannot store NaN or infinity (at %s)", dfPathName(path))
		}
		buf.WriteString(pad + "<real>" + strconv.FormatFloat(t, 'g', -1, 64) + "</real>\n")
	case *dfMap:
		if len(t.keys) == 0 {
			buf.WriteString(pad + "<dict/>\n")
			return nil
		}
		buf.WriteString(pad + "<dict>\n")
		for _, k := range t.keys {
			buf.WriteString(pad + "\t<key>" + dfXMLEscText(k) + "</key>\n")
			if err := dfPlistElem(buf, t.vals[k], depth+1, path+"."+k); err != nil {
				return err
			}
		}
		buf.WriteString(pad + "</dict>\n")
	case []any:
		if len(t) == 0 {
			buf.WriteString(pad + "<array/>\n")
			return nil
		}
		buf.WriteString(pad + "<array>\n")
		for i, x := range t {
			if err := dfPlistElem(buf, x, depth+1, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		buf.WriteString(pad + "</array>\n")
	}
	return nil
}

func dfPathName(p string) string {
	if p == "" {
		return "the top level"
	}
	return strings.TrimPrefix(p, ".")
}

// ---------------------------------------------------------------- CSV / TSV

func dfTableConvert(_ context.Context, job Job) error {
	inExt := dfInputExt(job)
	outExt := strings.ToLower(filepath.Ext(job.Output))
	if dfFormat(inExt) != "csv" || dfFormat(outExt) != "csv" {
		return Userf("cannot convert %s to %s", inExt, outExt)
	}
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	rows, err := readCSV(src, inExt)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if outExt == ".tsv" {
		w.Comma = '\t'
	}
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	return os.WriteFile(job.Output, buf.Bytes(), 0o644)
}

// ---------------------------------------------------------------- SQLite

func dfQuoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func dfSQLiteURI(path, query string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := (&url.URL{Scheme: "file", Path: p}).String()
	if query != "" {
		u += "?" + query
	}
	return u
}

func dfOpenSQLiteRO(path string) (*sql.DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 16)
	_, _ = io.ReadFull(f, head)
	f.Close()
	if string(head) != "SQLite format 3\x00" {
		return nil, Userf("%s is not a SQLite database", filepath.Base(path))
	}
	db, err := sql.Open("sqlite", dfSQLiteURI(path, "mode=ro&immutable=1"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func dfSQLiteTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, Userf("could not read the database: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func dfSQLiteValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(t) {
			return string(t)
		}
		return base64.StdEncoding.EncodeToString(t)
	}
	return dfNormalize(v)
}

func dfSQLiteEachRow(ctx context.Context, db *sql.DB, table string, fn func(cols []string, vals []any) error) error {
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+dfQuoteIdent(table))
	if err != nil {
		return Userf("could not read table %q: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i := range vals {
			vals[i] = dfSQLiteValue(vals[i])
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(cols, vals); err != nil {
			return err
		}
	}
	return rows.Err()
}

func dfSQLite(ctx context.Context, job Job) error {
	inExt := dfInputExt(job)
	outExt := strings.ToLower(filepath.Ext(job.Output))
	switch dfFormat(inExt) {
	case "sqlite":
		if outExt == ".json" {
			return dfSQLiteToJSON(ctx, job)
		}
		return dfSQLiteToCSV(ctx, job)
	case "csv":
		return dfCSVToSQLite(ctx, job, inExt)
	case "json", "jsonl":
		return dfJSONToSQLite(ctx, job, dfFormat(inExt))
	}
	return Userf("cannot convert %s to %s", inExt, outExt)
}

func dfSQLiteToJSON(ctx context.Context, job Job) error {
	db, err := dfOpenSQLiteRO(job.Input())
	if err != nil {
		return err
	}
	defer db.Close()
	tables, err := dfSQLiteTables(ctx, db)
	if err != nil {
		return err
	}
	root := newDfMap()
	for _, t := range tables {
		list := []any{}
		err := dfSQLiteEachRow(ctx, db, t, func(cols []string, vals []any) error {
			row := newDfMap()
			for i, c := range cols {
				row.set(c, vals[i])
			}
			list = append(list, row)
			return nil
		})
		if err != nil {
			return err
		}
		root.set(t, list)
	}
	return os.WriteFile(job.Output, dfJSONBytes(root, true), 0o644)
}

func dfSafeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, ". ")
	if s == "" {
		s = "table"
	}
	return s
}

func dfSQLiteToCSV(ctx context.Context, job Job) (err error) {
	db, err := dfOpenSQLiteRO(job.Input())
	if err != nil {
		return err
	}
	defer db.Close()
	tables, err := dfSQLiteTables(ctx, db)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return Userf("the database has no tables")
	}
	if err := os.MkdirAll(job.Output, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(job.Output)
		}
	}()
	used := map[string]bool{}
	for _, t := range tables {
		base := dfSafeFileName(t)
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		used[strings.ToLower(name)] = true
		f, ferr := os.Create(filepath.Join(job.Output, name+".csv"))
		if ferr != nil {
			return ferr
		}
		w := csv.NewWriter(f)
		wroteHeader := false
		err = dfSQLiteEachRow(ctx, db, t, func(cols []string, vals []any) error {
			if !wroteHeader {
				wroteHeader = true
				if err := w.Write(cols); err != nil {
					return err
				}
			}
			rec := make([]string, len(vals))
			for i, v := range vals {
				rec[i], _ = dfScalarString(v)
			}
			return w.Write(rec)
		})
		if err == nil && !wroteHeader {
			// An empty table still gets its header row.
			var cols []string
			cols, err = dfSQLiteColumns(ctx, db, t)
			if err == nil {
				err = w.Write(cols)
			}
		}
		w.Flush()
		if err == nil {
			err = w.Error()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func dfSQLiteColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+dfQuoteIdent(table)+" LIMIT 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rows.Columns()
}

// dfTableName turns a file name into a safe SQL table name.
func dfTableName(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	var sb strings.Builder
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	s := strings.Trim(sb.String(), "_")
	if s == "" {
		return "data"
	}
	if r, _ := utf8.DecodeRuneInString(s); unicode.IsDigit(r) {
		s = "t_" + s
	}
	return s
}

// dfColumnNames makes names non-empty and unique, ignoring case like SQLite.
func dfColumnNames(in []string) []string {
	out := make([]string, len(in))
	seen := map[string]bool{}
	for i, n := range in {
		n = strings.TrimSpace(n)
		if n == "" {
			n = fmt.Sprintf("column%d", i+1)
		}
		cand := n
		for k := 2; seen[strings.ToLower(cand)]; k++ {
			cand = fmt.Sprintf("%s_%d", n, k)
		}
		seen[strings.ToLower(cand)] = true
		out[i] = cand
	}
	return out
}

func dfSQLiteWrite(ctx context.Context, path, table string, cols, types []string, rows [][]any) error {
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", dfSQLiteURI(path, ""))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) error {
		db.Close()
		_ = os.Remove(path)
		return err
	}
	defs := make([]string, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		defs[i] = dfQuoteIdent(c) + " " + types[i]
		marks[i] = "?"
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+dfQuoteIdent(table)+" ("+strings.Join(defs, ", ")+")"); err != nil {
		return fail(Userf("could not create the table: %v", err))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+dfQuoteIdent(table)+" VALUES ("+strings.Join(marks, ",")+")")
	if err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r...); err != nil {
			stmt.Close()
			_ = tx.Rollback()
			return fail(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	if err := db.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func dfCSVToSQLite(ctx context.Context, job Job, ext string) error {
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	records, err := readCSV(src, ext)
	if err != nil {
		return err
	}
	cols := dfColumnNames(headerNames(records[0]))
	body := records[1:]
	types := make([]string, len(cols))
	for ci := range cols {
		isInt, isFloat, hasVal := true, true, false
		for _, r := range body {
			if ci >= len(r) || r[ci] == "" {
				continue
			}
			hasVal = true
			if !dfIntRe.MatchString(r[ci]) {
				isInt = false
			}
			if !dfFloatRe.MatchString(r[ci]) {
				isFloat = false
			}
			if !isInt && !isFloat {
				break
			}
		}
		switch {
		case hasVal && isInt:
			types[ci] = "INTEGER"
		case hasVal && isFloat:
			types[ci] = "REAL"
		default:
			types[ci] = "TEXT"
		}
	}
	rows := make([][]any, len(body))
	for ri, r := range body {
		row := make([]any, len(cols))
		for ci := range cols {
			if ci >= len(r) {
				continue
			}
			s := r[ci]
			switch types[ci] {
			case "INTEGER":
				if s != "" {
					n, _ := strconv.ParseInt(s, 10, 64)
					row[ci] = n
				}
			case "REAL":
				if s != "" {
					f, _ := strconv.ParseFloat(s, 64)
					row[ci] = f
				}
			default:
				row[ci] = s
			}
		}
		rows[ri] = row
	}
	return dfSQLiteWrite(ctx, job.Output, dfTableName(job.Name(0)), cols, types, rows)
}

func dfJSONToSQLite(ctx context.Context, job Job, format string) error {
	src, err := readData(job.Input())
	if err != nil {
		return err
	}
	tree, err := dfReadTree(format, src, "")
	if err != nil {
		return err
	}
	var items []any
	switch t := tree.(type) {
	case []any:
		items = t
	case *dfMap:
		items = []any{t}
	default:
		return Userf("JSON to SQLite needs a list of objects")
	}
	var names []string
	seen := map[string]bool{}
	for _, it := range items {
		m, ok := it.(*dfMap)
		if !ok {
			return Userf("JSON to SQLite needs a list of objects (found %s)", dfKindName(it))
		}
		for _, k := range m.keys {
			if !seen[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
	}
	if len(names) == 0 {
		return Userf("there are no fields to store in a table")
	}
	cols := dfColumnNames(names)
	types := make([]string, len(cols))
	for ci, k := range names {
		var nInt, nFloat, nBool, nText int
		for _, it := range items {
			switch t := it.(*dfMap).vals[k].(type) {
			case nil:
			case bool:
				nBool++
			case int64:
				nInt++
			case float64:
				nFloat++
			case dfNum:
				if dfIntRe.MatchString(string(t)) {
					nInt++
				} else {
					nFloat++
				}
			default:
				nText++
			}
		}
		switch {
		case nText > 0 || (nBool > 0 && nInt+nFloat > 0):
			types[ci] = "TEXT"
		case nFloat > 0:
			types[ci] = "REAL"
		case nInt > 0 || nBool > 0:
			types[ci] = "INTEGER"
		default:
			types[ci] = "TEXT"
		}
	}
	rows := make([][]any, len(items))
	for ri, it := range items {
		m := it.(*dfMap)
		row := make([]any, len(cols))
		for ci, k := range names {
			v := m.vals[k]
			if v == nil {
				continue
			}
			switch types[ci] {
			case "INTEGER":
				switch t := v.(type) {
				case bool:
					if t {
						row[ci] = int64(1)
					} else {
						row[ci] = int64(0)
					}
				case int64:
					row[ci] = t
				case dfNum:
					n, _ := strconv.ParseInt(string(t), 10, 64)
					row[ci] = n
				}
			case "REAL":
				switch t := v.(type) {
				case int64:
					row[ci] = float64(t)
				case float64:
					row[ci] = t
				case dfNum:
					f, _ := strconv.ParseFloat(string(t), 64)
					row[ci] = f
				}
			default:
				if s, ok := dfScalarString(v); ok {
					row[ci] = s
				} else {
					var b bytes.Buffer
					dfWriteJSON(&b, v)
					row[ci] = b.String()
				}
			}
		}
		rows[ri] = row
	}
	return dfSQLiteWrite(ctx, job.Output, dfTableName(job.Name(0)), cols, types, rows)
}

// ---------------------------------------------------------------- Parquet
//
// Flat files only (no nested or repeated columns). Logical types are mapped
// for dates and timestamps; everything else follows the physical type.

func dfParquetConvert(v parquet.Value, logical string) any {
	if v.IsNull() {
		return nil
	}
	unsigned := strings.Contains(logical, "isSigned=false")
	switch v.Kind() {
	case parquet.Boolean:
		return v.Boolean()
	case parquet.Int32:
		n := v.Int32()
		if strings.HasPrefix(logical, "DATE") {
			return time.Unix(int64(n)*86400, 0).UTC().Format("2006-01-02")
		}
		if unsigned {
			return int64(uint32(n))
		}
		return int64(n)
	case parquet.Int64:
		n := v.Int64()
		if strings.HasPrefix(logical, "TIMESTAMP") {
			var t time.Time
			switch {
			case strings.Contains(logical, "NANOS"):
				t = time.Unix(0, n).UTC()
			case strings.Contains(logical, "MICROS"):
				t = time.UnixMicro(n).UTC()
			default:
				t = time.UnixMilli(n).UTC()
			}
			return t.Format(time.RFC3339Nano)
		}
		if unsigned {
			return dfUint(uint64(n))
		}
		return n
	case parquet.Float:
		f, _ := strconv.ParseFloat(strconv.FormatFloat(float64(v.Float()), 'g', -1, 32), 64)
		return f
	case parquet.Double:
		return v.Double()
	case parquet.ByteArray, parquet.FixedLenByteArray:
		b := v.ByteArray()
		if utf8.Valid(b) {
			return string(b)
		}
		return base64.StdEncoding.EncodeToString(b)
	}
	return v.String()
}

func dfParquet(ctx context.Context, job Job) error {
	f, err := os.Open(job.Input())
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		return Userf("could not read the Parquet file: %v", err)
	}
	schema := pf.Schema()
	paths := schema.Columns()
	names := make([]string, len(paths))
	logical := make([]string, len(paths))
	for i, p := range paths {
		if len(p) != 1 {
			return Userf("nested Parquet columns (%s) are not supported yet", strings.Join(p, "."))
		}
		names[i] = p[0]
		if leaf, ok := schema.Lookup(p...); ok {
			logical[i] = leaf.Node.Type().String()
		}
	}
	toJSON := strings.EqualFold(filepath.Ext(job.Output), ".json")
	out, err := os.Create(job.Output)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(out)
	var cw *csv.Writer
	if toJSON {
		bw.WriteString("[")
	} else {
		cw = csv.NewWriter(bw)
		if err := cw.Write(names); err != nil {
			out.Close()
			return err
		}
	}
	count := 0
	fail := func(err error) error {
		out.Close()
		_ = os.Remove(job.Output)
		return err
	}
	for _, rg := range pf.RowGroups() {
		rr := rg.Rows()
		buf := make([]parquet.Row, 256)
		for {
			if err := ctx.Err(); err != nil {
				rr.Close()
				return fail(err)
			}
			n, rerr := rr.ReadRows(buf)
			for _, row := range buf[:n] {
				cells := make([]any, len(names))
				seen := make([]bool, len(names))
				for _, v := range row {
					c := v.Column()
					if c < 0 || c >= len(names) {
						continue
					}
					if seen[c] {
						rr.Close()
						return fail(Userf("list columns (%s) are not supported yet", names[c]))
					}
					seen[c] = true
					cells[c] = dfParquetConvert(v, logical[c])
				}
				if toJSON {
					m := newDfMap()
					for i, nm := range names {
						m.set(nm, cells[i])
					}
					if count > 0 {
						bw.WriteString(",")
					}
					bw.WriteString("\n  ")
					var jb bytes.Buffer
					dfWriteJSON(&jb, m)
					bw.Write(jb.Bytes())
				} else {
					rec := make([]string, len(cells))
					for i, c := range cells {
						rec[i], _ = dfScalarString(c)
					}
					if err := cw.Write(rec); err != nil {
						rr.Close()
						return fail(err)
					}
				}
				count++
			}
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				rr.Close()
				return fail(Userf("could not read the Parquet rows: %v", rerr))
			}
			if n == 0 {
				break
			}
		}
		rr.Close()
	}
	if toJSON {
		if count > 0 {
			bw.WriteString("\n")
		}
		bw.WriteString("]\n")
	} else {
		cw.Flush()
		if err := cw.Error(); err != nil {
			return fail(err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fail(err)
	}
	return out.Close()
}
