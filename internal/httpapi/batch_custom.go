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
}

// listBatchableJobs returns every job eligible to be put on a bed by hand.
//
// The same pool the planner draws from, so a job the planner would refuse -
// held, flagged, personalisation unresolved - is not offered here either. A
// dialog that let somebody pick a held job would be offering to overrule a hold
// by accident.
func (s *Server) listBatchableJobs(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListBatchableJobs(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the jobs waiting to be batched.")
		return
	}

	dtos := s.productionJobsDTO(ctx, rows)
	out := batchableJobsResponse{
		Jobs:        make([]batchableJob, 0, len(dtos)),
		UnitsPerBed: s.bedUnitCap(),
	}
	for i, dto := range dtos {
		out.Jobs = append(out.Jobs, batchableJob{
			productionJobResponse: dto,
			CompatibilityKey:      compatibilityKeyString(rows[i]),
			ColourLabel:           jobColourKey(rows[i]),
		})
	}
	c.JSON(http.StatusOK, out)
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
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the batch.")
		return
	}

	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	if err := s.store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
		BatchID: &batch.ID, JobIds: ids,
	}); err != nil {
		// The empty batch is left behind rather than deleted: it is visible,
		// harmless and deletable, whereas a failed cleanup would hide the fact
		// that anything went wrong at all.
		detail(c, http.StatusInternalServerError, "Could not put the chosen products on the batch.")
		return
	}

	updated, ok := s.recomputeBatchPlate(ctx, c, batch)
	if !ok {
		return
	}
	c.JSON(http.StatusCreated, batchDTO(updated))
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
	return out, nil
}

// batchableNow mirrors ListBatchableJobs' WHERE clause, job by job.
//
// Deliberately a second statement of the same rule, because the first is SQL
// and cannot explain itself to an operator. Each refusal names the job and what
// is wrong with it, which "that job is not eligible" would not.
func batchableNow(j gen.ProductionJob) error {
	switch {
	case j.BatchID != nil:
		return fmt.Errorf("%s is already on a bed", j.JobNumber)
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
