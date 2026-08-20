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
	"sort"
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
	// PlateSlicedAt says whether TotalPrintTimeMinutes is a measurement of
	// this actual bed or batchTimeFromJobs' MAX-of-jobs approximation. Null
	// means the latter - the plate slice has not run, is still queued, or
	// failed (in which case PlateSliceError says why).
	PlateSlicedAt   *time.Time `json:"plate_sliced_at"`
	PlateSliceError *string    `json:"plate_slice_error"`
	// Measured on the merged plate, present only once it has been sliced.
	// Support and purge in particular are properties of the combined plate
	// rather than of any single job on it, which is why they cannot be summed
	// from per-job estimates.
	TotalLayers      *int32          `json:"total_layers"`
	SupportGrams     *float64        `json:"support_grams"`
	PurgeGrams       *float64        `json:"purge_grams"`
	ColourChanges    *int32          `json:"colour_changes"`
	FilamentByColour json.RawMessage `json:"filament_by_colour"`
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
		PlateSlicedAt: db.TimePtr(b.PlateSlicedAt), PlateSliceError: b.PlateSliceError,
		TotalLayers: b.TotalLayers, SupportGrams: db.NumFloatPtr(b.SupportGrams),
		PurgeGrams: db.NumFloatPtr(b.PurgeGrams), ColourChanges: b.ColourChanges,
		FilamentByColour: rawOrEmptyArray(b.FilamentByColour),
	}
}

// rawOrEmptyArray keeps a null or empty JSONB column rendering as [] rather
// than null, so the frontend's array schema never has to special-case it.
func rawOrEmptyArray(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
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
	s.registerBatchPrint(g)
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

	b, err := s.updateBatchAnd(ctx, params, completing)
	if err != nil {
		writeStatusError(c, err, "Could not update the batch.")
		return
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
	// Parsed here rather than in ApproveBatchFor: the override arrives as a
	// string only over HTTP, and a malformed one is a request-shape error.
	var machineID *uuid.UUID
	if req.MachineID != nil {
		parsed, err := uuid.Parse(*req.MachineID)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "The machine_id is not a valid identifier.")
			return
		}
		machineID = &parsed
	}

	b, err := s.ApproveBatchFor(c.Request.Context(), id, machineID, currentUserID(c))
	if err != nil {
		writeStatusError(c, err, "Could not approve the batch.")
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
	// Recomputed from the batch's NEW membership. Leaving these behind was a
	// real defect: the machine scheduler ranks load on total_print_time_minutes,
	// so a batch edited after creation was assigned on the strength of a plate
	// that no longer existed.
	total, effective := batchTimeFromJobs(jobs)
	updated, err := s.store.Q.UpdateBatchDerivedMetrics(ctx, gen.UpdateBatchDerivedMetricsParams{
		ID: batch.ID, PreviewFileID: &fileID, UnitsPerBed: &unitsPerBed,
		BedUtilizationPercent: &utilisation, TotalFilamentGrams: &filament,
		TotalPrintTimeMinutes: total, EffectiveTimePerUnitMinutes: effective,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the batch.")
		return gen.Batch{}, false
	}
	// No re-slice here. UpdateBatchDerivedMetrics has already cleared
	// plate_sliced_at, because the measurement described a bed that no longer
	// exists, and the batch is back on the fast estimate recomputed just above.
	// This is a Draft being edited - still a proposal, still free to change
	// again - so it is measured at approval like every other bed, not on each
	// keystroke of a job being added or removed.
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
	ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob, uploadedBy string,
) (fileID uuid.UUID, unitsPerBed *int32, utilisation *float64, err error) {
	if batch.PreviewFileID != nil {
		return *batch.PreviewFileID, batch.UnitsPerBed, db.NumFloatPtr(batch.BedUtilizationPercent), nil
	}
	// Building a plate downloads every job's model. The HTTP path checks this
	// via filesReady before the handler runs; a non-HTTP caller has no such
	// gate, and without this the nil client panics deep inside the merge
	// rather than reporting that storage was never configured.
	if s.storage == nil {
		return uuid.Nil, nil, nil, statusErr(http.StatusServiceUnavailable,
			"Object storage is not configured, so the merged plate cannot be built.")
	}
	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber)
	if herr != nil {
		return uuid.Nil, nil, nil, statusErr(herr.status, herr.msg)
	}
	id, err := s.storePlateAs(ctx, batch.ID, "plate", batch.BatchNumber, plate.data, plate.bbox, uploadedBy)
	if err != nil {
		return uuid.Nil, nil, nil, err
	}
	return id, int32ptr(plate.unitsPerBed), &plate.utilisation, nil
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

	// Largest first. bedpack is a guillotine packer, and guillotine packing is
	// ORDER-SENSITIVE: placing a small part first splits the free space into
	// offcuts too narrow for a large one later, so the same set of units can
	// pack in one order and be rejected in another.
	//
	// This is not theoretical. The planner tries four orderings and keeps the
	// best (see production.packingStrategies), then this function re-packed the
	// same jobs in whatever order the database returned them - and rejected
	// beds the planner had just proved fit. A batch of 3x(88x200) plus
	// 4x(55x69) fits comfortably three-across with the hooks in the leftover
	// strip, but interleaved small-first it does not, and the batch silently
	// lost its preview plate with "The batch's jobs do not fit on a single bed".
	//
	// Sorting by descending area is the standard heuristic for this packer and
	// matches the planner's own strategyArea. Placements carry RefID, so
	// reordering here cannot mis-attach a mesh to the wrong job.
	sort.SliceStable(units, func(a, b int) bool {
		return units[a].XMM*units[a].YMM > units[b].XMM*units[b].YMM
	})

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

// storePlate is storePlateAs for a request-scoped caller: the plate is
// attributed to the signed-in user, and the two failure modes are written to
// the response here rather than returned.
func (s *Server) storePlate(ctx context.Context, batchID uuid.UUID, kind, batchNumber string, data []byte, bbox meshio.Bbox, c *gin.Context) (uuid.UUID, error) {
	fileID, err := s.storePlateAs(ctx, batchID, kind, batchNumber, data, bbox, currentUserID(c))
	if err != nil {
		writeStatusError(c, err, "Could not store the merged plate.")
		return uuid.Nil, err
	}
	return fileID, nil
}

// storePlateAs uploads merged plate bytes and records a file asset (with the
// plate's overall bbox, so its dimensions can be displayed/downloaded
// alongside it), returning its id. uploadedBy is the signed-in user for a
// request-driven merge and "system" for one a background process builds,
// matching design_match.go's convention.
func (s *Server) storePlateAs(
	ctx context.Context, batchID uuid.UUID, kind, batchNumber string,
	data []byte, bbox meshio.Bbox, uploadedBy string,
) (uuid.UUID, error) {
	key := fmt.Sprintf("batches/%s/%s.stl", batchID, kind)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "model/stl"); err != nil {
		return uuid.Nil, statusErrf(http.StatusInternalServerError, "Could not store the merged plate.", err)
	}
	fileID := uuid.New()
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: batchNumber + "-" + kind + ".stl", ContentType: "model/stl",
		SizeBytes: int64(len(data)), StorageKey: key, UploadedBy: uploadedBy,
		BboxXMm: &bbox.XMM, BboxYMm: &bbox.YMM, BboxZMm: &bbox.ZMM,
	}); err != nil {
		return uuid.Nil, statusErrf(http.StatusInternalServerError, "Could not record the merged plate.", err)
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
			// Already on a Draft. ListReplannableJobs is the only source that
			// returns batched jobs at all, and everything it returns with a
			// batch_id is on a pending_approval batch by its own predicate - so
			// this is exactly "is in a Draft". The planning window keeps these
			// whatever they score, or the run could not reproduce that Draft's
			// job set and would dissolve it purely because the backlog grew.
			InDraft: j.BatchID != nil,
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

// batchTimeFromJobs is the fast (Level 1) batch estimate: the summed per-unit
// print time of everything on the bed, discounted for the overhead parts share
// when printed together. It is what batch planning and machine scheduling run
// on until the merged plate is really sliced, at which point the measurement
// replaces it (callers check plate_sliced_at first).
//
// Sum, not max. Max was the previous model and wrong in the worst direction: a
// bed of eight 40-minute parts was scheduled as 40 minutes, so a machine could
// be handed a full day of work while looking nearly idle to the scheduler.
//
// A job with no estimate no longer voids the whole batch. It used to return
// (nil, nil) the moment one job lacked a time, which stripped the estimate from
// every job beside it and made the bed look free; jobs now carry a
// geometry-derived default (see applyMatch), and anything still missing is
// simply skipped rather than poisoning its neighbours.
func batchTimeFromJobs(jobs []gen.ProductionJob) (*int32, *float64) {
	units := 0
	unitMinutes := make([]int, 0, len(jobs))
	quantities := make([]int, 0, len(jobs))
	for _, j := range jobs {
		qty := int(jobQuantity(j.Quantity))
		units += qty
		if j.EstimatedPrintTimeMinutes == nil {
			continue
		}
		unitMinutes = append(unitMinutes, int(*j.EstimatedPrintTimeMinutes))
		quantities = append(quantities, qty)
	}
	if len(unitMinutes) == 0 {
		return nil, nil
	}
	total := int32(production.EstimateBatchMinutes(unitMinutes, quantities, production.DefaultBatchTimeCorrection))
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
