package meshio

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

func v(x, y, z float64) orientation.Vec3 { return orientation.Vec3{X: x, Y: y, Z: z} }

// A unit tetrahedron - four faces, four distinct corners.
func tetra() []orientation.Triangle {
	a, b, c, d := v(0, 0, 0), v(1, 0, 0), v(0, 1, 0), v(0, 0, 1)
	return []orientation.Triangle{
		{V0: a, V1: b, V2: c}, {V0: a, V1: c, V2: d},
		{V0: a, V1: d, V2: b}, {V0: b, V1: d, V2: c},
	}
}

func readModel(t *testing.T, data []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "3D/3dmodel.model" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open model: %v", err)
		}
		defer func() { _ = rc.Close() }()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read model: %v", err)
		}
		return string(body)
	}
	t.Fatal("3D/3dmodel.model missing from the archive")
	return ""
}

// The whole point: a plank whose base and lettering print in different colours.
// STL cannot carry that and neither can OpenSCAD's own 3MF export, so if this
// is wrong the customer's colour choice never reaches the printer.
func TestWrite3MFCarriesEachPartsColour(t *testing.T) {
	data, err := Write3MF([]Part{
		{Name: "Base", Colour: "#FFFFFF", Triangles: tetra()},
		{Name: "Lettering", Colour: "#e4002b", Triangles: tetra()},
	})
	if err != nil {
		t.Fatalf("Write3MF: %v", err)
	}

	model := readModel(t, data)
	for _, want := range []string{
		`<basematerials id="1">`,
		`displaycolor="#FFFFFFFF"`,
		// Lower-case input must be normalised; some slicers match case-sensitively.
		`displaycolor="#E4002BFF"`,
		`<object id="2" type="model" pid="1" pindex="0"`,
		`<object id="3" type="model" pid="1" pindex="1"`,
		// The build places the assembly that holds both meshes - see
		// TestWrite3MFHoldsAProductsPartsTogether.
		`<item objectid="4"/>`,
	} {
		if !strings.Contains(model, want) {
			t.Errorf("model is missing %s", want)
		}
	}
}

// A 3MF is a zip with a fixed set of parts. Miss one and a slicer rejects the
// whole file rather than showing a partial model.
func TestWrite3MFHasTheRequiredArchiveParts(t *testing.T) {
	data, err := Write3MF([]Part{{Name: "Base", Colour: "#FFFFFF", Triangles: tetra()}})
	if err != nil {
		t.Fatalf("Write3MF: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	found := map[string]bool{}
	for _, f := range zr.File {
		found[f.Name] = true
	}
	for _, want := range []string{"[Content_Types].xml", "_rels/.rels", "3D/3dmodel.model"} {
		if !found[want] {
			t.Errorf("archive is missing %s", want)
		}
	}
}

// STL repeats a vertex per triangle; 3MF indexes them. A slicer treats a mesh
// whose triangles do not share vertices as full of holes and refuses it.
func TestWrite3MFMergesSharedVertices(t *testing.T) {
	data, err := Write3MF([]Part{{Name: "Base", Colour: "#FFFFFF", Triangles: tetra()}})
	if err != nil {
		t.Fatalf("Write3MF: %v", err)
	}
	model := readModel(t, data)
	// Four faces, twelve corners, but only four distinct positions.
	if got := strings.Count(model, "<vertex "); got != 4 {
		t.Errorf("wrote %d vertices for a tetrahedron, want 4 - vertices are not being merged", got)
	}
	if got := strings.Count(model, "<triangle "); got != 4 {
		t.Errorf("wrote %d triangles, want 4", got)
	}
}

// A colour nobody chose is worse than a build that failed, so anything not
// #RRGGBB is refused rather than defaulted.
func TestWrite3MFRejectsBadInput(t *testing.T) {
	for _, c := range []struct {
		name  string
		parts []Part
	}{
		{"no parts", nil},
		{"colour without a hash", []Part{{Name: "B", Colour: "FFFFFF", Triangles: tetra()}}},
		{"colour too short", []Part{{Name: "B", Colour: "#FFF", Triangles: tetra()}}},
		{"colour not hex", []Part{{Name: "B", Colour: "#GGGGGG", Triangles: tetra()}}},
		{"named colour", []Part{{Name: "B", Colour: "white", Triangles: tetra()}}},
		{"no geometry", []Part{{Name: "B", Colour: "#FFFFFF"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Write3MF(c.parts); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
