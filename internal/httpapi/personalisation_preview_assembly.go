package httpapi

// Showing the customer the whole product, not just the part Tensor renders.
//
// A Royal diya holder is an arch plate with a name on it, a foot, and a cup.
// Tensor renders the first two; the foot and the cup are printed from their
// own files and attached at the bench, so the preview showed a diya holder
// with no holder - and asked somebody to approve it.
//
// COMPOSED ON THE WAY OUT, so the file on disk stays the file the floor
// slices. The same decision heartedPreview already makes for the plank's
// hearts, and for the same reason: the picture and the print answer different
// questions, and a model edited to look right is a model that prints wrong.

import (
	"context"
	"io"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// assembledPreview is the job's model with its product's printed components
// merged in, or the model unchanged when there are none.
//
// BEST EFFORT, ALWAYS. Every failure here returns the model it was given: a
// component whose file has gone, a storage read that times out, a mesh that
// will not parse. The customer is waiting on a picture of the thing they
// bought, and showing them the half Tensor can draw beats showing them an
// error about a cup.
func (s *Server) assembledPreview(
	ctx context.Context, job gen.ProductionJob, model []byte,
) []byte {
	sku := strings.TrimSpace(deref(job.Sku))
	if sku == "" {
		return model
	}
	rows, err := s.store.Q.ListPreviewComponentsForSKU(ctx, sku)
	if err != nil || len(rows) == 0 {
		return model
	}

	parts, err := meshio.Read3MF(model)
	if err != nil || len(parts) == 0 {
		// Not readable as parts, so there is nothing to add to.
		return model
	}
	out := make([]meshio.Part, 0, len(parts)+len(rows))
	for _, p := range parts {
		out = append(out, meshio.Part{
			Name: p.Name, Colour: p.Colour, Material: p.Material, Triangles: p.Triangles,
		})
	}

	// The colour the customer will receive, which is what a piece with no
	// fixed colour of its own takes - the foot is the colour of the holder,
	// while the cup is gold on every colourway.
	chosen := previewChosenColour(job)
	chosenHex := ""
	if chosen != "" {
		if hex, err := s.resolveColourHex(ctx, chosen); err == nil {
			chosenHex = hex
		}
	}

	added := 0
	for _, r := range rows {
		tris, ok := s.componentTriangles(ctx, r)
		if !ok {
			continue
		}
		colour := deref(r.PreviewColour)
		if colour == "" {
			colour = chosenHex
		}
		if colour == "" {
			// No fixed colour and no resolvable customer colour. Drawn in the
			// plank's white rather than skipped: a missing foot reads as a
			// product with no foot, which is worse than one whose foot is the
			// wrong shade in a picture.
			colour = BasePlateColour
		}
		out = append(out, meshio.Part{
			Name: r.Name, Colour: colour, Material: deref(job.Material), Triangles: tris,
		})
		added++
	}
	if added == 0 {
		return model
	}

	merged, err := meshio.Write3MF(out)
	if err != nil {
		obs.FromContext(ctx).Warn("preview: could not assemble the components, showing the model alone",
			"job", job.JobNumber, "error", err)
		return model
	}
	return merged
}

// previewChosenColour is the colourway this job will be made in.
//
// The customer's own choice first, then the variant they bought - the same
// order renderColouredPlank reads, so the picture and the plate agree about
// what colour the thing is.
func previewChosenColour(job gen.ProductionJob) string {
	if c := strings.TrimSpace(deref(job.PersonalisationColour)); c != "" {
		return c
	}
	if c := colourFromVariant(job.VariantTitle); c != "" {
		return c
	}
	return strings.TrimSpace(deref(job.Colour))
}

// componentTriangles reads one component's mesh out of object storage.
func (s *Server) componentTriangles(
	ctx context.Context, r gen.ListPreviewComponentsForSKURow,
) ([]orientation.Triangle, bool) {
	if s.storage == nil {
		return nil, false
	}
	obj, err := s.storage.Get(ctx, r.StorageKey)
	if err != nil {
		if !storage.IsNotFound(err) {
			obs.FromContext(ctx).Warn("preview: could not read a component's model",
				"component", r.Name, "error", err)
		}
		return nil, false
	}
	defer func() { _ = obj.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(obj.Body, maxRecolourBytes))
	if err != nil {
		return nil, false
	}

	// A .3mf may carry several objects - the whole assembly exported at once -
	// and every one of them is part of this component.
	if strings.HasSuffix(strings.ToLower(r.Filename), ".3mf") {
		parts, err := meshio.Read3MF(data)
		if err != nil {
			return nil, false
		}
		tris := make([]orientation.Triangle, 0, 1024)
		for _, p := range parts {
			tris = append(tris, p.Triangles...)
		}
		return tris, len(tris) > 0
	}

	mesh, err := meshFromSTL(data)
	if err != nil || len(mesh.Triangles) == 0 {
		obs.FromContext(ctx).Warn("preview: a component's model could not be read",
			"component", r.Name, "file", r.Filename)
		return nil, false
	}
	return mesh.Triangles, true
}
