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
	"strconv"
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
// A 3MF is a zip whose 3D/3dmodel.model is XML describing the build.
//
// The naive reading - "collect every object's mesh from 3dmodel.model" - works
// only for the simplest exports. Every slicer in real use (Bambu Studio,
// PrusaSlicer, Orca) writes the PRODUCTION EXTENSION instead:
//
//	3D/3dmodel.model      <object id="2"><components>
//	                        <component p:path="/3D/Objects/object_1.model"
//	                                   objectid="1" transform="..."/>
//	3D/Objects/object_1.model   <- the actual vertices and triangles live here
//
// The root file then holds no triangles at all, so the naive reader returns
// "3MF contained no triangles" for a perfectly valid model. Measured against a
// real folder of Bambu exports, 5 of 11 failed outright and a sixth reported a
// vase as 4.7mm tall because only a fragment was read.
//
// So: follow component references into other parts of the archive, and apply
// the transforms. Both matter for a bounding box - a component can be rotated
// or scaled, and a model whose parts are placed by transform is the wrong size
// without them.

type xml3MFModel struct {
	Objects []xml3MFObject `xml:"resources>object"`
	Items   []xml3MFItem   `xml:"build>item"`
}
type xml3MFObject struct {
	ID         int               `xml:"id,attr"`
	Vertices   []xml3MFVertex    `xml:"mesh>vertices>vertex"`
	Triangles  []xml3MFTriangle  `xml:"mesh>triangles>triangle"`
	Components []xml3MFComponent `xml:"components>component"`
}
type xml3MFComponent struct {
	ObjectID int `xml:"objectid,attr"`
	// Path is the production extension's p:path. Empty means "an object in
	// this same part". Go matches attributes on local name, so the p: prefix
	// needs no namespace handling here.
	Path      string `xml:"path,attr"`
	Transform string `xml:"transform,attr"`
}
type xml3MFItem struct {
	ObjectID  int    `xml:"objectid,attr"`
	Transform string `xml:"transform,attr"`
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

// affine is 3MF's 4x3 transform, as the 12 numbers the format writes them in.
//
// 3MF uses row-vector convention: a point is [x y z 1] multiplied on the LEFT
// by the matrix, so the last three numbers are the translation and the first
// nine are column-major within each row. Getting this transposed silently
// produces a plausible-but-wrong box for anything rotated, which is exactly the
// kind of error that survives review - hence spelling it out.
type affine struct {
	m   [12]float64
	set bool
}

func (a affine) apply(v Vec3) Vec3 {
	if !a.set {
		return v
	}
	return Vec3{
		X: v.X*a.m[0] + v.Y*a.m[3] + v.Z*a.m[6] + a.m[9],
		Y: v.X*a.m[1] + v.Y*a.m[4] + v.Z*a.m[7] + a.m[10],
		Z: v.X*a.m[2] + v.Y*a.m[5] + v.Z*a.m[8] + a.m[11],
	}
}

// mul composes two transforms: outer applied after inner.
func (a affine) mul(inner affine) affine {
	if !a.set {
		return inner
	}
	if !inner.set {
		return a
	}
	var out affine
	out.set = true
	for row := range 3 {
		for col := range 3 {
			out.m[row*3+col] = inner.m[row*3]*a.m[col] +
				inner.m[row*3+1]*a.m[3+col] +
				inner.m[row*3+2]*a.m[6+col]
		}
	}
	t := a.apply(Vec3{X: inner.m[9], Y: inner.m[10], Z: inner.m[11]})
	out.m[9], out.m[10], out.m[11] = t.X, t.Y, t.Z
	return out
}

// parseAffine reads 3MF's 12-number transform attribute. An absent or
// malformed value yields the identity rather than an error: a transform we
// cannot read is far better treated as "no transform" than as a reason to
// reject an otherwise-valid model.
func parseAffine(s string) affine {
	if strings.TrimSpace(s) == "" {
		return affine{}
	}
	fields := strings.Fields(s)
	if len(fields) != 12 {
		return affine{}
	}
	var a affine
	for i, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return affine{}
		}
		a.m[i] = v
	}
	a.set = true
	return a
}

// max3MFModelBytes bounds the decompressed model XML read from a 3MF archive, so
// a crafted archive (a zip bomb) cannot exhaust memory.
const max3MFModelBytes = 512 << 20 // 512 MiB

// rootModelPart is where a 3MF's build always starts.
const rootModelPart = "3d/3dmodel.model"

// max3MFComponentDepth bounds component recursion. A 3MF may legally nest
// components, and a malformed or hostile one may reference itself; depth plus
// the visited set below make either terminate.
const max3MFComponentDepth = 16

// max3MFTriangles caps how much geometry one archive may contribute. These
// files reach tens of millions of triangles for a detailed model, and the
// bounding box - the only thing callers need here - is fully determined long
// before that.
const max3MFTriangles = 20_000_000

// archive3MF is one opened 3MF and its parsed model parts, so a part shared by
// several components is read once rather than per reference.
type archive3MF struct {
	files  map[string]*zip.File
	parsed map[string]*xml3MFModel
}

// Load3MF opens the 3MF archive at path and returns its build as one mesh,
// following component references into other parts of the archive and applying
// build/component transforms.
func Load3MF(path string) (Mesh, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Mesh{}, err
	}
	defer func() { _ = zr.Close() }()

	a := &archive3MF{
		files:  make(map[string]*zip.File, len(zr.File)),
		parsed: map[string]*xml3MFModel{},
	}
	for _, f := range zr.File {
		a.files[normalisePart(f.Name)] = f
	}

	root, err := a.part(rootModelPart)
	if err != nil {
		return Mesh{}, err
	}

	var tris []Triangle
	// The build section is the authoritative list of what is actually on the
	// plate. Falling back to "every object in the root part" covers exports
	// that omit it, and preserves the behaviour simple single-mesh 3MFs had.
	if len(root.Items) > 0 {
		for _, item := range root.Items {
			tris = a.collect(tris, rootModelPart, item.ObjectID, parseAffine(item.Transform), 0, map[string]bool{})
		}
	} else {
		for _, obj := range root.Objects {
			tris = a.collect(tris, rootModelPart, obj.ID, affine{}, 0, map[string]bool{})
		}
	}

	if len(tris) == 0 {
		return Mesh{}, errors.New("orientation: 3MF contained no triangles")
	}
	return buildMesh(tris), nil
}

// ItemBounds is the axis-aligned extent of one build item.
type ItemBounds struct {
	Min, Max Vec3
}

// Size is the item's X/Y/Z extent in the file's units (millimetres for every
// slicer-written 3MF seen in practice).
func (b ItemBounds) Size() (x, y, z float64) {
	return b.Max.X - b.Min.X, b.Max.Y - b.Min.Y, b.Max.Z - b.Min.Z
}

// Load3MFItems returns the bounds of each build item separately, rather than
// the single merged box Load3MF gives.
//
// The distinction matters when a 3MF is a slicer PROJECT rather than a single
// model. Bambu Studio exports the whole plate: a file with four squid figures
// arranged side by side measures 428mm across as one box, which fits no bed and
// describes no product. Each build item is one printable unit, and that is what
// a catalogue entry should be measured as.
//
// Bounds only, not meshes: a detailed plate runs to millions of triangles per
// item and callers asking this question want dimensions, not geometry.
func Load3MFItems(path string) ([]ItemBounds, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()

	a := &archive3MF{
		files:  make(map[string]*zip.File, len(zr.File)),
		parsed: map[string]*xml3MFModel{},
	}
	for _, f := range zr.File {
		a.files[normalisePart(f.Name)] = f
	}
	root, err := a.part(rootModelPart)
	if err != nil {
		return nil, err
	}

	type ref struct {
		id int
		at affine
	}
	var refs []ref
	if len(root.Items) > 0 {
		for _, it := range root.Items {
			refs = append(refs, ref{id: it.ObjectID, at: parseAffine(it.Transform)})
		}
	} else {
		for _, obj := range root.Objects {
			refs = append(refs, ref{id: obj.ID})
		}
	}

	out := make([]ItemBounds, 0, len(refs))
	for _, r := range refs {
		tris := a.collect(nil, rootModelPart, r.id, r.at, 0, map[string]bool{})
		if len(tris) == 0 {
			continue
		}
		m := buildMesh(tris)
		out = append(out, ItemBounds{Min: m.Min, Max: m.Max})
	}
	if len(out) == 0 {
		return nil, errors.New("orientation: 3MF contained no triangles")
	}
	return out, nil
}

// part reads and caches one .model part of the archive.
func (a *archive3MF) part(name string) (*xml3MFModel, error) {
	name = normalisePart(name)
	if m, ok := a.parsed[name]; ok {
		return m, nil
	}
	f, ok := a.files[name]
	if !ok {
		return nil, fmt.Errorf("orientation: 3MF has no %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	var model xml3MFModel
	if err := xml.NewDecoder(io.LimitReader(rc, max3MFModelBytes)).Decode(&model); err != nil {
		return nil, err
	}
	a.parsed[name] = &model
	return &model, nil
}

// collect appends the triangles of one object - its own mesh plus everything
// its components reference - transformed into the caller's space.
//
// Best-effort by design: a component pointing at a missing part, or a cycle, is
// skipped rather than failing the whole read. A partial box beats no box, and
// the caller can still see the model.
func (a *archive3MF) collect(
	tris []Triangle, partName string, objectID int, at affine, depth int, visiting map[string]bool,
) []Triangle {
	if depth > max3MFComponentDepth || len(tris) >= max3MFTriangles {
		return tris
	}
	partName = normalisePart(partName)
	key := fmt.Sprintf("%s#%d", partName, objectID)
	if visiting[key] {
		return tris // cycle
	}
	visiting[key] = true
	defer delete(visiting, key)

	model, err := a.part(partName)
	if err != nil {
		return tris
	}
	obj := findObject(model, objectID)
	if obj == nil {
		return tris
	}

	for _, t := range obj.Triangles {
		if !validIndices(t, len(obj.Vertices)) {
			continue
		}
		tris = append(tris, newTriangle(
			at.apply(vertexToVec(obj.Vertices[t.V1])),
			at.apply(vertexToVec(obj.Vertices[t.V2])),
			at.apply(vertexToVec(obj.Vertices[t.V3])),
		))
		if len(tris) >= max3MFTriangles {
			return tris
		}
	}

	for _, c := range obj.Components {
		child := partName
		if c.Path != "" {
			child = c.Path
		}
		tris = a.collect(tris, child, c.ObjectID, at.mul(parseAffine(c.Transform)), depth+1, visiting)
	}
	return tris
}

// findObject locates an object by id, falling back to positional order for
// exports that omit the id attribute on a single-object part.
func findObject(model *xml3MFModel, id int) *xml3MFObject {
	for i := range model.Objects {
		if model.Objects[i].ID == id {
			return &model.Objects[i]
		}
	}
	if len(model.Objects) == 1 && model.Objects[0].ID == 0 {
		return &model.Objects[0]
	}
	return nil
}

// normalisePart makes archive paths comparable: 3MF references are absolute
// ("/3D/Objects/x.model") while zip entries are not, and case varies by writer.
func normalisePart(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.ToSlash(name), "/"))
}

func validIndices(t xml3MFTriangle, n int) bool {
	return t.V1 >= 0 && t.V1 < n && t.V2 >= 0 && t.V2 < n && t.V3 >= 0 && t.V3 < n
}

func vertexToVec(v xml3MFVertex) Vec3 { return Vec3{v.X, v.Y, v.Z} }
