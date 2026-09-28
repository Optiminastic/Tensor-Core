package httpapi

// Checking a bed's models against the orders they came from, and rebuilding the
// ones that disagree.
//
// A model is built once, from the order line the job was made from, and then
// nothing looks at it again. That was fine until the line a job read could be
// the wrong one: plankParamsForJob matched on SKU, every line of a five-plank
// order carries the same SKU, and four planks were built from the first line -
// NAVYA & KRISHNA four times, against three other customers' orders. Nothing in
// the database disagreed with itself, because nothing recorded what each model
// had actually been built from.
//
// file_assets.render_params records that now (migration 0073), so this is an
// exact comparison rather than a guess at a filename: the template, the heart
// count and both names, against what the order says today.
//
// Only the mismatches are re-rendered. A bed of four where one is wrong costs
// one render, not four, and the response names which ones and why - because
// "the other three are fine" is the part an operator has to be able to believe.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// batchRebuildResponse reports what the check found and what it did about it.
type batchRebuildResponse struct {
	BatchNumber string `json:"batch_number"`
	Checked     int    `json:"checked"`
	// Queued is the jobs whose model disagreed with their order and which are
	// being rebuilt now.
	Queued []rebuiltJob `json:"queued"`
	// Correct is the jobs whose model already matches, named rather than
	// counted: an operator deciding whether to trust a bed wants to see that
	// the others were checked, not merely that one was wrong.
	Correct []string `json:"correct"`
	// Skipped is the jobs this cannot speak for - an uploaded design has no
	// template to compare against, and an order that cannot be read as a render
	// would fail the same way again.
	Skipped []rebuiltJob `json:"skipped"`
	Note    string       `json:"note"`
}

// rebuiltJob is one job and why it was queued or skipped, in words an operator
// can act on.
type rebuiltJob struct {
	JobNumber string `json:"job_number"`
	Reason    string `json:"reason"`
}

// rebuildBatchModels re-renders the jobs on a bed whose models disagree with
// their orders.
//
// The plate is NOT rebuilt here. A render is 20-45 seconds on the worker, so
// merging now would bake in the very models this is replacing; the plate is
// rebuilt when the bed's last render lands - see replateWhenRendersSettle.
func (s *Server) rebuildBatchModels(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	if len(jobs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "This batch holds no jobs.")
		return
	}
	if s.modelEnqueuer == nil {
		detail(c, http.StatusConflict, "Model generation is not configured on this service.")
		return
	}

	// Empty slices, not nil: Go marshals a nil slice as null, and a caller
	// parsing "the jobs it rebuilt" against an array type chokes on null - which
	// is exactly how a request that worked perfectly read as a failure on the
	// Batches page.
	out := batchRebuildResponse{
		BatchNumber: batch.BatchNumber, Checked: len(jobs),
		Queued: []rebuiltJob{}, Correct: []string{}, Skipped: []rebuiltJob{},
	}
	for _, job := range jobs {
		state, reason := s.modelAgreesWithOrder(ctx, job)
		switch state {
		case modelCorrect:
			out.Correct = append(out.Correct, job.JobNumber)
		case modelUncheckable:
			out.Skipped = append(out.Skipped, rebuiltJob{JobNumber: job.JobNumber, Reason: reason})
		case modelWrong:
			if err := s.modelEnqueuer.Enqueue(ctx, job.ID); err != nil {
				out.Skipped = append(out.Skipped, rebuiltJob{
					JobNumber: job.JobNumber, Reason: "could not queue the render",
				})
				continue
			}
			out.Queued = append(out.Queued, rebuiltJob{JobNumber: job.JobNumber, Reason: reason})
		}
	}

	if len(out.Queued) == 0 {
		out.Note = "Every model on this bed already matches its order."
	} else {
		out.Note = fmt.Sprintf(
			"Rebuilding %d of %d. The plate is rebuilt once the renders finish.",
			len(out.Queued), out.Checked)
	}
	obs.FromContext(ctx).Info("batch models checked against their orders",
		"batch", batch.BatchNumber, "checked", out.Checked,
		"queued", len(out.Queued), "skipped", len(out.Skipped))
	c.JSON(http.StatusAccepted, out)
}

// modelState is what the check could establish about one job's model.
type modelState int

const (
	// modelCorrect: the stored render inputs match what the order says today.
	modelCorrect modelState = iota
	// modelWrong: they differ, or the model predates render_params and cannot
	// be vouched for. Unknown counts as wrong deliberately - a model nobody can
	// check is not a model anybody should trust.
	modelWrong
	// modelUncheckable: nothing to compare, and re-rendering would not help.
	modelUncheckable
)

// modelAgreesWithOrder compares one job's stored render inputs against the
// order line it was made from.
func (s *Server) modelAgreesWithOrder(ctx context.Context, job gen.ProductionJob) (modelState, string) {
	if !s.rendersProduct(ctx, deref(job.Sku), deref(job.ProductName)) {
		return modelUncheckable, "this product's model is uploaded, not rendered"
	}
	if job.OrderID == nil {
		return modelUncheckable, "this job has no order to check against"
	}
	if job.PrintFileID == nil {
		return modelWrong, "no model yet"
	}

	// Through the same resolver a render uses, so a registry product is
	// compared against the registry's answer rather than against what the
	// plank path would have made of it.
	plan, err := s.renderPlanForJob(ctx, *job.OrderID, job)
	if err != nil {
		// The order cannot be read as a render at all - a missing name, a
		// colour with no swatch. Re-rendering would fail the same way, and the
		// job already carries that reason.
		return modelUncheckable, err.Error()
	}

	file, err := s.store.Q.GetFileAsset(ctx, *job.PrintFileID)
	if err != nil {
		return modelWrong, "the model file is missing"
	}
	if len(file.RenderParams) == 0 {
		// Rendered before 0073, so nothing recorded what it was built from.
		return modelWrong, "built before Tensor recorded what models are made from"
	}

	var have storedRenderParams
	if err := json.Unmarshal(file.RenderParams, &have); err != nil {
		return modelWrong, "what this model was built from cannot be read"
	}

	// plan.Stored is exactly what a render of this order would write beside
	// its file, so the comparison is against the record that render WOULD
	// leave rather than a second construction of it that could drift.
	return compareRenderParams(have, plan.Stored)
}

// compareRenderParams is the comparison itself, with no database behind it.
//
// Split out from modelAgreesWithOrder so the rule can be tested: the two reads
// above it are I/O, and this is the part that decides whether a customer gets
// the plank they ordered.
//
// Names first, then hearts, then the template - most legible difference first,
// since the reason is read by somebody deciding whether to trust a bed. Every
// field is compared; matching on names alone is what made a wrong heart count
// invisible on JOB-115059-2.
func compareRenderParams(have, want storedRenderParams) (modelState, string) {
	// A registry product compares on its mapped arguments, because it has no
	// NameLeft or heart count to compare - it may have neither concept. Both
	// sides are checked: a product that gained a mapping since its model was
	// built has a "have" with no Args and a "want" with some, which is a real
	// disagreement and not a reason to fall through to the plank fields and
	// find them all equal and empty.
	if len(have.Args) > 0 || len(want.Args) > 0 {
		if why, differs := mappedArgsDiffer(have, want); differs {
			return modelWrong, why
		}
		if have.Template != want.Template {
			return modelWrong, fmt.Sprintf("built from %s, ordered as %s",
				have.Template, want.Template)
		}
		// Which design file this model is. A combo's three models share a
		// product and an order, so without this the rose's model and the
		// keychain's differ only by their arguments - and two parts whose
		// mapping happens to name the same variable would compare equal.
		//
		// Both sides normalised first. Every model rendered before parts
		// existed has no role recorded, and a product with one design file
		// renders as 'body': those are the same file, and comparing the raw
		// strings would call every model on every bed wrong the first time
		// this ran.
		if haveRole, wantRole := roleOrBody(have.Role), roleOrBody(want.Role); !strings.EqualFold(haveRole, wantRole) {
			return modelWrong, fmt.Sprintf("built as the %s, ordered as the %s", haveRole, wantRole)
		}
		return modelCorrect, ""
	}
	switch {
	case have.NameLeft != want.NameLeft || have.NameRight != want.NameRight:
		return modelWrong, fmt.Sprintf("built as %s & %s, ordered as %s & %s",
			have.NameLeft, have.NameRight, want.NameLeft, want.NameRight)
	case have.Hearts != want.Hearts:
		return modelWrong, fmt.Sprintf("built with %d hearts, ordered with %d",
			have.Hearts, want.Hearts)
	case have.Template != want.Template:
		return modelWrong, fmt.Sprintf("built from %s, ordered as %s",
			have.Template, want.Template)
	}
	return modelCorrect, ""
}

// roleOrBody names a design file for a message, including the models rendered
// before parts existed, whose record carries no role at all.
func roleOrBody(role string) string {
	if strings.TrimSpace(role) == "" {
		return designRoleBody
	}
	return role
}

// mappedArgsDiffer compares two registry renders' arguments.
//
// Names the first field that disagrees rather than reporting "different":
// the message reaches somebody deciding whether to rebuild a bed, and
// "built with NAME_L=\"AMENA\", ordered as \"AMINA\"" is a decision they can
// make. "The arguments differ" is not.
func mappedArgsDiffer(have, want storedRenderParams) (string, bool) {
	if len(have.Args) != len(want.Args) {
		return fmt.Sprintf("built from %d mapped fields, ordered with %d",
			len(have.Args), len(want.Args)), true
	}
	// Sorted, so the same disagreement reports the same way twice rather than
	// naming whichever field map iteration reached first.
	names := make([]string, 0, len(want.Args))
	for name := range want.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if have.Args[name] != want.Args[name] {
			return fmt.Sprintf("built with %s=%s, ordered as %s",
				name, have.Args[name], want.Args[name]), true
		}
	}
	return "", false
}

// replateBatchAfterRender is replateWhenRendersSettle for a caller holding only
// a job id - the render worker, which is handed one by River.
//
// A job that cannot be read is not an error worth reporting here: the render
// itself succeeded, and this is the tidying that follows it.
func (s *Server) replateBatchAfterRender(ctx context.Context, jobID uuid.UUID) {
	job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		obs.FromContext(ctx).Debug("could not read a job after its render",
			"job", jobID, "error", err)
		return
	}
	s.replateWhenRendersSettle(ctx, job)
}

// replateWhenRendersSettle rebuilds a bed's plate once every job on it has a
// model again.
//
// Called after a render lands. The plate is a snapshot of the models at merge
// time, so rebuilding it after the FIRST render of a four-plank rebuild would
// bake in three models that are about to change. Waiting until none of the
// bed's jobs is still rendering is what makes "rebuild the batch" one act
// rather than four.
//
// Best-effort: a bed whose plate cannot be rebuilt keeps the one it has, which
// is exactly the position it was in before this existed.
func (s *Server) replateWhenRendersSettle(ctx context.Context, job gen.ProductionJob) {
	if job.BatchID == nil {
		return
	}
	batchID := *job.BatchID
	log := obs.FromContext(ctx)

	siblings, err := s.store.Q.ListJobsForBatch(ctx, &batchID)
	if err != nil {
		log.Debug("could not read a bed's jobs after a render", "error", err)
		return
	}
	for _, sibling := range siblings {
		if modelStatusOf(sibling) == ModelGenerating {
			// Another render on this bed is still running; that one re-plates.
			return
		}
	}
	if err := s.RebuildBatchPlate(ctx, batchID); err != nil {
		log.Warn("could not rebuild a bed's plate after its renders finished",
			"batch", batchID, "error", err)
		return
	}
	log.Info("bed re-plated after its models were rebuilt", "batch", batchID)
}
