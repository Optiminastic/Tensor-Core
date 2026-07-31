package meshio

import (
	"math"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// rect builds a flat w x d rectangle in the XY plane at the origin (two triangles).
func rect(w, d float64) []orientation.Triangle {
	v00 := orientation.Vec3{X: 0, Y: 0, Z: 0}
	v10 := orientation.Vec3{X: w, Y: 0, Z: 0}
	v11 := orientation.Vec3{X: w, Y: d, Z: 0}
	v01 := orientation.Vec3{X: 0, Y: d, Z: 0}
	return []orientation.Triangle{
		{V0: v00, V1: v10, V2: v11},
		{V0: v00, V1: v11, V2: v01},
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestMergeTranslatesToOffset(t *testing.T) {
	data := MergeBinarySTL("test", []Placed{
		{Triangles: rect(10, 10), XOffsetMM: 100, YOffsetMM: 50},
	})
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		t.Fatalf("re-parse merged STL: %v", err)
	}
	if !approx(mesh.Min.X, 100) || !approx(mesh.Min.Y, 50) {
		t.Errorf("min = %+v, want (100,50,_)", mesh.Min)
	}
	if !approx(mesh.Max.X, 110) || !approx(mesh.Max.Y, 60) {
		t.Errorf("max = %+v, want (110,60,_)", mesh.Max)
	}
}

func TestMergeRotationSwapsDimensions(t *testing.T) {
	// A 20x10 part, rotated 90 degrees, should measure 10 wide by 20 deep.
	data := MergeBinarySTL("rot", []Placed{
		{Triangles: rect(20, 10), Rotated: true},
	})
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if !approx(mesh.Max.X-mesh.Min.X, 10) || !approx(mesh.Max.Y-mesh.Min.Y, 20) {
		t.Errorf("dims = (%v,%v), want (10,20)", mesh.Max.X-mesh.Min.X, mesh.Max.Y-mesh.Min.Y)
	}
}

func TestMergeCombinesParts(t *testing.T) {
	data := MergeBinarySTL("multi", []Placed{
		{Triangles: rect(10, 10), XOffsetMM: 0, YOffsetMM: 0},
		{Triangles: rect(10, 10), XOffsetMM: 50, YOffsetMM: 0},
	})
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(mesh.Triangles) != 4 {
		t.Errorf("merged triangles = %d, want 4", len(mesh.Triangles))
	}
	// The union spans from the first part's origin to the second part's far edge.
	if !approx(mesh.Min.X, 0) || !approx(mesh.Max.X, 60) {
		t.Errorf("x-span = [%v,%v], want [0,60]", mesh.Min.X, mesh.Max.X)
	}
}
