package httpapi

// Changing a colour without rendering anything.
//
// A colour change does not alter one triangle. The plank is the same plank,
// the lettering is the same lettering, and the only difference is which hex a
// piece carries - which a 3MF stores per part. Running OpenSCAD again to
// produce identical geometry costs 20-45 seconds of CPU and makes the customer
// watch a spinner for the one edit they are most likely to make.
//
// So a colour-only change reads the model back, swaps the hex on the pieces
// that follow the customer, and writes it out again. The round trip is the
// same pair of functions that produced the file in the first place
// (meshio.Read3MF / Write3MF), so nothing is lost in translation - this is not
// a second way of building a model, it is the same bytes with one field
// changed.
//
// A TEXT change still renders. The geometry really is different then, and
// there is no shortcut worth inventing.

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
)

// recolourModel rewrites the job's stored model in a new colour.
//
// Returns false when it cannot be done - no model yet, an STL rather than a
// 3MF, a colour that will not resolve, nothing on the model that follows the
// customer - and the caller falls back to a full render. Never an error the
// customer sees: the values are already saved and what prints is already
// right; this only decides how quickly the picture catches up.
func (s *Server) recolourModel(
	ctx context.Context, job gen.ProductionJob, colour string,
) bool {
	if job.PrintFileID == nil || s.storage == nil {
		return false
	}
	// A COLOUR THAT CHANGES THE FILE IS NOT A RECOLOUR.
	//
	// This path rewrites the hex on an EXISTING model and keeps its geometry,
	// which is right when a colourway is a palette over one shared drawing.
	// It is wrong when the colourways are separate files - a Royal holder's
	// purple and red are different .scad files, each already in its own
	// colours - because the fast path would hand back the red holder painted
	// purple rather than the purple one.
	//
	// Today's self-colouring products fall out of this function anyway, a few
	// lines below, for having no piece that follows the customer. That is
	// luck rather than a rule: a product with a file per SKU AND a piece that
	// follows the customer would take the fast path and keep the wrong
	// drawing. So the question is asked directly.
	if s.recolouringWouldChangeTheFile(ctx, job, colour) {
		return false
	}
	hex, err := s.resolveColourHex(ctx, colour)
	if err != nil || hex == "" {
		return false
	}

	// Which pieces take the customer's colour, and which are fixed by the
	// design. A product whose every piece is fixed has nothing to recolour,
	// and saying so is how it falls through to a render rather than writing an
	// identical file.
	// THE RENDERED NAME IS NOT THE PIECE'S NAME. A piece that follows the
	// customer is written into the model as "<COLOUR> <piece>" - "BLACK text"
	// - because that is what an operator reads in the slicer's object list.
	// Matching on the piece name alone finds nothing, which is a fast path
	// that silently never fires.
	follows := make([]string, 0, 2)
	for _, part := range s.colourPartsForJob(ctx, job) {
		if part.FollowsCustomer() {
			follows = append(follows, strings.TrimSpace(part.Name))
		}
	}
	if len(follows) == 0 {
		return false
	}

	row, err := s.store.Q.GetFileAsset(ctx, *job.PrintFileID)
	if err != nil {
		return false
	}
	obj, err := s.storage.Get(ctx, row.StorageKey)
	if err != nil {
		return false
	}
	defer func() { _ = obj.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(obj.Body, maxRecolourBytes))
	if err != nil {
		return false
	}

	parts, err := meshio.Read3MF(data)
	if err != nil || len(parts) == 0 {
		// A single-colour fallback is still an STL, which carries no per-part
		// colour at all. Nothing to swap.
		return false
	}

	changed := false
	for i := range parts {
		piece, ok := followedPiece(parts[i].Name, follows)
		if !ok {
			continue
		}
		if strings.EqualFold(parts[i].Colour, hex) {
			continue
		}
		parts[i].Colour = hex
		// Renamed as well, exactly as a render would have named it. Leaving
		// "BLACK text" on a green piece would put the wrong colour in front of
		// whoever is loading the plate.
		parts[i].Name = strings.ToUpper(strings.TrimSpace(colour)) + " " + piece
		changed = true
	}
	if !changed {
		// Already that colour. Writing an identical file would mint a new
		// model id and make the page wait for a change it would not see.
		return false
	}

	model, err := meshio.Write3MF(parts)
	if err != nil {
		return false
	}
	// Stored as a NEW file, like a render, so the job's model id changes and
	// the customer's page knows the picture it is holding is stale.
	//
	// CARRYING THE OLD FILE'S RECORD FORWARD, not storing an empty one. The
	// record beside a model is what answers "is this still what the order
	// says?" (modelAgreesWithOrder) and, now, how many hearts the preview
	// draws. A recolour changes neither: it is the same plank, the same names
	// and the same template, with one hex swapped. Written blank, every
	// colour-only edit made the model read as built-from-nothing - which meant
	// a re-render on the next bed rebuild, undoing the whole point of this fast
	// path, and a preview that silently lost its heart.
	fileID, err := s.storeGeneratedModel(ctx, job, recolouredPlan(job, row), model)
	if err != nil {
		return false
	}
	if _, err := s.store.Q.SetProductionJobPrintFile(ctx,
		gen.SetProductionJobPrintFileParams{ID: job.ID, PrintFileID: &fileID}); err != nil {
		return false
	}
	return true
}

// maxRecolourBytes bounds what is pulled into memory to swap a hex.
//
// A plank is a few megabytes; anything far past that is not a plank, and this
// runs while a customer waits.
const maxRecolourBytes = 64 << 20

// followedPiece matches a model part against the pieces that follow the
// customer, returning the piece's own name.
//
// Tolerates the colour prefix a render adds, and matches the bare name too, so
// a model written before that convention still recolours.
func followedPiece(partName string, follows []string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(partName))
	for _, piece := range follows {
		want := strings.ToLower(piece)
		if name == want || strings.HasSuffix(name, " "+want) {
			return piece, true
		}
	}
	return "", false
}

// recolouredPlan is the record the model already carried, so a recolour leaves
// the same answer behind that the render did.
//
// The label is taken back out of the old filename rather than rebuilt from the
// names: the filename is jobNumber + "-" + label + ext exactly as
// storeGeneratedModel wrote it, and that holds for a registry product whose
// label is built another way entirely.
// recolouringWouldChangeTheFile reports whether this colour moves the job to
// another colourway's drawing.
//
// Asked by comparing the file the job resolves to NOW against the file it
// would resolve to with the new colour, which is exactly the question - and
// which is answered by the same skuForColourway the renderer and the picture
// use, so the three cannot disagree about what a colour means.
//
// False on anything it cannot answer: an unregistered product, a lookup
// failure, a product with no file for this role. All of those are states in
// which the fast path behaves as it always has.
func (s *Server) recolouringWouldChangeTheFile(
	ctx context.Context, job gen.ProductionJob, colour string,
) bool {
	sku := strings.TrimSpace(deref(job.Sku))
	if sku == "" {
		return false
	}
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		return false
	}
	role := partRoleOf(job)

	now, haveNow := s.templateForVariant(ctx, product.ID,
		s.skuForColourway(ctx, product.ID, sku, job), role)

	after := job
	chosen := colour
	after.PersonalisationColour = &chosen
	then, haveThen := s.templateForVariant(ctx, product.ID,
		s.skuForColourway(ctx, product.ID, sku, after), role)

	return haveNow && haveThen && !strings.EqualFold(now, then)
}

func recolouredPlan(job gen.ProductionJob, row gen.FileAsset) renderPlan {
	plan := renderPlan{
		Label: strings.TrimSuffix(
			strings.TrimPrefix(row.Filename, job.JobNumber+"-"),
			filepath.Ext(row.Filename)),
	}
	if len(row.RenderParams) == 0 {
		// Rendered before Tensor recorded this. There is nothing to carry, and
		// inventing a record would claim knowledge the old file never had.
		return plan
	}
	var stored storedRenderParams
	if err := json.Unmarshal(row.RenderParams, &stored); err != nil {
		return plan
	}
	plan.Template, plan.Stored = stored.Template, stored
	return plan
}
