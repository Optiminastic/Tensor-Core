package httpapi

// A job that was judged before its product was set up.
//
// Job creation decides once, at import, whether Tensor makes this product and
// which part each job prints. An order that arrives while the registry is
// still half-configured is stamped no_approved_design and given the default
// part, body - and nothing ever goes back to look.
//
// Two things then went wrong, both measured on the Desk Name Plank:
//
//   - The queue said CREATING. modelStatusOf re-asks "does Tensor make this?"
//     every time the page loads, the finished setup now says yes, and a
//     generated job with no file and no error read as a render in flight. No
//     render had ever been queued. It would have said Creating for ever.
//
//   - REBUILD DID NOT FIX IT. It re-queued the job as it stood, still on part
//     body, and the render looked for a design and a mapping under body, found
//     neither, and fell through to the Dual Name Plank template - "a plank
//     needs both names" on a desk plate.
//
// So the badge now says what is true and names the action, and the action
// does what the badge says.

import (
	"context"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// setUpAfterOrderWording is what the queue shows for such a job.
//
// Reported as the failed state's own reason rather than a new status: the
// model status is a fixed enum the dashboard validates, and a value it does
// not know breaks the whole jobs list until both sides are deployed together.
// A failed render with a sentence is a state it already draws, with the
// Rebuild control beside it.
const setUpAfterOrderWording = "This order came in before its product was set up in the registry, " +
	"so no model was ever queued. Rebuild the model to make it now."

// judgedBeforeSetup reports whether a generated job was stamped as not ours
// when it was created.
//
// no_approved_design is written by job creation when nothing in the design
// tables matched the SKU. On a product Tensor now renders, that stamp can only
// mean the registry was finished afterwards: a job created against the
// finished setup carries stl_missing until its model lands, not this.
func judgedBeforeSetup(j gen.ProductionJob) bool {
	return j.IssueReason != nil && strings.TrimSpace(*j.IssueReason) == "no_approved_design"
}

// adoptCurrentPart moves a single-part job onto the part its product prints
// today, before it is rebuilt.
//
// ONLY THE ONE-PART CASE. A combo judged before setup became one job where it
// should be three, and no change to that one job can make the other two - so
// a multi-part product is left exactly as it is and the job keeps whatever it
// had. Rebuild repairs what a rebuild can repair.
//
// Best effort: a failure here leaves the job on its old part, which is the
// behaviour it had before this existed, so it is logged by the caller's render
// rather than refused.
func (s *Server) adoptCurrentPart(ctx context.Context, job gen.ProductionJob) {
	roles := singleJobRoles(s.partRolesForSKU(ctx, deref(job.Sku)))
	if len(roles) != 1 {
		return
	}
	want := roles[0] // "" is the default part, body
	have := strings.TrimSpace(job.PartRole)
	if strings.EqualFold(roleOrBody(want), roleOrBody(have)) {
		return
	}
	part := roleOrBody(want)
	_ = s.store.Q.SetProductionJobPartRole(ctx, gen.SetProductionJobPartRoleParams{
		ID: job.ID, PartRole: &part,
	})
}
