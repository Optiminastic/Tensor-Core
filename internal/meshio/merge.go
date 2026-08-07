// Package meshio transforms and merges triangle meshes into a single binary STL,
// ported from print-queue-be's stl_geometry merge path. It reuses the orientation
// package's loaders (which produce the source Mesh) and adds the write side:
// normalise-to-origin, 90-degree turn, translate, and binary-STL encode. Pure, no
// I/O beyond returning bytes.
package meshio

import (
	"bytes"
	"encoding/binary"
	"math"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// vec is a working vertex; orientation.Vec3's arithmetic is unexported, so meshio
// operates on the coordinates directly.
type vec = orientation.Vec3

// Placed is one source mesh positioned on the bed: its triangles as loaded, the
// bed offset to move it to, and whether it was turned 90 degrees to fit.
type Placed struct {
	Triangles []orientation.Triangle
	XOffsetMM float64
	YOffsetMM float64
	Rotated   bool
}

// MergeBinarySTL lays every placed part onto the plate and encodes the union as a
// binary STL. Each part is normalised to the origin, optionally rotated (and
// re-normalised), then translated to its bed offset - matching the source merge.
func MergeBinarySTL(header string, parts []Placed) []byte {
	var all [][3]vec
	for _, p := range parts {
		tris := toTriples(p.Triangles)
		normalise(tris)
		if p.Rotated {
			rotateZ90(tris)
			normalise(tris)
		}
		translate(tris, p.XOffsetMM, p.YOffsetMM, 0)
		all = append(all, tris...)
	}
	return writeBinary(header, all)
}

// ConcatBinarySTL encodes several already-positioned meshes as one binary STL
// WITHOUT any normalisation - every part keeps its own coordinates, so the parts
// stay aligned as given (unlike MergeBinarySTL, which snaps each part to the
// origin). This is how a base model and a positioned text mesh are combined into
// one STL: the slicer fuses the overlapping bodies at slice time.
func ConcatBinarySTL(header string, meshes ...[]orientation.Triangle) []byte {
	var all [][3]vec
	for _, m := range meshes {
		all = append(all, toTriples(m)...)
	}
	return writeBinary(header, all)
}

// TranslateTriangles returns a copy of the triangles shifted by (dx, dy, dz). The
// input is not mutated; face normals are unaffected by a pure translation.
func TranslateTriangles(tris []orientation.Triangle, dx, dy, dz float64) []orientation.Triangle {
	out := make([]orientation.Triangle, len(tris))
	shift := func(v vec) vec { return vec{X: v.X + dx, Y: v.Y + dy, Z: v.Z + dz} }
	for i, t := range tris {
		out[i] = t
		out[i].V0 = shift(t.V0)
		out[i].V1 = shift(t.V1)
		out[i].V2 = shift(t.V2)
	}
	return out
}

// RotateZTriangles returns a copy of the triangles rotated by deg degrees CCW
// about the Z axis through the origin (x, y) -> (x*cos - y*sin, x*sin + y*cos, z).
// The input is not mutated. Winding is preserved (a proper rotation), so face
// normals stay outward. The extruded text mesh is centred on the origin, so this
// spins it in place before it is translated to its spot on the model.
func RotateZTriangles(tris []orientation.Triangle, deg float64) []orientation.Triangle {
	if deg == 0 {
		out := make([]orientation.Triangle, len(tris))
		copy(out, tris)
		return out
	}
	rad := deg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	spin := func(v vec) vec {
		return vec{X: v.X*cos - v.Y*sin, Y: v.X*sin + v.Y*cos, Z: v.Z}
	}
	out := make([]orientation.Triangle, len(tris))
	for i, t := range tris {
		out[i] = t
		out[i].V0 = spin(t.V0)
		out[i].V1 = spin(t.V1)
		out[i].V2 = spin(t.V2)
	}
	return out
}

// toTriples copies a triangle slice into mutable vertex triples.
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

// faceNormal is the unit outward normal computed from the vertices (STL-stored
// normals are unreliable, so we recompute).
func faceNormal(t [3]vec) vec {
	ux, uy, uz := t[1].X-t[0].X, t[1].Y-t[0].Y, t[1].Z-t[0].Z
	vx, vy, vz := t[2].X-t[0].X, t[2].Y-t[0].Y, t[2].Z-t[0].Z
	nx, ny, nz := uy*vz-uz*vy, uz*vx-ux*vz, ux*vy-uy*vx
	l := math.Sqrt(nx*nx + ny*ny + nz*nz)
	if l == 0 {
		return vec{}
	}
	return vec{X: nx / l, Y: ny / l, Z: nz / l}
}

// writeBinary encodes triangles as a binary STL: 80-byte header, uint32 count,
// then per facet a normal, three vertices, and a 2-byte attribute word.
func writeBinary(header string, tris [][3]vec) []byte {
	buf := &bytes.Buffer{}
	var head [80]byte
	copy(head[:], header)
	buf.Write(head[:])
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(tris)))

	for _, t := range tris {
		n := faceNormal(t)
		writeVec(buf, n)
		writeVec(buf, t[0])
		writeVec(buf, t[1])
		writeVec(buf, t[2])
		_ = binary.Write(buf, binary.LittleEndian, uint16(0))
	}
	return buf.Bytes()
}

func writeVec(buf *bytes.Buffer, v vec) {
	_ = binary.Write(buf, binary.LittleEndian, float32(v.X))
	_ = binary.Write(buf, binary.LittleEndian, float32(v.Y))
	_ = binary.Write(buf, binary.LittleEndian, float32(v.Z))
}
