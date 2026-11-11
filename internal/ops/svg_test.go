package ops

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const svgTestDoc = `<?xml version="1.0" encoding="UTF-8"?>
<!-- A comment that should disappear -->
<svg xmlns="http://www.w3.org/2000/svg" width="120" height="80" viewBox="0 0 120 80">
  <defs>
    <linearGradient id="g1" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0.00" stop-color="#ff0000"/>
      <stop offset="1.00" stop-color="#0000ff"/>
    </linearGradient>
  </defs>
  <!-- background -->
  <rect   x="0.000"  y="0.000"  width="120.000"  height="80.000"  fill="url(#g1)"  />
  <path d="M 10.000 10.000 L 110.000 10.000 L 110.000 70.000 Z" fill="none" stroke="#ffffff" stroke-width="2.00"/>
</svg>
`

func TestSVGOptimize(t *testing.T) {
	in := filepath.Join(t.TempDir(), "in.svg")
	if err := os.WriteFile(in, []byte(svgTestDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, "svg.optimize", []string{in}, "out.svg", Args{})
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if len(s) >= len(svgTestDoc) {
		t.Errorf("not smaller: %d >= %d", len(s), len(svgTestDoc))
	}
	if !strings.Contains(s, "viewBox=") {
		t.Errorf("viewBox was dropped: %s", s)
	}
	if strings.Contains(s, "comment") || strings.Contains(s, "background") {
		t.Errorf("comments survived: %s", s)
	}
	if !strings.Contains(s, "g1") {
		t.Errorf("gradient id was dropped: %s", s)
	}
	// Still well-formed XML.
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("output is not well-formed XML: %v\n%s", err, s)
		}
	}
}

func TestSVGOptimizeRejectsNonSVG(t *testing.T) {
	in := filepath.Join(t.TempDir(), "x.svg")
	_ = os.WriteFile(in, []byte("hello"), 0o644)
	if _, err := run(t, "svg.optimize", []string{in}, "out.svg", Args{}); err == nil {
		t.Fatal("expected an error")
	}
}
