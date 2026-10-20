package httpapi

// The numbers in here are measured, not invented. Each plank below is the
// bounding geometry OpenSCAD actually produced for that template at the
// product's 200 x 50 x 40, rendered with the same arguments the app sends:
//
//   dual_one_heart  PRAKASH / REYA     plate x -100..100  letters x -88.93..60.35
//   dnp_two_heart   SUBHANJANA / REYA  plate x -100..100  letters x -73.26..72.14
//
// both with the plate at z -1.24..1.05 and the lettering topping out at 38.76.
// A real render is 13 seconds and needs OpenSCAD, so the shapes are stood in
// for by boxes with those extents - what is under test is where the heart is
// put, and that is decided from the extents alone.

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const (
	plateMinX, plateMaxX = -100.0, 100.0
	plateMinY, plateMaxY = -25.0, 25.0
	plateMinZ, plateMaxZ = -1.24, 1.05
	letterMinZ           = -0.65
	letterTopZ           = 38.76
)

func TestHeartedPreviewPlacesOneHeartInTheRightMargin(t *testing.T) {
	model := plankModel(t, -88.93, 60.35)

	parts := readParts(t, heartedPreview(model, 1))
	if len(parts) != 3 {
		t.Fatalf("expected the plank's two parts plus one heart, got %d", len(parts))
	}

	heart := parts[2]
	if heart.Colour != previewHeartColour() {
		t.Errorf("heart colour = %q, want the shop's red %q", heart.Colour, previewHeartColour())
	}
	b := partBox(heart)

	// Inside the margin: clear of the last letter and still on the plate.
	if b.MinX < 60.35 {
		t.Errorf("the heart starts at x=%.2f, overlapping lettering that ends at 60.35", b.MinX)
	}
	if b.MaxX > plateMaxX {
		t.Errorf("the heart reaches x=%.2f, past the plate's end at %.2f", b.MaxX, plateMaxX)
	}

	// Centred in that margin, which is what "add properly" has to mean when
	// the margin is 39.65mm and the heart is 18mm.
	wantCX := (60.35 + plateMaxX) / 2
	if gotCX := (b.MinX + b.MaxX) / 2; math.Abs(gotCX-wantCX) > 0.01 {
		t.Errorf("heart centred at x=%.2f, want %.2f", gotCX, wantCX)
	}

	// Centred on the 50mm width, and level with the top of the lettering.
	if gotCY := (b.MinY + b.MaxY) / 2; math.Abs(gotCY) > 0.01 {
		t.Errorf("heart centred at y=%.2f, want the middle of the 50mm width (0)", gotCY)
	}
	if math.Abs(b.MaxZ-letterTopZ) > 0.01 {
		t.Errorf("heart tops out at z=%.2f, want the lettering's %.2f", b.MaxZ, letterTopZ)
	}

	// Unscaled, because it fits: the shop's heart is 18 x 18 x 38.
	if math.Abs((b.MaxX-b.MinX)-18) > 0.01 || math.Abs((b.MaxZ-b.MinZ)-38) > 0.01 {
		t.Errorf("heart is %.2f x %.2f, want the shop's 18 x 38 unscaled",
			b.MaxX-b.MinX, b.MaxZ-b.MinZ)
	}
}

func TestHeartedPreviewPlacesTwoHeartsAtBothEnds(t *testing.T) {
	model := plankModel(t, -73.26, 72.14)

	parts := readParts(t, heartedPreview(model, 2))
	if len(parts) != 4 {
		t.Fatalf("expected the plank's two parts plus two hearts, got %d", len(parts))
	}

	right, left := partBox(parts[2]), partBox(parts[3])
	if right.MinX < 72.14 {
		t.Errorf("the right heart starts at x=%.2f, overlapping lettering ending at 72.14", right.MinX)
	}
	if left.MaxX > -73.26 {
		t.Errorf("the left heart reaches x=%.2f, overlapping lettering starting at -73.26", left.MaxX)
	}
	if left.MinX < plateMinX || right.MaxX > plateMaxX {
		t.Errorf("a heart hangs off the plate: left starts %.2f, right ends %.2f",
			left.MinX, right.MaxX)
	}

	// EACH CENTRED IN ITS OWN MARGIN, which is not the same as mirrored. The
	// template reserves equal margins, but the lettering inside them is not
	// exactly centred - this plank's runs -73.26..72.14, 1.12mm to the left -
	// so the two gaps differ slightly and each heart follows its own. Centring
	// them symmetrically instead would push one of them towards the letters by
	// half that error, which is the one thing the placement must not do.
	for _, c := range []struct {
		name     string
		got      float64
		from, to float64
	}{
		{"right", (right.MinX + right.MaxX) / 2, 72.14, plateMaxX},
		{"left", (left.MinX + left.MaxX) / 2, plateMinX, -73.26},
	} {
		want := (c.from + c.to) / 2
		if math.Abs(c.got-want) > 0.01 {
			t.Errorf("%s heart centred at x=%.2f, want %.2f (the middle of its margin)",
				c.name, c.got, want)
		}
	}

	// Both still fit unscaled at the tightest real geometry seen: a 26.7mm
	// margin against an 18mm heart. If this ever fails the clamp has started
	// firing on an ordinary plank and the placement needs rethinking, not the
	// test relaxing.
	if w := right.MaxX - right.MinX; math.Abs(w-18) > 0.01 {
		t.Errorf("the right heart was scaled to %.2fmm wide; 18mm fits in this margin", w)
	}
}

func TestHeartedPreviewStillPlacesAHeartOnAOnePartModel(t *testing.T) {
	// A model whose plate and lettering arrived as one object. Identifying the
	// plate by thickness cannot work - the single part is both full-length and
	// full-height - so this is what exercises the vertex-height fallback.
	plate := boxMesh(plateMinX, plateMaxX, plateMinY, plateMaxY, plateMinZ, plateMaxZ)
	letters := boxMesh(-88.93, 60.35, -22, 22, letterMinZ, letterTopZ)
	model, err := meshio.Write3MF([]meshio.Part{{
		Name: "plank", Colour: "#FFFFFF", Material: "PLA",
		Triangles: append(plate, letters...),
	}})
	if err != nil {
		t.Fatalf("build the test plank: %v", err)
	}

	parts := readParts(t, heartedPreview(model, 1))
	if len(parts) != 2 {
		t.Fatalf("expected the plank plus one heart, got %d parts", len(parts))
	}
	b := partBox(parts[1])
	if b.MinX < 60.35 {
		t.Errorf("the heart starts at x=%.2f, overlapping lettering ending at 60.35", b.MinX)
	}
	if b.MaxX > plateMaxX {
		t.Errorf("the heart reaches x=%.2f, past the plate's end", b.MaxX)
	}
}

func TestHeartedPreviewLeavesAPlainPlankAlone(t *testing.T) {
	model := plankModel(t, -88.93, 60.35)

	// The variant the shop says already previews correctly. Nothing may be
	// added to it, and the bytes must come back as they went in - this is also
	// what every product that is not a plank relies on.
	if got := heartedPreview(model, 0); len(got) != len(model) {
		t.Errorf("a no-heart plank was rewritten: %d bytes in, %d out", len(model), len(got))
	}
}

func TestHeartedPreviewLeavesAnSTLAlone(t *testing.T) {
	// The single-colour fallback. It carries no parts to add a coloured one to,
	// and must be handed back rather than refused - that job's preview is
	// plain because its colour pipeline failed, which is a different problem.
	stl := []byte("solid plank\nendsolid plank\n")
	if got := heartedPreview(stl, 1); string(got) != string(stl) {
		t.Errorf("an STL model was rewritten")
	}
}

func TestHeartsForPreviewReadsTheModelsOwnRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params string
		want   int
	}{
		// The three per-heart-count templates. Nothing renders from them any
		// more, but their models are still in the bucket and the KEY is the
		// only count those rows carry.
		{"one heart", `{"template":"dual_one_heart"}`, 1},
		{"two hearts", `{"template":"dnp_two_heart"}`, 2},
		{"no heart", `{"template":"dnp_with_no_heart"}`, 0},
		{"a product this knows nothing about", `{"template":"soulmate_keychain"}`, 0},

		// The joinery template renders every count, so its key says nothing -
		// the count travels beside it. A row read by the key alone would draw
		// every plank in the catalogue plain.
		{"joinery, two hearts", `{"template":"dnp_heart_joinery","hearts":2}`, 2},
		{"joinery, one heart", `{"template":"dnp_heart_joinery","hearts":1}`, 1},
		{"joinery, no heart", `{"template":"dnp_heart_joinery","hearts":0}`, 0},
		// A count recorded against a legacy key must not override it either
		// way round: both sources agree here, and the point is that reading
		// the count first does not break the rows that only have a key.
		{"both sources agree", `{"template":"dnp_two_heart","hearts":2}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := heartsForPreview(gen.ProductionJob{}, []byte(tc.params))
			if got != tc.want {
				t.Errorf("heartsForPreview(%s) = %d, want %d", tc.params, got, tc.want)
			}
		})
	}
}

func TestHeartsForPreviewFallsBackToTheOrderWhenTheRecordIsSilent(t *testing.T) {
	// A recoloured model: the fast path used to store {"template": ""} and left
	// a genuine one-heart plank looking plain. JOB-115361-2 is this row.
	oneHeart := jobWithProperties(t, "1 RED HEART")

	for _, tc := range []struct {
		name   string
		params string
	}{
		{"recoloured, blank template", `{"template":""}`},
		{"nothing recorded at all", ``},
		{"a record that cannot be read", `{{{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := heartsForPreview(oneHeart, []byte(tc.params)); got != 1 {
				t.Errorf("heartsForPreview = %d, want the order's 1", got)
			}
		})
	}

	// And it must not invent one where the order asked for none.
	plain := jobWithProperties(t, "0 RED HEART")
	if got := heartsForPreview(plain, []byte(`{"template":""}`)); got != 0 {
		t.Errorf("heartsForPreview = %d on a no-heart order, want 0", got)
	}
}

func TestHeartsForPreviewTrustsARecordOverTheOrder(t *testing.T) {
	// The order says one heart; the model on disk was built from the no-heart
	// template. The picture has to describe the model, not the order - drawing
	// a heart here would claim the file has a margin reserved for one.
	job := jobWithProperties(t, "1 RED HEART")
	if got := heartsForPreview(job, []byte(`{"template":"dnp_with_no_heart"}`)); got != 0 {
		t.Errorf("heartsForPreview = %d, want 0 - the stored model has no heart margin", got)
	}
}

func TestPreviewHeartIsTheShopsOwnPiece(t *testing.T) {
	heart, err := personalise.PreviewHeart()
	if err != nil {
		t.Fatalf("the embedded heart did not load: %v", err)
	}
	// 18 x 18 x 38, as it came out of REDHEART.3mf. A heart that changes size
	// changes where it sits, so its dimensions are part of the contract.
	for _, d := range []struct {
		axis       string
		lo, hi, mm float64
	}{
		{"x", heart.Min.X, heart.Max.X, 18},
		{"y", heart.Min.Y, heart.Max.Y, 18},
		{"z", heart.Min.Z, heart.Max.Z, 38},
	} {
		if math.Abs((d.hi-d.lo)-d.mm) > 0.01 {
			t.Errorf("heart %s spans %.3f, want %.0fmm", d.axis, d.hi-d.lo, d.mm)
		}
	}
}

// --- helpers -------------------------------------------------------------------

// plankModel is a two-part plank: the full-width plate, and lettering reaching
// as far along it as the caller says.
func plankModel(t *testing.T, letterMinX, letterMaxX float64) []byte {
	t.Helper()
	model, err := meshio.Write3MF([]meshio.Part{
		{
			Name: "base", Colour: "#FFFFFF", Material: "PLA",
			Triangles: boxMesh(plateMinX, plateMaxX, plateMinY, plateMaxY, plateMinZ, plateMaxZ),
		},
		{
			Name: "BLACK text", Colour: "#1A1A1A", Material: "PLA",
			Triangles: boxMesh(letterMinX, letterMaxX, -22, 22, letterMinZ, letterTopZ),
		},
	})
	if err != nil {
		t.Fatalf("build the test plank: %v", err)
	}
	return model
}

// jobWithProperties is a job carrying the line the customer filled in, which is
// the snapshot the renderer itself reads to choose a template.
func jobWithProperties(t *testing.T, hearts string) gen.ProductionJob {
	t.Helper()
	props, err := json.Marshal([]production.LineProp{
		{Name: "STEP 3-", Value: hearts},
		{Name: "STEP 4-First Name-", Value: "MOHIT"},
		{Name: "STEP 5-Second Name", Value: "SHAILJA"},
	})
	if err != nil {
		t.Fatalf("build the test properties: %v", err)
	}
	return gen.ProductionJob{PersonalisationProperties: props}
}

func readParts(t *testing.T, model []byte) []meshio.Part {
	t.Helper()
	parts, err := meshio.Read3MF(model)
	if err != nil {
		t.Fatalf("read the composed preview: %v", err)
	}
	return parts
}

func partBox(p meshio.Part) box {
	b, _ := plankBox([]meshio.Part{p})
	return b
}

// boxMesh is an axis-aligned box as twelve triangles.
func boxMesh(x0, x1, y0, y1, z0, z1 float64) []orientation.Triangle {
	v := func(x, y, z float64) orientation.Vec3 { return orientation.Vec3{X: x, Y: y, Z: z} }
	c := [8]orientation.Vec3{
		v(x0, y0, z0), v(x1, y0, z0), v(x1, y1, z0), v(x0, y1, z0),
		v(x0, y0, z1), v(x1, y0, z1), v(x1, y1, z1), v(x0, y1, z1),
	}
	faces := [12][3]int{
		{0, 2, 1}, {0, 3, 2}, // bottom
		{4, 5, 6}, {4, 6, 7}, // top
		{0, 1, 5}, {0, 5, 4}, // front
		{2, 3, 7}, {2, 7, 6}, // back
		{1, 2, 6}, {1, 6, 5}, // right
		{3, 0, 4}, {3, 4, 7}, // left
	}
	out := make([]orientation.Triangle, 0, len(faces))
	for _, f := range faces {
		out = append(out, orientation.Triangle{V0: c[f[0]], V1: c[f[1]], V2: c[f[2]]})
	}
	return out
}
