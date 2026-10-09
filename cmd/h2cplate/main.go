// Builds a two-colour test plate through Tensor's OWN packing and plate writer.
//
// Not a fixture and not a hand-placed 3MF. It runs bedpack.PackColumnOn over
// the bed a one-unit batch is actually classed for, then meshio.Merge3MF, which
// is the pair of calls buildMergedPlate makes. So both things under test are
// the production ones: WHERE the plate lands (the packer) and the per-part
// extruder assignment (the writer).
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// box is an axis-aligned cuboid as 12 triangles, enough to slice.
func box(x0, y0, z0, dx, dy, dz float64) []orientation.Triangle {
	v := func(x, y, z float64) orientation.Vec3 {
		return orientation.Vec3{X: x0 + x*dx, Y: y0 + y*dy, Z: z0 + z*dz}
	}
	// Normal and Area are left zero: the 3MF writer emits vertices and
	// triangle indices only, and the slicer computes its own normals - STL
	// normals are famously wrong, which is why orientation recomputes them too.
	tri := func(a, b, c orientation.Vec3) orientation.Triangle {
		return orientation.Triangle{V0: a, V1: b, V2: c}
	}
	quad := func(a, b, c, d orientation.Vec3) []orientation.Triangle {
		return []orientation.Triangle{tri(a, b, c), tri(a, c, d)}
	}
	p000, p100 := v(0, 0, 0), v(1, 0, 0)
	p110, p010 := v(1, 1, 0), v(0, 1, 0)
	p001, p101 := v(0, 0, 1), v(1, 0, 1)
	p111, p011 := v(1, 1, 1), v(0, 1, 1)

	var t []orientation.Triangle
	t = append(t, quad(p000, p010, p110, p100)...) // bottom
	t = append(t, quad(p001, p101, p111, p011)...) // top
	t = append(t, quad(p000, p100, p101, p001)...) // front
	t = append(t, quad(p110, p010, p011, p111)...) // back
	t = append(t, quad(p000, p001, p011, p010)...) // left
	t = append(t, quad(p100, p110, p111, p101)...) // right
	return t
}

func main() {
	out := "h2c-two-colour-plate.3mf"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	// A plank is ONE unit carrying two colours: a white body and the lettering.
	// Two distinct colours is the condition for two filament slots, which is
	// what lets the plate address two nozzles.
	const plankX, plankY, plankZ = 200.0, 50.0, 6.0
	parts := []meshio.Part{
		{Name: "body-white", Colour: "#FFFFFF", Material: "PLA",
			Triangles: box(0, 0, 0, plankX, plankY*0.6, plankZ)},
		{Name: "letters-blue", Colour: "#2850E0", Material: "PLA",
			Triangles: box(0, plankY*0.7, 0, plankX, plankY*0.3, plankZ)},
	}

	// The bed a ONE-UNIT batch is classed for - the exact case that failed.
	family := production.BedFamilyForUnits(1)
	bed := bedpack.BedForFamily(family)

	placements, rejected := bedpack.PackColumnOn(bed, []bedpack.UnitFootprint{
		{RefID: "plank", XMM: plankX, YMM: plankY, ZMM: plankZ},
	})
	if len(rejected) > 0 || len(placements) != 1 {
		log.Fatalf("the plank did not fit the %s bed", family)
	}
	p := placements[0]

	data, bbox, err := meshio.Merge3MF([]meshio.PlacedModel{{
		Name: "TENSOR-H2C-TEST", Parts: parts,
		XOffsetMM: p.XOffsetMM, YOffsetMM: p.YOffsetMM, Rotated: p.Rotated,
	}})
	if err != nil {
		log.Fatalf("merge plate: %v", err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		log.Fatalf("save: %v", err)
	}

	fmt.Printf("wrote %s (%d bytes)\n", out, len(data))
	fmt.Printf("classed %s, bed starts at X=%.0f\n", family, bed.XOriginMM)
	fmt.Printf("placed at X=%.1f, so the plank spans %.1f..%.1f\n",
		p.XOffsetMM, p.XOffsetMM, p.XOffsetMM+plankX)
	fmt.Printf("plate size %.1f x %.1f x %.1f\n", bbox.XMM, bbox.YMM, bbox.ZMM)
	if p.XOffsetMM < 25 {
		fmt.Println("FAIL: before the H2C's second nozzle at X=25")
		os.Exit(1)
	}
	fmt.Println("OK: inside the strip both H2C nozzles reach (25..325)")
}
