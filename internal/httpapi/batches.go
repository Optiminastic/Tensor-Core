package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/bedpack"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type batchResponse struct {
	ID                          string     `json:"id"`
	BatchNumber                 string     `json:"batch_number"`
	MachineID                   *string    `json:"machine_id"`
	Status                      string     `json:"status"`
	ApprovedBy                  *string    `json:"approved_by"`
	ApprovedAt                  *time.Time `json:"approved_at"`
	MaterialShortage            bool       `json:"material_shortage"`
	MergedFileID                *string    `json:"merged_file_id"`
	PreviewFileID               *string    `json:"preview_file_id"`
	UnitsPerBed                 *int32     `json:"units_per_bed"`
	TotalPrintTimeMinutes       *int32     `json:"total_print_time_minutes"`
	EffectiveTimePerUnitMinutes *float64   `json:"effective_time_per_unit_minutes"`
	TotalFilamentGrams          *float64   `json:"total_filament_grams"`
	BedUtilizationPercent       *float64   `json:"bed_utilization_percent"`
	PackingStrategy             *string    `json:"packing_strategy"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
	JobsCount                   *int64     `json:"jobs_count,omitempty"`
	// OccupiedAreaMm2/FreeAreaMm2 are derived from BedUtilizationPercent at
	// response time (bedpack.BedXMM*BedYMM is the same denominator the
	// percentage was computed against) - no new column, cheap on every
	// response including list views.
	OccupiedAreaMm2 *float64 `json:"occupied_area_mm2"`
	FreeAreaMm2     *float64 `json:"free_area_mm2"`
	// PlateBbox* is the merged plate's overall combined size, in mm - only
	// attached on the single-batch GET (see attachPlateBbox), never on list
	// responses, so listing batches never pays for a per-row file lookup.
	PlateBboxXMm *float64 `json:"plate_bbox_x_mm,omitempty"`
	PlateBboxYMm *float64 `json:"plate_bbox_y_mm,omitempty"`
	PlateBboxZMm *float64 `json:"plate_bbox_z_mm,omitempty"`
}

func batchDTO(b gen.Batch) batchResponse {
	utilisation := db.NumFloatPtr(b.BedUtilizationPercent)
	occupied, free := occupiedFreeArea(utilisation)
	return batchResponse{
		ID: b.ID.String(), BatchNumber: b.BatchNumber, MachineID: uuidPtrStr(b.MachineID),
		Status: b.Status, ApprovedBy: b.ApprovedBy, ApprovedAt: db.TimePtr(b.ApprovedAt),
		MaterialShortage: b.MaterialShortage, MergedFileID: uuidPtrStr(b.MergedFileID),
		PreviewFileID: uuidPtrStr(b.PreviewFileID), UnitsPerBed: b.UnitsPerBed,
		TotalPrintTimeMinutes: b.TotalPrintTimeMinutes, EffectiveTimePerUnitMinutes: db.NumFloatPtr(b.EffectiveTimePerUnitMinutes),
		TotalFilamentGrams: db.NumFloatPtr(b.TotalFilamentGrams), BedUtilizationPercent: utilisation,
		OccupiedAreaMm2: occupied, FreeAreaMm2: free,
		PackingStrategy: b.PackingStrategy, CreatedAt: db.Time(b.CreatedAt), UpdatedAt: db.Time(b.UpdatedAt),
	}
}

// occupiedFreeArea derives absolute occupied/free mm^2 from a utilisation
// percentage, against the same full-nominal-bed denominator
// bedpack.UtilisationPercent used to compute that percentage in the first
// place - so the three numbers can never disagree with each other.
func occupiedFreeArea(percent *float64) (occupied, free *float64) {
	if percent == nil {
		return nil, nil
	}
	const bedAreaMM2 = bedpack.BedXMM * bedpack.BedYMM
	o := *percent / 100 * bedAreaMM2
	f := bedAreaMM2 - o
	if f < 0 {
		f = 0
	}
	return &o, &f
}

func (s *Server) registerBatches(r *gin.Engine) {
	g := r.Group("/batches")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.BatchRead.Key()), s.listBatches)
	g.POST("", s.guards.RequirePermission(auth.BatchManage.Key()), s.createBatch)
	g.POST("/auto-create", s.guards.RequirePermission(auth.BatchManage.Key()), s.autoCreateBatches)
	g.GET("/:id", s.guards.RequirePermission(auth.BatchRead.Key()), s.getBatch)
	g.PATCH("/:id", s.guards.RequirePermission(auth.BatchManage.Key()), s.patchBatch)
	g.GET("/:id/preview", s.guards.RequirePermission(auth.BatchRead.Key()), s.previewBatch)
	g.POST("/:id/approve", s.guards.RequirePermission(auth.BatchManage.Key()), s.approveBatch)
	g.GET("/:id/compatible-jobs", s.guards.RequirePermission(auth.BatchManage.Key()), s.listCompatibleJobs)
	g.POST("/:id/jobs", s.guards.RequirePermission(auth.BatchManage.Key()), s.addJobsToBatch)
	g.DELETE("/:id/jobs/:jobId", s.guards.RequirePermission(auth.BatchManage.Key()), s.removeJobFromBatch)
}

func (s *Server) listBatches(c *gin.Context) {
	page, ok := parsePageParams(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !page.paginate {
		rows, err := s.store.Q.ListBatches(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list batches.")
			return
		}
		out := make([]batchResponse, 0, len(rows))
		for _, b := range rows {
			out = append(out, batchDTO(b))
		}
		c.JSON(http.StatusOK, out)
		return
	}
	rows, err := s.store.Q.ListBatchesPage(ctx, gen.ListBatchesPageParams{
		CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list batches.")
		return
	}
	out := make([]batchResponse, 0, len(rows))
	for _, b := range rows {
		out = append(out, batchDTO(b))
	}
	if n := len(rows); n > 0 {
		setNextCursor(c, n, page.limit, db.Time(rows[n-1].CreatedAt), rows[n-1].ID)
	}
	c.JSON(http.StatusOK, out)
}

type createBatchRequest struct {
	MachineID *string `json:"machine_id"`
}

func (s *Server) createBatch(c *gin.Context) {
	var req createBatchRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	machineID, ok := s.resolveOptionalMachine(c, req.MachineID)
	if !ok {
		return
	}
	number, err := s.store.Q.NextBatchNumber(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not generate a batch number.")
		return
	}
	b, err := s.store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, MachineID: machineID, Status: production.BatchOpen,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the batch.")
		return
	}
	c.JSON(http.StatusCreated, batchDTO(b))
}

type patchBatchRequest struct {
	Status    *string `json:"status"`
	MachineID *string `json:"machine_id"`
}

func (s *Server) patchBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	body, ok := readBody(c)
	if !ok {
		return
	}
	var req patchBatchRequest
	if err := bindRawJSON(body, &req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return
	}
	ctx := c.Request.Context()

	params := gen.UpdateBatchParams{ID: id}
	if req.Status != nil {
		if !production.ValidBatchStatusTarget(*req.Status) {
			detail(c, http.StatusUnprocessableEntity, "That batch status is not valid.")
			return
		}
		params.Status = req.Status
	}
	if req.MachineID != nil {
		machineID, ok := s.resolveOptionalMachine(c, req.MachineID)
		if !ok {
			return
		}
		params.SetMachineID = true
		params.MachineID = machineID
	}
	if _, err := s.store.Q.GetBatchByID(ctx, id); err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	completing := req.Status != nil && *req.Status == production.BatchCompleted

	var b gen.Batch
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		b, err = q.UpdateBatch(ctx, params)
		if err != nil {
			return err
		}
		if completing {
			// The bed finished printing: every job on it is done unless it
			// individually failed (that job's reprint was already queued via
			// /fail, and force-completing it here would erase the failure
			// and let a bad print slip into assembly/QC as if it passed).
			// This is what actually makes a job reachable from the
			// Assembly/QC/Packaging queues without an operator manually
			// completing every job by hand.
			if _, err := q.CompleteProductionJobsForBatch(ctx, &id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the batch.")
		return
	}
	if completing {
		// A machine just freed up - worth a replan regardless of how much
		// else is currently queued, unlike the threshold-gated per-job
		// triggers (see triggerBatchPlan's doc comment).
		s.triggerBatchPlan(ctx)
	}
	c.JSON(http.StatusOK, batchDTO(b))
}

func (s *Server) getBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	b, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	dto := batchDTO(b)
	if count, err := s.store.Q.CountJobsInBatch(ctx, &id); err == nil {
		dto.JobsCount = &count
	}
	s.attachPlateBbox(ctx, &dto, b.MergedFileID, b.PreviewFileID)
	c.JSON(http.StatusOK, dto)
}

// attachPlateBbox best-effort loads the merged plate's overall dimensions
// (the approved merged file if the batch has one, else the Draft preview) so
// the batch detail page can show how much of the bed the combined plate
// actually uses. Never fails the response - a missing or not-yet-built file
// just leaves the fields nil.
func (s *Server) attachPlateBbox(ctx context.Context, dto *batchResponse, mergedFileID, previewFileID *uuid.UUID) {
	fileID := mergedFileID
	if fileID == nil {
		fileID = previewFileID
	}
	if fileID == nil {
		return
	}
	file, err := s.store.Q.GetFileAsset(ctx, *fileID)
	if err != nil {
		return
	}
	dto.PlateBboxXMm = db.NumFloatPtr(file.BboxXMm)
	dto.PlateBboxYMm = db.NumFloatPtr(file.BboxYMm)
	dto.PlateBboxZMm = db.NumFloatPtr(file.BboxZMm)
}

type autoCreateResponse struct {
	Created     []batchResponse          `json:"created"`
	Unbatchable []production.Unbatchable `json:"unbatchable"`
	// Held is every compatible, packed partition that landed under the
	// utilisation target with no override yet - not created, jobs stay
	// queued for the next run (see AutoCreateBatches/shouldCreateBatch).
	Held []heldPartitionResponse `json:"held"`
}

// heldPartitionResponse is production.PlannedBatch reduced to what a caller
// needs to know about a held (not yet created) partition.
type heldPartitionResponse struct {
	JobsCount             int     `json:"jobs_count"`
	BedUtilizationPercent float64 `json:"bed_utilization_percent"`
}

func heldDTO(held []production.PlannedBatch) []heldPartitionResponse {
	out := make([]heldPartitionResponse, 0, len(held))
	for _, h := range held {
		out = append(out, heldPartitionResponse{
			JobsCount: len(h.Jobs), BedUtilizationPercent: h.BedUtilisationPercent,
		})
	}
	return out
}

func (s *Server) autoCreateBatches(c *gin.Context) {
	// No storage needed here: footprints come from the file_assets bbox in the DB.
	// Merging the plate STL (which needs storage) happens at preview/approve.
	created, unbatchable, held, err := s.AutoCreateBatches(c.Request.Context())
	switch {
	case errors.Is(err, errListBatchableJobs):
		detail(c, http.StatusInternalServerError, "Could not read batchable jobs.")
		return
	case errors.Is(err, errPlanBatchableJobs):
		detail(c, http.StatusInternalServerError, "Could not read the jobs' print files.")
		return
	case err != nil:
		detail(c, http.StatusInternalServerError, "Could not create the batches.")
		return
	}

	out := make([]batchResponse, 0, len(created))
	for _, b := range created {
		out = append(out, batchDTO(b))
	}
	c.JSON(http.StatusOK, autoCreateResponse{Created: out, Unbatchable: unbatchable, Held: heldDTO(held)})
}

func (s *Server) previewBatch(c *gin.Context) {
	if !s.filesReady(c) {
		return
	}
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
	// Cache-aside: reuse the stored preview plate if it is still in storage.
	if batch.PreviewFileID != nil {
		if data, ok := s.fetchStored(ctx, *batch.PreviewFileID); ok {
			c.Data(http.StatusOK, "model/stl", data)
			return
		}
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber)
	if herr != nil {
		detail(c, herr.status, herr.msg)
		return
	}
	fileID, err := s.storePlate(ctx, id, "preview", batch.BatchNumber, plate.data, plate.bbox, c)
	if err != nil {
		return
	}
	if _, err := s.store.Q.SetBatchPreviewFile(ctx, gen.SetBatchPreviewFileParams{ID: id, PreviewFileID: &fileID}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not cache the preview.")
		return
	}
	c.Data(http.StatusOK, "model/stl", plate.data)
}

type approveBatchRequest struct {
	// MachineID is optional: a Draft batch already has one assigned by the
	// batch worker's scheduler (see AutoCreateBatches/assignMachineForBatch).
	// Pass it only to override that choice.
	MachineID *string `json:"machine_id"`
}

func (s *Server) approveBatch(c *gin.Context) {
	if !s.filesReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req approveBatchRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	if batch.Status != production.BatchPendingApproval {
		detail(c, http.StatusConflict, "This batch has already been approved.")
		return
	}

	var machineID *uuid.UUID
	if req.MachineID != nil {
		parsed, err := uuid.Parse(*req.MachineID)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "The machine_id is not a valid identifier.")
			return
		}
		machineID = &parsed
	} else {
		machineID = batch.MachineID
	}
	if machineID == nil {
		detail(c, http.StatusUnprocessableEntity, "This batch has no machine assigned yet; provide a machine_id.")
		return
	}
	if _, err := s.store.Q.GetMachineOps(ctx, *machineID); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	if len(jobs) == 0 {
		detail(c, http.StatusConflict, "The batch has no jobs to approve.")
		return
	}
	for _, j := range jobs {
		if j.PersonalisationStatus == production.PersonalisationPending {
			detail(c, http.StatusConflict, "A job in this batch has unvalidated personalisation.")
			return
		}
	}

	mergedID, unitsPerBed, utilisation, ok := s.mergedPlateFor(ctx, c, batch, jobs)
	if !ok {
		return
	}

	total, eff := batchTimeFromJobs(jobs)
	filament := sumFilament(jobs)
	split := filamentSplitForJobs(jobs)
	shortage := !s.filamentAvailableByColour(ctx, s.store.Q, split)
	approvedBy := currentUserID(c)

	var b gen.Batch
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		// Reserve filament in the same transaction as the Draft->Locked
		// transition below, so a lost race against the guard (WHERE
		// status = 'pending_approval') can never leave stock debited without
		// the batch actually moving to open, or vice versa.
		if err := s.adjustFilamentByColour(ctx, q, split, -1); err != nil {
			return err
		}
		var err error
		b, err = q.ApproveBatch(ctx, gen.ApproveBatchParams{
			ID: id, MachineID: machineID, ApprovedBy: &approvedBy, MergedFileID: &mergedID,
			MaterialShortage: shortage, UnitsPerBed: unitsPerBed, TotalPrintTimeMinutes: total,
			EffectiveTimePerUnitMinutes: eff, TotalFilamentGrams: &filament,
			BedUtilizationPercent: utilisation,
		})
		return err
	})
	if isNoRows(err) {
		detail(c, http.StatusConflict, "This batch has already been approved.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not approve the batch.")
		return
	}
	c.JSON(http.StatusOK, batchDTO(b))
}

// --- job membership editing (Draft batches only) -------------------------

// compatibilityKeyOf builds a job's "same machine configuration" signature
// (see production.CompatibilityKey) from its already-loaded row.
func compatibilityKeyOf(j gen.ProductionJob) production.CompatibilityKey {
	return production.CompatibilityKey{
		Material: deref(j.Material), NozzleLeft: numAsString(j.LeftNozzleMm),
		NozzleRight: numAsString(j.RightNozzleMm), QualityMM: numAsString(j.QualityMm),
		MachineFamily: deref(j.MachineFamily),
	}
}

// listCompatibleJobs returns unassigned jobs matching the batch's own
// configuration (derived from any one of its current jobs - they all share
// one key by construction, per how batches are grouped at creation).
func (s *Server) listCompatibleJobs(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetBatchByID(ctx, id); err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	if len(jobs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "This batch has no jobs yet to derive a compatible configuration from.")
		return
	}
	ref := jobs[0]
	rows, err := s.store.Q.ListUnassignedCompatibleJobs(ctx, gen.ListUnassignedCompatibleJobsParams{
		Material: ref.Material, LeftNozzleMm: db.NumFloatPtr(ref.LeftNozzleMm),
		RightNozzleMm: db.NumFloatPtr(ref.RightNozzleMm), QualityMm: db.NumFloatPtr(ref.QualityMm),
		MachineFamily: ref.MachineFamily,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list compatible jobs.")
		return
	}
	c.JSON(http.StatusOK, s.productionJobsDTO(ctx, rows))
}

type addJobsToBatchRequest struct {
	JobIDs []string `json:"job_ids"`
}

// addJobsToBatch assigns one or more currently-unassigned, configuration-
// matching jobs onto a Draft batch, then re-merges the plate and refreshes
// the batch's derived snapshot fields. Only pending_approval batches accept
// this - once approved, filament is already debited with no reversal path,
// so membership is locked in from there.
func (s *Server) addJobsToBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req addJobsToBatchRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.JobIDs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "job_ids must not be empty.")
		return
	}
	ctx := c.Request.Context()
	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	if batch.Status != production.BatchPendingApproval {
		detail(c, http.StatusConflict, "Only Draft batches can have jobs added.")
		return
	}
	if u := db.NumFloatPtr(batch.BedUtilizationPercent); u != nil && *u >= production.TargetBedUtilisationPercent {
		detail(c, http.StatusUnprocessableEntity, "This batch is full - remove a job before adding another.")
		return
	}

	existing, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	if len(existing) == 0 {
		detail(c, http.StatusUnprocessableEntity, "This batch has no jobs yet to derive a compatible configuration from.")
		return
	}
	key := compatibilityKeyOf(existing[0])

	ids := make([]uuid.UUID, 0, len(req.JobIDs))
	for _, raw := range req.JobIDs {
		jobID, err := uuid.Parse(raw)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "One of the job_ids is not a valid identifier.")
			return
		}
		job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
		if err != nil {
			dbError(c, err, "One of the jobs does not exist.", "Could not load a job.")
			return
		}
		if job.BatchID != nil {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf("Job %s is already assigned to a batch.", job.JobNumber))
			return
		}
		if compatibilityKeyOf(job) != key {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf("Job %s's material/nozzle/machine profile doesn't match this batch.", job.JobNumber))
			return
		}
		ids = append(ids, jobID)
	}

	if err := s.store.Q.AssignJobsToBatch(ctx, gen.AssignJobsToBatchParams{BatchID: &id, JobIds: ids}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not add the jobs to the batch.")
		return
	}

	updated, ok := s.recomputeBatchPlate(ctx, c, batch)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, batchDTO(updated))
}

// removeJobFromBatch detaches one job from a Draft batch, then re-merges the
// plate and refreshes the batch's derived snapshot fields. Always allowed on
// a Draft batch (no utilisation gate) - this is what lets an operator drop
// below the full threshold and add a different job afterward.
func (s *Server) removeJobFromBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	jobID, ok := parseUUIDParam(c, "jobId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	if batch.Status != production.BatchPendingApproval {
		detail(c, http.StatusConflict, "Only Draft batches can have jobs removed.")
		return
	}
	if _, err := s.store.Q.RemoveJobFromBatch(ctx, gen.RemoveJobFromBatchParams{ID: jobID, BatchID: &id}); err != nil {
		dbError(c, err, "That job is not on this batch.", "Could not remove the job.")
		return
	}

	updated, ok := s.recomputeBatchPlate(ctx, c, batch)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, batchDTO(updated))
}

// recomputeBatchPlate re-merges a batch's plate from its current job set and
// persists the refreshed preview/units/utilisation/filament snapshot, called
// after a job add/remove so the batch's derived state never goes stale
// relative to its actual membership. An emptied-out batch (every job
// removed) just clears back to unset rather than erroring - and needs no
// object storage to do so, so filesReady is only checked on the path that
// actually re-merges files. Writes the error response itself and returns
// ok=false on failure, same convention as mergedPlateFor.
func (s *Server) recomputeBatchPlate(ctx context.Context, c *gin.Context, batch gen.Batch) (gen.Batch, bool) {
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return gen.Batch{}, false
	}
	if len(jobs) == 0 {
		updated, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, gen.UpdateBatchDerivedMetricsParams{ID: batch.ID})
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not update the batch.")
			return gen.Batch{}, false
		}
		return updated, true
	}
	if !s.filesReady(c) {
		return gen.Batch{}, false
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber)
	if herr != nil {
		detail(c, herr.status, herr.msg)
		return gen.Batch{}, false
	}
	fileID, err := s.storePlate(ctx, batch.ID, "preview", batch.BatchNumber, plate.data, plate.bbox, c)
	if err != nil {
		return gen.Batch{}, false
	}
	filament := sumFilament(jobs)
	unitsPerBed := int32(plate.unitsPerBed)
	utilisation := plate.utilisation
	updated, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, gen.UpdateBatchDerivedMetricsParams{
		ID: batch.ID, PreviewFileID: &fileID, UnitsPerBed: &unitsPerBed,
		BedUtilizationPercent: &utilisation, TotalFilamentGrams: &filament,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the batch.")
		return gen.Batch{}, false
	}
	return updated, true
}

// mergedPlateFor resolves a batch's merged plate file, reusing the cached
// Draft-time preview (built by the batch worker - see AutoCreateBatches) when
// present, instead of re-merging on the request path. Falls back to building
// it synchronously for a batch that never got one cached (created via POST
// /batches, or the worker's cache attempt failed). Writes the error response
// itself and returns ok=false on any failure, same convention as
// resolveOptionalMachine/resolveOptionalFile.
func (s *Server) mergedPlateFor(
	ctx context.Context, c *gin.Context, batch gen.Batch, jobs []gen.ProductionJob,
) (fileID uuid.UUID, unitsPerBed *int32, utilisation *float64, ok bool) {
	if batch.PreviewFileID != nil {
		return *batch.PreviewFileID, batch.UnitsPerBed, db.NumFloatPtr(batch.BedUtilizationPercent), true
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber)
	if herr != nil {
		detail(c, herr.status, herr.msg)
		return uuid.Nil, nil, nil, false
	}
	id, err := s.storePlate(ctx, batch.ID, "plate", batch.BatchNumber, plate.data, plate.bbox, c)
	if err != nil {
		return uuid.Nil, nil, nil, false
	}
	return id, int32ptr(plate.unitsPerBed), &plate.utilisation, true
}

// --- merge orchestration ------------------------------------------------------

type plateResult struct {
	data        []byte
	unitsPerBed int
	utilisation float64
	bbox        meshio.Bbox
}

type httpErr struct {
	status int
	msg    string
}

// buildMergedPlate resolves each job's print-file footprint, packs the units onto
// one bed, downloads and merges the source models, and returns the plate STL. It
// yields a 409 when a job lacks a measurable print file or the batch overflows the
// bed.
func (s *Server) buildMergedPlate(ctx context.Context, jobs []gen.ProductionJob, batchNumber string) (plateResult, *httpErr) {
	type resolved struct {
		job  gen.ProductionJob
		file gen.FileAsset
		box  bedpack.UnitFootprint
	}
	items := make([]resolved, 0, len(jobs))
	var units []bedpack.UnitFootprint
	for _, j := range jobs {
		if j.PrintFileID == nil {
			return plateResult{}, &httpErr{http.StatusConflict, fmt.Sprintf("Job %s has no print file selected.", j.JobNumber)}
		}
		file, err := s.store.Q.GetFileAsset(ctx, *j.PrintFileID)
		if err != nil {
			return plateResult{}, &httpErr{http.StatusConflict, fmt.Sprintf("Job %s references a missing print file.", j.JobNumber)}
		}
		bx, by, bz := db.NumFloatPtr(file.BboxXMm), db.NumFloatPtr(file.BboxYMm), db.NumFloatPtr(file.BboxZMm)
		if bx == nil || by == nil || bz == nil {
			return plateResult{}, &httpErr{http.StatusConflict, fmt.Sprintf("Job %s has no measurable model dimensions.", j.JobNumber)}
		}
		box := bedpack.UnitFootprint{RefID: j.ID.String(), XMM: *bx, YMM: *by, ZMM: *bz}
		items = append(items, resolved{job: j, file: file, box: box})
		for range jobQuantity(j.Quantity) {
			units = append(units, box)
		}
	}

	placements, rejected := bedpack.Pack(units)
	if len(rejected) > 0 {
		return plateResult{}, &httpErr{http.StatusConflict, "The batch's jobs do not fit on a single bed."}
	}

	// Download and parse each distinct print file once.
	meshByJob := map[string][]orientation.Triangle{}
	for _, it := range items {
		mesh, herr := s.loadModelMesh(ctx, it.file)
		if herr != nil {
			return plateResult{}, herr
		}
		meshByJob[it.job.ID.String()] = mesh.Triangles
	}

	parts := make([]meshio.Placed, 0, len(placements))
	for _, p := range placements {
		parts = append(parts, meshio.Placed{
			Triangles: meshByJob[p.RefID], XOffsetMM: p.XOffsetMM, YOffsetMM: p.YOffsetMM, Rotated: p.Rotated,
		})
	}
	data, bbox := meshio.MergeBinarySTL(batchNumber, parts)
	return plateResult{
		data: data, unitsPerBed: len(units), utilisation: bedpack.UtilisationPercent(units), bbox: bbox,
	}, nil
}

// loadModelMesh downloads a file asset to a temp path and parses its mesh.
func (s *Server) loadModelMesh(ctx context.Context, file gen.FileAsset) (orientation.Mesh, *httpErr) {
	ext := filepath.Ext(file.Filename)
	tmp, err := os.CreateTemp("", "tensor-merge-*"+ext)
	if err != nil {
		return orientation.Mesh{}, &httpErr{http.StatusInternalServerError, "Could not buffer a model file."}
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := s.storage.Download(ctx, file.StorageKey, tmpPath); err != nil {
		return orientation.Mesh{}, &httpErr{http.StatusConflict, fmt.Sprintf("Could not read the model %q from storage.", file.Filename)}
	}
	mesh, err := orientation.LoadModel(tmpPath, ext)
	if err != nil {
		return orientation.Mesh{}, &httpErr{http.StatusConflict, fmt.Sprintf("The model %q could not be merged.", file.Filename)}
	}
	return mesh, nil
}

// storePlate uploads merged plate bytes and records a file asset (with the
// plate's overall bbox, so its dimensions can be displayed/downloaded
// alongside it), returning its id.
func (s *Server) storePlate(ctx context.Context, batchID uuid.UUID, kind, batchNumber string, data []byte, bbox meshio.Bbox, c *gin.Context) (uuid.UUID, error) {
	key := fmt.Sprintf("batches/%s/%s.stl", batchID, kind)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "model/stl"); err != nil {
		detail(c, http.StatusInternalServerError, "Could not store the merged plate.")
		return uuid.Nil, err
	}
	fileID := uuid.New()
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: batchNumber + "-" + kind + ".stl", ContentType: "model/stl",
		SizeBytes: int64(len(data)), StorageKey: key, UploadedBy: currentUserID(c),
		BboxXMm: &bbox.XMM, BboxYMm: &bbox.YMM, BboxZMm: &bbox.ZMM,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the merged plate.")
		return uuid.Nil, err
	}
	return fileID, nil
}

// fetchStored returns the bytes of a stored file asset, or ok=false if it or its
// object is gone (so the caller recomputes).
func (s *Server) fetchStored(ctx context.Context, fileID uuid.UUID) ([]byte, bool) {
	file, err := s.store.Q.GetFileAsset(ctx, fileID)
	if err != nil {
		return nil, false
	}
	obj, err := s.storage.Get(ctx, file.StorageKey)
	if err != nil {
		return nil, false
	}
	defer func() { _ = obj.Body.Close() }()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(obj.Body); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// --- helpers ------------------------------------------------------------------

// decodeColours parses a job's colours jsonb into a plain slice for the
// planner. An empty/invalid value is an empty set, never an error - a job with
// no colours recorded simply groups with other colourless jobs.
func decodeColours(raw []byte) []string {
	var out []string
	if len(raw) == 0 {
		return []string{}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return []string{}
	}
	return out
}

// numAsString renders a nullable numeric snapshot (nozzle/quality) as a
// grouping-key string - "" when null, so two null values still group together
// rather than becoming distinct "0" buckets.
func numAsString(n pgtype.Numeric) string {
	f := db.NumFloatPtr(n)
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// planJobsFor builds planner inputs from jobs, reading each job's print-file
// bounding box (a job with no measurable file gets a zero footprint, which the
// planner reports as unbatchable).
func (s *Server) planJobsFor(ctx context.Context, jobs []gen.ProductionJob) ([]production.PlanJob, error) {
	out := make([]production.PlanJob, 0, len(jobs))
	for _, j := range jobs {
		var box bedpack.UnitFootprint
		if j.PrintFileID != nil {
			file, err := s.store.Q.GetFileAsset(ctx, *j.PrintFileID)
			if err != nil && !isNoRows(err) {
				return nil, err
			}
			if err == nil {
				bx, by, bz := db.NumFloatPtr(file.BboxXMm), db.NumFloatPtr(file.BboxYMm), db.NumFloatPtr(file.BboxZMm)
				if bx != nil && by != nil && bz != nil {
					box = bedpack.UnitFootprint{RefID: j.ID.String(), XMM: *bx, YMM: *by, ZMM: *bz}
				}
			}
		}
		out = append(out, production.PlanJob{
			ID: j.ID.String(), JobNumber: j.JobNumber, ShopifyCustomerID: j.ShopifyCustomerID,
			Material: deref(j.Material), Colours: decodeColours(j.Colours),
			NozzleLeft: numAsString(j.LeftNozzleMm), NozzleRight: numAsString(j.RightNozzleMm),
			QualityMM: numAsString(j.QualityMm), MachineFamily: deref(j.MachineFamily),
			SupportUsed: j.SupportUsed != nil && *j.SupportUsed,
			InfillPct:   db.NumFloat(j.InfillPct), Priority: int(j.Priority),
			Quantity: int(j.Quantity), EstimatedMinutes: int32PtrToIntPtr(j.EstimatedPrintTimeMinutes),
			DueDate: db.TimePtr(j.DueDate), CreatedAt: db.Time(j.CreatedAt),
			FilamentGrams: db.NumFloat(j.FilamentGramsRequired), Footprint: box,
		})
	}
	return out, nil
}

// resolveOptionalMachine validates an optional machine id refers to a real machine.
func (s *Server) resolveOptionalMachine(c *gin.Context, raw *string) (*uuid.UUID, bool) {
	if raw == nil || *raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The machine_id is not a valid identifier.")
		return nil, false
	}
	if _, err := s.store.Q.GetMachineOps(c.Request.Context(), id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return nil, false
	}
	return &id, true
}

func batchMaterial(jobs []production.PlanJob) *string {
	for _, j := range jobs {
		if j.Material != "" {
			m := j.Material
			return &m
		}
	}
	return nil
}

// planColours unions every job's colours - what assignMachineForBatch's
// weighted scoring needs to judge a candidate machine's colour match.
func planColours(jobs []production.PlanJob) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		for _, c := range j.Colours {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func batchTimeFromJobs(jobs []gen.ProductionJob) (*int32, *float64) {
	units := 0
	var maxTime int32
	for _, j := range jobs {
		units += int(jobQuantity(j.Quantity))
		if j.EstimatedPrintTimeMinutes == nil {
			return nil, nil
		}
		if *j.EstimatedPrintTimeMinutes > maxTime {
			maxTime = *j.EstimatedPrintTimeMinutes
		}
	}
	total := maxTime
	if units == 0 {
		return &total, nil
	}
	eff := float64(total) / float64(units)
	return &total, &eff
}

func sumFilament(jobs []gen.ProductionJob) float64 {
	var sum float64
	for _, j := range jobs {
		sum += db.NumFloat(j.FilamentGramsRequired)
	}
	return sum
}
