package httpapi

// A plank with a charm gets no picture.
//
// The shop's decision, and it is about what the picture would be TRUE ABOUT
// rather than about whether one could be drawn. The charm file renders the
// plank and the two pegs it snaps onto; the charm itself is a separate piece
// the file never draws. So a preview would show the customer a plank with two
// small studs on the end and no charm on it - the one part of the product they
// chose by name, missing from the only picture they are shown.
//
// The fields and the colour options still appear, which is the whole of what
// the customer needs to check: the two names, their spelling, and the colour.
//
// Deliberately expressed through has_preview, which the page ALREADY handles
// as "there will never be a picture" - the Bone Tag and the Couple's Date
// Plank reach it the same way. Nothing on the frontend has to learn what a
// charm is.

import (
	"context"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// jobHasCharm reports whether this job's line carried a charm.
//
// Reads the properties STORED ON THE JOB, which is what the render itself
// reads - so the page and the model cannot disagree about whether this is a
// charm plank.
func jobHasCharm(job gen.ProductionJob) bool {
	props, ok := jobLineProperties(job)
	if !ok {
		return false
	}
	return personalise.CharmFromProperties(props) != ""
}

// showsPreviewFor decides whether the customer's page promises a picture.
//
// One function for both call sites on the public page. They used to each call
// rendersProduct directly, and a rule added to one of them would have shown a
// preview on the item view that the list said did not exist.
func (s *Server) showsPreviewFor(ctx context.Context, job gen.ProductionJob) bool {
	if jobHasCharm(job) {
		return false
	}
	return s.rendersProduct(ctx, deref(job.Sku), deref(job.ProductName))
}
