package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func convertData(t *testing.T, input, outName string) string {
	t.Helper()
	h, _ := Lookup("data.convert")
	out := filepath.Join(t.TempDir(), outName)
	job := Prepare(Job{Inputs: []string{input}, Names: []string{filepath.Base(input)}, Output: out})
	if err := h.Run(t.Context(), job); err != nil {
		t.Fatalf("convert %s -> %s: %v", input, outName, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCSVToJSONKeepsStringsAndUnicode(t *testing.T) {
	got := convertData(t, fixture("people.csv"), "people.json")
	want := `[
  {"id": "007", "name": "Zoë Saldaña", "city": "São Paulo", "note": "likes \"quotes\", commas"},
  {"id": "42", "name": "李雷", "city": "北京", "note": ""}
]
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSemicolonCSVIsDetected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "eu.csv")
	_ = os.WriteFile(p, []byte("\xef\xbb\xbfname;price\nKaffee;3,50\n"), 0o644)
	got := convertData(t, p, "eu.json")
	if !strings.Contains(got, `"name": "Kaffee", "price": "3,50"`) {
		t.Fatalf("semicolon CSV parsed wrong: %s", got)
	}
}

func TestCSVToMarkdown(t *testing.T) {
	got := convertData(t, fixture("people.csv"), "people.md")
	want := "| id | name | city | note |\n| --- | --- | --- | --- |\n| 007 | Zoë Saldaña | São Paulo | likes \"quotes\", commas |\n| 42 | 李雷 | 北京 |  |\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestJSONToCSVUnionsColumnsInOrder(t *testing.T) {
	got := convertData(t, fixture("people.json"), "people.csv")
	want := "name,age,tags,zip,active,address\n" +
		"Ada,36,\"[\"\"math\"\",\"\"engines\"\"]\",01234,,\n" +
		"Grace,85,,,true,\"{\"\"city\"\":\"\"Arlington\"\"}\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestJSONToYAMLPreservesOrderAndTypes(t *testing.T) {
	got := convertData(t, fixture("people.json"), "people.yaml")
	want := `- name: Ada
  age: 36
  tags:
    - math
    - engines
  zip: "01234"
- name: Grace
  age: 85
  active: true
  address:
    city: Arlington
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestYAMLToJSONResolvesAnchorsAndMergeKeys(t *testing.T) {
	got := convertData(t, fixture("config.yaml"), "config.json")
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, got)
	}
	if v["version"] != "1.0" || v["port"] != float64(8080) || v["debug"] != false || v["empty"] != nil {
		t.Fatalf("scalar types wrong: %v", v)
	}
	prod := v["prod"].(map[string]any)
	if prod["retries"] != float64(3) || prod["host"] != "example.com" {
		t.Fatalf("merge key not applied: %v", prod)
	}
	if strings.Index(got, `"name"`) > strings.Index(got, `"version"`) {
		t.Fatal("key order not preserved")
	}
	paths := v["paths"].([]any)
	if paths[1] != `C:\files` {
		t.Fatalf("escaped string = %q", paths[1])
	}
}

func TestRoundTripJSONYAMLJSON(t *testing.T) {
	yamlOut := filepath.Join(t.TempDir(), "people.yaml")
	_ = os.WriteFile(yamlOut, []byte(convertData(t, fixture("people.json"), "people.yaml")), 0o644)
	back := convertData(t, yamlOut, "people.json")
	var a, b any
	orig, _ := os.ReadFile(fixture("people.json"))
	_ = json.Unmarshal(orig, &a)
	_ = json.Unmarshal([]byte(back), &b)
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("round trip changed data:\n%s\n%s", ab, bb)
	}
}

func TestMarkdownToHTML(t *testing.T) {
	got := convertData(t, fixture("notes.md"), "notes.html")
	for _, want := range []string{"<!doctype html>", "<title>notes</title>", "<h1>Release notes ✨</h1>", "<strong>locally</strong>", "<table>", `type="checkbox"`} {
		if !strings.Contains(got, want) {
			t.Errorf("html missing %q", want)
		}
	}
}

func TestInvalidJSONReportsLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(p, []byte("{\n  \"a\": 1,\n  oops\n}"), 0o644)
	h, _ := Lookup("data.convert")
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{p}, Output: filepath.Join(t.TempDir(), "x.yaml")}))
	assertUserError(t, err)
	if !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error does not point at line 3: %v", err)
	}
}

func TestUnsupportedPairIsRejected(t *testing.T) {
	h, _ := Lookup("data.convert")
	err := h.Run(t.Context(), Prepare(Job{Inputs: []string{fixture("notes.md")}, Output: filepath.Join(t.TempDir(), "x.csv")}))
	assertUserError(t, err)
}
