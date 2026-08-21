package slicing

// Builds a sliceable Bambu Studio project by injecting a plate mesh into a
// per-machine settings template.
//
// Why this exists: Bambu Studio's CLI refuses to slice for a multi-extruder
// machine when the filament-to-nozzle map is passed as flags, failing with
// return_code -66. Every flag spelling was tried (--filament-map,
// --filament-map-mode, --load-filament-ids, --load-defaultfila) and none work,
// because the map is not a project setting: it lives on the *plate*, in
// Metadata/model_settings.config, as filament_maps / filament_map_mode. The only
// way to hand it to the CLI is inside a real project file.
//
// So each multi-extruder machine gets a template: a project Bambu Studio itself
// wrote, stripped down to its settings, with a placeholder mesh. Slicing swaps
// that placeholder for the packed plate and leaves everything else untouched.
// See templates/derive_template.py for how a template is produced.

import (
	"archive/zip"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

//go:embed templates/*.3mf
var templateFS embed.FS

// meshPath is the part inside a Bambu project that holds the object's geometry.
// The root model references it by relationship, so replacing this one part
// swaps the geometry without touching any other reference in the package.
const meshPath = "3D/Objects/object_1.model"

const rootModelPath = "3D/3dmodel.model"

// itemTransform matches the build item's placement matrix in the root model.
// 3MF stores it row-major as 12 values, the last three being the translation.
var itemTransform = regexp.MustCompile(`(<item[^>]*transform=")([^"]*)(")`)

// ErrNoTemplate means the machine has no settings template, so the caller should
// fall back to driving the slicer with individual presets.
var ErrNoTemplate = errors.New("slicing: no project template for machine")

// PlateTemplateName is the template file for a machine family and nozzle, using
// the same family/nozzle naming as the bundled presets ("h2c-0.4.3mf").
func PlateTemplateName(family string, nozzleMM float64) string {
	return fmt.Sprintf("%s-%s.3mf", strings.ToLower(family), trimNozzle(nozzleMM))
}

// HasPlateTemplate reports whether a settings template is bundled for a machine.
// Single-extruder machines slice fine from presets and need none.
func HasPlateTemplate(family string, nozzleMM float64) bool {
	_, err := templateFS.ReadFile("templates/" + PlateTemplateName(family, nozzleMM))
	return err == nil
}

// BuildPlateProject writes a sliceable project at dstPath: the settings template
// for family/nozzle, with its placeholder geometry replaced by the mesh at
// stlPath. Returns ErrNoTemplate when the machine has no template.
func BuildPlateProject(family string, nozzleMM float64, stlPath, dstPath string) error {
	template, err := templateFS.ReadFile("templates/" + PlateTemplateName(family, nozzleMM))
	if err != nil {
		return fmt.Errorf("%w: %s %gmm", ErrNoTemplate, family, nozzleMM)
	}
	stl, err := os.ReadFile(stlPath)
	if err != nil {
		return fmt.Errorf("read plate: %w", err)
	}
	mesh, err := orientation.LoadSTL(stl)
	if err != nil {
		return fmt.Errorf("parse plate %s: %w", stlPath, err)
	}
	if len(mesh.Triangles) == 0 {
		return fmt.Errorf("plate %s has no triangles", stlPath)
	}

	src, err := zip.NewReader(strings.NewReader(string(template)), int64(len(template)))
	if err != nil {
		return fmt.Errorf("read template: %w", err)
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	for _, f := range src.File {
		var replacement string
		switch f.Name {
		case meshPath:
			replacement = meshXML(mesh)
		case rootModelPath:
			original, err := readZipEntry(f)
			if err != nil {
				return err
			}
			replacement = placeOnBed(original, mesh.Max.Z-mesh.Min.Z)
		}
		if err := copyOrReplace(zw, f, replacement); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finalise project: %w", err)
	}
	return out.Close()
}

// copyOrReplace writes one template entry to the project, substituting
// replacement for its contents when non-empty.
func copyOrReplace(zw *zip.Writer, f *zip.File, replacement string) error {
	w, err := zw.Create(f.Name)
	if err != nil {
		return fmt.Errorf("write %s: %w", f.Name, err)
	}
	if replacement != "" {
		_, err = io.WriteString(w, replacement)
		return err
	}
	r, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	return err
}

func readZipEntry(f *zip.File) (string, error) {
	r, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", f.Name, err)
	}
	return string(data), nil
}

// placeOnBed rewrites the build item's Z translation to half the mesh height.
//
// Bambu centres an object's mesh on the origin and lifts it onto the bed with
// the build item transform, so the Z translation must be half the object's
// height for its underside to sit at Z=0. The template's own value describes its
// placeholder cube. X and Y are left alone: they are a valid bed position, and
// --arrange repositions in the XY plane anyway.
func placeOnBed(rootModel string, heightMM float64) string {
	return itemTransform.ReplaceAllStringFunc(rootModel, func(match string) string {
		parts := itemTransform.FindStringSubmatch(match)
		values := strings.Fields(parts[2])
		if len(values) != 12 {
			return match
		}
		values[11] = strconv.FormatFloat(heightMM/2, 'f', -1, 64)
		return parts[1] + strings.Join(values, " ") + parts[3]
	})
}

// meshXML renders a mesh as a 3MF object part, centred on the origin per Bambu's
// convention (see placeOnBed). An off-centre mesh reads as outside the print
// volume and the slice fails with return_code -50.
func meshXML(mesh orientation.Mesh) string {
	cx := (mesh.Min.X + mesh.Max.X) / 2
	cy := (mesh.Min.Y + mesh.Max.Y) / 2
	cz := (mesh.Min.Z + mesh.Max.Z) / 2

	// STL is a triangle soup that repeats every shared vertex; 3MF indexes them.
	index := make(map[orientation.Vec3]int, len(mesh.Triangles))
	vertices := make([]orientation.Vec3, 0, len(mesh.Triangles))
	corners := make([][3]int, 0, len(mesh.Triangles))
	for _, t := range mesh.Triangles {
		var c [3]int
		for i, v := range [3]orientation.Vec3{t.V0, t.V1, t.V2} {
			v = orientation.Vec3{X: v.X - cx, Y: v.Y - cy, Z: v.Z - cz}
			n, seen := index[v]
			if !seen {
				n = len(vertices)
				index[v] = n
				vertices = append(vertices, v)
			}
			c[i] = n
		}
		corners = append(corners, c)
	}

	var b strings.Builder
	b.WriteString(meshHeader)
	b.WriteString("    <vertices>\n")
	for _, v := range vertices {
		fmt.Fprintf(&b, "     <vertex x=\"%s\" y=\"%s\" z=\"%s\"/>\n", coord(v.X), coord(v.Y), coord(v.Z))
	}
	b.WriteString("    </vertices>\n    <triangles>\n")
	for _, c := range corners {
		fmt.Fprintf(&b, "     <triangle v1=\"%d\" v2=\"%d\" v3=\"%d\"/>\n", c[0], c[1], c[2])
	}
	b.WriteString("    </triangles>\n")
	b.WriteString(meshFooter)
	return b.String()
}

// coord formats a millimetre coordinate without an exponent, which the 3MF
// schema does not accept.
func coord(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// The object id and UUID match what the template's root model references, so the
// swapped-in geometry resolves exactly as the placeholder did.
const meshHeader = `<?xml version="1.0" encoding="UTF-8"?>
<model unit="millimeter" xml:lang="en-US" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:BambuStudio="http://schemas.bambulab.com/package/2021" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" requiredextensions="p">
 <metadata name="BambuStudio:3mfVersion">1</metadata>
 <resources>
  <object id="1" p:UUID="00010000-81cb-4c03-9d28-80fed5dfa1dc" type="model">
   <mesh>
`

const meshFooter = `   </mesh>
  </object>
 </resources>
 <build/>
</model>
`
