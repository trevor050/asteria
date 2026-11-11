package ops

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gisSampleGeoJSON = `{"type":"FeatureCollection","features":[
{"type":"Feature","properties":{"name":"Asteria matrix check","kind":"cafe"},"geometry":{"type":"Point","coordinates":[2.3522,48.8566,35.5]}},
{"type":"Feature","properties":{"name":"route"},"geometry":{"type":"LineString","coordinates":[[2.3522,48.8566],[2.36,48.86],[2.37,48.87]]}},
{"type":"Feature","properties":{"name":"park"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[10,0],[10,10],[0,10],[0,0]],[[2,2],[2,4],[4,4],[4,2],[2,2]]]}},
{"type":"Feature","properties":{},"geometry":{"type":"MultiPolygon","coordinates":[[[[20,20],[21,20],[21,21],[20,20]]],[[[30,30],[31,30],[31,31],[30,30]]]]}},
{"type":"Feature","properties":{},"geometry":{"type":"MultiPoint","coordinates":[[1.1234567,2.7654321],[3,4]]}},
{"type":"Feature","properties":{},"geometry":{"type":"MultiLineString","coordinates":[[[0,0],[1,1]],[[2,2],[3,3],[4,4]]]}}
]}`

func gisTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func gisTestRead(t *testing.T, path string) *gisDoc {
	t.Helper()
	doc, err := gisRead(path, strings.ToLower(filepath.Ext(path)))
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Base(path), err)
	}
	return doc
}

func gisPositions(g gisGeom) [][]float64 {
	var out [][]float64
	g.each(func(p []float64) { out = append(out, p) })
	return out
}

func gisSameDocs(t *testing.T, label string, a, b *gisDoc, tol float64) {
	t.Helper()
	if len(a.Features) != len(b.Features) {
		t.Fatalf("%s: %d features became %d", label, len(a.Features), len(b.Features))
	}
	for i := range a.Features {
		ga, gb := a.Features[i].Geom, b.Features[i].Geom
		if ga.Type != gb.Type {
			t.Errorf("%s: feature %d type %s became %s", label, i, ga.Type, gb.Type)
			continue
		}
		pa, pb := gisPositions(ga), gisPositions(gb)
		if len(pa) != len(pb) {
			t.Errorf("%s: feature %d has %d positions, then %d", label, i, len(pa), len(pb))
			continue
		}
		for j := range pa {
			if len(pa[j]) != len(pb[j]) {
				t.Errorf("%s: feature %d position %d lost its elevation", label, i, j)
				continue
			}
			for k := range pa[j] {
				if math.Abs(pa[j][k]-pb[j][k]) > tol {
					t.Errorf("%s: feature %d position %d: %v became %v", label, i, j, pa[j], pb[j])
				}
			}
		}
	}
}

func TestGeoJSONKMLRoundTrip(t *testing.T) {
	src := gisTestFile(t, "in.geojson", []byte(gisSampleGeoJSON))
	kml := mustRun(t, "geo.convert", []string{src}, "mid.kml", nil)
	back := mustRun(t, "geo.convert", []string{kml}, "out.geojson", nil)
	orig := gisTestRead(t, src)
	gisSameDocs(t, "geojson->kml->geojson", orig, gisTestRead(t, back), 1e-7)
	if name := gisName(gisTestRead(t, back).Features[0]); name != "Asteria matrix check" {
		t.Errorf("name after round trip = %q", name)
	}
	data, _ := os.ReadFile(kml)
	if !strings.Contains(string(data), "Asteria matrix check") {
		t.Error("KML does not contain the feature name")
	}
	// KMZ is the same KML in a zip.
	kmz := mustRun(t, "geo.convert", []string{src}, "mid.kmz", nil)
	gisSameDocs(t, "geojson->kmz", orig, gisTestRead(t, kmz), 1e-7)
}

const gisSampleGPX = `<?xml version="1.0"?>
<gpx version="1.1" creator="test" xmlns="http://www.topografix.com/GPX/1/1">
<wpt lat="48.8566" lon="2.3522"><ele>35</ele><name>Asteria matrix check</name></wpt>
<trk><name>climb</name><trkseg>
<trkpt lat="48.0" lon="2.0"><ele>100</ele><time>2024-05-01T10:00:00Z</time></trkpt>
<trkpt lat="48.01" lon="2.0"><ele>150</ele><time>2024-05-01T10:10:00Z</time></trkpt>
<trkpt lat="48.02" lon="2.0"><ele>120</ele><time>2024-05-01T10:20:00Z</time></trkpt>
</trkseg></trk></gpx>`

func TestGPXStats(t *testing.T) {
	src := gisTestFile(t, "ride.gpx", []byte(gisSampleGPX))
	out := mustRun(t, "geo.stats", []string{src}, "stats.json", nil)
	b, _ := os.ReadFile(out)
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	// 0.02 degrees of latitude is 2223.9 m.
	if d := st["distanceMeters"].(float64); math.Abs(d-2223.9) > 1 {
		t.Errorf("distance = %v, want ~2223.9", d)
	}
	if st["elevationGainMeters"].(float64) != 50 || st["elevationLossMeters"].(float64) != 30 {
		t.Errorf("gain/loss = %v/%v, want 50/30", st["elevationGainMeters"], st["elevationLossMeters"])
	}
	if st["durationSeconds"].(float64) != 1200 {
		t.Errorf("duration = %v, want 1200", st["durationSeconds"])
	}
	if st["movingTimeSeconds"].(float64) != 1200 {
		t.Errorf("moving time = %v, want 1200", st["movingTimeSeconds"])
	}
	if st["waypoints"].(float64) != 1 || st["tracks"].(float64) != 1 || st["points"].(float64) != 3 {
		t.Errorf("counts = %v", st)
	}
	bounds := st["bounds"].(map[string]any)
	if bounds["minLat"].(float64) != 48 || bounds["maxLat"].(float64) != 48.8566 {
		t.Errorf("bounds = %v", bounds)
	}
}

func TestGPXDistanceParisLondon(t *testing.T) {
	gpx := `<gpx version="1.1" creator="t" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>
<trkpt lat="48.8566" lon="2.3522"/><trkpt lat="51.5074" lon="-0.1278"/></trkseg></trk></gpx>`
	src := gisTestFile(t, "pl.gpx", []byte(gpx))
	out := mustRun(t, "geo.stats", []string{src}, "s.json", nil)
	b, _ := os.ReadFile(out)
	var st map[string]any
	_ = json.Unmarshal(b, &st)
	km := st["distanceMeters"].(float64) / 1000
	if math.Abs(km-343.5)/343.5 > 0.005 {
		t.Errorf("Paris-London = %.2f km, want 343.5 +-0.5%%", km)
	}
}

func TestGPXToKMLKeepsTimes(t *testing.T) {
	src := gisTestFile(t, "ride.gpx", []byte(gisSampleGPX))
	kml := mustRun(t, "geo.convert", []string{src}, "ride.kml", nil)
	data, _ := os.ReadFile(kml)
	if !strings.Contains(string(data), "gx:Track") {
		t.Error("track with times should be written as gx:Track")
	}
	gj := mustRun(t, "geo.convert", []string{kml}, "ride.geojson", nil)
	b, _ := os.ReadFile(gj)
	if !strings.Contains(string(b), "coordTimes") || !strings.Contains(string(b), "2024-05-01T10:20:00Z") {
		t.Errorf("times lost: %s", b)
	}
	doc := gisTestRead(t, gj)
	st, err := gisComputeStats(doc)
	if err != nil || st.DurationSeconds == nil || *st.DurationSeconds != 1200 {
		t.Errorf("stats after round trip: %+v %v", st, err)
	}
	// GPX writes points as wpt and lines as trk.
	gpx := mustRun(t, "geo.convert", []string{gj}, "again.gpx", nil)
	g, _ := os.ReadFile(gpx)
	if !strings.Contains(string(g), "<wpt ") || !strings.Contains(string(g), "<trk>") || !strings.Contains(string(g), "Asteria matrix check") {
		t.Errorf("gpx output: %s", g)
	}
}

func TestSimplifyNoisyLine(t *testing.T) {
	var line [][]float64
	for i := 0; i <= 100; i++ {
		noise := 0.00002 * math.Sin(float64(i)*1.7) // about 2 m
		line = append(line, []float64{2 + float64(i)*0.001, 48 + noise})
	}
	idx := gisSimplifyIdx(line, 10)
	if len(idx) != 2 || idx[0] != 0 || idx[1] != 100 {
		t.Errorf("noisy straight line kept %v, want only the endpoints", idx)
	}
	corner := [][]float64{{0, 0}, {0.01, 0}, {0.01, 0.01}}
	if got := gisSimplifyIdx(corner, 10); len(got) != 3 {
		t.Errorf("a real corner must survive: %v", got)
	}
	// Through the handler, with the tolerance as the skill's parameter.
	var coords []string
	for _, p := range line {
		coords = append(coords, fmt.Sprintf("[%v,%v]", p[0], p[1]))
	}
	gj := `{"type":"Feature","properties":{"name":"t"},"geometry":{"type":"LineString","coordinates":[` + strings.Join(coords, ",") + `]}}`
	src := gisTestFile(t, "t.geojson", []byte(gj))
	out := mustRun(t, "geo.simplify", []string{src}, "s.geojson", Args{"tolerance": "10"})
	doc := gisTestRead(t, out)
	if n := len(doc.Features[0].Geom.L[0]); n != 2 {
		t.Errorf("handler kept %d points, want 2", n)
	}
	out = mustRun(t, "geo.simplify", []string{src}, "s5.geojson", Args{"tolerance": "0.5"})
	if n := len(gisTestRead(t, out).Features[0].Geom.L[0]); n < 20 {
		t.Errorf("a 0.5 m tolerance kept only %d points", n)
	}
	_, err := run(t, "geo.simplify", []string{src}, "bad.geojson", Args{"tolerance": "far"})
	assertUserError(t, err)
}

func TestCSVPoints(t *testing.T) {
	csv := "name,lat,lon,ele,kind\nAsteria matrix check,48.85,2.35,35,cafe\nB,51.5,-0.12,10,pub\n"
	src := gisTestFile(t, "p.csv", []byte(csv))
	out := mustRun(t, "geo.convert", []string{src}, "p.geojson", nil)
	doc := gisTestRead(t, out)
	if len(doc.Features) != 2 {
		t.Fatalf("features = %d", len(doc.Features))
	}
	p := doc.Features[0].Geom.P
	if p[0] != 2.35 || p[1] != 48.85 || p[2] != 35 {
		t.Errorf("point = %v", p)
	}
	if gisName(doc.Features[0]) != "Asteria matrix check" || gisPropString(doc.Features[1].Props["kind"]) != "pub" {
		t.Errorf("properties = %v", doc.Features[1].Props)
	}
	// And back out as CSV, lines as vertices with a feature id.
	gj := gisTestFile(t, "l.geojson", []byte(gisSampleGeoJSON))
	csvOut := mustRun(t, "geo.convert", []string{gj}, "l.csv", nil)
	b, _ := os.ReadFile(csvOut)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !strings.HasPrefix(lines[0], "id,name,latitude,longitude,elevation,type,part,vertex") {
		t.Errorf("header = %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "1,Asteria matrix check,48.8566,2.3522,35.5,Point") {
		t.Errorf("first row = %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], "2,route,48.8566,2.3522,,LineString,1,1") {
		t.Errorf("first vertex row = %s", lines[2])
	}
	_, err := run(t, "geo.convert", []string{gisTestFile(t, "n.csv", []byte("a,b\n1,2\n"))}, "x.geojson", nil)
	assertUserError(t, err)
}

func TestProjectedCoordinatesRefused(t *testing.T) {
	gj := `{"type":"Feature","properties":{},"geometry":{"type":"Point","coordinates":[500000,4649776]}}`
	_, err := run(t, "geo.convert", []string{gisTestFile(t, "m.geojson", []byte(gj))}, "x.kml", nil)
	assertUserError(t, err)
}

// ---------------------------------------------------------------- shapefile

func gisTestSHP(shapeType uint32, recs [][]byte) []byte {
	var body []byte
	for i, c := range recs {
		h := make([]byte, 8)
		binary.BigEndian.PutUint32(h[0:], uint32(i+1))
		binary.BigEndian.PutUint32(h[4:], uint32(len(c)/2))
		body = append(append(body, h...), c...)
	}
	head := make([]byte, 100)
	binary.BigEndian.PutUint32(head[0:], 9994)
	binary.BigEndian.PutUint32(head[24:], uint32((100+len(body))/2))
	binary.LittleEndian.PutUint32(head[28:], 1000)
	binary.LittleEndian.PutUint32(head[32:], shapeType)
	return append(head, body...)
}

func gisTestPointRec(x, y float64) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[0:], 1)
	binary.LittleEndian.PutUint64(b[4:], math.Float64bits(x))
	binary.LittleEndian.PutUint64(b[12:], math.Float64bits(y))
	return b
}

func gisTestPolygonRec(ring [][2]float64) []byte {
	b := make([]byte, 48+16*len(ring))
	le := binary.LittleEndian
	le.PutUint32(b[0:], 5)
	le.PutUint32(b[36:], 1)
	le.PutUint32(b[40:], uint32(len(ring)))
	le.PutUint32(b[44:], 0)
	for i, p := range ring {
		le.PutUint64(b[48+16*i:], math.Float64bits(p[0]))
		le.PutUint64(b[56+16*i:], math.Float64bits(p[1]))
	}
	return b
}

func gisTestDBF(rows [][2]string) []byte {
	le := binary.LittleEndian
	hLen, rLen := 32+32*2+1, 1+20+10
	b := make([]byte, 32)
	b[0] = 3
	le.PutUint32(b[4:], uint32(len(rows)))
	le.PutUint16(b[8:], uint16(hLen))
	le.PutUint16(b[10:], uint16(rLen))
	field := func(name string, typ byte, length int) []byte {
		f := make([]byte, 32)
		copy(f, name)
		f[11], f[16] = typ, byte(length)
		return f
	}
	b = append(b, field("name", 'C', 20)...)
	b = append(b, field("pop", 'N', 10)...)
	b = append(b, 0x0D)
	for _, r := range rows {
		b = append(b, ' ')
		b = append(b, []byte(fmt.Sprintf("%-20s%10s", r[0], r[1]))...)
	}
	return b
}

func gisTestZip(t *testing.T, name string, files map[string][]byte, order []string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range order {
		w, _ := zw.Create(n)
		w.Write(files[n])
	}
	zw.Close()
	return gisTestFile(t, name, buf.Bytes())
}

const gisWGS84PRJ = `GEOGCS["GCS_WGS_1984",DATUM["D_WGS_1984",SPHEROID["WGS_1984",6378137.0,298.257223563]],PRIMEM["Greenwich",0.0],UNIT["Degree",0.0174532925199433]]`

func TestShapefileZip(t *testing.T) {
	files := map[string][]byte{
		"pts.shp":  gisTestSHP(1, [][]byte{gisTestPointRec(2.3522, 48.8566), gisTestPointRec(-0.1278, 51.5074)}),
		"pts.dbf":  gisTestDBF([][2]string{{"Asteria matrix check", "42"}, {"London", "9"}}),
		"pts.prj":  []byte(gisWGS84PRJ),
		"poly.shp": gisTestSHP(5, [][]byte{gisTestPolygonRec([][2]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}, {0, 0}})}),
	}
	zp := gisTestZip(t, "shapes.zip", files, []string{"pts.shp", "pts.dbf", "pts.prj", "poly.shp"})
	out := mustRun(t, "geo.convert", []string{zp}, "shapes.geojson", nil)
	doc := gisTestRead(t, out)
	if len(doc.Features) != 3 {
		t.Fatalf("features = %d, want 3", len(doc.Features))
	}
	f0 := doc.Features[0]
	if f0.Geom.Type != "Point" || f0.Geom.P[0] != 2.3522 || f0.Geom.P[1] != 48.8566 {
		t.Errorf("point = %+v", f0.Geom)
	}
	if gisName(f0) != "Asteria matrix check" || gisPropString(f0.Props["pop"]) != "42" {
		t.Errorf("attributes = %v", f0.Props)
	}
	if f0.Props["layer"] != "pts" {
		t.Errorf("layer = %v", f0.Props["layer"])
	}
	poly := doc.Features[2].Geom
	if poly.Type != "Polygon" || len(poly.L) != 1 || len(poly.L[0]) != 5 {
		t.Errorf("polygon = %+v", poly)
	}
}

func TestShapefileRefusesProjected(t *testing.T) {
	prj := `PROJCS["NAD_1983_UTM_Zone_18N",GEOGCS["GCS_North_American_1983",DATUM["D_North_American_1983",SPHEROID["GRS_1980",6378137,298.257222101]],PRIMEM["Greenwich",0],UNIT["Degree",0.0174532925199433]],PROJECTION["Transverse_Mercator"],UNIT["Meter",1]]`
	files := map[string][]byte{
		"p.shp": gisTestSHP(1, [][]byte{gisTestPointRec(500000, 4649776)}),
		"p.prj": []byte(prj),
	}
	zp := gisTestZip(t, "utm.zip", files, []string{"p.shp", "p.prj"})
	_, err := run(t, "geo.convert", []string{zp}, "x.geojson", nil)
	assertUserError(t, err)
	if err == nil || !strings.Contains(err.Error(), "projected") {
		t.Errorf("error = %v", err)
	}
	// No .prj but evidently projected coordinates: refused by the range check.
	files = map[string][]byte{"p.shp": files["p.shp"]}
	zp = gisTestZip(t, "noprj.zip", files, []string{"p.shp"})
	_, err = run(t, "geo.convert", []string{zp}, "x.geojson", nil)
	assertUserError(t, err)
}
