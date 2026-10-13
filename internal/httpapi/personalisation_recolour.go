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
	"io"
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
	fileID, err := s.storeGeneratedModel(ctx, job, renderPlan{}, model)
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
