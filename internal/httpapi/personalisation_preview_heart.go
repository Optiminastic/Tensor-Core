package httpapi

// Drawing the heart into the preview, and only into the preview.
//
// A one-heart plank reserves a wide empty margin at its right end and a
// two-heart plank reserves one at each end. A heart is fitted into that space
// after the plank is printed - it is a separate piece, it is not printed with
// the plank, and no template draws one. The file the floor slices must stay
// exactly as it is.
//
// The customer, though, is being shown that file and asked "is this right?".
// Without a heart they are looking at a plank with a conspicuous blank end and
// are being asked to approve a product they did not order.
//
// So the heart is COMPOSED IN WHEN A PREVIEW IS SERVED and never stored. There
// is exactly one model on disk, it has no heart, and that is the one the
// printer gets - the heart cannot reach a plate because it is never written
// anywhere a plate is built from. A stored second file would be a second thing
// to keep in step and a second thing to pick up by mistake.
//
// Placed from the model's own geometry rather than from numbers copied out of
// the templates. The margin a template reserves is in pre-scale units and the
// whole model is then scaled non-uniformly to the product's 200 x 50 x 40 - on
// one measured plank x by 0.530, on another by 0.354, because the scale follows
// the length of the names. Reading the gap off the triangles cannot drift from
// the model it is drawn on; a hardcoded 39.8mm would have been wrong on the
// second plank.

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// heartedPreview returns the model with the plank's hearts drawn in.
//
// Returns the bytes it was given whenever it cannot do that - no hearts on this
// product, an STL rather than a 3MF, geometry it cannot make sense of. Never an
// error: a preview without a heart is still a true picture of what will be
// printed, and it must not take the page down. The caller logs; the customer
// sees their plank.
func heartedPreview(model []byte, hearts int) []byte {
	if hearts <= 0 {
		return model
	}
	parts, err := meshio.Read3MF(model)
	if err != nil || len(parts) == 0 {
		// A single-colour fallback is still an STL, which has no parts to add
		// one to. That job's colour pipeline has already failed and its
		// preview is plain for that reason, not this one.
		return model
	}
	heart, err := personalise.PreviewHeart()
	if err != nil {
		return model
	}

	plank, ok := plankBox(parts)
	if !ok {
		return model
	}
	letters, ok := letteringSpan(parts, plank)
	if !ok {
		return model
	}

	// The right end first, because a one-heart plank wears its heart there -
	// MARGIN_R is the enlarged one in dual_one_heart.scad, MARGIN_L stays at
	// the plain variant's 20.
	gaps := []gap{{from: letters.max, to: plank.MaxX}}
	if hearts >= 2 {
		gaps = append(gaps, gap{from: plank.MinX, to: letters.min})
	}

	material := parts[0].Material
	colour := previewHeartColour()
	plain := len(parts)
	for _, g := range gaps {
		tris, ok := placeHeart(heart, g, plank)
		if !ok {
			continue
		}
		parts = append(parts, meshio.Part{
			// Says what it is and that it is not for printing. These bytes are
			// never stored and never sliced, but a customer can save the
			// picture and somebody may yet open the file.
			Name:      "RED heart (preview only)",
			Colour:    colour,
			Material:  material,
			Triangles: tris,
		})
	}
	if len(parts) == plain {
		// Every gap was refused, so there is nothing to rewrite.
		return model
	}

	out, err := meshio.Write3MF(parts)
	if err != nil {
		return model
	}
	return out
}

// previewHeartColour is the heart's colour, which is always red.
//
// Taken from the shop's own table so the heart matches the red on the colour
// picker rather than being a second opinion about what red is. Deliberately
// NOT resolveColourHex: that prefers the hex a spool reported from the AMS, and
// the heart is not filament - it is a piece fitted by hand, so what some tray
// holds has nothing to say about it.
func previewHeartColour() string {
	if hex := colourHexFor("red"); hex != "" {
		return hex
	}
	// Only if red is ever taken out of the table. A heart drawn in the wrong
	// red is better than a plank with a hole at the end.
	return "#E4002B"
}

// gap is the empty space along the plank that a heart goes into.
type gap struct{ from, to float64 }

func (g gap) width() float64 { return g.to - g.from }

func (g gap) centre() float64 { return (g.from + g.to) / 2 }

// box is a model's extent, in the model's own coordinates.
type box struct {
	MinX, MaxX float64
	MinY, MaxY float64
	MinZ, MaxZ float64
}

// plankBox measures the whole model.
func plankBox(parts []meshio.Part) (box, bool) {
	b := box{
		MinX: math.MaxFloat64, MaxX: -math.MaxFloat64,
		MinY: math.MaxFloat64, MaxY: -math.MaxFloat64,
		MinZ: math.MaxFloat64, MaxZ: -math.MaxFloat64,
	}
	seen := false
	for _, p := range parts {
		for _, t := range p.Triangles {
			for _, v := range [3]orientation.Vec3{t.V0, t.V1, t.V2} {
				seen = true
				b.MinX = min(b.MinX, v.X)
				b.MaxX = max(b.MaxX, v.X)
				b.MinY = min(b.MinY, v.Y)
				b.MaxY = max(b.MaxY, v.Y)
				b.MinZ = min(b.MinZ, v.Z)
				b.MaxZ = max(b.MaxZ, v.Z)
			}
		}
	}
	return b, seen
}

// span is how far the lettering reaches along the plank.
type span struct{ min, max float64 }

// letteringSpan finds where the letters end and the empty margin begins.
//
// THE PLATE IS IDENTIFIED BY ITS THINNESS, and then ignored. It runs the full
// 200mm and would otherwise answer "the letters reach the very end" on every
// plank, leaving no margin to put anything in. Its name cannot be used: a piece
// that follows the customer is written as "BLACK text", which is the trap
// recolourModel already fell into, and the plate's own name varies. Height
// separates them with room to spare - on a measured plank the plate spans z
// -1.24..1.55 against lettering reaching 38.76, so under a fifth of the
// model's height is the plate and nothing else comes close.
//
// Measured over the WHOLE of the lettering, not just its upper part. Cutting by
// vertex height instead looked equivalent and was not: a glyph is widest near
// the plate, so a cut at 15% of the height put the last letter 4.5mm short of
// where it really ends and the heart 2.3mm off centre.
func letteringSpan(parts []meshio.Part, b box) (span, bool) {
	// A part taller than this share of the model is lettering; anything
	// flatter is the plate.
	const plateShare = 0.2

	height := b.MaxZ - b.MinZ
	s := span{min: math.MaxFloat64, max: -math.MaxFloat64}
	seen := false
	for _, p := range parts {
		pb, ok := plankBox([]meshio.Part{p})
		if !ok {
			continue
		}
		if height > 0 && (pb.MaxZ-pb.MinZ) <= plateShare*height {
			continue
		}
		seen = true
		s.min = min(s.min, pb.MinX)
		s.max = max(s.max, pb.MaxX)
	}

	// A model whose plate and lettering are one part cannot be separated that
	// way: the single part is both full-length and full-height, so the span
	// above covers the whole plank and leaves no margin. Cut by vertex height
	// instead, which does divide one part - less accurate, as the note above
	// says, but the alternative is drawing no heart at all.
	if !seen || s.max-s.min >= 0.98*(b.MaxX-b.MinX) {
		return letteringAboveThePlate(parts, b)
	}
	return s, true
}

// letteringAboveThePlate is the fallback: the X reach of everything standing
// clear of the plate, whichever part it belongs to.
func letteringAboveThePlate(parts []meshio.Part, b box) (span, bool) {
	cut := b.MinZ + 0.15*(b.MaxZ-b.MinZ)
	s := span{min: math.MaxFloat64, max: -math.MaxFloat64}
	seen := false
	for _, p := range parts {
		for _, t := range p.Triangles {
			for _, v := range [3]orientation.Vec3{t.V0, t.V1, t.V2} {
				if v.Z <= cut {
					continue
				}
				seen = true
				s.min = min(s.min, v.X)
				s.max = max(s.max, v.X)
			}
		}
	}
	return s, seen
}

// placeHeart moves the heart into a gap, returning its triangles there.
//
// Centred across the plank's width - the 50mm depth - and sat so its top is
// level with the top of the lettering, which is what makes it read as part of
// the same object rather than something resting beside it.
//
// Scaled DOWN only, and only when it would not otherwise fit. The hearts
// measured on real planks have 4.9mm of clearance either side at the tightest
// (two hearts, a ten-letter name), so this is insurance against a name longer
// than any seen rather than the normal path - and shrinking a heart is a better
// answer than one that overlaps the letters or hangs off the end.
func placeHeart(heart orientation.Mesh, g gap, b box) ([]orientation.Triangle, bool) {
	const tooNarrow = 2.0 // mm. Below this there is no heart worth drawing.
	if g.width() <= tooNarrow {
		return nil, false
	}

	width := heart.Max.X - heart.Min.X
	if width <= 0 {
		return nil, false
	}
	scale := 1.0
	if width > g.width() {
		scale = g.width() / width
	}

	// Where the heart's own centre and top are, so the offsets below are
	// measured from the piece rather than from assumptions about it.
	hx := (heart.Min.X + heart.Max.X) / 2
	hy := (heart.Min.Y + heart.Max.Y) / 2
	cx, cy, topZ := g.centre(), (b.MinY+b.MaxY)/2, b.MaxZ

	at := func(v orientation.Vec3) orientation.Vec3 {
		return orientation.Vec3{
			X: cx + (v.X-hx)*scale,
			Y: cy + (v.Y-hy)*scale,
			Z: topZ - (heart.Max.Z-v.Z)*scale,
		}
	}

	out := make([]orientation.Triangle, 0, len(heart.Triangles))
	for _, t := range heart.Triangles {
		// Vertices only. Normal and Area are recomputed by whatever loads this
		// next and are not written into a 3MF at all, so carrying the heart's
		// originals through a scale would be carrying stale numbers.
		out = append(out, orientation.Triangle{V0: at(t.V0), V1: at(t.V1), V2: at(t.V2)})
	}
	return out, true
}

// heartsForPreview is how many hearts the plank in this file wears.
//
// THE FILE'S OWN RECORD FIRST, the order second. What is drawn has to describe
// the model being drawn: if the stored model came off the no-heart template,
// putting a heart beside it would misrepresent the file however many the order
// asked for.
//
// The order is the fallback for when the record says nothing, which happens for
// real. JOB-115361-2 carries {"template": ""} - it was recoloured, and the fast
// path used to store a blank record (fixed in recolourModel, but the rows it
// already wrote are still there), so a genuine one-heart plank would be drawn
// plain. Models from before render_params existed are silent in the same way.
// The job's own property snapshot answers both, with no extra query: it is the
// very thing templateForHearts read to choose the template.
func heartsForPreview(job gen.ProductionJob, renderParams []byte) int {
	if template, ok := recordedTemplate(renderParams); ok {
		return personalise.HeartsForTemplate(template)
	}

	props, ok := jobLineProperties(job)
	if !ok {
		return 0
	}
	params, err := personalise.ParamsFromProperties(props)
	if err != nil {
		// Not a plank, or a plank missing a name. Either way this is not the
		// place to complain about it - the model was built by whatever could
		// read it, and it gets drawn as it is.
		return 0
	}
	// ForProduct for the same reason the renderer calls it: the photo frame
	// prints from the no-heart template whatever a stray heart property says,
	// and a frame with a heart drawn on it would be a product that does not
	// exist.
	params = params.ForProduct(deref(job.Sku), deref(job.ProductName))
	return personalise.HeartsForTemplate(params.Template)
}

// recordedTemplate is the template a stored model says it came from, and
// whether it says anything at all.
func recordedTemplate(renderParams []byte) (string, bool) {
	if len(renderParams) == 0 {
		return "", false
	}
	var stored storedRenderParams
	if err := json.Unmarshal(renderParams, &stored); err != nil {
		return "", false
	}
	template := strings.TrimSpace(stored.Template)
	return template, template != ""
}
