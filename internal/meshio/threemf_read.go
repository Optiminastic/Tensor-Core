package meshio

// Reading a 3MF back into coloured parts.
//
// The inverse of Write3MF, and deliberately its neighbour: one file writes the
// format and one reads it, so a change to how colour is carried cannot be made
// in one direction and forgotten in the other.
//
// This exists because a bed is merged from files that were themselves written
// here. orientation.LoadModel can already read a 3MF, but it flattens the whole
// package into one triangle soup - which is exactly the information that has to
// survive, since a plank's white base and coloured lettering are two objects and
// merging four planks means keeping them apart.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

// modelPath is where the core spec puts the model, and where Write3MF puts it.
// The relationship in _rels/.rels is the general way to find it; every file this
// reads is one this package wrote, so the fixed path is checked first and the
// relationship is not parsed.
const modelPath = "3D/3dmodel.model"

// Read3MF returns each object in the package as a Part.
//
// A part whose object carries no material reference comes back with an empty
// Colour rather than an invented one. That is not a failure: an operator can
// upload a 3MF from anywhere for a non-generated job, and such a file has no
// opinion about colour. The caller decides what an uncoloured mesh should be
// printed as - guessing here would put a colour into a file that never said one.
func Read3MF(data []byte) ([]Part, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a 3mf package: %w", err)
	}

	var raw []byte
	for _, f := range zr.File {
		// Case-insensitive and separator-tolerant: the spec allows either
		// slash, and writers differ on capitalisation.
		if !strings.EqualFold(strings.ReplaceAll(f.Name, `\`, "/"), modelPath) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", modelPath, err)
		}
		raw, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", modelPath, err)
		}
		break
	}
	if raw == nil {
		return nil, fmt.Errorf("3mf package has no %s", modelPath)
	}

	var m xmlModel
	if err := xml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", modelPath, err)
	}

	// Materials are addressed by (pid, pindex): pid names a <basematerials>
	// group, pindex a <base> within it. Indexed by group id so an object
	// pointing at a group this file does not define resolves to nothing rather
	// than to whichever group happened to be first.
	groups := make(map[string][]string, len(m.Resources.BaseMaterials))
	for _, g := range m.Resources.BaseMaterials {
		colours := make([]string, 0, len(g.Base))
		for _, b := range g.Base {
			colours = append(colours, trimDisplayColour(b.DisplayColour))
		}
		groups[g.ID] = colours
	}

	// A slicer export carries neither its geometry nor its colour where the
	// core spec puts them. Both are resolved here rather than in a second
	// reader, so every caller gets the same answer about the same file. See
	// threemf_bambu.go.
	external := componentGeometry(zr, componentPaths(m.Resources.Objects))
	byExtruder := readBambuColours(zr)

	parts := make([]Part, 0, len(m.Resources.Objects))
	for _, o := range m.Resources.Objects {
		tris := trianglesOf(o)
		if len(tris) == 0 {
			// Before giving up: an object with no mesh of its own may be an
			// assembly pointing at one in another file.
			tris = componentTriangles(external, o)
		}
		// Still nothing means an object that is genuinely empty. Write3MF
		// rejects those, so passing one on would produce a file this package
		// cannot read back.
		if len(tris) == 0 {
			continue
		}
		colour := colourFor(groups, o)
		if colour == "" {
			// No material, so ask the slicer's own record of which filament
			// this object prints in.
			colour = byExtruder[o.ID]
		}
		parts = append(parts, Part{
			Name:      partName(o),
			Colour:    colour,
			Triangles: tris,
		})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("3mf package has no object with geometry")
	}
	return parts, nil
}

// colourFor resolves an object's (pid, pindex) to "#RRGGBB", or "" when it
// references no material or one this file does not define.
func colourFor(groups map[string][]string, o xmlObject) string {
	if o.PID == "" || o.PIndex == nil {
		return ""
	}
	colours, ok := groups[o.PID]
	if !ok || *o.PIndex < 0 || *o.PIndex >= len(colours) {
		return ""
	}
	return colours[*o.PIndex]
}

// trianglesOf rebuilds an object's triangles from its indexed vertices.
//
// A triangle referencing a vertex the mesh does not define is skipped rather
// than failing the read: one malformed face in a file an operator uploaded
// should not cost them the other twenty thousand.
func trianglesOf(o xmlObject) []orientation.Triangle {
	verts := o.Mesh.Vertices.Vertex
	out := make([]orientation.Triangle, 0, len(o.Mesh.Triangles.Triangle))
	for _, t := range o.Mesh.Triangles.Triangle {
		if !inRange(t.V1, len(verts)) || !inRange(t.V2, len(verts)) || !inRange(t.V3, len(verts)) {
			continue
		}
		out = append(out, orientation.Triangle{
			V0: orientation.Vec3{X: verts[t.V1].X, Y: verts[t.V1].Y, Z: verts[t.V1].Z},
			V1: orientation.Vec3{X: verts[t.V2].X, Y: verts[t.V2].Y, Z: verts[t.V2].Z},
			V2: orientation.Vec3{X: verts[t.V3].X, Y: verts[t.V3].Y, Z: verts[t.V3].Z},
		})
	}
	return out
}

func inRange(i, n int) bool { return i >= 0 && i < n }

// trimDisplayColour turns a 3MF displaycolor into the "#RRGGBB" Part wants.
//
// The attribute is #RRGGBBAA in files this package writes and #RRGGBB in plenty
// of others; alpha is dropped either way, because a printer has no use for it.
// Anything that is not a colour at all yields "", which reads the same as a part
// that declared none.
func trimDisplayColour(raw string) string {
	hex, ok := normaliseDisplayHex(raw)
	if !ok {
		return ""
	}
	return hex
}

// normaliseDisplayHex accepts "#RRGGBB", "#RRGGBBAA" and either without the
// leading hash.
func normaliseDisplayHex(raw string) (string, bool) {
	h := strings.TrimSpace(raw)
	h = strings.TrimPrefix(h, "#")
	if len(h) == 8 {
		h = h[:6]
	}
	if len(h) != 6 {
		return "", false
	}
	for _, r := range h {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return "", false
		}
	}
	return "#" + strings.ToUpper(h), true
}

// --- the subset of 3dmodel.model this needs -----------------------------------
//
// Field tags carry local names with no namespace, which Go's decoder matches in
// any namespace. That is what makes this read both the core-2015/02 files this
// package writes and the otherwise-identical ones a slicer exports under a
// later namespace.

type xmlModel struct {
	XMLName   xml.Name `xml:"model"`
	Resources struct {
		BaseMaterials []struct {
			ID   string `xml:"id,attr"`
			Base []struct {
				Name          string `xml:"name,attr"`
				DisplayColour string `xml:"displaycolor,attr"`
			} `xml:"base"`
		} `xml:"basematerials"`
		Objects []xmlObject `xml:"object"`
	} `xml:"resources"`
}

type xmlObject struct {
	ID     string `xml:"id,attr"`
	Name   string `xml:"name,attr"`
	PID    string `xml:"pid,attr"`
	PIndex *int   `xml:"pindex,attr"`
	// Components are how the production extension splits a package: the
	// object here is a reference and its mesh lives in another file. A
	// slicer export is always shaped this way.
	Components struct {
		Component []struct {
			Path     string `xml:"path,attr"`
			ObjectID string `xml:"objectid,attr"`
		} `xml:"component"`
	} `xml:"components"`
	Mesh struct {
		Vertices struct {
			Vertex []struct {
				X float64 `xml:"x,attr"`
				Y float64 `xml:"y,attr"`
				Z float64 `xml:"z,attr"`
			} `xml:"vertex"`
		} `xml:"vertices"`
		Triangles struct {
			Triangle []struct {
				V1 int `xml:"v1,attr"`
				V2 int `xml:"v2,attr"`
				V3 int `xml:"v3,attr"`
			} `xml:"triangle"`
		} `xml:"triangles"`
	} `xml:"mesh"`
}

// componentPaths lists the other model files these objects reference.
func componentPaths(objects []xmlObject) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, o := range objects {
		for _, c := range o.Components.Component {
			if c.Path == "" || seen[c.Path] {
				continue
			}
			seen[c.Path] = true
			out = append(out, c.Path)
		}
	}
	return out
}

// componentTriangles gathers the geometry an object references in other files.
//
// Transforms are deliberately NOT applied. Every caller here cares about the
// mesh as drawn - its size, its colours, the plate it is merged onto - and the
// placement a slicer recorded is about that slicer's bed, not ours. Merge3MF
// does the positioning for a bed Tensor builds.
func componentTriangles(
	external map[string]map[string]xmlObject, o xmlObject,
) []orientation.Triangle {
	var out []orientation.Triangle
	for _, c := range o.Components.Component {
		objects, ok := external[c.Path]
		if !ok {
			continue
		}
		referenced, ok := objects[c.ObjectID]
		if !ok {
			continue
		}
		out = append(out, trianglesOf(referenced)...)
	}
	return out
}

// partName is what to call this piece in a slicer's object list.
//
// An object in a slicer export carries no name attribute - the name lives in
// model_settings.config beside the extruder - so the id is used rather than an
// empty string, which would make several parts indistinguishable.
func partName(o xmlObject) string {
	if name := strings.TrimSpace(o.Name); name != "" {
		return name
	}
	if o.ID != "" {
		return "object " + o.ID
	}
	return ""
}
