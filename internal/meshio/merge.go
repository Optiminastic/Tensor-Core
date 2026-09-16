// Package meshio transforms and merges triangle meshes into a single binary STL,
// ported from print-queue-be's stl_geometry merge path. It reuses the orientation
// package's loaders (which produce the source Mesh) and adds the write side:
// normalise-to-origin, 90-degree turn, translate, and binary-STL encode. Pure, no
// I/O beyond returning bytes.
package meshio

import (
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// vec is a working vertex; orientation.Vec3's arithmetic is unexported, so meshio
// operates on the coordinates directly.
type vec = orientation.Vec3

// Placed is one source mesh positioned on the bed: its triangles as loaded, the
// bed offset to move it to, and whether it was turned 90 degrees to fit.
// Bbox is a merged plate's axis-aligned combined size, in millimetres - how
// much of the bed the whole plate (every part, as actually placed) occupies.
type Bbox struct {
	XMM float64
	YMM float64
	ZMM float64
}

func toTriples(tris []orientation.Triangle) [][3]vec {
	out := make([][3]vec, len(tris))
	for i, t := range tris {
		out[i] = [3]vec{t.V0, t.V1, t.V2}
	}
	return out
}

// bounds returns the axis-aligned min/max corners of the mesh.
func bounds(tris [][3]vec) (mn, mx vec) {
	first := true
	for _, t := range tris {
		for _, v := range t {
			if first {
				mn, mx, first = v, v, false
				continue
			}
			mn = vec{X: math.Min(mn.X, v.X), Y: math.Min(mn.Y, v.Y), Z: math.Min(mn.Z, v.Z)}
			mx = vec{X: math.Max(mx.X, v.X), Y: math.Max(mx.Y, v.Y), Z: math.Max(mx.Z, v.Z)}
		}
	}
	return mn, mx
}

// normalise shifts the mesh so its minimum corner sits at the origin.
func normalise(tris [][3]vec) {
	mn, _ := bounds(tris)
	translate(tris, -mn.X, -mn.Y, -mn.Z)
}

// rotateZ90 turns the mesh 90 degrees CCW about Z: (x, y, z) -> (-y, x, z). This
// preserves winding, so face normals stay outward.
func rotateZ90(tris [][3]vec) {
	for i := range tris {
		for j := range tris[i] {
			v := tris[i][j]
			tris[i][j] = vec{X: -v.Y, Y: v.X, Z: v.Z}
		}
	}
}

// translate moves every vertex by (dx, dy, dz).
func translate(tris [][3]vec, dx, dy, dz float64) {
	for i := range tris {
		for j := range tris[i] {
			tris[i][j].X += dx
			tris[i][j].Y += dy
			tris[i][j].Z += dz
		}
	}
}
