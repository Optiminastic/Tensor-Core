package orientation

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Unit-cube geometry shared by the parser tests: 8 vertices, 12 outward-wound
// triangles, so the STL and 3MF fixtures describe the exact same mesh.
var cubeVerts = [8]Vec3{
	{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
	{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1},
}

var cubeTris = [12][3]int{
	{0, 3, 1}, {1, 3, 2}, // bottom (-Z)
	{4, 5, 7}, {5, 6, 7}, // top (+Z)
	{0, 1, 5}, {0, 5, 4}, // front (-Y)
	{3, 7, 2}, {2, 7, 6}, // back (+Y)
	{0, 4, 3}, {4, 7, 3}, // left (-X)
	{1, 2, 5}, {2, 6, 5}, // right (+X)
}

func cubeTriangleCoords() [][3]Vec3 {
	out := make([][3]Vec3, 0, len(cubeTris))
	for _, t := range cubeTris {
		out = append(out, [3]Vec3{cubeVerts[t[0]], cubeVerts[t[1]], cubeVerts[t[2]]})
	}
	return out
}

func binarySTL(tris [][3]Vec3) []byte {
	buf := new(bytes.Buffer)
	buf.Write(make([]byte, stlHeaderBytes))
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(tris)))
	for _, t := range tris {
		for i := 0; i < 3; i++ { // stored normal (ignored on read)
			_ = binary.Write(buf, binary.LittleEndian, float32(0))
		}
		for _, v := range t {
			_ = binary.Write(buf, binary.LittleEndian, float32(v.X))
			_ = binary.Write(buf, binary.LittleEndian, float32(v.Y))
			_ = binary.Write(buf, binary.LittleEndian, float32(v.Z))
		}
		_ = binary.Write(buf, binary.LittleEndian, uint16(0))
	}
	return buf.Bytes()
}

func write3MF(t *testing.T) string {
	t.Helper()
	var xml bytes.Buffer
	xml.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	xml.WriteString(`<model unit="millimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">`)
	xml.WriteString(`<resources><object id="1" type="model"><mesh><vertices>`)
	for _, v := range cubeVerts {
		fmt.Fprintf(&xml, `<vertex x="%g" y="%g" z="%g"/>`, v.X, v.Y, v.Z)
	}
	xml.WriteString(`</vertices><triangles>`)
	for _, tr := range cubeTris {
		fmt.Fprintf(&xml, `<triangle v1="%d" v2="%d" v3="%d"/>`, tr[0], tr[1], tr[2])
	}
	xml.WriteString(`</triangles></mesh></object></resources><build><item objectid="1"/></build></model>`)

	path := filepath.Join(t.TempDir(), "cube.3mf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create 3mf: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create("3D/3dmodel.model")
	if err != nil {
		t.Fatalf("zip entry: %v", err)
	}
	if _, err := w.Write(xml.Bytes()); err != nil {
		t.Fatalf("write model: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

// write3MFProductionExtension writes a 3MF laid out the way Bambu Studio /
// MakerWorld project files are: the root 3D/3dmodel.model holds only a
// <component p:path> reference, and the real cube mesh lives in a separate
// 3D/Objects/object_1.model part. Load3MF must follow the reference.
func write3MFProductionExtension(t *testing.T) string {
	t.Helper()

	var root bytes.Buffer
	root.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	root.WriteString(`<model unit="millimeter" ` +
		`xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" ` +
		`xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" ` +
		`requiredextensions="p">`)
	root.WriteString(`<resources><object id="2" type="model"><components>`)
	root.WriteString(`<component p:path="/3D/Objects/object_1.model" objectid="1"/>`)
	root.WriteString(`</components></object></resources><build><item objectid="2"/></build></model>`)

	var part bytes.Buffer
	part.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	part.WriteString(`<model unit="millimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">`)
	part.WriteString(`<resources><object id="1" type="model"><mesh><vertices>`)
	for _, v := range cubeVerts {
		fmt.Fprintf(&part, `<vertex x="%g" y="%g" z="%g"/>`, v.X, v.Y, v.Z)
	}
	part.WriteString(`</vertices><triangles>`)
	for _, tr := range cubeTris {
		fmt.Fprintf(&part, `<triangle v1="%d" v2="%d" v3="%d"/>`, tr[0], tr[1], tr[2])
	}
	part.WriteString(`</triangles></mesh></object></resources></model>`)

	path := filepath.Join(t.TempDir(), "cube-prod.3mf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create 3mf: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range map[string][]byte{
		"3D/3dmodel.model":          root.Bytes(),
		"3D/Objects/object_1.model": part.Bytes(),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

// A production-extension 3MF (mesh in a referenced part, not inline) must load
// the same geometry as its STL twin - the MakerWorld/Bambu project case that
// previously read as "no triangles".
func TestLoad3MF_FollowsComponentReferences(t *testing.T) {
	stl, err := LoadSTL(binarySTL(cubeTriangleCoords()))
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	tmf, err := Load3MF(write3MFProductionExtension(t))
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}
	if len(tmf.Triangles) != len(stl.Triangles) {
		t.Fatalf("triangle counts differ: stl=%d 3mf=%d", len(stl.Triangles), len(tmf.Triangles))
	}
	if stl.Min != tmf.Min || stl.Max != tmf.Max {
		t.Fatalf("bbox differ: stl %v..%v vs 3mf %v..%v", stl.Min, stl.Max, tmf.Min, tmf.Max)
	}
}

func TestLoadBinarySTL_Cube(t *testing.T) {
	m, err := LoadSTL(binarySTL(cubeTriangleCoords()))
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	if len(m.Triangles) != 12 {
		t.Fatalf("triangles = %d, want 12", len(m.Triangles))
	}
	if m.Min != (Vec3{0, 0, 0}) || m.Max != (Vec3{1, 1, 1}) {
		t.Fatalf("bbox = %v..%v, want 0..1", m.Min, m.Max)
	}
}

func TestLoadASCIISTL(t *testing.T) {
	ascii := "solid t\n" +
		"facet normal 0 0 0\nouter loop\n" +
		"vertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\n" +
		"endloop\nendfacet\nendsolid t\n"
	m, err := LoadSTL([]byte(ascii))
	if err != nil {
		t.Fatalf("LoadSTL ascii: %v", err)
	}
	if len(m.Triangles) != 1 {
		t.Fatalf("triangles = %d, want 1", len(m.Triangles))
	}
	if got := m.Triangles[0].Normal; math.Abs(got.Z-1) > 1e-9 {
		t.Fatalf("normal = %v, want +Z", got)
	}
}

func TestLoad3MF_MatchesSTLTwin(t *testing.T) {
	stl, err := LoadSTL(binarySTL(cubeTriangleCoords()))
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	tmf, err := Load3MF(write3MF(t))
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}
	if len(stl.Triangles) != len(tmf.Triangles) {
		t.Fatalf("triangle counts differ: stl=%d 3mf=%d", len(stl.Triangles), len(tmf.Triangles))
	}
	if stl.Min != tmf.Min || stl.Max != tmf.Max {
		t.Fatalf("bbox differ: stl %v..%v vs 3mf %v..%v", stl.Min, stl.Max, tmf.Min, tmf.Max)
	}
}

func TestLoadModel_STEPUnsupported(t *testing.T) {
	if _, err := LoadModel("whatever.step", ".step"); err != ErrUnsupportedFormat {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}

func TestRecommend_CubeAlreadyOptimal(t *testing.T) {
	m, err := LoadSTL(binarySTL(cubeTriangleCoords()))
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	rec, ok := Recommend(m, Options{})
	if !ok {
		t.Fatal("Recommend ok = false, want true")
	}
	if !rec.AlreadyOptimal {
		t.Fatalf("cube should be already optimal, got %+v", rec)
	}
	if rec.OverhangAreaBaseline != 0 {
		t.Fatalf("cube baseline overhang = %g, want 0", rec.OverhangAreaBaseline)
	}
}

// TestRecommend_OverhangReduced builds an elevated downward-facing flat triangle
// (normal -Z at z=1) plus a small vertical wall reaching the plate at z=0. As
// uploaded, the elevated triangle needs support; flipping it upside down removes
// the overhang entirely, so the recommendation is a ~180deg flip about X.
func TestRecommend_OverhangReduced(t *testing.T) {
	elevated := newTriangle(Vec3{0, 0, 1}, Vec3{0, 2, 1}, Vec3{2, 0, 1}) // normal -Z, area 2
	wall := newTriangle(Vec3{3, 0, 0}, Vec3{3, 1, 0}, Vec3{3, 0, 1})     // normal +X, touches z=0
	m := buildMesh([]Triangle{elevated, wall})

	if math.Abs(elevated.Normal.Z+1) > 1e-9 {
		t.Fatalf("fixture normal = %v, want -Z", elevated.Normal)
	}

	rec, ok := Recommend(m, Options{})
	if !ok {
		t.Fatal("Recommend ok = false, want true")
	}
	if rec.AlreadyOptimal {
		t.Fatalf("expected an improvement, got already-optimal: %+v", rec)
	}
	if math.Abs(rec.OverhangAreaBaseline-2) > 1e-6 {
		t.Fatalf("baseline overhang = %g, want ~2", rec.OverhangAreaBaseline)
	}
	// The optimiser drives the overhang to zero (a face resting flat on the
	// plate), so the reduction is essentially total.
	if rec.OverhangAreaRecommended > 1e-6 {
		t.Fatalf("recommended overhang = %g, want ~0", rec.OverhangAreaRecommended)
	}
	if rec.EstReductionPct < 0.9 {
		t.Fatalf("reduction = %g, want >=0.9", rec.EstReductionPct)
	}
	if rec.RotationDegrees <= 0 {
		t.Fatalf("rotation degrees = %g, want > 0", rec.RotationDegrees)
	}
	if rec.Description == "" {
		t.Fatal("description is empty")
	}
}
