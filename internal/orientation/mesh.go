// Package orientation computes, from a model's mesh geometry, the resting
// orientation that needs the least support material. It is pure: a mesh (or a
// model file) goes in, a recommendation comes out - no DB, no globals.
//
// Support is needed under faces that point downward more steeply than the
// printer can self-support. That is a direct property of the face normals, so
// the optimal orientation is a computed answer, not a learned one (this is the
// same "Tweaker" approach Cura and Bambu use). See optimize.go for the search.
package orientation

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupportedFormat means the model format carries no readable triangle mesh
// here (e.g. STEP, which is B-rep CAD). Callers treat it as "skip, not fail".
var ErrUnsupportedFormat = errors.New("orientation: unsupported model format")

// Vec3 is a 3D point or direction.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

func (a Vec3) sub(b Vec3) Vec3    { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) dot(b Vec3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a Vec3) cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}
func (a Vec3) length() float64      { return math.Sqrt(a.dot(a)) }
func (a Vec3) scale(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }

// normalize returns the unit vector, or the zero vector for a zero-length input.
func (a Vec3) normalize() Vec3 {
	l := a.length()
	if l == 0 {
		return Vec3{}
	}
	return a.scale(1 / l)
}

// Triangle is one facet with its outward normal and area precomputed.
type Triangle struct {
	V0, V1, V2 Vec3
	Normal     Vec3
	Area       float64
}

// Mesh is a triangle soup plus its axis-aligned bounds.
type Mesh struct {
	Triangles []Triangle
	Min, Max  Vec3
}

func (m Mesh) empty() bool { return len(m.Triangles) == 0 }

// newTriangle computes the normal (right-hand rule) and area from the vertices,
// rather than trusting a normal from the file (STL normals are often wrong).
func newTriangle(a, b, c Vec3) Triangle {
	cross := b.sub(a).cross(c.sub(a))
	area := 0.5 * cross.length()
	return Triangle{V0: a, V1: b, V2: c, Normal: cross.normalize(), Area: area}
}

// buildMesh assembles a Mesh from triangles, dropping degenerate (zero-area)
// facets and computing the bounding box.
func buildMesh(tris []Triangle) Mesh {
	kept := tris[:0]
	first := true
	var mn, mx Vec3
	for _, t := range tris {
		if t.Area == 0 {
			continue
		}
		for _, v := range [3]Vec3{t.V0, t.V1, t.V2} {
			if first {
				mn, mx, first = v, v, false
				continue
			}
			mn = Vec3{math.Min(mn.X, v.X), math.Min(mn.Y, v.Y), math.Min(mn.Z, v.Z)}
			mx = Vec3{math.Max(mx.X, v.X), math.Max(mx.Y, v.Y), math.Max(mx.Z, v.Z)}
		}
		kept = append(kept, t)
	}
	return Mesh{Triangles: kept, Min: mn, Max: mx}
}

// LoadModel reads the model at path, using ext (e.g. ".stl", ".3mf") to pick a
// parser. It returns ErrUnsupportedFormat for formats without a readable mesh.
func LoadModel(path, ext string) (Mesh, error) {
	switch strings.ToLower(ext) {
	case ".stl":
		data, err := os.ReadFile(path)
		if err != nil {
			return Mesh{}, err
		}
		return LoadSTL(data)
	case ".3mf":
		return Load3MF(path)
	default:
		return Mesh{}, ErrUnsupportedFormat
	}
}

const (
	stlHeaderBytes  = 80
	stlCountBytes   = 4
	stlRecordBytes  = 50 // normal(12) + 3 vertices(36) + attribute(2)
	stlFloatsPerRec = 12
)

// LoadSTL parses binary or ASCII STL. Format is detected by size: a binary STL
// is exactly 84 + 50*n bytes; anything else is treated as ASCII.
func LoadSTL(data []byte) (Mesh, error) {
	if len(data) >= stlHeaderBytes+stlCountBytes {
		n := binary.LittleEndian.Uint32(data[stlHeaderBytes : stlHeaderBytes+stlCountBytes])
		if int64(len(data)) == int64(stlHeaderBytes+stlCountBytes)+int64(stlRecordBytes)*int64(n) {
			return loadBinarySTL(data, int(n))
		}
	}
	return loadASCIISTL(data)
}

func loadBinarySTL(data []byte, n int) (Mesh, error) {
	tris := make([]Triangle, 0, n)
	offset := stlHeaderBytes + stlCountBytes
	for i := 0; i < n; i++ {
		rec := data[offset : offset+stlRecordBytes]
		var f [stlFloatsPerRec]float32
		for j := 0; j < stlFloatsPerRec; j++ {
			f[j] = math.Float32frombits(binary.LittleEndian.Uint32(rec[j*4 : j*4+4]))
		}
		// f[0:3] is the (unreliable) stored normal; recompute from vertices.
		v0 := Vec3{float64(f[3]), float64(f[4]), float64(f[5])}
		v1 := Vec3{float64(f[6]), float64(f[7]), float64(f[8])}
		v2 := Vec3{float64(f[9]), float64(f[10]), float64(f[11])}
		tris = append(tris, newTriangle(v0, v1, v2))
		offset += stlRecordBytes
	}
	return buildMesh(tris), nil
}

func loadASCIISTL(data []byte) (Mesh, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	verts := make([]Vec3, 0, 3)
	tris := make([]Triangle, 0, 256)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 4 && fields[0] == "vertex" {
			v, err := parseVertex(fields[1:])
			if err != nil {
				return Mesh{}, err
			}
			verts = append(verts, v)
			if len(verts) == 3 {
				tris = append(tris, newTriangle(verts[0], verts[1], verts[2]))
				verts = verts[:0]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Mesh{}, err
	}
	if len(tris) == 0 {
		return Mesh{}, errors.New("orientation: STL contained no triangles")
	}
	return buildMesh(tris), nil
}

func parseVertex(fields []string) (Vec3, error) {
	var v Vec3
	if _, err := fmt.Sscanf(fields[0], "%g", &v.X); err != nil {
		return v, err
	}
	if _, err := fmt.Sscanf(fields[1], "%g", &v.Y); err != nil {
		return v, err
	}
	if _, err := fmt.Sscanf(fields[2], "%g", &v.Z); err != nil {
		return v, err
	}
	return v, nil
}

// --- 3MF -------------------------------------------------------------------
// A 3MF is a zip whose 3D/3dmodel.model is XML holding one or more meshes. We
// read every object's mesh and merge the triangles. Component/build transforms
// are not applied (v1): a single-part 3MF, the common print case, needs none.

type xml3MFModel struct {
	Objects []xml3MFObject `xml:"resources>object"`
}
type xml3MFObject struct {
	Vertices  []xml3MFVertex   `xml:"mesh>vertices>vertex"`
	Triangles []xml3MFTriangle `xml:"mesh>triangles>triangle"`
}
type xml3MFVertex struct {
	X float64 `xml:"x,attr"`
	Y float64 `xml:"y,attr"`
	Z float64 `xml:"z,attr"`
}
type xml3MFTriangle struct {
	V1 int `xml:"v1,attr"`
	V2 int `xml:"v2,attr"`
	V3 int `xml:"v3,attr"`
}

// max3MFModelBytes bounds the decompressed model XML read from a 3MF archive, so
// a crafted archive (a zip bomb) cannot exhaust memory.
const max3MFModelBytes = 512 << 20 // 512 MiB

// Load3MF opens the 3MF archive at path and parses its model mesh(es).
func Load3MF(path string) (Mesh, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Mesh{}, err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if strings.EqualFold(filepath.ToSlash(f.Name), "3D/3dmodel.model") {
			rc, err := f.Open()
			if err != nil {
				return Mesh{}, err
			}
			defer func() { _ = rc.Close() }()
			return parse3MFModel(io.LimitReader(rc, max3MFModelBytes))
		}
	}
	return Mesh{}, errors.New("orientation: 3MF has no 3D/3dmodel.model")
}

func parse3MFModel(r io.Reader) (Mesh, error) {
	var model xml3MFModel
	if err := xml.NewDecoder(r).Decode(&model); err != nil {
		return Mesh{}, err
	}
	tris := make([]Triangle, 0, 256)
	for _, obj := range model.Objects {
		for _, t := range obj.Triangles {
			if !validIndices(t, len(obj.Vertices)) {
				continue
			}
			v0 := vertexToVec(obj.Vertices[t.V1])
			v1 := vertexToVec(obj.Vertices[t.V2])
			v2 := vertexToVec(obj.Vertices[t.V3])
			tris = append(tris, newTriangle(v0, v1, v2))
		}
	}
	if len(tris) == 0 {
		return Mesh{}, errors.New("orientation: 3MF contained no triangles")
	}
	return buildMesh(tris), nil
}

func validIndices(t xml3MFTriangle, n int) bool {
	return t.V1 >= 0 && t.V1 < n && t.V2 >= 0 && t.V2 < n && t.V3 >= 0 && t.V3 < n
}

func vertexToVec(v xml3MFVertex) Vec3 { return Vec3{v.X, v.Y, v.Z} }
