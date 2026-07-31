package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

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
}

func batchDTO(b gen.Batch) batchResponse {
	return batchResponse{
		ID: b.ID.String(), BatchNumber: b.BatchNumber, MachineID: uuidPtrStr(b.MachineID),
		Status: b.Status, ApprovedBy: b.ApprovedBy, ApprovedAt: db.TimePtr(b.ApprovedAt),
		MaterialShortage: b.MaterialShortage, MergedFileID: uuidPtrStr(b.MergedFileID),
		PreviewFileID: uuidPtrStr(b.PreviewFileID), UnitsPerBed: b.UnitsPerBed,
		TotalPrintTimeMinutes: b.TotalPrintTimeMinutes, EffectiveTimePerUnitMinutes: db.NumFloatPtr(b.EffectiveTimePerUnitMinutes),
		TotalFilamentGrams: db.NumFloatPtr(b.TotalFilamentGrams), BedUtilizationPercent: db.NumFloatPtr(b.BedUtilizationPercent),
		PackingStrategy: b.PackingStrategy, CreatedAt: db.Time(b.CreatedAt), UpdatedAt: db.Time(b.UpdatedAt),
	}
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
	number, err := production.NewBatchNumber()
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
	b, err := s.store.Q.UpdateBatch(ctx, params)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the batch.")
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
	c.JSON(http.StatusOK, dto)
}

type autoCreateResponse struct {
	Created     []batchResponse          `json:"created"`
	Unbatchable []production.Unbatchable `json:"unbatchable"`
}

func (s *Server) autoCreateBatches(c *gin.Context) {
	// No storage needed here: footprints come from the file_assets bbox in the DB.
	// Merging the plate STL (which needs storage) happens at preview/approve.
	ctx := c.Request.Context()
	jobs, err := s.store.Q.ListBatchableJobs(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read batchable jobs.")
		return
	}

	planJobs, err := s.planJobsFor(ctx, jobs)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the jobs' print files.")
		return
	}
	planned, unbatchable := production.Plan(planJobs)

	created := make([]batchResponse, 0, len(planned))
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		for _, p := range planned {
			number, err := production.NewBatchNumber()
			if err != nil {
				return err
			}
			shortage := !s.filamentAvailable(ctx, q, batchMaterial(p.Jobs), p.TotalFilamentGrams)
			b, err := q.InsertBatch(ctx, gen.InsertBatchParams{
				ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
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
			created = append(created, batchDTO(b))
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the batches.")
		return
	}
	c.JSON(http.StatusOK, autoCreateResponse{Created: created, Unbatchable: unbatchable})
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
	fileID, err := s.storePlate(ctx, id, "preview", batch.BatchNumber, plate.data, c)
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
	MachineID string `json:"machine_id" binding:"required"`
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
	machineID, err := uuid.Parse(req.MachineID)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The machine_id is not a valid identifier.")
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	if _, err := s.store.Q.GetMachineOps(ctx, machineID); err != nil {
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

	plate, herr := s.buildMergedPlate(ctx, jobs, batch.BatchNumber)
	if herr != nil {
		detail(c, herr.status, herr.msg)
		return
	}
	mergedID, err := s.storePlate(ctx, id, "plate", batch.BatchNumber, plate.data, c)
	if err != nil {
		return
	}

	total, eff := batchTimeFromJobs(jobs)
	filament := sumFilament(jobs)
	material := batchMaterialFromJobs(jobs)
	approvedBy := currentUserID(c)
	b, err := s.store.Q.ApproveBatch(ctx, gen.ApproveBatchParams{
		ID: id, MachineID: &machineID, ApprovedBy: &approvedBy, MergedFileID: &mergedID,
		MaterialShortage: !s.filamentAvailable(ctx, s.store.Q, material, filament),
		UnitsPerBed:      int32ptr(plate.unitsPerBed), TotalPrintTimeMinutes: total,
		EffectiveTimePerUnitMinutes: eff, TotalFilamentGrams: &filament,
		BedUtilizationPercent: &plate.utilisation,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not approve the batch.")
		return
	}
	c.JSON(http.StatusOK, batchDTO(b))
}

// --- merge orchestration ------------------------------------------------------

type plateResult struct {
	data        []byte
	unitsPerBed int
	utilisation float64
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
	data := meshio.MergeBinarySTL(batchNumber, parts)
	return plateResult{data: data, unitsPerBed: len(units), utilisation: bedpack.UtilisationPercent(units)}, nil
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

// storePlate uploads merged plate bytes and records a file asset, returning its id.
func (s *Server) storePlate(ctx context.Context, batchID uuid.UUID, kind, batchNumber string, data []byte, c *gin.Context) (uuid.UUID, error) {
	key := fmt.Sprintf("batches/%s/%s.stl", batchID, kind)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "model/stl"); err != nil {
		detail(c, http.StatusInternalServerError, "Could not store the merged plate.")
		return uuid.Nil, err
	}
	fileID := uuid.New()
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: batchNumber + "-" + kind + ".stl", ContentType: "model/stl",
		SizeBytes: int64(len(data)), StorageKey: key, UploadedBy: currentUserID(c),
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
			ID: j.ID.String(), JobNumber: j.JobNumber,
			Material: deref(j.Material), Colour: deref(j.Colour), Nozzle: deref(j.NozzleProfile),
			Quantity: int(j.Quantity), EstimatedMinutes: int32PtrToIntPtr(j.EstimatedPrintTimeMinutes),
			DueDate: db.TimePtr(j.DueDate), FilamentGrams: db.NumFloat(j.FilamentGramsRequired), Footprint: box,
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

func batchMaterialFromJobs(jobs []gen.ProductionJob) *string {
	for _, j := range jobs {
		if j.Material != nil && *j.Material != "" {
			return j.Material
		}
	}
	return nil
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

func jobIDsOf(jobs []production.PlanJob) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		if id, err := uuid.Parse(j.ID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
