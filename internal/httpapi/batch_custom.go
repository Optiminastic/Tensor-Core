package httpapi

// Building a bed by hand.
//
// The planner decides what shares a plate, and for the ordinary run of Dual
// Name Planks it decides well. It cannot decide everything: a customer rings up
// and wants their two planks together, a bed needs filling with whatever is
// blue to use up a spool before it runs out, a reprint should ride along with
// the order it belongs to. None of that is a rule worth teaching the planner,
// and all of it is obvious to the person looking at the orders.
//
// So this is the same bed the planner would build, assembled by a person: the
// SAME eligibility (ListBatchableJobs), the SAME compatibility key, the SAME
// unit cap, and the same Draft status at the end. What changes is who chose.
// The constraints are not relaxed for a human - a plate that mixes two colours
// prints one of them wrong whoever assembled it.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// batchableJobsResponse is the pool a hand-built bed is chosen from.
type batchableJobsResponse struct {
	Jobs []batchableJob `json:"jobs"`
	// UnitsPerBed is how many products one plate holds, so the dialog can count
	// places rather than making somebody guess when to stop.
	UnitsPerBed int `json:"units_per_bed"`
}

type batchableJob struct {
	productionJobResponse
	// CompatibilityKey is an opaque string: two jobs may share a bed exactly
	// when theirs match. Computed here rather than rebuilt in the browser
	// because it folds in NormalisedColourKey, and a second implementation of
	// that in another language is a rule that will drift - quietly, and into a
	// plate that prints in the wrong colour.
	CompatibilityKey string `json:"compatibility_key"`
	// ColourLabel is what to show beside the swatch: the bed's colour in the
	// order's own words.
	ColourLabel string `json:"colour_label"`
	// Available reports whether this product can be put on a bed right now.
	// False ones are still listed, because the question somebody opens this
	// dialog with is "where are my unfulfilled orders" and an omitted row
	// answers it with silence.
	Available bool `json:"available"`
	// UnavailableReason says why not, in words. Empty when available.
	UnavailableReason string `json:"unavailable_reason"`
	// OnBed names the bed this product already sits on, so "locked" has
	// somewhere to point.
	OnBed string `json:"on_bed"`
}

// listBatchableJobs returns every product from an unfulfilled order that could
// go on a bed.
//
// The planner's own pool: products not yet on a bed, PLUS products sitting in a
// Draft. The second half is the important one. On a floor that plans
// continuously almost everything queued is already on some Draft within
// minutes, so a list of only the unbatched is empty nearly all the time - which
// is exactly what this dialog showed, while the screen behind it was full of
// Draft beds somebody wanted to rearrange.
//
// A Draft is a proposal: no filament is reserved and no plate is promised, so
// its products are free to be moved. Approved and beyond are absent, and stay
// absent - moving a product off a bed whose filament is spoken for and whose
// plate is already sliced is a different and much more expensive act.
//
// Held, flagged and unvalidated products are excluded here as they are there. A
// dialog that let somebody pick a held job would be offering to overrule a hold
// by accident.
func (s *Server) listBatchableJobs(c *gin.Context) {
	ctx := c.Request.Context()
	jobs, err := s.store.Q.ListJobsForCustomBatch(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the products waiting.")
		return
	}
	beds, err := s.bedsHolding(ctx, jobs)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the beds those products are on.")
		return
	}

	dtos := s.productionJobsDTO(ctx, jobs)
	out := batchableJobsResponse{
		Jobs:        make([]batchableJob, 0, len(dtos)),
		UnitsPerBed: s.bedUnitCap(),
	}
	for i, dto := range dtos {
		// The zero row when the product is on no bed, which unavailableBecause
		// reads as "not on one" rather than "on an unreadable one".
		var bed gen.ListBatchIdentityForIDsRow
		if jobs[i].BatchID != nil {
			bed = beds[*jobs[i].BatchID]
		}
		reason := unavailableBecause(jobs[i], bed)
		out.Jobs = append(out.Jobs, batchableJob{
			productionJobResponse: dto,
			CompatibilityKey:      compatibilityKeyString(jobs[i]),
			ColourLabel:           jobColourKey(jobs[i]),
			Available:             reason == "",
			UnavailableReason:     reason,
			OnBed:                 bed.BatchNumber,
		})
	}
	c.JSON(http.StatusOK, out)
}

// bedsHolding reads the beds these products sit on, by id.
//
// One query for the whole list rather than one per product, and separate from
// the job query so that one can return production_jobs rows unchanged.
func (s *Server) bedsHolding(
	ctx context.Context, jobs []gen.ProductionJob,
) (map[uuid.UUID]gen.ListBatchIdentityForIDsRow, error) {
	out := map[uuid.UUID]gen.ListBatchIdentityForIDsRow{}
	ids := dedupeIDs(jobs, func(j gen.ProductionJob) *uuid.UUID { return j.BatchID })
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.Q.ListBatchIdentityForIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// unavailableBecause explains why a product cannot go on a bed right now, or
// returns empty when it can.
//
// The same rules the create path enforces, said in words rather than enforced
// in silence. Ordered most-actionable first: a hold and a flag are somebody's
// to clear, whereas "already on a locked bed" is simply how the floor works.
func unavailableBecause(j gen.ProductionJob, bed gen.ListBatchIdentityForIDsRow) string {
	switch {
	case j.Held:
		return "on hold"
	case j.IssueReason != nil:
		return "needs attention: " + *j.IssueReason
	case j.PersonalisationStatus != production.PersonalisationValidated &&
		j.PersonalisationStatus != production.PersonalisationNotRequired:
		return "personalisation not checked yet"
	case j.BatchID == nil:
		return ""
	case bed.Status == production.BatchPendingApproval:
		// A Draft is a proposal, so its planks can be rearranged - including
		// off another hand-built bed, which is one person moving their own
		// work rather than the planner overruling it.
		return ""
	case bed.Status == "":
		// The bed vanished between the two reads. Treating an unknown bed as
		// movable would be the one guess here that prints something.
		return "on a bed Tensor cannot read"
	default:
		return "already on a locked bed"
	}
}

type createCustomBatchRequest struct {
	JobIDs []string `json:"job_ids"`
}

// createCustomBatch builds a Draft bed from jobs somebody chose.
//
// Validated before anything is written, and refused whole rather than in part:
// a half-built bed is worse than none, because the jobs that did land are now
// off the pool and somebody has to find them again to understand why.
func (s *Server) createCustomBatch(c *gin.Context) {
	var req createCustomBatchRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.JobIDs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "Choose at least one product for this bed.")
		return
	}
	ctx := c.Request.Context()

	jobs, err := s.eligibleJobsFor(ctx, req.JobIDs)
	if err != nil {
		writeStatusError(c, err, "Could not read the chosen jobs.")
		return
	}
	if err := checkOneBedWorth(jobs, s.bedUnitCap()); err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// A bed always ends in a plate, so storage is required - checked before
	// anything is written rather than when the build reaches for it.
	if !s.plateableBatch(c, len(jobs)) {
		return
	}

	number, err := s.store.Q.NextBatchNumber(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not generate a batch number.")
		return
	}
	// Draft, like every auto-created bed. A hand-built bed is still a plan
	// until somebody locks it, and locking is what reserves the filament.
	batch, err := s.store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
		// Built by a person, so the planner leaves it alone. Without this it
		// would be dissolved on the next planning run - a seven-minute timer
		// plus several events - and its planks redistributed, with nothing on
		// screen to say why the bed had gone.
		Manual: true,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the batch.")
		return
	}

	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	// Noted before the move, because afterwards nothing records where these
	// products came from.
	sources := dedupeIDs(jobs, func(j gen.ProductionJob) *uuid.UUID { return j.BatchID })

	if err := s.store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: &batch.ID, JobIds: ids,
	}); err != nil {
		// The empty batch is left behind rather than deleted: it is visible,
		// harmless and deletable, whereas a failed cleanup would hide the fact
		// that anything went wrong at all.
		detail(c, http.StatusInternalServerError, "Could not put the chosen products on the batch.")
		return
	}

	s.tidyDraftsLeftBehind(ctx, sources, batch.ID)

	updated, ok := s.recomputeBatchPlate(ctx, c, batch)
	if !ok {
		return
	}
	c.JSON(http.StatusCreated, batchDTO(updated))
}

// tidyDraftsLeftBehind repairs the Drafts these products were taken from.
//
// A bed describes the plate built from the products on it - its utilisation,
// its print time, its merged plate file. Take one away and all three describe a
// bed that no longer exists, so each source is rebuilt from what it still has.
// A source emptied completely is deleted: an empty Draft is a row that looks
// like work and is not, and the planner would propose it again from scratch
// anyway.
//
// Best-effort, and deliberately after the new bed exists. The products have
// already moved; failing the request now would report a failure for something
// that succeeded, and leave the caller unsure whether to try again.
func (s *Server) tidyDraftsLeftBehind(ctx context.Context, sources []uuid.UUID, built uuid.UUID) {
	log := obs.FromContext(ctx)
	for _, id := range sources {
		if id == built {
			continue
		}
		remaining, err := s.store.Q.ListJobsForBatch(ctx, &id)
		if err != nil {
			log.Warn("could not re-read a bed products were taken from", "batch", id, "error", err)
			continue
		}
		if len(remaining) == 0 {
			if _, err := s.store.Q.DeleteBatch(ctx, id); err != nil {
				log.Warn("could not remove an emptied draft bed", "batch", id, "error", err)
			}
			continue
		}
		// Its plate, utilisation and print time all describe a bed that no
		// longer exists, and the machine scheduler ranks load on that print
		// time - so they are cleared rather than left to mislead. The planner
		// owns this bed and rebuilds it properly on its next run, which the
		// trigger below asks for.
		if _, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, gen.UpdateBatchDerivedMetricsParams{
			ID: id,
		}); err != nil {
			log.Warn("could not clear a source bed's stale plate", "batch", id, "error", err)
		}
	}
	if len(sources) > 0 {
		s.triggerBatchPlan(ctx)
	}
}

// eligibleJobsFor loads the chosen jobs and refuses any that may not be batched.
//
// Re-read and re-checked server-side rather than trusted from the request. The
// dialog only ever offers eligible jobs, but a request is not a dialog: these
// ids arrive from a client, and the list may have been on screen for minutes
// while somebody chose, by which time another operator's bed may already have
// claimed one of them.
func (s *Server) eligibleJobsFor(ctx context.Context, raw []string) ([]gen.ProductionJob, error) {
	out := make([]gen.ProductionJob, 0, len(raw))
	seen := map[uuid.UUID]bool{}
	for _, id := range raw {
		jobID, err := uuid.Parse(id)
		if err != nil {
			return nil, statusErr(http.StatusUnprocessableEntity,
				"One of the chosen products is not a valid identifier.")
		}
		if seen[jobID] {
			continue
		}
		seen[jobID] = true

		job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
		if err != nil {
			return nil, statusErr(http.StatusNotFound, "One of the chosen products no longer exists.")
		}
		if err := batchableNow(job); err != nil {
			return nil, statusErr(http.StatusUnprocessableEntity, err.Error())
		}
		out = append(out, job)
	}
	if err := s.refuseCommittedBeds(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// refuseCommittedBeds refuses any product already on a bed that is past Draft.
//
// batchableNow cannot answer this: the job row carries a batch id and not that
// batch's status, and the difference is the whole rule. Taking a plank off a
// proposal costs nothing; taking one off a locked bed strands reserved filament
// and a plate that has already been sliced around the gap it leaves.
//
// One query for the whole selection rather than one per product.
func (s *Server) refuseCommittedBeds(ctx context.Context, jobs []gen.ProductionJob) error {
	ids := dedupeIDs(jobs, func(j gen.ProductionJob) *uuid.UUID { return j.BatchID })
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.store.Q.ListBatchStatusesForIDs(ctx, ids)
	if err != nil {
		return statusErr(http.StatusInternalServerError, "Could not check the beds those products are on.")
	}
	status := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		status[r.ID] = r.Status
	}
	for _, j := range jobs {
		if j.BatchID == nil {
			continue
		}
		if st := status[*j.BatchID]; st != production.BatchPendingApproval {
			return statusErr(http.StatusUnprocessableEntity, fmt.Sprintf(
				"%s is on a bed that has already been locked, so it cannot be moved", j.JobNumber))
		}
	}
	return nil
}

// batchableNow mirrors ListBatchableJobs' WHERE clause, job by job.
//
// Deliberately a second statement of the same rule, because the first is SQL
// and cannot explain itself to an operator. Each refusal names the job and what
// is wrong with it, which "that job is not eligible" would not.
func batchableNow(j gen.ProductionJob) error {
	switch {
	// Being on a bed is NOT a refusal here: a Draft is a proposal and its
	// products can be moved onto a bed somebody is building by hand. Which
	// beds are still proposals is checked in refuseCommittedBeds, which can
	// see their status; this row cannot.
	case j.Status != production.StatusQueued:
		return fmt.Errorf("%s is %s, so it cannot go on a bed", j.JobNumber, j.Status)
	// The raw quantity, not jobQuantity, which clamps a zero up to one. A
	// split job's original row stays at zero once every unit has been peeled
	// off into its own rows; clamping would offer that spent row as one more
	// plank to print.
	case j.Quantity <= 0:
		return fmt.Errorf("%s has nothing left to print", j.JobNumber)
	case j.Held:
		return fmt.Errorf("%s is on hold", j.JobNumber)
	case j.IssueReason != nil:
		return fmt.Errorf("%s needs attention first: %s", j.JobNumber, *j.IssueReason)
	case j.PersonalisationStatus != production.PersonalisationValidated &&
		j.PersonalisationStatus != production.PersonalisationNotRequired:
		return fmt.Errorf("%s's personalisation has not been checked yet", j.JobNumber)
	}
	return nil
}

// checkOneBedWorth refuses a selection that could not physically be one bed.
//
// The same two rules the planner and the add-to-batch path enforce, for the
// same reason: a plate declares one filament per slot and holds a fixed number
// of places, and neither becomes negotiable because a person did the choosing.
func checkOneBedWorth(jobs []gen.ProductionJob, unitCap int) error {
	if len(jobs) == 0 {
		return fmt.Errorf("choose at least one product for this bed")
	}
	key := compatibilityKeyOf(jobs[0])
	if key.Colour == "" {
		return fmt.Errorf("%s records no filament colour, so nothing can be matched to it",
			jobs[0].JobNumber)
	}
	for _, j := range jobs[1:] {
		if compatibilityKeyOf(j) != key {
			return fmt.Errorf(
				"%s does not match %s - a bed holds one colour, material and nozzle setup",
				j.JobNumber, jobs[0].JobNumber)
		}
	}
	if units := unitsOf(jobs); units > unitCap {
		return fmt.Errorf("a bed holds %d products; this is %d", unitCap, units)
	}
	return nil
}

// compatibilityKeyString renders a compatibility key for the browser to compare.
//
// Opaque on purpose. The dialog needs to know only whether two jobs may share a
// bed, never why, and giving it the parts would invite it to start deciding for
// itself - at which point the rule lives in two languages and one of them is
// wrong. fmt's %v over the struct changes if the struct does, which is the
// correct coupling: a new field that splits beds must split them here too.
func compatibilityKeyString(j gen.ProductionJob) string {
	return fmt.Sprintf("%v", compatibilityKeyOf(j))
}
