package meshio

import (
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// Decimate reduces a triangle set by grid vertex-clustering: the bounding box is
// split into a gridN^3 lattice, every vertex snaps to its cell's centroid, and
// triangles whose corners collapse into one cell are dropped. Cheap (two linear
// passes) and good enough for an orbit preview - it is NOT a quality-preserving
// simplification and must never replace the model that goes to the slicer.
func Decimate(tris []orientation.Triangle, min, max orientation.Vec3, gridN int) []orientation.Triangle {
	if gridN < 2 || len(tris) == 0 {
		return tris
	}
	maxDim := math.Max(max.X-min.X, math.Max(max.Y-min.Y, max.Z-min.Z))
	if maxDim <= 0 {
		return tris
	}
	cell := maxDim / float64(gridN)
	stride := gridN + 2 // +2 so the max corner never overflows the lattice
	keyOf := func(v orientation.Vec3) int {
		return int((v.X-min.X)/cell) +
			int((v.Y-min.Y)/cell)*stride +
			int((v.Z-min.Z)/cell)*stride*stride
	}

	type acc struct {
		x, y, z float64
		n       int
	}
	cells := make(map[int]*acc)
	addV := func(v orientation.Vec3) {
		k := keyOf(v)
		a := cells[k]
		if a == nil {
			a = &acc{}
			cells[k] = a
		}
		a.x += v.X
		a.y += v.Y
		a.z += v.Z
		a.n++
	}
	for _, t := range tris {
		addV(t.V0)
		addV(t.V1)
		addV(t.V2)
	}
	rep := func(v orientation.Vec3) orientation.Vec3 {
		a := cells[keyOf(v)]
		return orientation.Vec3{X: a.x / float64(a.n), Y: a.y / float64(a.n), Z: a.z / float64(a.n)}
	}

	out := make([]orientation.Triangle, 0, len(tris)/4)
	for _, t := range tris {
		ka, kb, kc := keyOf(t.V0), keyOf(t.V1), keyOf(t.V2)
		if ka == kb || kb == kc || ka == kc {
			continue
		}
		out = append(out, orientation.Triangle{V0: rep(t.V0), V1: rep(t.V1), V2: rep(t.V2)})
	}
	return out
}
