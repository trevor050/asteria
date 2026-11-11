package ops

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A unit cube: eight vertices, six outward-wound quads (12 triangles).
const mshCubeOBJ = `# unit cube
o Asteria matrix check
v 0 0 0
v 1 0 0
v 1 1 0
v 0 1 0
v 0 0 1
v 1 0 1
v 1 1 1
v 0 1 1
f 1 4 3 2
f 5 6 7 8
f 1 2 6 5
f 4 8 7 3
f 1 5 8 4
f 2 3 7 6
`

func mshWriteTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mshTestCube(t *testing.T) *mshMesh {
	t.Helper()
	m, err := mshReadOBJ(strings.NewReader(mshCubeOBJ))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.finish(); err != nil {
		t.Fatal(err)
	}
	return m
}

// mshTris lists every triangle as text, in order.
func mshTris(m *mshMesh) []string {
	var out []string
	for _, o := range m.Objects {
		for t := 0; t < o.triCount(); t++ {
			var parts []string
			for k := 0; k < 3; k++ {
				i := o.Idx[3*t+k]
				parts = append(parts, fmt.Sprintf("%g %g %g", o.Pos[3*i], o.Pos[3*i+1], o.Pos[3*i+2]))
			}
			out = append(out, strings.Join(parts, "|"))
		}
	}
	return out
}

func mshInfoJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	out := mustRun(t, "mesh.info", []string{path}, "info.json", nil)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatalf("info is not JSON: %v\n%s", err, b)
	}
	return info
}

func mshExpectCube(t *testing.T, label string, info map[string]any) {
	t.Helper()
	if got := info["triangles"].(float64); got != 12 {
		t.Errorf("%s: triangles = %v, want 12", label, got)
	}
	if got := info["vertices"].(float64); got != 8 {
		t.Errorf("%s: vertices = %v, want 8", label, got)
	}
	if got, ok := info["volume"].(float64); !ok || math.Abs(got-1) > 1e-6 {
		t.Errorf("%s: volume = %v, want 1", label, info["volume"])
	}
	if got := info["surfaceArea"].(float64); math.Abs(got-6) > 1e-6 {
		t.Errorf("%s: surfaceArea = %v, want 6", label, got)
	}
	if info["watertight"] != true {
		t.Errorf("%s: watertight = %v, want true", label, info["watertight"])
	}
	if info["windingInverted"] == true {
		t.Errorf("%s: winding reported inverted", label)
	}
	bb := info["boundingBox"].(map[string]any)
	if size := bb["size"].([]any); size[0].(float64) != 1 || size[1].(float64) != 1 || size[2].(float64) != 1 {
		t.Errorf("%s: bounding box size = %v", label, size)
	}
}

func TestMeshCubeEveryFormat(t *testing.T) {
	src := mshWriteTestFile(t, "cube.obj", []byte(mshCubeOBJ))
	want := mshTris(mshTestCube(t))
	if len(want) != 12 {
		t.Fatalf("cube has %d triangles", len(want))
	}
	mshExpectCube(t, "obj", mshInfoJSON(t, src))
	for _, ext := range []string{".stl", ".ply", ".glb", ".3mf", ".obj"} {
		out := mustRun(t, "mesh.convert", []string{src}, "cube"+ext, nil)
		mshExpectCube(t, ext, mshInfoJSON(t, out))
		back, err := mshRead(out, ext)
		if err != nil {
			t.Fatalf("%s: re-reading output: %v", ext, err)
		}
		if got := mshTris(back); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: triangles changed in conversion\n got %v\nwant %v", ext, got, want)
		}
		if name := back.Objects[0].Name; name != "Asteria matrix check" {
			t.Errorf("%s: object name = %q, want the marker", ext, name)
		}
	}
}

func TestMeshOBJNegativeIndicesAndQuad(t *testing.T) {
	obj := "v 0 0 0\nv 1 0 0\nv 1 1 0\nv 0 1 0\nf -4 -3 -2 -1\nv 0 0 1\nf -1 -2 -3\nusemtl steel\nf 1 2 3\n"
	m, err := mshReadOBJ(strings.NewReader(obj))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"0 0 0|1 0 0|1 1 0",
		"0 0 0|1 1 0|0 1 0",
		"0 0 1|0 1 0|1 1 0",
		"0 0 0|1 0 0|1 1 0",
	}
	if got := mshTris(m); !reflect.DeepEqual(got, want) {
		t.Errorf("triangles = %v, want %v", got, want)
	}
	if len(m.Objects) != 1 {
		t.Errorf("objects = %d, want 1", len(m.Objects))
	}
	if _, err := mshReadOBJ(strings.NewReader("v 0 0 0\nf 1 2 3\n")); err == nil {
		t.Error("a face with a missing vertex should fail")
	} else {
		var ue *UserError
		if !errors.As(err, &ue) {
			t.Errorf("error %v is not a UserError", err)
		}
	}
}

func TestMeshOBJObjectsAndMaterial(t *testing.T) {
	obj := "o a\nv 0 0 0\nv 1 0 0\nv 0 1 0\nusemtl red\nf 1 2 3\no b\nv 0 0 1\nv 1 0 1\nv 0 1 1\nf 4 5 6\n"
	m, err := mshReadOBJ(strings.NewReader(obj))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Objects) != 2 || m.Objects[0].Name != "a" || m.Objects[1].Name != "b" {
		t.Fatalf("objects = %+v", m.Objects)
	}
	if m.Objects[0].Material != "red" || m.Objects[1].Material != "red" {
		t.Errorf("materials = %q, %q, want red (the active usemtl)", m.Objects[0].Material, m.Objects[1].Material)
	}
}

func mshBinarySTL(header string, tris [][3][3]float32) []byte {
	var b bytes.Buffer
	h := make([]byte, 80)
	copy(h, header)
	b.Write(h)
	binary.Write(&b, binary.LittleEndian, uint32(len(tris)))
	for _, tri := range tris {
		binary.Write(&b, binary.LittleEndian, [3]float32{})
		for _, v := range tri {
			binary.Write(&b, binary.LittleEndian, v)
		}
		b.Write([]byte{0, 0})
	}
	return b.Bytes()
}

func mshCubeTriangles(t *testing.T) [][3][3]float32 {
	t.Helper()
	m := mshTestCube(t)
	o := m.Objects[0]
	var tris [][3][3]float32
	for k := 0; k < o.triCount(); k++ {
		var tri [3][3]float32
		for c := 0; c < 3; c++ {
			i := o.Idx[3*k+c]
			tri[c] = [3]float32{o.Pos[3*i], o.Pos[3*i+1], o.Pos[3*i+2]}
		}
		tris = append(tris, tri)
	}
	return tris
}

func TestMeshBinarySTLStartingWithSolid(t *testing.T) {
	data := mshBinarySTL("solid this header lies, the file is binary", mshCubeTriangles(t))
	if len(data) != 84+12*50 {
		t.Fatalf("test file has %d bytes", len(data))
	}
	p := mshWriteTestFile(t, "tricky.stl", data)
	mshExpectCube(t, "binary stl", mshInfoJSON(t, p))
}

func TestMeshASCIISTL(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("solid cube\n")
	for _, tri := range mshCubeTriangles(t) {
		sb.WriteString("facet normal 0 0 0\n outer loop\n")
		for _, v := range tri {
			fmt.Fprintf(&sb, "  vertex %g %g %g\n", v[0], v[1], v[2])
		}
		sb.WriteString(" endloop\nendfacet\n")
	}
	sb.WriteString("endsolid cube\n")
	p := mshWriteTestFile(t, "ascii.stl", []byte(sb.String()))
	mshExpectCube(t, "ascii stl", mshInfoJSON(t, p))
	m, err := mshRead(p, ".stl")
	if err != nil || m.Objects[0].Name != "cube" {
		t.Errorf("name = %v, err = %v", m, err)
	}
}

func TestMeshPLYAscii(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("ply\nformat ascii 1.0\ncomment object Asteria matrix check\nelement vertex 8\nproperty float x\nproperty float y\nproperty float z\nelement face 6\nproperty list uchar int vertex_indices\nend_header\n")
	for _, v := range [][3]int{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, {0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}} {
		fmt.Fprintf(&sb, "%d %d %d\n", v[0], v[1], v[2])
	}
	for _, f := range []string{"4 0 3 2 1", "4 4 5 6 7", "4 0 1 5 4", "4 3 7 6 2", "4 0 4 7 3", "4 1 2 6 5"} {
		sb.WriteString(f + "\n")
	}
	p := mshWriteTestFile(t, "cube.ply", []byte(sb.String()))
	mshExpectCube(t, "ascii ply", mshInfoJSON(t, p))
	m, _ := mshRead(p, ".ply")
	if m.Objects[0].Name != "Asteria matrix check" {
		t.Errorf("name = %q", m.Objects[0].Name)
	}
}

func TestMeshOpenAndNonClosed(t *testing.T) {
	p := mshWriteTestFile(t, "tri.obj", []byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	info := mshInfoJSON(t, p)
	if info["watertight"] != false {
		t.Errorf("a single triangle is not watertight: %v", info["watertight"])
	}
	if v, present := info["volume"]; !present || v != nil {
		t.Errorf("volume of an open mesh should be null, got %v (present %v)", v, present)
	}
	if info["openEdges"].(float64) != 3 {
		t.Errorf("openEdges = %v, want 3", info["openEdges"])
	}
}

func TestMeshGLBLayout(t *testing.T) {
	src := mshWriteTestFile(t, "cube.obj", []byte(mshCubeOBJ))
	out := mustRun(t, "mesh.convert", []string{src}, "cube.glb", nil)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	le := binary.LittleEndian
	if string(data[:4]) != "glTF" || le.Uint32(data[4:]) != 2 {
		t.Fatalf("bad GLB header % x", data[:12])
	}
	if int(le.Uint32(data[8:])) != len(data) {
		t.Errorf("header length %d != file size %d", le.Uint32(data[8:]), len(data))
	}
	jl := int(le.Uint32(data[12:]))
	if le.Uint32(data[16:]) != 0x4E4F534A {
		t.Errorf("first chunk is not JSON")
	}
	if jl%4 != 0 {
		t.Errorf("JSON chunk length %d is not 4-byte aligned", jl)
	}
	jsonChunk := data[20 : 20+jl]
	var doc map[string]any
	if err := json.Unmarshal(jsonChunk, &doc); err != nil {
		t.Fatalf("JSON chunk invalid: %v", err)
	}
	trimmed := bytes.TrimRight(jsonChunk, " ")
	for _, c := range jsonChunk[len(trimmed):] {
		if c != ' ' {
			t.Fatalf("JSON padding must be spaces")
		}
	}
	bo := 20 + jl
	bl := int(le.Uint32(data[bo:]))
	if le.Uint32(data[bo+4:]) != 0x004E4942 {
		t.Errorf("second chunk is not BIN")
	}
	if bl%4 != 0 {
		t.Errorf("BIN chunk length %d is not 4-byte aligned", bl)
	}
	if bo+8+bl != len(data) {
		t.Errorf("BIN chunk ends at %d, file has %d bytes", bo+8+bl, len(data))
	}
	declared := int(doc["buffers"].([]any)[0].(map[string]any)["byteLength"].(float64))
	for _, c := range data[bo+8+declared:] {
		if c != 0 {
			t.Errorf("BIN padding must be zeros")
		}
	}
	// glTF with the same buffer embedded as a data: URI reads identically.
	doc["buffers"] = []any{map[string]any{
		"byteLength": declared,
		"uri":        "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(data[bo+8:bo+8+declared]),
	}}
	gj, _ := json.Marshal(doc)
	gp := mshWriteTestFile(t, "cube.gltf", gj)
	a, err := mshRead(out, ".glb")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mshRead(gp, ".gltf")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mshTris(a), mshTris(b)) || len(mshTris(a)) != 12 {
		t.Errorf("gltf and glb disagree")
	}
	// External buffers are refused with a readable message.
	doc["buffers"] = []any{map[string]any{"byteLength": declared, "uri": "cube.bin"}}
	ej, _ := json.Marshal(doc)
	ep := mshWriteTestFile(t, "ext.gltf", ej)
	_, err = mshRead(ep, ".gltf")
	if err == nil || !strings.Contains(err.Error(), "external") {
		t.Errorf("external buffer error = %v", err)
	}
}

func TestMeshGLBNodeTransform(t *testing.T) {
	// One triangle, node translated by (5,0,0) and scaled by 2.
	var bin bytes.Buffer
	binary.Write(&bin, binary.LittleEndian, []float32{0, 0, 0, 1, 0, 0, 0, 1, 0})
	doc := map[string]any{
		"asset":       map[string]any{"version": "2.0"},
		"scene":       0,
		"scenes":      []any{map[string]any{"nodes": []int{0}}},
		"nodes":       []any{map[string]any{"name": "moved", "mesh": 0, "translation": []float64{5, 0, 0}, "scale": []float64{2, 2, 2}}},
		"meshes":      []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0}}}}},
		"accessors":   []any{map[string]any{"bufferView": 0, "componentType": 5126, "count": 3, "type": "VEC3"}},
		"bufferViews": []any{map[string]any{"buffer": 0, "byteLength": 36}},
		"buffers":     []any{map[string]any{"byteLength": 36, "uri": "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(bin.Bytes())}},
	}
	j, _ := json.Marshal(doc)
	p := mshWriteTestFile(t, "t.gltf", j)
	m, err := mshRead(p, ".gltf")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"5 0 0|7 0 0|5 2 0"}
	if got := mshTris(m); !reflect.DeepEqual(got, want) || m.Objects[0].Name != "moved" {
		t.Errorf("got %v (%s), want %v", got, m.Objects[0].Name, want)
	}
}

func TestMesh3MFPackage(t *testing.T) {
	src := mshWriteTestFile(t, "cube.obj", []byte(mshCubeOBJ))
	out := mustRun(t, "mesh.convert", []string{src}, "cube.3mf", nil)
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if !reflect.DeepEqual(names, []string{"[Content_Types].xml", "_rels/.rels", "3D/3dmodel.model"}) {
		t.Errorf("package entries = %v", names)
	}
	rc, _ := zr.File[2].Open()
	var model bytes.Buffer
	model.ReadFrom(rc)
	rc.Close()
	for _, want := range []string{`unit="millimeter"`, `<item objectid="1"/>`, `name="Asteria matrix check"`} {
		if !strings.Contains(model.String(), want) {
			t.Errorf("model missing %s", want)
		}
	}
	if n := strings.Count(model.String(), "<vertex "); n != 8 {
		t.Errorf("vertices written = %d, want 8 (welded)", n)
	}
}

func mshWrite3MFPackage(t *testing.T, model string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/></Types>`,
		"_rels/.rels":         `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Target="/3D/3dmodel.model" Id="rel0" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/></Relationships>`,
		"3D/3dmodel.model":    model,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	return mshWriteTestFile(t, "in.3mf", buf.Bytes())
}

func TestMesh3MFBuildTransformAndUnits(t *testing.T) {
	model := func(unit string) string {
		return `<?xml version="1.0"?><model unit="` + unit + `" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources>
<object id="1" type="model" name="tri"><mesh><vertices><vertex x="0" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="0" y="1" z="0"/></vertices>
<triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object></resources>
<build><item objectid="1" transform="1 0 0 0 1 0 0 0 1 10 20 30"/></build></model>`
	}
	m, err := mshRead(mshWrite3MFPackage(t, model("millimeter")), ".3mf")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := mshTris(m), []string{"10 20 30|11 20 30|10 21 30"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	m, err = mshRead(mshWrite3MFPackage(t, model("inch")), ".3mf")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Objects[0].Pos[3]; math.Abs(float64(got)-11*25.4) > 1e-3 {
		t.Errorf("inch model x = %v, want %v mm", got, 11*25.4)
	}
}

func TestMeshTriangleCap(t *testing.T) {
	m := &mshMesh{tris: mshMaxTriangles}
	err := m.addTris(1)
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "50 million") {
		t.Errorf("cap error = %v", err)
	}
}

func TestMeshNoTriangles(t *testing.T) {
	p := mshWriteTestFile(t, "empty.obj", []byte("v 0 0 0\n"))
	_, err := run(t, "mesh.info", []string{p}, "x.json", nil)
	assertUserError(t, err)
}
