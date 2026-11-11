package ops

// Map data: GeoJSON, KML/KMZ, GPX, zipped Shapefile and CSV points on one
// small feature model. Every reader pairs with every writer, plus track
// statistics and Douglas-Peucker simplification.
//
// Coordinates are always [longitude, latitude] or [longitude, latitude,
// elevation] in WGS84 degrees. Projected data (shapefiles in a metre-based
// system, for example) is refused instead of being written with wrong
// coordinates.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func init() {
	register(Handler{Name: "geo.convert", Run: gisConvert})
	register(Handler{Name: "geo.stats", Run: gisStats})
	register(Handler{Name: "geo.simplify", Run: gisSimplify})
}

const (
	gisMaxFileBytes = 1 << 30
	gisEarthRadius  = 6371008.8
)

// ---------------------------------------------------------------- model

// gisGeom is one geometry. Type is a GeoJSON type name; the field that
// holds the coordinates depends on it:
//
//	Point            P
//	MultiPoint       MP
//	LineString       L (one line)
//	MultiLineString  L (many lines)
//	Polygon          L (rings, outer first)
//	MultiPolygon     PG
//
// T optionally holds a timestamp per vertex, parallel to L, for line types.
type gisGeom struct {
	Type string
	P    []float64
	MP   [][]float64
	L    [][][]float64
	T    [][]time.Time
	PG   [][][][]float64
}

type gisFeature struct {
	Geom  gisGeom
	Props map[string]any
}

type gisDoc struct {
	Features []gisFeature
}

func (g gisGeom) isLine() bool { return g.Type == "LineString" || g.Type == "MultiLineString" }

func (g gisGeom) hasTimes() bool {
	if !g.isLine() {
		return false
	}
	for _, ts := range g.T {
		for _, t := range ts {
			if !t.IsZero() {
				return true
			}
		}
	}
	return false
}

// each calls fn for every position of the geometry.
func (g gisGeom) each(fn func(p []float64)) {
	if g.P != nil {
		fn(g.P)
	}
	for _, p := range g.MP {
		fn(p)
	}
	for _, line := range g.L {
		for _, p := range line {
			fn(p)
		}
	}
	for _, poly := range g.PG {
		for _, ring := range poly {
			for _, p := range ring {
				fn(p)
			}
		}
	}
}

func gisName(f gisFeature) string {
	for _, k := range []string{"name", "Name", "NAME", "title"} {
		if s, ok := f.Props[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func gisPropString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func gisCopyProps(p map[string]any) map[string]any {
	out := make(map[string]any, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	return out
}

func gisFmtF(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func gisParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func gisFmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// gisValidate refuses data that is evidently not longitude/latitude.
func gisValidate(doc *gisDoc) error {
	var bad []float64
	for _, f := range doc.Features {
		f.Geom.each(func(p []float64) {
			if bad != nil {
				return
			}
			if len(p) < 2 || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) ||
				math.Abs(p[0]) > 360 || math.Abs(p[1]) > 90 {
				bad = p
			}
		})
		if bad != nil {
			break
		}
	}
	if bad != nil {
		if len(bad) >= 2 {
			return Userf("coordinates such as (%v, %v) are not longitude/latitude degrees; the data looks projected. Reproject it to WGS84 first", bad[0], bad[1])
		}
		return Userf("a coordinate has fewer than two numbers")
	}
	return nil
}

// ---------------------------------------------------------------- dispatch

func gisInExt(job Job) string {
	e := strings.ToLower(filepath.Ext(job.Name(0)))
	if e == "" {
		e = strings.ToLower(filepath.Ext(job.Input()))
	}
	return e
}

func gisReadFileBytes(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, gisMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > gisMaxFileBytes {
		return nil, Userf("file is larger than 1 GiB, which is more than Asteria reads as map data")
	}
	return data, nil
}

func gisRead(path, ext string) (*gisDoc, error) {
	var doc *gisDoc
	var err error
	switch ext {
	case ".geojson", ".json":
		var data []byte
		if data, err = gisReadFileBytes(path); err == nil {
			doc, err = gisParseGeoJSON(data)
		}
	case ".kml":
		var data []byte
		if data, err = gisReadFileBytes(path); err == nil {
			doc, err = gisParseKML(data)
		}
	case ".kmz":
		doc, err = gisReadKMZ(path)
	case ".gpx":
		var data []byte
		if data, err = gisReadFileBytes(path); err == nil {
			doc, err = gisParseGPX(data)
		}
	case ".zip":
		doc, err = gisReadShapefileZip(path)
	case ".csv", ".tsv":
		var data []byte
		if data, err = gisReadFileBytes(path); err == nil {
			doc, err = gisParseCSV(data, ext)
		}
	default:
		return nil, Userf("%s is not a map format Asteria reads (GeoJSON, KML, KMZ, GPX, zipped Shapefile, CSV)", ext)
	}
	if err != nil {
		return nil, err
	}
	if len(doc.Features) == 0 {
		return nil, Userf("no map features found in this file")
	}
	if err := gisValidate(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func gisWrite(path, ext string, doc *gisDoc) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	switch ext {
	case ".geojson", ".json":
		err = gisWriteGeoJSON(w, doc)
	case ".kml":
		err = gisWriteKML(w, doc)
	case ".kmz":
		err = gisWriteKMZ(w, doc)
	case ".gpx":
		err = gisWriteGPX(w, doc)
	case ".csv":
		err = gisWriteCSV(w, doc)
	default:
		err = Userf("cannot write %s map files", ext)
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func gisConvert(ctx context.Context, job Job) error {
	inExt := gisInExt(job)
	outExt := strings.ToLower(filepath.Ext(job.Output))
	doc, err := gisRead(job.Input(), inExt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	job.Progress(0.5)
	return gisWrite(job.Output, outExt, doc)
}

// ---------------------------------------------------------------- GeoJSON

type gjGeom struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
	Geometries  []gjGeom        `json:"geometries"`
}

type gjTop struct {
	Type        string          `json:"type"`
	Features    []gjTop         `json:"features"`
	Geometry    *gjGeom         `json:"geometry"`
	Properties  map[string]any  `json:"properties"`
	Coordinates json.RawMessage `json:"coordinates"`
	Geometries  []gjGeom        `json:"geometries"`
}

func gisParseGeoJSON(data []byte) (*gisDoc, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var top gjTop
	if err := dec.Decode(&top); err != nil {
		return nil, Userf("not valid GeoJSON: %v", err)
	}
	doc := &gisDoc{}
	var walk func(t gjTop, depth int) error
	walk = func(t gjTop, depth int) error {
		if depth > 8 {
			return Userf("GeoJSON is nested too deeply")
		}
		switch t.Type {
		case "FeatureCollection":
			for _, f := range t.Features {
				if err := walk(f, depth+1); err != nil {
					return err
				}
			}
		case "Feature":
			if t.Geometry == nil {
				return nil
			}
			geoms, err := gisGJGeoms(*t.Geometry, 0)
			if err != nil {
				return err
			}
			for _, g := range geoms {
				f := gisFeature{Geom: g, Props: gisCopyProps(t.Properties)}
				gisTakeCoordTimes(&f)
				doc.Features = append(doc.Features, f)
			}
		case "Point", "MultiPoint", "LineString", "MultiLineString", "Polygon", "MultiPolygon", "GeometryCollection":
			geoms, err := gisGJGeoms(gjGeom{Type: t.Type, Coordinates: t.Coordinates, Geometries: t.Geometries}, 0)
			if err != nil {
				return err
			}
			for _, g := range geoms {
				doc.Features = append(doc.Features, gisFeature{Geom: g, Props: map[string]any{}})
			}
		default:
			return Userf("not GeoJSON: expected a FeatureCollection, Feature or geometry, got type %q", t.Type)
		}
		return nil
	}
	if err := walk(top, 0); err != nil {
		return nil, err
	}
	return doc, nil
}

func gisCheckPos(p []float64) error {
	if len(p) < 2 {
		return Userf("a GeoJSON position needs at least longitude and latitude")
	}
	return nil
}

func gisCheckPositions(ps [][]float64) error {
	for _, p := range ps {
		if err := gisCheckPos(p); err != nil {
			return err
		}
	}
	return nil
}

func gisGJGeoms(g gjGeom, depth int) ([]gisGeom, error) {
	if depth > 8 {
		return nil, Userf("GeoJSON geometries are nested too deeply")
	}
	empty := len(g.Coordinates) == 0 || string(g.Coordinates) == "null"
	bad := func(err error) error { return Userf("invalid %s coordinates: %v", g.Type, err) }
	switch g.Type {
	case "Point":
		if empty {
			return nil, nil
		}
		var c []float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, bad(err)
		}
		if len(c) == 0 {
			return nil, nil
		}
		if err := gisCheckPos(c); err != nil {
			return nil, err
		}
		return []gisGeom{{Type: "Point", P: c}}, nil
	case "MultiPoint":
		if empty {
			return nil, nil
		}
		var c [][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, bad(err)
		}
		if len(c) == 0 {
			return nil, nil
		}
		if err := gisCheckPositions(c); err != nil {
			return nil, err
		}
		return []gisGeom{{Type: "MultiPoint", MP: c}}, nil
	case "LineString":
		if empty {
			return nil, nil
		}
		var c [][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, bad(err)
		}
		if len(c) == 0 {
			return nil, nil
		}
		if err := gisCheckPositions(c); err != nil {
			return nil, err
		}
		return []gisGeom{{Type: "LineString", L: [][][]float64{c}}}, nil
	case "MultiLineString", "Polygon":
		if empty {
			return nil, nil
		}
		var c [][][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, bad(err)
		}
		if len(c) == 0 {
			return nil, nil
		}
		for _, l := range c {
			if err := gisCheckPositions(l); err != nil {
				return nil, err
			}
		}
		return []gisGeom{{Type: g.Type, L: c}}, nil
	case "MultiPolygon":
		if empty {
			return nil, nil
		}
		var c [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, bad(err)
		}
		if len(c) == 0 {
			return nil, nil
		}
		for _, poly := range c {
			for _, l := range poly {
				if err := gisCheckPositions(l); err != nil {
					return nil, err
				}
			}
		}
		return []gisGeom{{Type: "MultiPolygon", PG: c}}, nil
	case "GeometryCollection":
		var out []gisGeom
		for _, sub := range g.Geometries {
			gs, err := gisGJGeoms(sub, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, gs...)
		}
		return out, nil
	}
	return nil, Userf("unsupported GeoJSON geometry type %q", g.Type)
}

// gisTakeCoordTimes moves a "coordTimes" property (the togeojson convention
// for per-vertex timestamps) into the geometry.
func gisTakeCoordTimes(f *gisFeature) {
	raw, ok := f.Props["coordTimes"]
	if !ok || !f.Geom.isLine() {
		return
	}
	delete(f.Props, "coordTimes")
	list, ok := raw.([]any)
	if !ok {
		return
	}
	parse := func(v any) []time.Time {
		arr, _ := v.([]any)
		out := make([]time.Time, len(arr))
		for i, x := range arr {
			s, _ := x.(string)
			out[i], _ = gisParseTime(s)
		}
		return out
	}
	var ts [][]time.Time
	if f.Geom.Type == "LineString" {
		ts = [][]time.Time{parse(list)}
	} else {
		for _, v := range list {
			ts = append(ts, parse(v))
		}
	}
	if len(ts) != len(f.Geom.L) {
		return
	}
	for i := range ts {
		if len(ts[i]) != len(f.Geom.L[i]) {
			return
		}
	}
	f.Geom.T = ts
}

type gjOutGeom struct {
	Type        string `json:"type"`
	Coordinates any    `json:"coordinates"`
}

type gjOutFeature struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   gjOutGeom      `json:"geometry"`
}

func gisCoordsAny(g gisGeom) any {
	switch g.Type {
	case "Point":
		return g.P
	case "MultiPoint":
		return g.MP
	case "LineString":
		if len(g.L) > 0 {
			return g.L[0]
		}
		return [][]float64{}
	case "MultiPolygon":
		return g.PG
	}
	return g.L
}

func gisWriteGeoJSON(w io.Writer, doc *gisDoc) error {
	if _, err := io.WriteString(w, "{\"type\":\"FeatureCollection\",\"features\":[\n"); err != nil {
		return err
	}
	for i, f := range doc.Features {
		props := gisCopyProps(f.Props)
		if f.Geom.hasTimes() {
			var ct any
			enc := func(ts []time.Time) []string {
				out := make([]string, len(ts))
				for j, t := range ts {
					if !t.IsZero() {
						out[j] = gisFmtTime(t)
					}
				}
				return out
			}
			if f.Geom.Type == "LineString" {
				ct = enc(f.Geom.T[0])
			} else {
				all := make([][]string, len(f.Geom.T))
				for j, ts := range f.Geom.T {
					all[j] = enc(ts)
				}
				ct = all
			}
			props["coordTimes"] = ct
		}
		b, err := json.Marshal(gjOutFeature{Type: "Feature", Properties: props, Geometry: gjOutGeom{Type: f.Geom.Type, Coordinates: gisCoordsAny(f.Geom)}})
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		sep := ",\n"
		if i == len(doc.Features)-1 {
			sep = "\n"
		}
		if _, err := io.WriteString(w, sep); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]}\n")
	return err
}

// ---------------------------------------------------------------- XML tree

type gisNode struct {
	Name     string
	Attrs    map[string]string
	Children []*gisNode
	Text     string
}

func (n *gisNode) child(name string) *gisNode {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (n *gisNode) childrenNamed(name string) []*gisNode {
	if n == nil {
		return nil
	}
	var out []*gisNode
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *gisNode) text() string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.Text)
}

func gisParseXML(data []byte) (*gisNode, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	root := &gisNode{}
	stack := []*gisNode{root}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Userf("not valid XML: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &gisNode{Name: t.Name.Local}
			if len(t.Attr) > 0 {
				n.Attrs = make(map[string]string, len(t.Attr))
				for _, a := range t.Attr {
					n.Attrs[a.Name.Local] = a.Value
				}
			}
			top := stack[len(stack)-1]
			top.Children = append(top.Children, n)
			stack = append(stack, n)
			if len(stack) > 256 {
				return nil, Userf("XML is nested too deeply")
			}
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].Text += string(t)
		}
	}
	if len(root.Children) == 0 {
		return nil, Userf("not valid XML: no root element")
	}
	return root.Children[0], nil
}

func gisEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ---------------------------------------------------------------- KML

var gisCommaSpace = regexp.MustCompile(`\s*,\s*`)

func gisParseCoordList(s string) [][]float64 {
	s = gisCommaSpace.ReplaceAllString(s, ",")
	var out [][]float64
	for _, tup := range strings.Fields(s) {
		parts := strings.Split(tup, ",")
		if len(parts) < 2 {
			continue
		}
		lon, err1 := strconv.ParseFloat(parts[0], 64)
		lat, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		pos := []float64{lon, lat}
		if len(parts) >= 3 && parts[2] != "" {
			if z, err := strconv.ParseFloat(parts[2], 64); err == nil {
				pos = append(pos, z)
			}
		}
		out = append(out, pos)
	}
	return out
}

func gisParseKML(data []byte) (*gisDoc, error) {
	root, err := gisParseXML(data)
	if err != nil {
		return nil, err
	}
	if root.Name != "kml" && root.Name != "Document" && root.Name != "Folder" && root.Name != "Placemark" {
		return nil, Userf("not a KML file (root element is <%s>)", root.Name)
	}
	doc := &gisDoc{}
	if root.Name == "Placemark" {
		gisKMLPlacemark(root, doc)
	} else {
		gisKMLWalk(root, doc, 0)
	}
	return doc, nil
}

func gisKMLWalk(n *gisNode, doc *gisDoc, depth int) {
	if depth > 128 {
		return
	}
	for _, c := range n.Children {
		switch c.Name {
		case "Placemark":
			gisKMLPlacemark(c, doc)
		case "Document", "Folder":
			gisKMLWalk(c, doc, depth+1)
		}
	}
}

func gisKMLPlacemark(pm *gisNode, doc *gisDoc) {
	props := map[string]any{}
	if s := pm.child("name").text(); s != "" {
		props["name"] = s
	}
	if s := pm.child("description").text(); s != "" {
		props["description"] = s
	}
	if ts := pm.child("TimeStamp"); ts != nil {
		if s := ts.child("when").text(); s != "" {
			props["time"] = s
		}
	} else if sp := pm.child("TimeSpan"); sp != nil {
		if s := sp.child("begin").text(); s != "" {
			props["time"] = s
		}
	}
	if ed := pm.child("ExtendedData"); ed != nil {
		for _, d := range ed.childrenNamed("Data") {
			if k := d.Attrs["name"]; k != "" {
				props[k] = d.child("value").text()
			}
		}
		for _, sd := range ed.childrenNamed("SchemaData") {
			for _, d := range sd.childrenNamed("SimpleData") {
				if k := d.Attrs["name"]; k != "" {
					props[k] = d.text()
				}
			}
		}
	}
	for _, g := range gisMergeGeoms(gisKMLGeoms(pm, 0)) {
		doc.Features = append(doc.Features, gisFeature{Geom: g, Props: gisCopyProps(props)})
	}
}

func gisKMLGeoms(n *gisNode, depth int) []gisGeom {
	if depth > 16 {
		return nil
	}
	var out []gisGeom
	for _, c := range n.Children {
		switch c.Name {
		case "Point":
			if pts := gisParseCoordList(c.child("coordinates").text()); len(pts) > 0 {
				out = append(out, gisGeom{Type: "Point", P: pts[0]})
			}
		case "LineString", "LinearRing":
			if pts := gisParseCoordList(c.child("coordinates").text()); len(pts) > 0 {
				out = append(out, gisGeom{Type: "LineString", L: [][][]float64{pts}})
			}
		case "Polygon":
			var rings [][][]float64
			if outer := c.child("outerBoundaryIs").child("LinearRing"); outer != nil {
				if pts := gisParseCoordList(outer.child("coordinates").text()); len(pts) > 0 {
					rings = append(rings, pts)
				}
			}
			if len(rings) > 0 {
				for _, ib := range c.childrenNamed("innerBoundaryIs") {
					if lr := ib.child("LinearRing"); lr != nil {
						if pts := gisParseCoordList(lr.child("coordinates").text()); len(pts) > 0 {
							rings = append(rings, pts)
						}
					}
				}
				out = append(out, gisGeom{Type: "Polygon", L: rings})
			}
		case "MultiGeometry":
			out = append(out, gisMergeGeoms(gisKMLGeoms(c, depth+1))...)
		case "Track":
			if g, ok := gisKMLTrack(c); ok {
				out = append(out, g)
			}
		case "MultiTrack":
			var sub []gisGeom
			for _, t := range c.childrenNamed("Track") {
				if g, ok := gisKMLTrack(t); ok {
					sub = append(sub, g)
				}
			}
			out = append(out, gisMergeGeoms(sub)...)
		}
	}
	return out
}

func gisKMLTrack(n *gisNode) (gisGeom, bool) {
	var whens []time.Time
	for _, w := range n.childrenNamed("when") {
		t, _ := gisParseTime(w.text())
		whens = append(whens, t)
	}
	var line [][]float64
	for _, c := range n.childrenNamed("coord") {
		f := strings.Fields(c.text())
		if len(f) < 2 {
			continue
		}
		lon, e1 := strconv.ParseFloat(f[0], 64)
		lat, e2 := strconv.ParseFloat(f[1], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		pos := []float64{lon, lat}
		if len(f) >= 3 {
			if z, err := strconv.ParseFloat(f[2], 64); err == nil {
				pos = append(pos, z)
			}
		}
		line = append(line, pos)
	}
	if len(line) == 0 {
		return gisGeom{}, false
	}
	ts := make([]time.Time, len(line))
	copy(ts, whens)
	return gisGeom{Type: "LineString", L: [][][]float64{line}, T: [][]time.Time{ts}}, true
}

// gisMergeGeoms folds several same-kind geometries into the Multi* type.
func gisMergeGeoms(gs []gisGeom) []gisGeom {
	if len(gs) <= 1 {
		return gs
	}
	kind := gs[0].Type
	for _, g := range gs {
		if g.Type != kind {
			return gs
		}
	}
	switch kind {
	case "Point":
		m := gisGeom{Type: "MultiPoint"}
		for _, g := range gs {
			m.MP = append(m.MP, g.P)
		}
		return []gisGeom{m}
	case "LineString":
		m := gisGeom{Type: "MultiLineString"}
		hasT := false
		for _, g := range gs {
			if g.T != nil {
				hasT = true
			}
		}
		for _, g := range gs {
			m.L = append(m.L, g.L[0])
			if hasT {
				if g.T != nil {
					m.T = append(m.T, g.T[0])
				} else {
					m.T = append(m.T, make([]time.Time, len(g.L[0])))
				}
			}
		}
		return []gisGeom{m}
	case "Polygon":
		m := gisGeom{Type: "MultiPolygon"}
		for _, g := range gs {
			m.PG = append(m.PG, g.L)
		}
		return []gisGeom{m}
	}
	return gs
}

func gisReadKMZ(path string) (*gisDoc, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, Userf("not a readable KMZ (zip) file: %v", err)
	}
	defer zr.Close()
	var pick *zip.File
	for _, f := range zr.File {
		lower := strings.ToLower(f.Name)
		if !strings.HasSuffix(lower, ".kml") || f.FileInfo().IsDir() {
			continue
		}
		if pick == nil || strings.EqualFold(f.Name, "doc.kml") {
			pick = f
		}
	}
	if pick == nil {
		return nil, Userf("this KMZ contains no .kml file")
	}
	data, err := gisZipRead(pick)
	if err != nil {
		return nil, err
	}
	return gisParseKML(data)
}

func gisZipRead(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > gisMaxFileBytes {
		return nil, Userf("%s is larger than 1 GiB inside the zip", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, gisMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > gisMaxFileBytes {
		return nil, Userf("%s is larger than 1 GiB inside the zip", f.Name)
	}
	return data, nil
}

func gisFmtPos(p []float64) string {
	if len(p) >= 3 {
		return gisFmtF(p[0]) + "," + gisFmtF(p[1]) + "," + gisFmtF(p[2])
	}
	return gisFmtF(p[0]) + "," + gisFmtF(p[1])
}

func gisCoordString(ps [][]float64) string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(gisFmtPos(p))
	}
	return b.String()
}

func gisKMLTrackXML(b *strings.Builder, line [][]float64, ts []time.Time) {
	b.WriteString("<gx:Track>")
	for i := range line {
		b.WriteString("<when>")
		if i < len(ts) && !ts[i].IsZero() {
			b.WriteString(gisFmtTime(ts[i]))
		}
		b.WriteString("</when>")
	}
	for _, p := range line {
		b.WriteString("<gx:coord>")
		b.WriteString(gisFmtF(p[0]) + " " + gisFmtF(p[1]))
		if len(p) >= 3 {
			b.WriteString(" " + gisFmtF(p[2]))
		}
		b.WriteString("</gx:coord>")
	}
	b.WriteString("</gx:Track>")
}

func gisKMLPolygonXML(b *strings.Builder, rings [][][]float64) {
	b.WriteString("<Polygon>")
	for i, r := range rings {
		if i == 0 {
			b.WriteString("<outerBoundaryIs><LinearRing><coordinates>")
		} else {
			b.WriteString("<innerBoundaryIs><LinearRing><coordinates>")
		}
		b.WriteString(gisCoordString(r))
		if i == 0 {
			b.WriteString("</coordinates></LinearRing></outerBoundaryIs>")
		} else {
			b.WriteString("</coordinates></LinearRing></innerBoundaryIs>")
		}
	}
	b.WriteString("</Polygon>")
}

func gisKMLGeomXML(b *strings.Builder, g gisGeom) {
	switch g.Type {
	case "Point":
		b.WriteString("<Point><coordinates>" + gisFmtPos(g.P) + "</coordinates></Point>")
	case "MultiPoint":
		b.WriteString("<MultiGeometry>")
		for _, p := range g.MP {
			b.WriteString("<Point><coordinates>" + gisFmtPos(p) + "</coordinates></Point>")
		}
		b.WriteString("</MultiGeometry>")
	case "LineString":
		if len(g.L) == 0 {
			return
		}
		if g.hasTimes() {
			gisKMLTrackXML(b, g.L[0], g.T[0])
			return
		}
		b.WriteString("<LineString><tessellate>1</tessellate><coordinates>" + gisCoordString(g.L[0]) + "</coordinates></LineString>")
	case "MultiLineString":
		if g.hasTimes() {
			b.WriteString("<gx:MultiTrack>")
			for i, l := range g.L {
				var ts []time.Time
				if i < len(g.T) {
					ts = g.T[i]
				}
				gisKMLTrackXML(b, l, ts)
			}
			b.WriteString("</gx:MultiTrack>")
			return
		}
		b.WriteString("<MultiGeometry>")
		for _, l := range g.L {
			b.WriteString("<LineString><tessellate>1</tessellate><coordinates>" + gisCoordString(l) + "</coordinates></LineString>")
		}
		b.WriteString("</MultiGeometry>")
	case "Polygon":
		gisKMLPolygonXML(b, g.L)
	case "MultiPolygon":
		b.WriteString("<MultiGeometry>")
		for _, poly := range g.PG {
			gisKMLPolygonXML(b, poly)
		}
		b.WriteString("</MultiGeometry>")
	}
}

func gisWriteKML(w io.Writer, doc *gisDoc) error {
	bw := bufio.NewWriter(w)
	bw.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	bw.WriteString("<kml xmlns=\"http://www.opengis.net/kml/2.2\" xmlns:gx=\"http://www.google.com/kml/ext/2.2\">\n<Document>\n")
	for _, f := range doc.Features {
		var b strings.Builder
		b.WriteString("<Placemark>")
		if s := gisName(f); s != "" {
			b.WriteString("<name>" + gisEsc(s) + "</name>")
		}
		if s := gisPropString(f.Props["description"]); s != "" {
			b.WriteString("<description>" + gisEsc(s) + "</description>")
		}
		if s := gisPropString(f.Props["time"]); s != "" && !f.Geom.hasTimes() {
			if t, ok := gisParseTime(s); ok {
				b.WriteString("<TimeStamp><when>" + gisFmtTime(t) + "</when></TimeStamp>")
			}
		}
		keys := gisExtraKeys(f.Props, "name", "Name", "NAME", "title", "description", "time")
		if len(keys) > 0 {
			b.WriteString("<ExtendedData>")
			for _, k := range keys {
				b.WriteString("<Data name=\"" + gisEsc(k) + "\"><value>" + gisEsc(gisPropString(f.Props[k])) + "</value></Data>")
			}
			b.WriteString("</ExtendedData>")
		}
		gisKMLGeomXML(&b, f.Geom)
		b.WriteString("</Placemark>\n")
		bw.WriteString(b.String())
	}
	bw.WriteString("</Document>\n</kml>\n")
	return bw.Flush()
}

func gisExtraKeys(props map[string]any, skip ...string) []string {
	var keys []string
outer:
	for k := range props {
		for _, s := range skip {
			if k == s {
				continue outer
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func gisWriteKMZ(w io.Writer, doc *gisDoc) error {
	zw := zip.NewWriter(w)
	ew, err := zw.Create("doc.kml")
	if err != nil {
		return err
	}
	if err := gisWriteKML(ew, doc); err != nil {
		return err
	}
	return zw.Close()
}

// ---------------------------------------------------------------- GPX

func gisParseGPX(data []byte) (*gisDoc, error) {
	root, err := gisParseXML(data)
	if err != nil {
		return nil, err
	}
	if root.Name != "gpx" {
		return nil, Userf("not a GPX file (root element is <%s>)", root.Name)
	}
	doc := &gisDoc{}
	pos := func(n *gisNode) ([]float64, time.Time, bool) {
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(n.Attrs["lat"]), 64)
		lon, e2 := strconv.ParseFloat(strings.TrimSpace(n.Attrs["lon"]), 64)
		if e1 != nil || e2 != nil {
			return nil, time.Time{}, false
		}
		p := []float64{lon, lat}
		if s := n.child("ele").text(); s != "" {
			if z, err := strconv.ParseFloat(s, 64); err == nil {
				p = append(p, z)
			}
		}
		t, _ := gisParseTime(n.child("time").text())
		return p, t, true
	}
	props := func(n *gisNode) map[string]any {
		m := map[string]any{}
		if s := n.child("name").text(); s != "" {
			m["name"] = s
		}
		d := n.child("desc").text()
		if d == "" {
			d = n.child("cmt").text()
		}
		if d != "" {
			m["description"] = d
		}
		return m
	}
	line := func(pts []*gisNode) ([][]float64, []time.Time) {
		var l [][]float64
		var ts []time.Time
		for _, pt := range pts {
			p, t, ok := pos(pt)
			if !ok {
				continue
			}
			l = append(l, p)
			ts = append(ts, t)
		}
		return l, ts
	}
	for _, c := range root.Children {
		switch c.Name {
		case "wpt":
			p, t, ok := pos(c)
			if !ok {
				continue
			}
			m := props(c)
			if !t.IsZero() {
				m["time"] = gisFmtTime(t)
			}
			if s := c.child("sym").text(); s != "" {
				m["sym"] = s
			}
			doc.Features = append(doc.Features, gisFeature{Geom: gisGeom{Type: "Point", P: p}, Props: m})
		case "rte":
			l, ts := line(c.childrenNamed("rtept"))
			if len(l) == 0 {
				continue
			}
			doc.Features = append(doc.Features, gisFeature{Geom: gisGeom{Type: "LineString", L: [][][]float64{l}, T: [][]time.Time{ts}}, Props: props(c)})
		case "trk":
			var lines [][][]float64
			var times [][]time.Time
			for _, seg := range c.childrenNamed("trkseg") {
				l, ts := line(seg.childrenNamed("trkpt"))
				if len(l) == 0 {
					continue
				}
				lines = append(lines, l)
				times = append(times, ts)
			}
			if len(lines) == 0 {
				continue
			}
			g := gisGeom{Type: "LineString", L: lines, T: times}
			if len(lines) > 1 {
				g.Type = "MultiLineString"
			}
			doc.Features = append(doc.Features, gisFeature{Geom: g, Props: props(c)})
		}
	}
	return doc, nil
}

func gisWriteGPX(w io.Writer, doc *gisDoc) error {
	bw := bufio.NewWriter(w)
	bw.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	bw.WriteString("<gpx version=\"1.1\" creator=\"Asteria\" xmlns=\"http://www.topografix.com/GPX/1/1\">\n")
	pt := func(tag string, p []float64, t time.Time) string {
		var b strings.Builder
		b.WriteString("<" + tag + " lat=\"" + gisFmtF(p[1]) + "\" lon=\"" + gisFmtF(p[0]) + "\">")
		if len(p) >= 3 {
			b.WriteString("<ele>" + gisFmtF(p[2]) + "</ele>")
		}
		if !t.IsZero() {
			b.WriteString("<time>" + gisFmtTime(t) + "</time>")
		}
		return b.String()
	}
	for _, f := range doc.Features {
		name := gisName(f)
		desc := gisPropString(f.Props["description"])
		meta := ""
		if name != "" {
			meta += "<name>" + gisEsc(name) + "</name>"
		}
		if desc != "" {
			meta += "<desc>" + gisEsc(desc) + "</desc>"
		}
		var ptTime time.Time
		if t, ok := gisParseTime(gisPropString(f.Props["time"])); ok {
			ptTime = t
		}
		g := f.Geom
		switch g.Type {
		case "Point":
			bw.WriteString(pt("wpt", g.P, ptTime) + meta + "</wpt>\n")
		case "MultiPoint":
			for _, p := range g.MP {
				bw.WriteString(pt("wpt", p, ptTime) + meta + "</wpt>\n")
			}
		case "LineString", "MultiLineString", "Polygon", "MultiPolygon":
			rings := g.L
			if g.Type == "MultiPolygon" {
				rings = nil
				for _, poly := range g.PG {
					rings = append(rings, poly...)
				}
			}
			bw.WriteString("<trk>" + meta)
			for i, l := range rings {
				bw.WriteString("<trkseg>")
				for j, p := range l {
					var t time.Time
					if g.isLine() && i < len(g.T) && j < len(g.T[i]) {
						t = g.T[i][j]
					}
					bw.WriteString(pt("trkpt", p, t) + "</trkpt>")
				}
				bw.WriteString("</trkseg>")
			}
			bw.WriteString("</trk>\n")
		}
	}
	bw.WriteString("</gpx>\n")
	return bw.Flush()
}

// ---------------------------------------------------------------- CSV

func gisHeaderIndex(headers []string, prefs ...string) int {
	for _, want := range prefs {
		for i, h := range headers {
			if h == want {
				return i
			}
		}
	}
	return -1
}

func gisParseCSV(data []byte, ext string) (*gisDoc, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	comma := ','
	if ext == ".tsv" {
		comma = '\t'
	} else {
		line := data
		if i := bytes.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		best := bytes.Count(line, []byte{','})
		for _, c := range []rune{';', '\t'} {
			if n := bytes.Count(line, []byte(string(c))); n > best {
				best, comma = n, c
			}
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return nil, Userf("this CSV has no header row")
	}
	low := make([]string, len(header))
	for i, h := range header {
		low[i] = strings.ToLower(strings.TrimSpace(h))
	}
	latI := gisHeaderIndex(low, "latitude", "lat", "latitude_deg", "lat_deg", "y", "y_coord", "ycoord")
	lonI := gisHeaderIndex(low, "longitude", "lon", "lng", "long", "longitude_deg", "lon_deg", "x", "x_coord", "xcoord")
	if latI < 0 || lonI < 0 {
		return nil, Userf("couldn't find latitude/longitude columns; name them lat/lon, latitude/longitude, lng or y/x")
	}
	eleI := gisHeaderIndex(low, "elevation", "ele", "altitude", "alt", "z")
	nameI := gisHeaderIndex(low, "name", "title", "label")
	doc := &gisDoc{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Userf("this CSV could not be read: %v", err)
		}
		if latI >= len(rec) || lonI >= len(rec) {
			continue
		}
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(rec[latI]), 64)
		lon, e2 := strconv.ParseFloat(strings.TrimSpace(rec[lonI]), 64)
		if e1 != nil || e2 != nil || math.IsNaN(lat) || math.IsNaN(lon) {
			continue
		}
		p := []float64{lon, lat}
		if eleI >= 0 && eleI < len(rec) {
			if z, err := strconv.ParseFloat(strings.TrimSpace(rec[eleI]), 64); err == nil && !math.IsNaN(z) && !math.IsInf(z, 0) {
				p = append(p, z)
			}
		}
		props := map[string]any{}
		for i, v := range rec {
			if i == latI || i == lonI || i == eleI || i >= len(header) {
				continue
			}
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if i == nameI {
				props["name"] = v
				continue
			}
			props[strings.TrimSpace(header[i])] = gisCSVValue(v)
		}
		doc.Features = append(doc.Features, gisFeature{Geom: gisGeom{Type: "Point", P: p}, Props: props})
	}
	if len(doc.Features) == 0 {
		return nil, Userf("no rows with valid latitude/longitude numbers in this CSV")
	}
	return doc, nil
}

func gisCSVValue(v string) any {
	if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		if len(v) > 1 && v[0] == '0' && v[1] != '.' {
			return v
		}
		if strings.ContainsAny(v, "xXpP_") || strings.HasPrefix(strings.ToLower(v), "inf") || strings.HasPrefix(strings.ToLower(v), "nan") {
			return v
		}
		return json.Number(v)
	}
	return v
}

func gisWriteCSV(w io.Writer, doc *gisDoc) error {
	anyEle, anyTime, anyLine := false, false, false
	extras := map[string]bool{}
	for _, f := range doc.Features {
		f.Geom.each(func(p []float64) {
			if len(p) >= 3 {
				anyEle = true
			}
		})
		if f.Geom.Type != "Point" {
			anyLine = true
		}
		if gisPropString(f.Props["time"]) != "" || f.Geom.hasTimes() {
			anyTime = true
		}
		for _, k := range gisExtraKeys(f.Props, "name", "time") {
			extras[k] = true
		}
	}
	var extraKeys []string
	for k := range extras {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	header := []string{"id", "name", "latitude", "longitude"}
	if anyEle {
		header = append(header, "elevation")
	}
	if anyTime {
		header = append(header, "time")
	}
	if anyLine {
		header = append(header, "type", "part", "vertex")
	}
	header = append(header, extraKeys...)
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	row := func(id int, f gisFeature, p []float64, t time.Time, part, vertex string) error {
		rec := []string{strconv.Itoa(id), gisName(f), gisFmtF(p[1]), gisFmtF(p[0])}
		if anyEle {
			e := ""
			if len(p) >= 3 {
				e = gisFmtF(p[2])
			}
			rec = append(rec, e)
		}
		if anyTime {
			ts := gisPropString(f.Props["time"])
			if !t.IsZero() {
				ts = gisFmtTime(t)
			}
			rec = append(rec, ts)
		}
		if anyLine {
			rec = append(rec, f.Geom.Type, part, vertex)
		}
		for _, k := range extraKeys {
			rec = append(rec, gisPropString(f.Props[k]))
		}
		return cw.Write(rec)
	}
	for i, f := range doc.Features {
		id := i + 1
		g := f.Geom
		var err error
		switch g.Type {
		case "Point":
			err = row(id, f, g.P, time.Time{}, "", "")
		case "MultiPoint":
			for j, p := range g.MP {
				if err = row(id, f, p, time.Time{}, "", strconv.Itoa(j+1)); err != nil {
					break
				}
			}
		case "MultiPolygon":
			part := 0
			for _, poly := range g.PG {
				for _, ring := range poly {
					part++
					for j, p := range ring {
						if err = row(id, f, p, time.Time{}, strconv.Itoa(part), strconv.Itoa(j+1)); err != nil {
							break
						}
					}
				}
			}
		default:
			for pi, l := range g.L {
				for j, p := range l {
					var t time.Time
					if g.isLine() && pi < len(g.T) && j < len(g.T[pi]) {
						t = g.T[pi][j]
					}
					if err = row(id, f, p, t, strconv.Itoa(pi+1), strconv.Itoa(j+1)); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ---------------------------------------------------------------- Shapefile

func gisReadShapefileZip(path string) (*gisDoc, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, Userf("not a readable zip file: %v", err)
	}
	defer zr.Close()
	byName := map[string]*zip.File{}
	var shps []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		lower := strings.ToLower(strings.ReplaceAll(f.Name, "\\", "/"))
		if strings.Contains(lower, "__macosx/") || strings.HasPrefix(lower[strings.LastIndex(lower, "/")+1:], "._") {
			continue
		}
		byName[lower] = f
		if strings.HasSuffix(lower, ".shp") {
			shps = append(shps, f)
		}
	}
	if len(shps) == 0 {
		return nil, Userf("this zip has no .shp file; zip a Shapefile (.shp, .shx, .dbf, .prj) and try again")
	}
	doc := &gisDoc{}
	for _, shp := range shps {
		lower := strings.ToLower(strings.ReplaceAll(shp.Name, "\\", "/"))
		base := lower[:len(lower)-4]
		prj := ""
		if f := byName[base+".prj"]; f != nil {
			b, err := gisZipRead(f)
			if err != nil {
				return nil, err
			}
			prj = string(b)
		}
		if err := gisCheckPRJ(prj); err != nil {
			return nil, err
		}
		shpData, err := gisZipRead(shp)
		if err != nil {
			return nil, err
		}
		var dbf *gisDBF
		if f := byName[base+".dbf"]; f != nil {
			b, err := gisZipRead(f)
			if err != nil {
				return nil, err
			}
			if dbf, err = gisParseDBF(b); err != nil {
				return nil, err
			}
		}
		feats, err := gisParseSHP(shpData, dbf)
		if err != nil {
			return nil, err
		}
		if len(shps) > 1 {
			layer := base[strings.LastIndex(base, "/")+1:]
			for i := range feats {
				feats[i].Props["layer"] = layer
			}
		}
		doc.Features = append(doc.Features, feats...)
	}
	return doc, nil
}

var (
	gisProjRe = regexp.MustCompile(`(?i)^\s*(?:PROJCS|PROJCRS|PROJECTEDCRS)\s*\[\s*"([^"]*)"`)
	gisGeogRe = regexp.MustCompile(`(?i)(?:GEOGCS|GEOGCRS|GEOGRAPHICCRS)\s*\[\s*"([^"]*)"`)
)

// gisCheckPRJ accepts an empty .prj or a geographic WGS84-equivalent one.
func gisCheckPRJ(prj string) error {
	p := strings.TrimSpace(prj)
	if p == "" {
		return nil
	}
	if m := gisProjRe.FindStringSubmatch(p); m != nil {
		return Userf("this Shapefile uses a projected coordinate system (%s). Asteria only converts longitude/latitude (WGS84) data; reproject it to WGS84 first, for example in QGIS", m[1])
	}
	m := gisGeogRe.FindStringSubmatch(p)
	if m == nil {
		return Userf("this Shapefile's .prj file isn't a recognised coordinate system definition; reproject it to WGS84 first")
	}
	low := strings.ToLower(p)
	for _, ok := range []string{"wgs_1984", "wgs 84", "wgs84", "wgs_84", "nad83", "nad_1983", "north_american_datum_1983", "etrs89", "etrs_1989", "grs_1980", "grs 1980"} {
		if strings.Contains(low, ok) {
			return nil
		}
	}
	return Userf("this Shapefile uses the coordinate system %q, not WGS84; reproject it to WGS84 first, for example in QGIS", m[1])
}

type gisDBFField struct {
	name     string
	typ      byte
	offset   int
	length   int
	decimals int
}

type gisDBF struct {
	fields    []gisDBFField
	data      []byte
	headerLen int
	recLen    int
	count     int
}

func gisParseDBF(data []byte) (*gisDBF, error) {
	if len(data) < 33 {
		return nil, Userf("the Shapefile's .dbf attribute table is truncated")
	}
	le := binary.LittleEndian
	d := &gisDBF{data: data, count: int(le.Uint32(data[4:])), headerLen: int(le.Uint16(data[8:])), recLen: int(le.Uint16(data[10:]))}
	off := 1
	for pos := 32; pos+32 <= len(data) && pos < d.headerLen && data[pos] != 0x0D; pos += 32 {
		raw := data[pos : pos+11]
		if i := bytes.IndexByte(raw, 0); i >= 0 {
			raw = raw[:i]
		}
		fld := gisDBFField{name: strings.TrimSpace(string(raw)), typ: data[pos+11], offset: off, length: int(data[pos+16]), decimals: int(data[pos+17])}
		off += fld.length
		d.fields = append(d.fields, fld)
	}
	if d.recLen < off {
		d.recLen = off
	}
	if d.headerLen+d.recLen*d.count > len(data) {
		if d.recLen > 0 {
			d.count = (len(data) - d.headerLen) / d.recLen
		}
		if d.count < 0 {
			d.count = 0
		}
	}
	return d, nil
}

func gisLatin1(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// row returns the attributes of record i, or ok=false for deleted records.
func (d *gisDBF) row(i int) (map[string]any, bool) {
	out := map[string]any{}
	if d == nil || i < 0 || i >= d.count {
		return out, true
	}
	rec := d.data[d.headerLen+i*d.recLen : d.headerLen+(i+1)*d.recLen]
	if len(rec) > 0 && rec[0] == '*' {
		return nil, false
	}
	for _, f := range d.fields {
		if f.offset+f.length > len(rec) || f.name == "" {
			continue
		}
		s := strings.TrimSpace(strings.Trim(gisLatin1(rec[f.offset:f.offset+f.length]), "\x00"))
		if s == "" {
			continue
		}
		switch f.typ {
		case 'N', 'F':
			if f.decimals == 0 {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil {
					out[f.name] = n
					continue
				}
			}
			if v, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
				out[f.name] = v
			}
		case 'L':
			switch s[0] {
			case 'Y', 'y', 'T', 't':
				out[f.name] = true
			case 'N', 'n', 'F', 'f':
				out[f.name] = false
			}
		case 'D':
			if len(s) == 8 {
				out[f.name] = s[:4] + "-" + s[4:6] + "-" + s[6:]
			} else {
				out[f.name] = s
			}
		default:
			out[f.name] = s
		}
	}
	return out, true
}

func gisParseSHP(data []byte, dbf *gisDBF) ([]gisFeature, error) {
	if len(data) < 100 || binary.BigEndian.Uint32(data[0:]) != 9994 {
		return nil, Userf("the .shp file is not a valid Shapefile")
	}
	var feats []gisFeature
	pos, rec := 100, 0
	for pos+8 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[pos+4:])) * 2
		pos += 8
		if n < 4 || pos+n > len(data) {
			return nil, Userf("the .shp file is truncated or damaged")
		}
		c := data[pos : pos+n]
		pos += n
		idx := rec
		rec++
		g, ok, err := gisShapeGeom(c)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		props, alive := dbf.row(idx)
		if !alive {
			continue
		}
		feats = append(feats, gisFeature{Geom: g, Props: props})
	}
	return feats, nil
}

func gisShapeGeom(c []byte) (gisGeom, bool, error) {
	le := binary.LittleEndian
	st := int(int32(le.Uint32(c[0:])))
	f64 := func(off int) float64 { return math.Float64frombits(le.Uint64(c[off:])) }
	short := Userf("the .shp file has a truncated record")
	hasZ := st == 11 || st == 13 || st == 15 || st == 18
	switch st {
	case 0:
		return gisGeom{}, false, nil
	case 1, 11, 21:
		if len(c) < 20 {
			return gisGeom{}, false, short
		}
		p := []float64{f64(4), f64(12)}
		if hasZ && len(c) >= 28 {
			if z := f64(20); z > -1e38 {
				p = append(p, z)
			}
		}
		return gisGeom{Type: "Point", P: p}, true, nil
	case 8, 18, 28:
		if len(c) < 40 {
			return gisGeom{}, false, short
		}
		n := int(le.Uint32(c[36:]))
		if n < 0 || n > len(c)/16 || 40+16*n > len(c) {
			return gisGeom{}, false, short
		}
		pts := make([][]float64, n)
		for i := range pts {
			pts[i] = []float64{f64(40 + 16*i), f64(48 + 16*i)}
		}
		if hasZ {
			gisAttachZ(c, 40+16*n, pts, f64)
		}
		if n == 0 {
			return gisGeom{}, false, nil
		}
		return gisGeom{Type: "MultiPoint", MP: pts}, true, nil
	case 3, 13, 23, 5, 15, 25:
		if len(c) < 44 {
			return gisGeom{}, false, short
		}
		np := int(le.Uint32(c[36:]))
		n := int(le.Uint32(c[40:]))
		if np < 0 || n < 0 || np > len(c)/4 || n > len(c)/16 || 44+4*np+16*n > len(c) {
			return gisGeom{}, false, short
		}
		ptsOff := 44 + 4*np
		all := make([][]float64, n)
		for i := range all {
			all[i] = []float64{f64(ptsOff + 16*i), f64(ptsOff + 16*i + 8)}
		}
		if hasZ {
			gisAttachZ(c, ptsOff+16*n, all, f64)
		}
		var rings [][][]float64
		for i := 0; i < np; i++ {
			a := int(le.Uint32(c[44+4*i:]))
			b := n
			if i+1 < np {
				b = int(le.Uint32(c[44+4*(i+1):]))
			}
			if a < 0 || b > n || a >= b {
				continue
			}
			rings = append(rings, all[a:b])
		}
		if len(rings) == 0 {
			return gisGeom{}, false, nil
		}
		if st == 3 || st == 13 || st == 23 {
			if len(rings) == 1 {
				return gisGeom{Type: "LineString", L: rings}, true, nil
			}
			return gisGeom{Type: "MultiLineString", L: rings}, true, nil
		}
		return gisBuildPolygons(rings), true, nil
	}
	return gisGeom{}, false, Userf("the Shapefile contains shape type %d (for example MultiPatch), which Asteria can't convert", st)
}

func gisAttachZ(c []byte, off int, pts [][]float64, f64 func(int) float64) {
	zOff := off + 16
	if zOff+8*len(pts) > len(c) {
		return
	}
	for i := range pts {
		if z := f64(zOff + 8*i); z > -1e38 {
			pts[i] = append(pts[i], z)
		}
	}
}

// gisBuildPolygons groups rings by orientation: clockwise rings start a
// polygon, counter-clockwise rings are holes in the polygon before them.
func gisBuildPolygons(rings [][][]float64) gisGeom {
	var polys [][][][]float64
	for _, r := range rings {
		area := 0.0
		for i := 0; i+1 < len(r); i++ {
			area += r[i][0]*r[i+1][1] - r[i+1][0]*r[i][1]
		}
		if area > 0 && len(polys) > 0 {
			last := len(polys) - 1
			polys[last] = append(polys[last], r)
		} else {
			polys = append(polys, [][][]float64{r})
		}
	}
	if len(polys) == 1 {
		return gisGeom{Type: "Polygon", L: polys[0]}
	}
	return gisGeom{Type: "MultiPolygon", PG: polys}
}

// ---------------------------------------------------------------- stats

func gisHaversine(lat1, lon1, lat2, lon2 float64) float64 {
	p1 := lat1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	dphi := (lat2 - lat1) * math.Pi / 180
	dl := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dphi/2)*math.Sin(dphi/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * gisEarthRadius * math.Asin(math.Min(1, math.Sqrt(a)))
}

type gisBounds struct {
	MinLat float64 `json:"minLat"`
	MinLon float64 `json:"minLon"`
	MaxLat float64 `json:"maxLat"`
	MaxLon float64 `json:"maxLon"`
}

type gisStatsOut struct {
	Waypoints           int        `json:"waypoints"`
	Tracks              int        `json:"tracks"`
	Segments            int        `json:"segments"`
	Points              int        `json:"points"`
	DistanceMeters      float64    `json:"distanceMeters"`
	DistanceKm          float64    `json:"distanceKm"`
	ElevationGainMeters *float64   `json:"elevationGainMeters,omitempty"`
	ElevationLossMeters *float64   `json:"elevationLossMeters,omitempty"`
	MinElevation        *float64   `json:"minElevationMeters,omitempty"`
	MaxElevation        *float64   `json:"maxElevationMeters,omitempty"`
	StartTime           string     `json:"startTime,omitempty"`
	EndTime             string     `json:"endTime,omitempty"`
	DurationSeconds     *float64   `json:"durationSeconds,omitempty"`
	MovingTimeSeconds   *float64   `json:"movingTimeSeconds,omitempty"`
	AverageSpeedKmh     *float64   `json:"averageMovingSpeedKmh,omitempty"`
	Bounds              *gisBounds `json:"bounds,omitempty"`
}

func gisRound(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

func gisComputeStats(doc *gisDoc) (gisStatsOut, error) {
	var out gisStatsOut
	var bounds gisBounds
	haveBounds := false
	var dist, gain, loss, moving float64
	var minEle, maxEle float64
	haveEle := false
	var first, last time.Time
	extend := func(p []float64) {
		if !haveBounds {
			bounds = gisBounds{MinLat: p[1], MaxLat: p[1], MinLon: p[0], MaxLon: p[0]}
			haveBounds = true
		}
		bounds.MinLat = math.Min(bounds.MinLat, p[1])
		bounds.MaxLat = math.Max(bounds.MaxLat, p[1])
		bounds.MinLon = math.Min(bounds.MinLon, p[0])
		bounds.MaxLon = math.Max(bounds.MaxLon, p[0])
		if len(p) >= 3 {
			if !haveEle {
				minEle, maxEle, haveEle = p[2], p[2], true
			}
			minEle = math.Min(minEle, p[2])
			maxEle = math.Max(maxEle, p[2])
		}
	}
	noteTime := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if last.IsZero() || t.After(last) {
			last = t
		}
	}
	for _, f := range doc.Features {
		f.Geom.each(extend)
		switch f.Geom.Type {
		case "Point", "MultiPoint":
			out.Waypoints++
			if t, ok := gisParseTime(gisPropString(f.Props["time"])); ok {
				noteTime(t)
			}
		case "LineString", "MultiLineString":
			out.Tracks++
			for si, seg := range f.Geom.L {
				out.Segments++
				out.Points += len(seg)
				var ts []time.Time
				if si < len(f.Geom.T) {
					ts = f.Geom.T[si]
				}
				lastEle, haveLast := 0.0, false
				for i, p := range seg {
					if i < len(ts) {
						noteTime(ts[i])
					}
					if len(p) >= 3 {
						if haveLast {
							if d := p[2] - lastEle; d > 0 {
								gain += d
							} else {
								loss -= d
							}
						}
						lastEle, haveLast = p[2], true
					}
					if i == 0 {
						continue
					}
					q := seg[i-1]
					d := gisHaversine(q[1], q[0], p[1], p[0])
					dist += d
					if i < len(ts) && i-1 < len(ts) && !ts[i].IsZero() && !ts[i-1].IsZero() {
						if dt := ts[i].Sub(ts[i-1]).Seconds(); dt > 0 && d/dt >= 0.3 {
							moving += dt
						}
					}
				}
			}
		}
	}
	if !haveBounds {
		return out, Userf("no coordinates to measure in this file")
	}
	out.DistanceMeters = gisRound(dist, 2)
	out.DistanceKm = gisRound(dist/1000, 4)
	out.Bounds = &bounds
	if haveEle {
		g, l, lo, hi := gisRound(gain, 2), gisRound(loss, 2), gisRound(minEle, 2), gisRound(maxEle, 2)
		out.ElevationGainMeters, out.ElevationLossMeters, out.MinElevation, out.MaxElevation = &g, &l, &lo, &hi
	}
	if !first.IsZero() {
		out.StartTime, out.EndTime = gisFmtTime(first), gisFmtTime(last)
		dur := gisRound(last.Sub(first).Seconds(), 1)
		out.DurationSeconds = &dur
		if moving > 0 {
			m := gisRound(moving, 1)
			out.MovingTimeSeconds = &m
			s := gisRound(dist/moving*3.6, 2)
			out.AverageSpeedKmh = &s
		}
	}
	return out, nil
}

func gisStats(ctx context.Context, job Job) error {
	doc, err := gisRead(job.Input(), gisInExt(job))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := gisComputeStats(doc)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(job.Output, append(b, '\n'), 0o644)
}

// ---------------------------------------------------------------- simplify

func gisSegDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// gisSimplifyIdx runs Douglas-Peucker on one line and returns the indices of
// the vertices to keep. Tolerance is in metres.
func gisSimplifyIdx(line [][]float64, tol float64) []int {
	n := len(line)
	if n < 3 {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	lat0 := 0.0
	for _, p := range line {
		lat0 += p[1]
	}
	lat0 /= float64(n)
	k := math.Pi / 180 * gisEarthRadius
	cosLat := math.Cos(lat0 * math.Pi / 180)
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i, p := range line {
		xs[i] = p[0] * cosLat * k
		ys[i] = p[1] * k
	}
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	type span struct{ a, b int }
	stack := []span{{0, n - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.b-s.a < 2 {
			continue
		}
		maxD, at := 0.0, -1
		for i := s.a + 1; i < s.b; i++ {
			if d := gisSegDist(xs[i], ys[i], xs[s.a], ys[s.a], xs[s.b], ys[s.b]); d > maxD {
				maxD, at = d, i
			}
		}
		if at >= 0 && maxD > tol {
			keep[at] = true
			stack = append(stack, span{s.a, at}, span{at, s.b})
		}
	}
	var idx []int
	for i, k := range keep {
		if k {
			idx = append(idx, i)
		}
	}
	return idx
}

func gisSimplifyDoc(doc *gisDoc, tol float64) {
	simplifyRing := func(r [][]float64) [][]float64 {
		idx := gisSimplifyIdx(r, tol)
		if len(idx) < 4 {
			return r
		}
		out := make([][]float64, len(idx))
		for i, j := range idx {
			out[i] = r[j]
		}
		return out
	}
	for fi := range doc.Features {
		g := &doc.Features[fi].Geom
		switch g.Type {
		case "LineString", "MultiLineString":
			for i, l := range g.L {
				idx := gisSimplifyIdx(l, tol)
				nl := make([][]float64, len(idx))
				var nt []time.Time
				for k, j := range idx {
					nl[k] = l[j]
					if i < len(g.T) && j < len(g.T[i]) {
						nt = append(nt, g.T[i][j])
					}
				}
				g.L[i] = nl
				if i < len(g.T) {
					g.T[i] = nt
				}
			}
		case "Polygon":
			for i, r := range g.L {
				g.L[i] = simplifyRing(r)
			}
		case "MultiPolygon":
			for _, poly := range g.PG {
				for i, r := range poly {
					poly[i] = simplifyRing(r)
				}
			}
		}
	}
}

func gisSimplify(ctx context.Context, job Job) error {
	s := strings.ToLower(job.Args.String("tolerance", "10"))
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(s, "meters"), "m"))
	tol, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(tol) || tol <= 0 || tol > 100000 {
		return Userf("tolerance must be a distance in metres, such as 5, 10 or 25 (got %q)", job.Args.String("tolerance", "10"))
	}
	inExt := gisInExt(job)
	doc, err := gisRead(job.Input(), inExt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	gisSimplifyDoc(doc, tol)
	return gisWrite(job.Output, strings.ToLower(filepath.Ext(job.Output)), doc)
}
