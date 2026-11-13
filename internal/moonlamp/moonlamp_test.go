package moonlamp

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/orientation"
)

func grey(w, h int, v uint8) image.Image {
	img := image.NewGray(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: v}), image.Point{}, draw.Src)
	return img
}

func key(v orientation.Vec3) string { return fmt.Sprintf("%.5f,%.5f,%.5f", v.X, v.Y, v.Z) }

// A slicer refuses, or silently repairs into something else, a mesh with a
// hole in it or with faces pointing both ways. Every edge must be used by
// exactly two triangles, once in each direction.
func TestSolidIsWatertightAndFacesOutward(t *testing.T) {
	m, err := Build(grey(600, 800, 128), "6th Dec 2026", "")
	if err != nil {
		t.Fatal(err)
	}
	tris := m.Solid()
	edges := map[[2]string]int{}
	var volume float64
	minZ, maxZ, maxR := math.Inf(1), math.Inf(-1), 0.0
	for _, tr := range tris {
		vs := [3]orientation.Vec3{tr.V0, tr.V1, tr.V2}
		for k := 0; k < 3; k++ {
			edges[[2]string{key(vs[k]), key(vs[(k+1)%3])}]++
			minZ, maxZ = math.Min(minZ, vs[k].Z), math.Max(maxZ, vs[k].Z)
			maxR = math.Max(maxR, math.Hypot(vs[k].X, vs[k].Y))
		}
		a, b, c := tr.V0, tr.V1, tr.V2
		volume += (a.X*(b.Y*c.Z-b.Z*c.Y) - a.Y*(b.X*c.Z-b.Z*c.X) + a.Z*(b.X*c.Y-b.Y*c.X)) / 6
	}
	for e, n := range edges {
		if n != 1 || edges[[2]string{e[1], e[0]}] != 1 {
			t.Fatalf("edge %v used %d times forward, %d back", e, n, edges[[2]string{e[1], e[0]}])
		}
	}
	if volume <= 0 {
		t.Fatalf("volume %.1f mm3: faces point inward", volume)
	}
	// About 65 mm across and 68 mm tall, standing on z = 0.
	if minZ != 0 || maxZ < 66 || maxZ > 69 || maxR < 31 || maxR > 33 {
		t.Fatalf("z %.2f..%.2f, radius %.2f", minZ, maxZ, maxR)
	}
	// A 0.6-2.6 mm shell over a 60 mm ball (~12,000 mm2 x ~1.6 mm) plus the neck:
	// about 20 cm3. A solid 65 mm ball would be ~144 cm3.
	if volume < 12000 || volume > 32000 {
		t.Fatalf("volume %.0f mm3 is not a thin shell", volume)
	}
	t.Logf("%d triangles, %.1f cm3", len(tris), volume/1000)
}

// A white photo makes the wall thin where it sits; the text lightens the
// moon beside it.
func TestPhotoAndTextChangeTheWall(t *testing.T) {
	plain, err := Build(nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	withWhite, err := Build(grey(600, 800, 255), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := withWhite.Sample(0, 0); got < 0.99 {
		t.Fatalf("centre of a white photo = %.2f, want ~1", got)
	}
	if plain.Sample(0, 0) > 0.95 {
		t.Fatal("the bare moon is already white at the centre - the test proves nothing")
	}
	text, err := Build(grey(600, 800, 0), "LOVE", "")
	if err != nil {
		t.Fatal(err)
	}
	brightest := 0.0
	for lon := -180.0; lon < 180; lon += 0.5 {
		if lon > -70 && lon < 70 {
			continue // the photo
		}
		brightest = math.Max(brightest, text.Sample(lon, textLat))
	}
	if brightest < 0.95 {
		t.Fatalf("no text found beside the photo (brightest %.2f)", brightest)
	}
}

func TestSizeForSKU(t *testing.T) {
	for sku, want := range map[string]int{"3DMS01-4P": 4, "3DMS01-5p": 5, "3DMS01-6P": 6, "3DMS01-6": 0, "3DMS01-7P": 0, "": 0} {
		s, ok := SizeForSKU(sku)
		if (want == 0) == ok || (ok && s.Inches != want) {
			t.Errorf("SizeForSKU(%q) = %v %v, want %d", sku, s.Inches, ok, want)
		}
	}
}

// The skin OpenSCAD cuts must be closed, or the cut leaves holes in the moon.
func TestSkinSTLIsClosed(t *testing.T) {
	m, err := BuildWith(Sized(), nil, "RAHUL", "")
	if err != nil {
		t.Fatal(err)
	}
	s := Size{Inches: 4, R: 50, Step: 2} // coarse: the topology is what is tested
	mm, err := loadSTL(m.SkinSTL(s))
	if err != nil {
		t.Fatal(err)
	}
	edges := map[[2]int]int{}
	for _, f := range mm.faces {
		for k := 0; k < 3; k++ {
			edges[[2]int{f[k], f[(k+1)%3]}]++
		}
	}
	for e, n := range edges {
		if n != 1 || edges[[2]int{e[1], e[0]}] != 1 {
			t.Fatalf("edge %v: %d forward, %d back", e, n, edges[[2]int{e[1], e[0]}])
		}
	}
	if w := mm.hi[0] - mm.lo[0]; w < 101 || w > 106 {
		t.Fatalf("width %.1f mm, want ~103", w)
	}
}

func TestWriteKitIsABambuProject(t *testing.T) {
	m, _ := BuildWith(Sized(), nil, "", "")
	skin := m.SkinSTL(Size{Inches: 4, R: 50, Step: 3})
	kit, err := WriteKit(Plate(Size{Inches: 4, R: 50}, "T1", skin, skin), MoonPrintSettings)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(kit), int64(len(kit)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range z.File {
		names[f.Name] = true
	}
	for _, want := range []string{"3D/3dmodel.model", "Metadata/project_settings.config", "Metadata/model_settings.config",
		"3D/Objects/object_1.model", "3D/Objects/object_4.model"} {
		if !names[want] {
			t.Errorf("3MF missing %s", want)
		}
	}
	if s := setJSON(`{"a": "1", "layer_height": "0.2", "c": [1, 2]}`, "layer_height", `"0.16"`); !strings.Contains(s, `"layer_height": "0.16"`) {
		t.Fatalf("setJSON: %s", s)
	}
}
