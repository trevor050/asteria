package ops

import (
	"math"
	"path/filepath"
	"testing"
)

// The committed .cube and .3dl fixtures were written by a separate script
// (not this package) and describe the same warm grade; both readers must
// agree with each other everywhere in the cube.
func TestLUTFixturesAgree(t *testing.T) {
	c, err := lutParseFile(filepath.Join("..", "..", "testdata", "formats", "sample.cube"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := lutParseFile(filepath.Join("..", "..", "testdata", "formats", "sample.3dl"))
	if err != nil {
		t.Fatal(err)
	}
	worst := 0.0
	for _, p := range [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 0, 1}, {0.2, 0.7, 0.4}, {0.9, 0.1, 0.5}, {1, 1, 1}} {
		r1, g1, b1 := c.eval(p[0], p[1], p[2])
		r2, g2, b2 := d.eval(p[0], p[1], p[2])
		for _, dv := range []float32{r1 - r2, g1 - g2, b1 - b2} {
			worst = math.Max(worst, math.Abs(float64(dv)))
		}
	}
	if worst > 2.0/1023 {
		t.Fatalf(".cube and .3dl disagree by %.4f", worst)
	}
	// The grade lifts red and lowers blue.
	if r, _, b := c.eval(0.5, 0.5, 0.5); !(r > 0.53 && b < 0.47) {
		t.Fatalf("eval(0.5) = %v %v", r, b)
	}
}
