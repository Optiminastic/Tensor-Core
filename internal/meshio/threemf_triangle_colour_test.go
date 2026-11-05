package meshio

import (
	"archive/zip"
	"bytes"
	"testing"
)

// zipModel packs one 3dmodel.model into a minimal 3MF.
func zipModel(t *testing.T, model string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("3D/3dmodel.model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(model)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A tetrahedron's worth of triangles on one object, all naming material p1.
func object(id, p1 string) string {
	return `<object id="` + id + `" type="model" pid="1" pindex="0"><mesh><vertices>` +
		`<vertex x="0" y="0" z="0"/><vertex x="10" y="0" z="0"/>` +
		`<vertex x="0" y="10" z="0"/><vertex x="0" y="0" z="10"/></vertices><triangles>` +
		`<triangle v1="0" v2="1" v3="2" pid="1" p1="` + p1 + `"/>` +
		`<triangle v1="0" v2="1" v3="3" pid="1" p1="` + p1 + `"/>` +
		`<triangle v1="0" v2="2" v3="3" pid="1" p1="` + p1 + `"/>` +
		`<triangle v1="1" v2="2" v3="3" pid="1" p1="` + p1 + `"/>` +
		`</triangles></mesh></object>`
}

// OpenSCAD's shape exactly: every object says pindex="0" - its "Default"
// base - and the real colour is on each triangle. Read at the object, a
// brown-and-white crossword frame came back as three yellow parts.
func TestTheTrianglesColourWinsOverTheObjectDefault(t *testing.T) {
	model := `<?xml version="1.0" encoding="utf-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
<resources>
  <basematerials id="1">
    <base name="Default" displaycolor="#F9D72C00"/>
    <base name="Color 1" displaycolor="#6F4E3700"/>
    <base name="Color 2" displaycolor="#FFFFFF00"/>
  </basematerials>` + object("2", "1") + object("3", "2") + `
</resources><build/></model>`

	parts, err := Read3MF(zipModel(t, model))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if parts[0].Colour != "#6F4E37" || parts[1].Colour != "#FFFFFF" {
		t.Fatalf("colours = %s, %s; want #6F4E37, #FFFFFF - not the object's default",
			parts[0].Colour, parts[1].Colour)
	}
}

// A file that colours at the OBJECT and leaves its triangles bare - the shape
// every reader of this package relied on until now - is read exactly as before.
func TestAnObjectColourStillCountsWhenTrianglesNameNone(t *testing.T) {
	model := `<?xml version="1.0" encoding="utf-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
<resources>
  <basematerials id="1">
    <base name="Red" displaycolor="#C12E1FFF"/>
  </basematerials>
  <object id="2" type="model" pid="1" pindex="0"><mesh><vertices>
    <vertex x="0" y="0" z="0"/><vertex x="10" y="0" z="0"/>
    <vertex x="0" y="10" z="0"/><vertex x="0" y="0" z="10"/></vertices><triangles>
    <triangle v1="0" v2="1" v3="2"/><triangle v1="0" v2="1" v3="3"/>
    <triangle v1="0" v2="2" v3="3"/><triangle v1="1" v2="2" v3="3"/>
  </triangles></mesh></object>
</resources><build/></model>`

	parts, err := Read3MF(zipModel(t, model))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Colour != "#C12E1F" {
		t.Fatalf("got %+v; an object-level colour must still be read", parts)
	}
}
