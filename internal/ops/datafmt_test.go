package ops

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const dfFormats = "../../testdata/formats"

func dfFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// dfConv runs a handler on a file and returns the output text.
func dfConv(t *testing.T, handler, in, outName string, args Args) string {
	t.Helper()
	out := mustRun(t, handler, []string{in}, outName, args)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dfConvErr(t *testing.T, handler, in, outName string) error {
	t.Helper()
	_, err := run(t, handler, []string{in}, outName, nil)
	if err == nil {
		t.Fatalf("%s %s: expected an error", handler, filepath.Base(in))
	}
	return err
}

func dfJSONEq(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDfJSONTOMLRoundTrip(t *testing.T) {
	const src = `{"title":"x","n":3,"ratio":1.5,"on":true,"tags":["a","b"],"server":{"host":"h","ports":[1,2]},"items":[{"id":1},{"id":2}]}`
	toml := dfConv(t, "data.tree", dfFile(t, "a.json", src), "a.toml", nil)
	if !strings.Contains(toml, "[server]") {
		t.Fatalf("expected a [server] table:\n%s", toml)
	}
	back := dfConv(t, "data.tree", dfFile(t, "a.toml", toml), "a.json", nil)
	dfJSONEq(t, back, src)
}

func TestDfTOMLRejectsWhatItCannotHold(t *testing.T) {
	err := dfConvErr(t, "data.tree", dfFile(t, "list.json", `[1,2]`), "x.toml")
	if !strings.Contains(err.Error(), "top level") {
		t.Fatalf("unclear error: %v", err)
	}
	err = dfConvErr(t, "data.tree", dfFile(t, "nul.json", `{"a":{"b":null}}`), "x.toml")
	if !strings.Contains(err.Error(), "null") || !strings.Contains(err.Error(), "a.b") {
		t.Fatalf("unclear error: %v", err)
	}
}

func TestDfXMLToJSONMapping(t *testing.T) {
	const src = `<?xml version="1.0"?>
<library name="City" open="true">
  <book id="1">Dune</book>
  <book id="2">Emma</book>
  <note>hi <b>x</b></note>
  <empty/>
</library>
`
	got := dfConv(t, "data.tree", dfFile(t, "lib.xml", src), "lib.json", nil)
	want := `{
  "library": {
    "@name": "City",
    "@open": "true",
    "book": [
      {
        "@id": "1",
        "#text": "Dune"
      },
      {
        "@id": "2",
        "#text": "Emma"
      }
    ],
    "note": {
      "#text": "hi",
      "b": "x"
    },
    "empty": ""
  }
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDfJSONToXML(t *testing.T) {
	got := dfConv(t, "data.tree", dfFile(t, "o.json", `{"order":{"@id":"7","item":[{"@sku":"a","#text":"one"},"two"],"note":"x & y","none":null}}`), "o.xml", nil)
	want := `<?xml version="1.0" encoding="UTF-8"?>
<order id="7">
  <item sku="a">one</item>
  <item>two</item>
  <note>x &amp; y</note>
  <none/>
</order>
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	got = dfConv(t, "data.tree", dfFile(t, "l.json", `[1,2]`), "l.xml", nil)
	want = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<root>\n  <item>1</item>\n  <item>2</item>\n</root>\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDfXMLRoundTripThroughJSON(t *testing.T) {
	const src = `{"cfg":{"@v":"2","name":"n","tag":["a","b"],"sub":{"@k":"1","#text":"t"}}}`
	xml := dfConv(t, "data.tree", dfFile(t, "c.json", src), "c.xml", nil)
	back := dfConv(t, "data.tree", dfFile(t, "c.xml", xml), "c.json", nil)
	dfJSONEq(t, back, src)
}

func TestDfINI(t *testing.T) {
	const src = "; comment\ntop = 1\n[db]\nhost = localhost\nname = \"My DB\"\n[db2]\nx: y\n"
	got := dfConv(t, "data.tree", dfFile(t, "a.ini", src), "a.json", nil)
	dfJSONEq(t, got, `{"top":"1","db":{"host":"localhost","name":"My DB"},"db2":{"x":"y"}}`)

	ini := dfConv(t, "data.tree", dfFile(t, "b.json", `{"name":"app","debug":true,"db":{"host":"h","port":5432,"pass":" padded"}}`), "b.ini", nil)
	want := "name = app\ndebug = true\n\n[db]\nhost = h\nport = 5432\npass = \" padded\"\n"
	if ini != want {
		t.Fatalf("got:\n%q\nwant:\n%q", ini, want)
	}
	err := dfConvErr(t, "data.tree", dfFile(t, "deep.json", `{"a":{"b":{"c":1}}}`), "deep.ini")
	if !strings.Contains(err.Error(), "INI") {
		t.Fatalf("unclear error: %v", err)
	}
}

func TestDfEnvQuotingAndEscapes(t *testing.T) {
	src := "# comment\nexport A=1\nB=\"two words\"\nC='single $x'\nD=\"line1\\nline2\"\nE=plain # trailing\nF=\"multi\nline\"\nG=\n"
	got := dfConv(t, "data.tree", dfFile(t, "x.env", src), "x.json", nil)
	dfJSONEq(t, got, `{"A":"1","B":"two words","C":"single $x","D":"line1\nline2","E":"plain","F":"multi\nline","G":""}`)

	env := dfConv(t, "data.tree", dfFile(t, "y.json", `{"A":"1","B":"two words","C":"it's","D":"a\nb","E":"","F":5,"G":true,"H":null,"I":"say \"hi\""}`), "y.env", nil)
	want := "A=1\nB='two words'\nC=\"it's\"\nD=\"a\\nb\"\nE=\nF=5\nG=true\nH=\nI='say \"hi\"'\n"
	if env != want {
		t.Fatalf("got:\n%s\nwant:\n%s", env, want)
	}
	back := dfConv(t, "data.tree", dfFile(t, "z.env", env), "z.json", nil)
	dfJSONEq(t, back, `{"A":"1","B":"two words","C":"it's","D":"a\nb","E":"","F":"5","G":"true","H":"","I":"say \"hi\""}`)

	err := dfConvErr(t, "data.tree", dfFile(t, "n.json", `{"a":{"b":1}}`), "n.env")
	if !strings.Contains(err.Error(), "flat") {
		t.Fatalf("unclear error: %v", err)
	}
}

const dfPlistXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>name</key>
	<string>Asteria matrix check</string>
	<key>count</key>
	<integer>3</integer>
	<key>ratio</key>
	<real>1.5</real>
	<key>on</key>
	<true/>
	<key>tags</key>
	<array><string>a</string><string>b</string></array>
	<key>when</key>
	<date>2024-01-02T03:04:05Z</date>
	<key>blob</key>
	<data>
	aGVsbG8=
	</data>
</dict>
</plist>
`

func TestDfPlistXML(t *testing.T) {
	got := dfConv(t, "data.tree", dfFile(t, "p.plist", dfPlistXML), "p.json", nil)
	dfJSONEq(t, got, `{"name":"Asteria matrix check","count":3,"ratio":1.5,"on":true,"tags":["a","b"],"when":"2024-01-02T03:04:05Z","blob":"aGVsbG8="}`)

	const src = `{"a":"x","n":2,"f":0.5,"b":false,"l":[1,"two"],"d":{"k":"v"}}`
	plist := dfConv(t, "data.tree", dfFile(t, "q.json", src), "q.plist", nil)
	if !strings.Contains(plist, "<integer>2</integer>") || !strings.Contains(plist, "<real>0.5</real>") {
		t.Fatalf("numbers written wrong:\n%s", plist)
	}
	dfJSONEq(t, dfConv(t, "data.tree", dfFile(t, "q.plist", plist), "q.json", nil), src)

	err := dfConvErr(t, "data.tree", dfFile(t, "n.json", `{"a":null}`), "n.plist")
	if !strings.Contains(err.Error(), "null") {
		t.Fatalf("unclear error: %v", err)
	}
}

// Produced by Apple's `plutil -convert binary1` from an XML plist with the
// keys name, count, ratio, on and tags.
var dfBinaryPlist = []byte{
	0x62, 0x70, 0x6c, 0x69, 0x73, 0x74, 0x30, 0x30, 0xd5, 0x01, 0x02, 0x03,
	0x04, 0x05, 0x06, 0x09, 0x0a, 0x0b, 0x0c, 0x54, 0x74, 0x61, 0x67, 0x73,
	0x55, 0x63, 0x6f, 0x75, 0x6e, 0x74, 0x54, 0x6e, 0x61, 0x6d, 0x65, 0x55,
	0x72, 0x61, 0x74, 0x69, 0x6f, 0x52, 0x6f, 0x6e, 0xa2, 0x07, 0x08, 0x51,
	0x61, 0x51, 0x62, 0x10, 0x03, 0x5f, 0x10, 0x14, 0x41, 0x73, 0x74, 0x65,
	0x72, 0x69, 0x61, 0x20, 0x6d, 0x61, 0x74, 0x72, 0x69, 0x78, 0x20, 0x63,
	0x68, 0x65, 0x63, 0x6b, 0x23, 0x3f, 0xf8, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x09, 0x08, 0x13, 0x18, 0x1e, 0x23, 0x29, 0x2c, 0x2f, 0x31, 0x33,
	0x35, 0x4c, 0x55, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0d, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x56,
}

func TestDfBinaryPlist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.plist")
	if err := os.WriteFile(p, dfBinaryPlist, 0o644); err != nil {
		t.Fatal(err)
	}
	got := dfConv(t, "data.tree", p, "b.json", nil)
	dfJSONEq(t, got, `{"name":"Asteria matrix check","count":3,"ratio":1.5,"on":true,"tags":["a","b"]}`)
}

func TestDfJSONLAndCSV(t *testing.T) {
	csv := dfConv(t, "data.tree", dfFile(t, "a.jsonl", "{\"id\":1,\"note\":\"a b\"}\n\n{\"id\":2,\"note\":\"c\"}\n"), "a.csv", nil)
	if csv != "id,note\n1,a b\n2,c\n" {
		t.Fatalf("got %q", csv)
	}
	jl := dfConv(t, "data.tree", dfFile(t, "p.csv", "id,name\n1,Ada\n2,\"Gr,ace\"\n"), "p.jsonl", nil)
	if jl != "{\"id\":\"1\",\"name\":\"Ada\"}\n{\"id\":\"2\",\"name\":\"Gr,ace\"}\n" {
		t.Fatalf("got %q", jl)
	}
	jl = dfConv(t, "data.tree", dfFile(t, "arr.json", `[{"a":1},{"a":2}]`), "arr.jsonl", nil)
	if jl != "{\"a\":1}\n{\"a\":2}\n" {
		t.Fatalf("got %q", jl)
	}
	back := dfConv(t, "data.tree", dfFile(t, "arr.jsonl", jl), "arr.json", nil)
	dfJSONEq(t, back, `[{"a":1},{"a":2}]`)
}

func TestDfTSVAndCSV(t *testing.T) {
	csv := dfConv(t, "data.table", dfFile(t, "a.tsv", "a\tb\n1\t\"x,y\"\n"), "a.csv", nil)
	if csv != "a,b\n1,\"x,y\"\n" {
		t.Fatalf("got %q", csv)
	}
	tsv := dfConv(t, "data.table", dfFile(t, "b.csv", "a,b\n1,\"x\ty\"\n"), "b.tsv", nil)
	if tsv != "a\tb\n1\t\"x\ty\"\n" {
		t.Fatalf("got %q", tsv)
	}
}

func TestDfYAMLWriterKeepsTypes(t *testing.T) {
	const src = `{"zip":"01234","n":1,"s":"true","multi":"a\nb","list":[1,"x",null],"e":{},"f":2.5}`
	yml := dfConv(t, "data.tree", dfFile(t, "a.json", src), "a.yaml", nil)
	back := dfConv(t, "data.convert", dfFile(t, "a.yaml", yml), "a.json", nil)
	dfJSONEq(t, back, src)
	if !strings.Contains(yml, `zip: "01234"`) {
		t.Fatalf("zip must stay quoted:\n%s", yml)
	}
}

func TestDfFormatAndMinifyJSON(t *testing.T) {
	got := dfConv(t, "data.json", dfFile(t, "a.json", `{"b":1,"a":[1,2,{"c":"<x>"}],"e":{},"f":[]}`), "o.json", Args{"mode": "pretty"})
	want := "{\n  \"b\": 1,\n  \"a\": [\n    1,\n    2,\n    {\n      \"c\": \"<x>\"\n    }\n  ],\n  \"e\": {},\n  \"f\": []\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	got = dfConv(t, "data.json", dfFile(t, "b.json", "{ \"a\" : [ 1, 2 ] ,\n \"b\": \"x y\" }"), "o.json", Args{"mode": "minify"})
	if got != "{\"a\":[1,2],\"b\":\"x y\"}\n" {
		t.Fatalf("got %q", got)
	}
	err := dfConvErr(t, "data.json", dfFile(t, "bad.json", "{\"a\":\n1,}"), "o.json")
	if !strings.Contains(err.Error(), "line") {
		t.Fatalf("expected a line number: %v", err)
	}
}

func TestDfFormatXML(t *testing.T) {
	got := dfConv(t, "data.xmlFormat", dfFile(t, "a.xml", `<?xml version="1.0"?><a x="1"><b>text</b><c><d/></c><!-- note --></a>`), "o.xml", nil)
	want := "<?xml version=\"1.0\"?>\n<a x=\"1\">\n  <b>text</b>\n  <c>\n    <d/>\n  </c>\n  <!-- note -->\n</a>\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if err := dfConvErr(t, "data.xmlFormat", dfFile(t, "bad.xml", "<a><b></a>"), "o.xml"); !strings.Contains(err.Error(), "XML") {
		t.Fatalf("unclear error: %v", err)
	}
}

func dfMakeSQLite(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "demo.sqlite")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE notes (id INTEGER PRIMARY KEY, text TEXT, score REAL, data BLOB)`,
		`INSERT INTO notes VALUES (1, 'Asteria matrix check', 2.5, x'ff00')`,
		`INSERT INTO notes VALUES (2, NULL, NULL, NULL)`,
		`CREATE TABLE empty_t (a, b)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDfSQLiteToJSONAndCSVIsReadOnly(t *testing.T) {
	p := dfMakeSQLite(t)
	before, _ := os.ReadFile(p)
	got := dfConv(t, "data.sqlite", p, "o.json", nil)
	dfJSONEq(t, got, `{"empty_t":[],"notes":[{"id":1,"text":"Asteria matrix check","score":2.5,"data":"/wA="},{"id":2,"text":null,"score":null,"data":null}]}`)

	dir := mustRun(t, "data.sqlite", []string{p}, "tables", nil)
	notes, err := os.ReadFile(filepath.Join(dir, "notes.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if string(notes) != "id,text,score,data\n1,Asteria matrix check,2.5,/wA=\n2,,,\n" {
		t.Fatalf("notes.csv: %q", notes)
	}
	empty, err := os.ReadFile(filepath.Join(dir, "empty_t.csv"))
	if err != nil || string(empty) != "a,b\n" {
		t.Fatalf("empty_t.csv: %q %v", empty, err)
	}

	after, _ := os.ReadFile(p)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("the source database was modified")
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("extra files next to the database: %v", entries)
	}
	if err := dfConvErr(t, "data.sqlite", dfFile(t, "fake.sqlite", "not a database"), "o.json"); !strings.Contains(err.Error(), "SQLite") {
		t.Fatalf("unclear error: %v", err)
	}
}

func TestDfCSVToSQLiteInfersTypes(t *testing.T) {
	src := dfFile(t, "people.csv", "id,name,price,zip\n1,Ada,2.5,01234\n2,Grace,,90210\n3,Linus,10,\n")
	out := mustRun(t, "data.sqlite", []string{src}, "out.sqlite", nil)
	db, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`PRAGMA table_info(people)`)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		types[name] = typ
	}
	rows.Close()
	want := map[string]string{"id": "INTEGER", "name": "TEXT", "price": "REAL", "zip": "TEXT"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("column types %v, want %v", types, want)
	}

	var zip, tid, tprice string
	var price sql.NullFloat64
	if err := db.QueryRow(`SELECT zip, typeof(id), typeof(price), price FROM people WHERE id = 1`).Scan(&zip, &tid, &tprice, &price); err != nil {
		t.Fatal(err)
	}
	if zip != "01234" || tid != "integer" || tprice != "real" || price.Float64 != 2.5 {
		t.Fatalf("row 1: %q %s %s %v", zip, tid, tprice, price)
	}
	if err := db.QueryRow(`SELECT typeof(price) FROM people WHERE id = 2`).Scan(&tprice); err != nil || tprice != "null" {
		t.Fatalf("empty cell should be NULL: %q %v", tprice, err)
	}
}

func TestDfJSONToSQLite(t *testing.T) {
	src := dfFile(t, "items.json", `[{"id":1,"ok":true,"tags":["a"],"n":1.5},{"id":2,"ok":false,"n":null,"extra":"z"}]`)
	out := mustRun(t, "data.sqlite", []string{src}, "out.sqlite", nil)
	db, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ok int
	var tags string
	var n float64
	var extra sql.NullString
	if err := db.QueryRow(`SELECT ok, tags, n, extra FROM items WHERE id = 1`).Scan(&ok, &tags, &n, &extra); err != nil {
		t.Fatal(err)
	}
	if ok != 1 || tags != `["a"]` || n != 1.5 || extra.Valid {
		t.Fatalf("row 1: %d %q %v %v", ok, tags, n, extra)
	}
	var extra2 string
	if err := db.QueryRow(`SELECT ok, extra FROM items WHERE id = 2`).Scan(&ok, &extra2); err != nil || ok != 0 || extra2 != "z" {
		t.Fatalf("row 2: %d %q %v", ok, extra2, err)
	}
}

func TestDfFixturesCarryTheMarker(t *testing.T) {
	const marker = "Asteria matrix check"
	for _, name := range []string{"sample.toml", "sample.xml", "sample.ini", "sample.env", "sample.plist", "sample.jsonl"} {
		got := dfConv(t, "data.tree", filepath.Join(dfFormats, name), "out.json", nil)
		if !strings.Contains(got, marker) {
			t.Errorf("%s -> JSON lost the marker:\n%s", name, got)
		}
	}
	for _, out := range []string{"out.yaml", "out.xml", "out.jsonl"} {
		got := dfConv(t, "data.tree", filepath.Join(dfFormats, "sample.toml"), out, nil)
		if !strings.Contains(got, marker) {
			t.Errorf("sample.toml -> %s lost the marker:\n%s", out, got)
		}
	}
	got := dfConv(t, "data.tree", filepath.Join(dfFormats, "sample.tsv"), "out.jsonl", nil)
	if !strings.Contains(got, marker) {
		t.Errorf("sample.tsv lost the marker: %s", got)
	}
	got = dfConv(t, "data.sqlite", filepath.Join(dfFormats, "sample.sqlite"), "out.json", nil)
	if !strings.Contains(got, marker) || !strings.Contains(got, `"notes"`) {
		t.Errorf("sample.sqlite lost the marker: %s", got)
	}
	dfJSONEq(t, dfConv(t, "data.tree", filepath.Join(dfFormats, "sample.toml"), "t.json", nil),
		`{"title":"Asteria matrix check","count":3,"server":{"host":"localhost","ports":[8080,8081]}}`)
}
