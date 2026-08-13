package httpapi

// AutoCreateBatches is the Batch Optimizer's entry point (Stage 4 grouping +
// Stage 5 optimization, via production.Plan): list everything batchable, plan
// it, insert a Draft (pending_approval) batch per plan and assign its jobs.
// Shared by the kept manual POST /batches/auto-create endpoint and the River
// batch-plan worker (batch_plan_worker.go) - not a second, divergent
// implementation of either.
//
// This lives in httpapi, not internal/production, because it reads and writes
// the database (ListBatchableJobs, planJobsFor's file lookups, InsertBatch);
// internal/production stays a pure, DB-free package by design (see its package
// doc comment) - the same reasoning that put CreateJobsForOrder here instead of
// internal/production for Stage 2.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// Sentinel errors so callers (the HTTP handler, the worker) can tell which
// stage failed without parsing error strings - the HTTP handler maps each to
// the same distinct {"detail"} message it returned before this was extracted.
var (
	errListBatchableJobs = errors.New("list batchable jobs")
	errPlanBatchableJobs = errors.New("resolve batchable jobs' print files")
)

// AutoCreateBatches returns the newly-created batches, jobs that could never
// be placed, held partitions (compatible + packed, but under the utilisation
// target with no override yet - their jobs stay queued, not assigned to any
// batch, for the next run to reconsider), and an error.
func (s *Server) AutoCreateBatches(ctx context.Context) ([]gen.Batch, []production.Unbatchable, []production.PlannedBatch, error) {
	// Serialised, and skipped outright when nothing has changed. Re-planning
	// rebuilds every Draft's preview plate and re-enqueues its plate slice, so
	// repeating it on an identical pool does not merely waste work - it deletes
	// previews and slices before they can finish, and no batch ever ends up
	// with a measured print time.
	s.planMu.Lock()
	defer s.planMu.Unlock()

	signature, sigOK := s.poolSignature(ctx)
	if sigOK && signature == s.lastPlannedPool {
		obs.FromContext(ctx).Info("batch plan skipped, the job pool is unchanged since the last run",
			"pool", signature)
		return nil, nil, nil, nil
	}

	// The pool is unbatched jobs PLUS everything currently sitting in a Draft.
	// Drafts are dissolved and reformed below, which is what lets a bed that
	// formed half-empty keep absorbing compatible work instead of being frozen
	// at its creation size.
	jobs, err := s.store.Q.ListReplannableJobs(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errListBatchableJobs, err)
	}
	draftIDs, err := s.store.Q.ListDraftBatchIDs(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errListBatchableJobs, err)
	}
	// What each existing Draft currently holds, so an unchanged one can be
	// kept instead of destroyed and rebuilt - see keepUnchangedDrafts.
	existingDrafts := s.draftJobSets(ctx)

	planJobs, err := s.planJobsFor(ctx, jobs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errPlanBatchableJobs, err)
	}
	gate := production.BatchGate{
		MaxWait:           time.Duration(s.cfg.BatchMaxWaitHours * float64(time.Hour)),
		DueSoonWindow:     time.Duration(s.cfg.BatchDueSoonHours * float64(time.Hour)),
		AgingWindow:       time.Duration(s.cfg.BatchAgingWindowMinutes * float64(time.Minute)),
		AgingFloorPercent: s.cfg.BatchAgingFloorPercent,
		IdleMachines:      s.idleMachineCount(ctx),
	}
	planned, unbatchable, held := production.Plan(planJobs, time.Now(), gate)

	log := obs.FromContext(ctx)
	for _, h := range held {
		// Not silent: these jobs are genuinely not being batched this run -
		// fillBed already exhausted every compatible job in the cluster, the
		// result landed under the 80% target, and no override (urgent
		// priority, due soon, max wait) applied. They stay in the queue;
		// ListBatchableJobs picks them up again next run.
		log.Info("batch held below target, waiting for more compatible volume",
			"jobs", len(h.Jobs), "utilisation_percent", h.BedUtilisationPercent,
			"target_percent", production.TargetBedUtilisationPercent)
	}

	// originalQty is each job's quantity as loaded before Plan ran - the
	// yardstick splitJobIDsFor uses to tell an unsplit job (quantity
	// unchanged, commit it as-is, the overwhelmingly common case) from a
	// split fragment (quantity reduced, needs a new row - see
	// splitJobIDsFor's doc comment for why every committed fragment mints a
	// new row rather than only the second-and-later ones).
	originalQty := make(map[string]int32, len(planJobs))
	for _, pj := range planJobs {
		originalQty[pj.ID] = int32(pj.Quantity)
	}
	committed := make(map[string]bool, len(planJobs))

	// Minutes this run has already committed to each machine, so successive
	// batches in the same transaction see each other - see inRunLoad.
	pending := inRunLoad{}

	// Drafts whose job set this plan reproduces exactly. They are left alone:
	// same row, same id, same merged plate, same plate slice.
	//
	// Rebuilding them unconditionally looked harmless and was not. A plate
	// slice takes tens of seconds; re-planning runs every couple of minutes and
	// gave every rebuilt Draft a NEW id, so slices kept completing against
	// batches that no longer existed ("record plate slice result: no rows in
	// result set"). No Draft ever acquired a measured print time, nothing could
	// be promoted - promotion requires one - and all three machines sat idle
	// while dozens of Drafts accumulated.
	keep := map[uuid.UUID]bool{}
	for _, p := range planned {
		if id, ok := existingDrafts[jobSetKey(p.Jobs)]; ok && !keep[id] {
			keep[id] = true
		}
	}
	var toDissolve []uuid.UUID
	for _, id := range draftIDs {
		if !keep[id] {
			toDissolve = append(toDissolve, id)
		}
	}

	created := make([]gen.Batch, 0, len(planned))
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		// Dissolve the Drafts this plan supersedes, inside the same
		// transaction that writes their replacements. Both steps or neither:
		// a failure between them would leave jobs detached from a batch that
		// no longer exists and no new batch to hold them.
		//
		// Both statements re-check `status = 'pending_approval'` in the
		// database rather than trusting the id list read before planning, so a
		// batch a human approved in that window keeps its jobs and survives.
		// Its jobs are then double-assigned in this plan - the AssignJobsToBatch
		// below would move them - which is why the guard matters: they are
		// excluded from the unassign, and the approved batch's own membership
		// is what the machine actually prints.
		if len(toDissolve) > 0 {
			if err := q.UnassignJobsFromBatches(ctx, toDissolve); err != nil {
				return err
			}
			if err := q.DeleteDraftBatches(ctx, toDissolve); err != nil {
				return err
			}
		}
		for _, p := range planned {
			// Already on the floor and unchanged - leave it exactly as it is.
			if id, ok := existingDrafts[jobSetKey(p.Jobs)]; ok && keep[id] {
				continue
			}
			number, err := q.NextBatchNumber(ctx)
			if err != nil {
				return err
			}
			material := batchMaterial(p.Jobs)
			shortage := !s.filamentAvailable(ctx, q, material, p.TotalFilamentGrams)
			// Stage 9: auto-assign the best-scoring machine for this batch's
			// required family/material/colours; a human can still override
			// at approval.
			family, familyOK, familyWhy := batchMachineFamily(p.Jobs)
			if !familyOK {
				// Machine family is no longer a compatibility boundary (see
				// production.groupKey), so a bed CAN now hold jobs wanting
				// different families. Picking the first job's family would
				// assign the plate to a machine the rest of it cannot print
				// on. Leave it unassigned for a human instead of guessing.
				// Unassigned is not a soft state: ListApprovableDraftsForMachine
				// finds a machine's drafts *by* machine_id, so this batch can
				// never be picked up by any printer and will sit in Draft until
				// a human intervenes. Warn with the reason and the job count.
				log.Warn("batch left unassigned and no machine can ever pick it up: "+familyWhy,
					"jobs", len(p.Jobs))
			}
			var materialStr string
			if material != nil {
				materialStr = *material
			}
			assigned := s.assignMachineForBatch(ctx, family, materialStr, planColours(p.Jobs), pending)
			if assigned != nil {
				// Charge this machine for the batch it is about to receive, so
				// the next batch in this same run sees it as busier.
				mins := 0
				if p.TotalPrintTimeMinutes != nil {
					mins = *p.TotalPrintTimeMinutes
				}
				pending[*assigned] += mins
			}
			b, err := q.InsertBatch(ctx, gen.InsertBatchParams{
				ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
				MachineID:        assigned,
				MaterialShortage: shortage, UnitsPerBed: int32ptr(p.UnitsPerBed),
				TotalPrintTimeMinutes:       int32PtrFromInt(p.TotalPrintTimeMinutes),
				EffectiveTimePerUnitMinutes: p.EffectiveTimePerUnitMinutes,
				TotalFilamentGrams:          &p.TotalFilamentGrams,
				BedUtilizationPercent:       &p.BedUtilisationPercent,
				PackingStrategy:             &p.PackingStrategy,
			})
			if err != nil {
				return err
			}
			jobIDs, err := s.splitJobIDsFor(ctx, q, p.Jobs, originalQty, committed)
			if err != nil {
				return err
			}
			if err := q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
				BatchID: ptr(b.ID), JobIds: jobIDs,
			}); err != nil {
				return err
			}
			if p.BedUtilisationPercent < production.TargetBedUtilisationPercent {
				// Created despite being under target: an override fired
				// (urgent priority, due soon, or max wait) - worth flagging
				// distinctly from a normal >=80% batch.
				log.Warn("batch created under the bed-utilisation target via override",
					"batch", b.ID, "utilisation_percent", p.BedUtilisationPercent,
					"target_percent", production.TargetBedUtilisationPercent, "jobs", len(p.Jobs))
			}
			created = append(created, b)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("insert batches: %w", err)
	}

	// Recorded only after a successful plan, and re-read rather than reusing
	// the signature from the top: this run has just changed the pool itself
	// (splitting jobs, reassigning batch ids), so the pre-plan value would not
	// match what the next run computes and every run would look like a change.
	if after, ok := s.poolSignature(ctx); ok {
		s.lastPlannedPool = after
	}

	// Cache each Draft's merged plate now, off the tx and best-effort, so
	// approveBatch/previewBatch can promote/serve it instead of re-merging on
	// the request path (Stage 8's "move the merge off the request path"). A
	// cache failure never fails batch creation - it just falls back to
	// building synchronously later, same as before this cache existed.
	if s.storage != nil {
		for _, b := range created {
			s.cachePreview(ctx, b)
		}
		// Kept Drafts never pass through the creation loop, so nothing above
		// builds their plate or queues its slice. Most already have both from
		// the run that created them - but one whose slice failed, or which was
		// created before plate slicing existed, would otherwise sit unsliced
		// forever, and an unsliced Draft can never be promoted to a machine.
		s.ensurePlateSliceForKeptDrafts(ctx, keep)
	}
	return created, unbatchable, held, nil
}

// splitJobIDsFor resolves one PlannedBatch's jobs to the production_jobs row
// ids AssignJobsToBatch should link, handling production.packJobs's
// quantity-split fragments (see splitJobToFit): a job whose PlanJob.Quantity
// still matches its original, not-yet-committed quantity is linked directly,
// unchanged - the common case, zero extra queries. Any other occurrence
// (quantity reduced by a split, or a second/later fragment of the same job
// id within this run) always gets a brand-new row via splitProductionJob,
// linked via split_of_job_id, with the original's own quantity decremented
// by the same amount - so the original row keeps representing exactly
// what's left un-split, still fully accounted for whether that remainder
// ends up in another batch this run or stays queued for a later one.
func (s *Server) splitJobIDsFor(
	ctx context.Context, q *gen.Queries, jobs []production.PlanJob,
	originalQty map[string]int32, committed map[string]bool,
) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		id, err := uuid.Parse(j.ID)
		if err != nil {
			continue
		}
		full := !committed[j.ID] && int32(j.Quantity) == originalQty[j.ID]
		committed[j.ID] = true
		if full {
			ids = append(ids, id)
			continue
		}
		orig, err := q.GetProductionJobByID(ctx, id)
		if err != nil {
			return nil, err
		}
		fragNumber, err := q.NextJobNumber(ctx)
		if err != nil {
			return nil, err
		}
		frag, err := q.InsertProductionJob(ctx, splitProductionJob(orig, int32(j.Quantity), fragNumber))
		if err != nil {
			return nil, err
		}
		if _, err := q.DecrementProductionJobQuantity(ctx, gen.DecrementProductionJobQuantityParams{
			ID: id, Delta: int32(j.Quantity),
		}); err != nil {
			return nil, err
		}
		ids = append(ids, frag.ID)
	}
	return ids, nil
}

// cachePreview builds and stores one Draft batch's merged plate, recording it
// as the batch's preview_file_id.
func (s *Server) cachePreview(ctx context.Context, b gen.Batch) {
	log := obs.FromContext(ctx)
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &b.ID)
	if err != nil {
		log.Warn("could not load batch jobs for preview cache", "batch", b.ID, "error", err)
		return
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, b.BatchNumber)
	if herr != nil {
		log.Warn("could not build preview plate", "batch", b.ID, "error", herr.msg)
		return
	}
	fileID, err := s.storePlateSystem(ctx, b.ID, "preview", b.BatchNumber, plate.data, plate.bbox)
	if err != nil {
		log.Warn("could not store preview plate", "batch", b.ID, "error", err)
		return
	}
	if _, err := s.store.Q.SetBatchPreviewFile(ctx, gen.SetBatchPreviewFileParams{ID: b.ID, PreviewFileID: &fileID}); err != nil {
		log.Warn("could not cache preview file id", "batch", b.ID, "error", err)
	}
	// The plate now exists in object storage, which is the earliest moment it
	// can be sliced - hence enqueued here rather than in the batch-creation
	// transaction (see EnqueueBatch's doc comment for why that race matters).
	s.enqueuePlateSlice(ctx, b, jobs, fileID, plate.unitsPerBed)
}

// enqueuePlateSlice queues the measurement that replaces the batch's
// MAX-of-jobs time estimate with a real slice of this bed. Best-effort
// throughout: every early return leaves the batch on its approximation with
// plate_sliced_at NULL, which is where every batch sat before plate slicing
// existed.
func (s *Server) enqueuePlateSlice(ctx context.Context, b gen.Batch, jobs []gen.ProductionJob, plateFileID uuid.UUID, units int) {
	log := obs.FromContext(ctx)
	if s.enqueuer == nil {
		return
	}
	file, err := s.store.Q.GetFileAsset(ctx, plateFileID)
	if err != nil {
		log.Warn("could not resolve the plate's storage key", "batch", b.ID, "error", err)
		return
	}
	material, quality, infill, ok := s.plateSliceSpecFor(ctx, jobs)
	if !ok {
		// Every job on the bed failed to resolve back to an approved design,
		// so there is no quality preset to slice against. Not an error: a
		// batch of jobs whose designs were since unpublished is legitimate.
		log.Info("no design spec for the batch's plate, leaving its estimated time", "batch", b.ID)
		return
	}
	var plateHeight float64
	if z := db.NumFloatPtr(file.BboxZMm); z != nil {
		plateHeight = *z
	}
	if err := s.enqueuer.EnqueueBatch(ctx, slicing.SliceBatchArgs{
		BatchID: b.ID, PlateKey: file.StorageKey, Units: units,
		Material: material, Quality: quality, InfillPct: infill,
		Colours: planColoursFromJobs(jobs), PlateHeightMM: plateHeight,
	}); err != nil {
		log.Warn("could not enqueue the plate slice", "batch", b.ID, "error", err)
	}
}

// plateSliceSpecFor resolves the material, quality preset and infill to slice a
// batch's plate with.
//
// Material and quality are safe to read from any one job: production.groupKey
// partitions on exactly those (plus both nozzles), so no two jobs on one bed
// can disagree about them.
//
// Infill is different, and this is the deliberate cost of widening that key.
// Infill is NOT a compatibility boundary any more - a 15% job and a 25% job
// share a bed - but the merged plate is a single STL sliced once, with one
// global --sparse-infill-density (see RunSlice), so one number has to stand for
// the whole bed. This takes the HIGHEST infill present. That makes the plate
// estimate a deliberate upper bound on time and filament, which is the safe
// direction: the scheduler may believe a machine frees up later than it does,
// never sooner. The exact fix is per-object modifiers in an authored 3MF, which
// meshio cannot write today.
//
// The design is the source rather than the job because a job snapshots quality
// as a layer height in mm, and mm does not identify a preset: 0.20mm High
// Quality and 0.20mm Standard share a layer height and differ in speed, which
// is exactly what the time estimate turns on.
func (s *Server) plateSliceSpecFor(ctx context.Context, jobs []gen.ProductionJob) (material, quality string, infill float64, ok bool) {
	mixed := false
	for _, j := range jobs {
		if j.Sku == nil {
			continue
		}
		d, err := s.store.Q.GetDesignBySKU(ctx, j.Sku)
		if err != nil {
			continue
		}
		if !ok {
			material, quality, infill, ok = d.Material, d.Quality, d.InfillPct, true
			continue
		}
		if d.InfillPct != infill {
			mixed = true
		}
		if d.InfillPct > infill {
			infill = d.InfillPct
		}
	}
	if mixed {
		obs.FromContext(ctx).Info(
			"bed mixes infill densities; slicing the plate at the highest, so its time and filament are an upper bound",
			"infill_pct", infill, "jobs", len(jobs))
	}
	return material, quality, infill, ok
}

// storePlateSystem is storePlateAs attributed to systemActor (see
// production_events.go): the batch worker has no signed-in user to credit.
func (s *Server) storePlateSystem(ctx context.Context, batchID uuid.UUID, kind, batchNumber string, data []byte, bbox meshio.Bbox) (uuid.UUID, error) {
	return s.storePlateAs(ctx, batchID, kind, batchNumber, data, bbox, systemActor)
}

// triggerBatchPlan schedules a debounced replan (see BatchPlanEnqueuer)
// unconditionally - for the low-frequency, high-signal events (a batch
// completing, freeing up a machine; a reprint being created) where it's
// always worth a look regardless of how much else is queued. High-frequency
// per-job events (job creation, personalisation validation) go through
// triggerBatchPlanIfThresholdMet instead, so a burst of individual orders
// doesn't force a replan per order - see its own doc comment. Best-effort: a
// batch is not created transactionally with anything else, so a failure here
// is logged, not propagated - the caller's own work still succeeded, and the
// next natural trigger (including the periodic tick, see
// cmd/productionworker/main.go) picks up the same batchable jobs regardless.
func (s *Server) triggerBatchPlan(ctx context.Context) {
	if s.batchEnqueuer == nil {
		return
	}
	if err := s.batchEnqueuer.Enqueue(ctx); err != nil {
		obs.FromContext(ctx).Error("could not schedule batch replan", "error", err)
	}
}

// triggerBatchPlanIfThresholdMet is triggerBatchPlan gated on a cheap
// backlog count, for the two high-frequency per-job trigger sites (job
// creation, personalisation validation) - stopping "replan after every
// single order" per the user's explicit direction, while a periodic tick
// (cmd/productionworker/main.go) and the two unconditional low-frequency
// triggers (triggerBatchPlan) still guarantee batchable jobs are eventually
// reconsidered even below the threshold. A count-query failure fails OPEN
// (triggers anyway): a broken count check must never permanently starve the
// planner, and the periodic tick alone could otherwise be minutes away.
func (s *Server) triggerBatchPlanIfThresholdMet(ctx context.Context) {
	log := obs.FromContext(ctx)
	count, err := s.store.Q.CountBatchableJobs(ctx)
	if err != nil {
		log.Warn("could not count batchable jobs, triggering replan anyway", "error", err)
		s.triggerBatchPlan(ctx)
		return
	}
	if int(count) < s.cfg.BatchPlanJobThreshold {
		log.Info("batch replan skipped, below threshold", "batchable_jobs", count, "threshold", s.cfg.BatchPlanJobThreshold)
		return
	}
	s.triggerBatchPlan(ctx)
}

// batchMachineFamily is the one machine family every job on a bed needs, and
// whether they agree on it.
//
// Jobs with no family recorded are ignored rather than treated as a distinct
// family: an unset value means "unknown", not "different", and a bed of
// entirely-unknown jobs correctly yields ("", false) so nothing is assigned.
//
// This exists because machineFamily was removed from production.groupKey - the
// fleet is one family with one bed size today, so it was pure fragmentation.
// The day a second bed size arrives, put it back in the key and this becomes
// dead weight rather than a guard.
// why names which of the two failures happened, for the caller's log. The
// distinction matters more than it looks: both leave the batch unassigned and
// therefore invisible to every machine for ever, but "no family at all" points
// at the design catalogue (a design with no printer profile) while "disagree"
// points at the planner's grouping. Reporting the first as the second sends
// whoever is debugging a stalled floor looking for a mixed bed that does not
// exist - which is exactly what happened.
func batchMachineFamily(jobs []production.PlanJob) (family string, ok bool, why string) {
	for _, j := range jobs {
		if j.MachineFamily == "" {
			continue
		}
		if family == "" {
			family = j.MachineFamily
			continue
		}
		if j.MachineFamily != family {
			return "", false, "its jobs disagree on machine family"
		}
	}
	if family == "" {
		return "", false, "no job on it records a machine family, so their designs have no printer profile"
	}
	return family, true, ""
}

// idleMachineCount is how many printers could start a plate right now: fleet
// status idle, linked to a profile, and that profile online.
//
// Deliberately fails to ZERO rather than open. A lookup error here would
// otherwise silently switch every under-target partition to "create", which is
// the opposite of the conservative default - a batch held one cycle longer is
// recoverable, a bed committed at 20% is not.
func (s *Server) idleMachineCount(ctx context.Context) int {
	rows, err := s.store.Q.ListFleetMachinesWithFamily(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("could not count idle machines; holding under-target batches as usual", "error", err)
		return 0
	}
	idle := 0
	for _, r := range rows {
		if r.Status != production.FleetMachineIdle || r.MachineProfileID == nil {
			continue
		}
		// An unlinked or offline profile cannot be scheduled onto, so its
		// machine being "idle" is not spare capacity.
		if r.ProfileStatus == nil || *r.ProfileStatus != production.MachineOnline {
			continue
		}
		idle++
	}
	return idle
}

// poolSignature identifies the current replannable job pool cheaply: how many
// jobs, and the most recent change among them.
//
// Re-planning dissolves every Draft and rebuilds it, which also rebuilds each
// Draft's merged preview plate - an object-storage download and mesh merge per
// batch - and re-enqueues its plate slice. Doing that on an unchanged pool is
// pure waste, and worse than waste: at any real batch count the rebuild takes
// longer than the interval between runs, so previews and plate slices are
// deleted before they can finish and no batch ever gets a measured print time.
//
// Returns ok=false if the signature cannot be read, which callers treat as
// "plan anyway" - a broken check must never silently stop batching.
func (s *Server) poolSignature(ctx context.Context) (string, bool) {
	var count int64
	var latest *time.Time
	err := s.store.Pool.QueryRow(ctx, `
		SELECT count(*), max(j.updated_at) FROM production_jobs j
		LEFT JOIN batches b ON b.id = j.batch_id
		WHERE (j.batch_id IS NULL OR b.status = 'pending_approval')
		  AND j.status = 'queued'
		  AND j.quantity > 0
		  AND j.personalisation_status IN ('validated', 'not_required')
		  AND j.issue_reason IS NULL`).Scan(&count, &latest)
	if err != nil {
		return "", false
	}
	stamp := "none"
	if latest != nil {
		stamp = latest.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%d@%s", count, stamp), true
}

// planColoursFromJobs is planColours for already-loaded job rows: the distinct
// colours on a bed, in first-seen order. Used to label the plate's filament
// split, since the slicer reports per extruder rather than per colour name.
func planColoursFromJobs(jobs []gen.ProductionJob) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		for _, c := range decodeColours(j.Colours) {
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// jobSetKey identifies a batch by exactly what is on it: its job ids, sorted so
// the key does not depend on packing order. Two plans that put the same jobs on
// a bed produce the same key even if the packer ordered them differently.
func jobSetKey(jobs []production.PlanJob) string {
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// draftJobSets maps each existing Draft's job set to its batch id. Best-effort:
// on error every Draft is simply treated as changed, which is the rebuild-
// everything behaviour this replaces.
func (s *Server) draftJobSets(ctx context.Context) map[string]uuid.UUID {
	rows, err := s.store.Q.ListDraftBatchJobIDs(ctx)
	if err != nil {
		return nil
	}
	byBatch := map[uuid.UUID][]string{}
	for _, r := range rows {
		byBatch[r.BatchID] = append(byBatch[r.BatchID], r.JobID.String())
	}
	out := make(map[string]uuid.UUID, len(byBatch))
	for id, ids := range byBatch {
		sort.Strings(ids)
		out[strings.Join(ids, ",")] = id
	}
	return out
}

// ensurePlateSliceForKeptDrafts gives any preserved Draft that still has no
// measured plate a preview and a queued slice. Best-effort per batch: one that
// cannot be merged simply stays on its estimate.
func (s *Server) ensurePlateSliceForKeptDrafts(ctx context.Context, keep map[uuid.UUID]bool) {
	for id := range keep {
		b, err := s.store.Q.GetBatchByID(ctx, id)
		if err != nil || b.PlateSlicedAt.Valid {
			continue
		}
		// cachePreview rebuilds the plate and enqueues the slice; it is
		// idempotent enough to repeat, and only runs for Drafts genuinely
		// missing a measurement.
		s.cachePreview(ctx, b)
	}
}
