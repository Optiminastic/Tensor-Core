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

func (s *Server) AutoCreateBatches(ctx context.Context) ([]gen.Batch, []production.Unbatchable, error) {
	jobs, err := s.store.Q.ListBatchableJobs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errListBatchableJobs, err)
	}

	planJobs, err := s.planJobsFor(ctx, jobs)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errPlanBatchableJobs, err)
	}
	planned, unbatchable := production.Plan(planJobs)

	created := make([]gen.Batch, 0, len(planned))
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		for _, p := range planned {
			number, err := production.NewBatchNumber()
			if err != nil {
				return err
			}
			shortage := !s.filamentAvailable(ctx, q, batchMaterial(p.Jobs), p.TotalFilamentGrams)
			// Stage 9: auto-assign the earliest-free machine for this batch's
			// required family; a human can still override at approval.
			var family string
			if len(p.Jobs) > 0 {
				family = p.Jobs[0].MachineFamily
			}
			b, err := q.InsertBatch(ctx, gen.InsertBatchParams{
				ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
				MachineID:        s.assignMachineForBatch(ctx, family),
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
			if err := q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{
				BatchID: ptr(b.ID), JobIds: jobIDsOf(p.Jobs),
			}); err != nil {
				return err
			}
			if p.BedUtilisationPercent < production.TargetBedUtilisationPercent {
				// Not silently accepted: fillBed already exhausted every
				// compatible job left in this cluster before closing the
				// bed, so this is a genuine exception (too few/large
				// leftover jobs to reach the target), not a packing miss.
				obs.FromContext(ctx).Warn("batch created under the bed-utilisation target",
					"batch", b.ID, "utilisation_percent", p.BedUtilisationPercent,
					"target_percent", production.TargetBedUtilisationPercent, "jobs", len(p.Jobs))
			}
			created = append(created, b)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("insert batches: %w", err)
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
	return created, unbatchable, nil
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

// triggerBatchPlan schedules a debounced replan (see BatchPlanEnqueuer) after
// something may have made new jobs batchable - a job creation run, or a
// personalisation check that resolved to validated. Best-effort: a batch is
// not created transactionally with anything else (unlike CreateJobsForOrder's
// job-creation enqueue), so a failure here is logged, not propagated - the
// caller's own work (the jobs, the validated personalisation) still succeeded,
// and the next natural trigger picks up the same batchable jobs regardless.
func (s *Server) triggerBatchPlan(ctx context.Context) {
	if s.batchEnqueuer == nil {
		return
	}
	if err := s.batchEnqueuer.Enqueue(ctx); err != nil {
		obs.FromContext(ctx).Error("could not schedule batch replan", "error", err)
	}
}
