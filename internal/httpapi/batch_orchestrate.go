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
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
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
	jobs, err := s.store.Q.ListBatchableJobs(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errListBatchableJobs, err)
	}

	planJobs, err := s.planJobsFor(ctx, jobs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errPlanBatchableJobs, err)
	}
	gate := production.BatchGate{
		MaxWait:           time.Duration(s.cfg.BatchMaxWaitHours * float64(time.Hour)),
		DueSoonWindow:     time.Duration(s.cfg.BatchDueSoonHours * float64(time.Hour)),
		AgingWindow:       time.Duration(s.cfg.BatchAgingWindowMinutes * float64(time.Minute)),
		AgingFloorPercent: s.cfg.BatchAgingFloorPercent,
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

	created := make([]gen.Batch, 0, len(planned))
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		for _, p := range planned {
			number, err := q.NextBatchNumber(ctx)
			if err != nil {
				return err
			}
			material := batchMaterial(p.Jobs)
			shortage := !s.filamentAvailable(ctx, q, material, p.TotalFilamentGrams)
			// Stage 9: auto-assign the best-scoring machine for this batch's
			// required family/material/colours; a human can still override
			// at approval.
			var family, materialStr string
			if len(p.Jobs) > 0 {
				family = p.Jobs[0].MachineFamily
			}
			if material != nil {
				materialStr = *material
			}
			b, err := q.InsertBatch(ctx, gen.InsertBatchParams{
				ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
				MachineID:        s.assignMachineForBatch(ctx, family, materialStr, planColours(p.Jobs)),
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

	// Cache each Draft's merged plate now, off the tx and best-effort, so
	// approveBatch/previewBatch can promote/serve it instead of re-merging on
	// the request path (Stage 8's "move the merge off the request path"). A
	// cache failure never fails batch creation - it just falls back to
	// building synchronously later, same as before this cache existed.
	if s.storage != nil {
		for _, b := range created {
			s.cachePreview(ctx, b)
		}
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
}

// storePlateSystem is storePlate without the *gin.Context storePlate needs
// only for currentUserID/error-response-writing - the batch worker has
// neither. Uploaded by "system", matching design_match.go's convention for
// files a background process creates.
func (s *Server) storePlateSystem(ctx context.Context, batchID uuid.UUID, kind, batchNumber string, data []byte, bbox meshio.Bbox) (uuid.UUID, error) {
	key := fmt.Sprintf("batches/%s/%s.stl", batchID, kind)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "model/stl"); err != nil {
		return uuid.Nil, err
	}
	fileID := uuid.New()
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: batchNumber + "-" + kind + ".stl", ContentType: "model/stl",
		SizeBytes: int64(len(data)), StorageKey: key, UploadedBy: "system",
		BboxXMm: &bbox.XMM, BboxYMm: &bbox.YMM, BboxZMm: &bbox.ZMM,
	}); err != nil {
		return uuid.Nil, err
	}
	return fileID, nil
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
