package moonlamp

// A Bambu Studio project with several separate objects on one plate - a port
// of the shop's MoonKit.cs. Each object sits on z = 0, centred on its own bed
// position, and the project carries the print settings the moon needs.

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

//go:embed assets/bambu_settings.json
var bambuSettings string

// KitObject is one object on the plate, from a binary STL, at bed (X, Y).
type KitObject struct {
	Name string
	STL  []byte
	X, Y float64
}

// MoonPrintSettings are make_moon_sizes.ps1's overrides: 0.16 mm layers,
// 4 walls, solid infill, a small tree support from the plate up inside the
// ball to the pole (the dome closes flat there), and a 5 mm brim.
var MoonPrintSettings = map[string]string{
	"layer_height":                `"0.16"`,
	"wall_loops":                  `"4"`,
	"sparse_infill_density":       `"100%"`,
	"sparse_infill_pattern":       `"zig-zag"`,
	"enable_support":              `"1"`,
	"support_type":                `"tree(auto)"`,
	"support_on_build_plate_only": `"1"`,
	"support_threshold_angle":     `"15"`,
	"enable_prime_tower":          `"0"`,
	"brim_type":                   `"outer_only"`,
	"brim_width":                  `"5"`,
	"seam_position":               `"back"`,
	"filament_colour":             `["#FFFFFF", "#FFFFFF", "#FFFFFF", "#FFFFFF", "#FFFFFF"]`,
}

// Plate lays out a sized moon's four objects on the 330 x 320 plate exactly
// as make_moon_sizes.ps1 does: the ball on the left, cap and stands in a
// column beside it.
func Plate(s Size, order string, top, cap []byte) []KitObject {
	d := 2 * (s.R + 2.7) // widest the ball can be
	xTop, xCol := 15+d/2, 15+d+15+40
	return []KitObject{
		{Name: fmt.Sprintf("Moon top %din %s", s.Inches, order), STL: top, X: xTop, Y: 160},
		{Name: fmt.Sprintf("Moon cap %din %s", s.Inches, order), STL: cap, X: xCol, Y: 255},
		{Name: "Stand L", STL: StandSTL, X: xCol, Y: 170},
		{Name: "Stand L (wire groove down)", STL: StandGrooveSTL, X: xCol, Y: 90},
	}
}

type mesh struct {
	verts [][3]float32
	faces [][3]int
	lo    [3]float64
	hi    [3]float64
}

// loadSTL welds a binary STL's vertices and drops degenerate faces.
func loadSTL(b []byte) (mesh, error) {
	if len(b) < 84 {
		return mesh{}, errors.New("not a binary STL")
	}
	n := int(binary.LittleEndian.Uint32(b[80:84]))
	if len(b) < 84+50*n {
		return mesh{}, errors.New("truncated STL")
	}
	m := mesh{lo: [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}, hi: [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}}
	index := make(map[[3]uint32]int, n/2)
	for i := 0; i < n; i++ {
		rec := b[84+50*i:]
		var tri [3]int
		for k := 0; k < 3; k++ {
			var key [3]uint32
			var v [3]float32
			for c := 0; c < 3; c++ {
				key[c] = binary.LittleEndian.Uint32(rec[12+12*k+4*c:])
				v[c] = math.Float32frombits(key[c])
				m.lo[c] = math.Min(m.lo[c], float64(v[c]))
				m.hi[c] = math.Max(m.hi[c], float64(v[c]))
			}
			id, ok := index[key]
			if !ok {
				id = len(m.verts)
				index[key] = id
				m.verts = append(m.verts, v)
			}
			tri[k] = id
		}
		if tri[0] != tri[1] && tri[1] != tri[2] && tri[0] != tri[2] {
			m.faces = append(m.faces, tri)
		}
	}
	return m, nil
}

func uuid(n int) string { return fmt.Sprintf("%08x-61cb-4c03-9d28-80fed5dfa1dc", n) }

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
}

const modelHead = `<?xml version="1.0" encoding="UTF-8"?>
<model unit="millimeter" xml:lang="en-US" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:BambuStudio="http://schemas.bambulab.com/package/2021" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" requiredextensions="p">
`

// WriteKit writes the project. overrides replace top-level keys of the
// embedded Bambu settings.
func WriteKit(objs []KitObject, overrides map[string]string) ([]byte, error) {
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	var main, rels, settings, build, plate strings.Builder
	main.WriteString(modelHead + " <metadata name=\"Application\">BambuStudio-02.08.02.61</metadata>\n <metadata name=\"BambuStudio:3mfVersion\">1</metadata>\n <resources>\n")
	rels.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\">\n")
	settings.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<config>\n")
	plate.WriteString("  <plate>\n    <metadata key=\"plater_id\" value=\"1\"/>\n    <metadata key=\"plater_name\" value=\"\"/>\n    <metadata key=\"locked\" value=\"false\"/>\n")

	for o, obj := range objs {
		m, err := loadSTL(obj.STL)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", obj.Name, err)
		}
		cx, cy, cz := (m.lo[0]+m.hi[0])/2, (m.lo[1]+m.hi[1])/2, m.lo[2]
		meshID, topID := 2*o+1, 2*o+2
		objFile := fmt.Sprintf("3D/Objects/object_%d.model", o+1)
		w, err := create(z, objFile)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		b.Grow(len(m.verts)*60 + len(m.faces)*50)
		b.WriteString(modelHead + " <metadata name=\"BambuStudio:3mfVersion\">1</metadata>\n <resources>\n")
		fmt.Fprintf(&b, "  <object id=\"%d\" p:UUID=\"%s\" type=\"model\">\n   <mesh>\n    <vertices>\n", meshID, uuid(0x10000+o))
		for _, v := range m.verts {
			fmt.Fprintf(&b, "     <vertex x=\"%s\" y=\"%s\" z=\"%s\"/>\n",
				num5(float64(v[0])-cx), num5(float64(v[1])-cy), num5(float64(v[2])-cz))
		}
		b.WriteString("    </vertices>\n    <triangles>\n")
		for _, f := range m.faces {
			fmt.Fprintf(&b, "     <triangle v1=\"%d\" v2=\"%d\" v3=\"%d\"/>\n", f[0], f[1], f[2])
		}
		b.WriteString("    </triangles>\n   </mesh>\n  </object>\n </resources>\n</model>\n")
		if _, err := w.Write([]byte(b.String())); err != nil {
			return nil, err
		}
		fmt.Fprintf(&main, "  <object id=\"%d\" p:UUID=\"%s\" type=\"model\">\n   <components>\n    <component p:path=\"/%s\" objectid=\"%d\" p:UUID=\"%s\" transform=\"1 0 0 0 1 0 0 0 1 0 0 0\"/>\n   </components>\n  </object>\n",
			topID, uuid(0x30000+o), objFile, meshID, uuid(0x20000+o))
		fmt.Fprintf(&build, "  <item objectid=\"%d\" p:UUID=\"%s\" transform=\"1 0 0 0 1 0 0 0 1 %s %s 0\" printable=\"1\"/>\n",
			topID, uuid(0x40000+o), num5(obj.X), num5(obj.Y))
		fmt.Fprintf(&rels, " <Relationship Target=\"/%s\" Id=\"rel-%d\" Type=\"http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel\"/>\n", objFile, o+1)
		fmt.Fprintf(&settings, "  <object id=\"%d\">\n    <metadata key=\"name\" value=\"%s\"/>\n    <metadata key=\"extruder\" value=\"1\"/>\n    <part id=\"%d\" subtype=\"normal_part\">\n      <metadata key=\"name\" value=\"%s\"/>\n      <metadata key=\"matrix\" value=\"1 0 0 0 0 1 0 0 0 0 1 0 0 0 0 1\"/>\n      <mesh_stat face_count=\"%d\" edges_fixed=\"0\" degenerate_facets=\"0\" facets_removed=\"0\" facets_reversed=\"0\" backwards_edges=\"0\"/>\n    </part>\n  </object>\n",
			topID, esc(obj.Name), meshID, esc(obj.Name), len(m.faces))
		fmt.Fprintf(&plate, "    <model_instance>\n      <metadata key=\"object_id\" value=\"%d\"/>\n      <metadata key=\"instance_id\" value=\"0\"/>\n      <metadata key=\"identify_id\" value=\"%d\"/>\n    </model_instance>\n", topID, 100+o)
	}
	main.WriteString(" </resources>\n <build p:UUID=\"" + uuid(2) + "\">\n" + build.String() + " </build>\n</model>\n")
	rels.WriteString("</Relationships>\n")
	settings.WriteString(plate.String() + "  </plate>\n</config>\n")

	js := bambuSettings
	for k, v := range overrides {
		js = setJSON(js, k, v)
	}
	files := []struct{ name, body string }{
		{"[Content_Types].xml", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Types xmlns=\"http://schemas.openxmlformats.org/package/2006/content-types\">\n <Default Extension=\"rels\" ContentType=\"application/vnd.openxmlformats-package.relationships+xml\"/>\n <Default Extension=\"model\" ContentType=\"application/vnd.ms-package.3dmanufacturing-3dmodel+xml\"/>\n <Default Extension=\"png\" ContentType=\"image/png\"/>\n <Default Extension=\"gcode\" ContentType=\"text/x.gcode\"/>\n</Types>\n"},
		{"_rels/.rels", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Relationships xmlns=\"http://schemas.openxmlformats.org/package/2006/relationships\">\n <Relationship Target=\"/3D/3dmodel.model\" Id=\"rel-1\" Type=\"http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel\"/>\n</Relationships>\n"},
		{"3D/3dmodel.model", main.String()},
		{"3D/_rels/3dmodel.model.rels", rels.String()},
		{"Metadata/model_settings.config", settings.String()},
		{"Metadata/project_settings.config", js},
		{"Metadata/slice_info.config", "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<config>\n  <header>\n    <header_item key=\"X-BBL-Client-Type\" value=\"slicer\"/>\n    <header_item key=\"X-BBL-Client-Version\" value=\"02.08.02.61\"/>\n  </header>\n</config>\n"},
	}
	for _, f := range files {
		w, err := create(z, f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// create adds a deflated entry stamped with the time it was made - zip.Create
// leaves the date zero, which reads as 1980-00-00 and upsets some readers.
func create(z *zip.Writer, name string) (io.Writer, error) {
	return z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
}

// num5 formats like C#'s "0.#####": up to five decimals, no trailing zeros.
func num5(v float64) string {
	s := fmt.Sprintf("%.5f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

// setJSON replaces the value of a top-level key in Bambu's flat JSON, or
// appends the key - MoonKit.SetJson.
func setJSON(js, key, value string) string {
	k := `"` + key + `": `
	i := strings.Index(js, k)
	if i < 0 {
		last := strings.LastIndex(js, "}")
		return strings.TrimRight(js[:last], " \n\r\t") + ",\n    " + k + value + "\n}\n"
	}
	s := i + len(k)
	e := s
	switch js[s] {
	case '[':
		depth := 0
		for e = s; e < len(js); e++ {
			if js[e] == '[' {
				depth++
			} else if js[e] == ']' {
				depth--
				if depth == 0 {
					e++
					break
				}
			}
		}
	case '"':
		e = strings.Index(js[s+1:], `"`) + s + 2
	default:
		for e < len(js) && js[e] != ',' && js[e] != '\n' && js[e] != '}' {
			e++
		}
	}
	return js[:s] + value + js[e:]
}
