package meshio

// Reading a 3MF that Bambu Studio wrote.
//
// Read3MF was written against the files this package writes and the core spec
// they follow: geometry inside 3D/3dmodel.model, colour in a <basematerials>
// group addressed by (pid, pindex). A slicer export is neither of those things,
// and a designer hands over exactly that.
//
// Two differences, and both are total rather than partial:
//
//   - THE GEOMETRY IS NOT IN THE MODEL FILE. 3dmodel.model holds an object made
//     of <components> pointing at /3D/Objects/object_N.model through the
//     production extension, so a reader that looks only at the first file finds
//     an object with no triangles and concludes the package has none.
//   - THE COLOUR IS NOT A MATERIAL. Bambu records which EXTRUDER an object
//     prints on, in Metadata/model_settings.config, and what each extruder is
//     loaded with in Metadata/project_settings.config's filament_colour. There
//     is no <basematerials> anywhere.
//
// Measured on a designer's REDHEART.3mf: no basematerials, no triangles, and
// filament_colour ["#C12E1F", "#80FFFF"] with the object on extruder 1. Read as
// the core spec alone, it is an empty package with no colours - which is what
// put an uploaded design on the floor saying "no filament colour recorded".

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

const (
	bambuModelSettings   = "Metadata/model_settings.config"
	bambuProjectSettings = "Metadata/project_settings.config"
)

// bambuColours is what a slicer export says about colour: which filament each
// object prints in, by object id.
type bambuColours map[string]string

// readBambuColours maps every object id to the hex of the filament it prints
// in, or returns nil when the package is not a slicer export.
//
// Both halves are required. Without model_settings there is nothing to say
// which object uses which extruder, and without project_settings an extruder
// number is not a colour.
func readBambuColours(zr *zip.Reader) bambuColours {
	extruders := readObjectExtruders(zr)
	if len(extruders) == 0 {
		return nil
	}
	filaments := readFilamentColours(zr)
	if len(filaments) == 0 {
		return nil
	}

	out := make(bambuColours, len(extruders))
	for objectID, extruder := range extruders {
		// One-based, as everything that faces a person about a printer is.
		if extruder < 1 || extruder > len(filaments) {
			continue
		}
		if hex, ok := normaliseDisplayHex(filaments[extruder-1]); ok {
			out[objectID] = hex
		}
	}
	return out
}

// readObjectExtruders reads which extruder each object prints on.
func readObjectExtruders(zr *zip.Reader) map[string]int {
	raw := fileFrom(zr, bambuModelSettings)
	if raw == nil {
		return nil
	}
	var cfg struct {
		Objects []struct {
			ID       string `xml:"id,attr"`
			Metadata []struct {
				Key   string `xml:"key,attr"`
				Value string `xml:"value,attr"`
			} `xml:"metadata"`
		} `xml:"object"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return nil
	}

	out := make(map[string]int, len(cfg.Objects))
	for _, o := range cfg.Objects {
		for _, m := range o.Metadata {
			if m.Key != "extruder" {
				continue
			}
			if n, err := strconv.Atoi(strings.TrimSpace(m.Value)); err == nil {
				out[o.ID] = n
			}
		}
	}
	return out
}

// readFilamentColours reads what each extruder is loaded with.
//
// The file is JSON despite the .config extension, and every value in it is a
// string or an array of strings - filament_colour is the latter, one entry per
// slot in slot order.
func readFilamentColours(zr *zip.Reader) []string {
	raw := fileFrom(zr, bambuProjectSettings)
	if raw == nil {
		return nil
	}
	var cfg struct {
		FilamentColour []string `json:"filament_colour"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	return cfg.FilamentColour
}

// fileFrom returns one file's bytes, matched the way Read3MF matches: case
// insensitively and tolerant of either separator.
func fileFrom(zr *zip.Reader, path string) []byte {
	for _, f := range zr.File {
		if !strings.EqualFold(strings.ReplaceAll(f.Name, `\`, "/"), path) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		raw, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil
		}
		return raw
	}
	return nil
}

// componentGeometry reads the object files a production-extension package
// points at, keyed by the path they live under.
//
// Each one is an ordinary 3MF model document holding the meshes the main file
// only references, so it is parsed exactly as the main one is.
func componentGeometry(zr *zip.Reader, paths []string) map[string]map[string]xmlObject {
	out := make(map[string]map[string]xmlObject, len(paths))
	for _, p := range paths {
		raw := fileFrom(zr, strings.TrimPrefix(p, "/"))
		if raw == nil {
			continue
		}
		var m xmlModel
		if err := xml.Unmarshal(raw, &m); err != nil {
			continue
		}
		byID := make(map[string]xmlObject, len(m.Resources.Objects))
		for _, o := range m.Resources.Objects {
			byID[o.ID] = o
		}
		out[p] = byID
	}
	return out
}

// IsSlicedPlate reports whether a 3MF is a slicer's OUTPUT rather than a model.
//
// Bambu Studio writes two different things with the same extension. A project
// holds the meshes somebody drew; a ".gcode.3mf" holds a finished plate - the
// toolpaths, the supports, the prime tower, arranged for one bed on one
// printer.
//
// They must not be confused at an upload. Tensor packs its own beds and merges
// its own plates, so a plate that has already been packed measures as the whole
// bed rather than as the product - the failure Load3MFItems describes, where
// four figures side by side measure 428mm across and fit nothing. Accepting one
// would put a job on the floor whose dimensions describe somebody else's
// arrangement.
//
// Detected from the contents rather than the name: a file renamed on the way
// out is still a plate, and the toolpaths are what make it one.
func IsSlicedPlate(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		name := strings.ToLower(strings.ReplaceAll(f.Name, `\`, "/"))
		if strings.HasSuffix(name, ".gcode") || strings.HasSuffix(name, ".gcode.md5") {
			return true
		}
	}
	return false
}
