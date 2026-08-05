package httpapi

import (
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// maxUploadBytes caps an STL upload. Real parts slice well under this; the cap
// keeps a runaway file from filling storage.
const maxUploadBytes = 60 << 20 // 60 MB

// designForm is the validated multipart upload: the answers plus the model file.
type designForm struct {
	Brand       string
	Name        string
	Material    string
	Finish      string
	Quality     string
	Colour      *string
	UnitsPerBed int32
	InfillPct   float64
	Machine     nozzleSpec
}

// pipelineReady fails closed with 503 when object storage or the slice enqueuer
// is not configured, so the upload routes never half-work.
func (s *Server) pipelineReady(c *gin.Context) bool {
	if s.storage == nil || s.enqueuer == nil {
		detail(c, http.StatusServiceUnavailable, "The design pipeline is not configured.")
		return false
	}
	return true
}

func (s *Server) createDesign(c *gin.Context) {
	if !s.pipelineReady(c) {
		return
	}
	ctx := c.Request.Context()
	form, fileHeader, ok := s.parseDesignForm(c)
	if !ok {
		return
	}
	exists, err := s.store.Q.BrandExists(ctx, form.Brand)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not verify the brand.")
		return
	}
	if !exists {
		detail(c, http.StatusNotFound, fmt.Sprintf("Brand '%s' is not configured yet.", form.Brand))
		return
	}
	ext, ok := validateModelFile(c, fileHeader)
	if !ok {
		return
	}

	user, _ := auth.UserFrom(c)
	designID := uuid.New()
	stlKey := fmt.Sprintf("designs/%s/model%s", designID, ext)
	if !s.uploadModel(c, stlKey, fileHeader) {
		return
	}

	row, ok := s.insertDesignAndJob(c, designID, stlKey, user.ID, form)
	if !ok {
		return
	}
	c.JSON(http.StatusCreated, designDTO(row.ID, row.BrandSlug, row.Name, row.CreatedBy, row.Status,
		row.Material, row.Colour, row.Finish, row.UnitsPerBed, row.Quality, row.InfillPct, row.Sku,
		row.CreatedAt, row.UpdatedAt))
}

// parseDesignForm reads and validates every multipart field and the file header.
func (s *Server) parseDesignForm(c *gin.Context) (designForm, *multipart.FileHeader, bool) {
	var f designForm
	f.Brand = strings.ToLower(strings.TrimSpace(c.PostForm("brand")))
	if !slugPattern.MatchString(f.Brand) {
		detail(c, http.StatusUnprocessableEntity, "A valid 'brand' is required.")
		return f, nil, false
	}
	f.Name = strings.TrimSpace(c.PostForm("name"))
	if f.Name == "" || len(f.Name) > 160 {
		detail(c, http.StatusUnprocessableEntity, "Name must be 1 to 160 characters.")
		return f, nil, false
	}
	f.Material = strings.TrimSpace(c.PostForm("material"))
	f.Finish = strings.ToLower(strings.TrimSpace(c.PostForm("finish")))
	f.Quality = strings.ToLower(strings.TrimSpace(c.PostForm("quality")))
	if !validateSpecValues(c, f.Material, f.Finish, f.Quality) {
		return f, nil, false
	}
	if colour := strings.TrimSpace(c.PostForm("colour")); colour != "" {
		f.Colour = &colour
	}
	units, ok := parseUnits(c, c.PostForm("units_per_bed"))
	if !ok {
		return f, nil, false
	}
	f.UnitsPerBed = units
	infill, ok := parseInfill(c, c.PostForm("infill_pct"))
	if !ok {
		return f, nil, false
	}
	f.InfillPct = infill
	machine, ok := parseNozzleSpec(c)
	if !ok {
		return f, nil, false
	}
	f.Machine = machine

	fileHeader, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "An STL, 3MF or STEP file is required.")
		return f, nil, false
	}
	return f, fileHeader, true
}

// validateSpecValues checks the enum-like answers against the accepted sets.
func validateSpecValues(c *gin.Context, material, finish, quality string) bool {
	if !validMaterials[material] {
		detail(c, http.StatusUnprocessableEntity, "Material must be one of PLA Basics, PA-CF.")
		return false
	}
	if !validFinishes[finish] {
		detail(c, http.StatusUnprocessableEntity, "Finish must be one of none, sanded, painted.")
		return false
	}
	if !validQualities[quality] {
		detail(c, http.StatusUnprocessableEntity,
			"Quality must be one of 0.08-high, 0.12-high, 0.16-high, 0.16-standard, "+
				"0.20-high, 0.20-standard, 0.24-standard.")
		return false
	}
	return true
}

// parseNozzleSpec reads and validates the design form's dual-nozzle slicing
// config (left/right nozzle diameter and flow), resolved to a machine_profiles
// row inside insertDesignAndJob - see design_machine_link.go.
func parseNozzleSpec(c *gin.Context) (nozzleSpec, bool) {
	left, ok := parseNozzleMM(c, c.PostForm("left_nozzle_mm"), "left_nozzle_mm")
	if !ok {
		return nozzleSpec{}, false
	}
	right, ok := parseNozzleMM(c, c.PostForm("right_nozzle_mm"), "right_nozzle_mm")
	if !ok {
		return nozzleSpec{}, false
	}
	leftFlow := strings.ToLower(strings.TrimSpace(c.PostForm("left_flow")))
	if !validFlow[leftFlow] {
		detail(c, http.StatusUnprocessableEntity, "left_flow must be one of standard, high_flow.")
		return nozzleSpec{}, false
	}
	rightFlow := strings.ToLower(strings.TrimSpace(c.PostForm("right_flow")))
	if !validFlow[rightFlow] {
		detail(c, http.StatusUnprocessableEntity, "right_flow must be one of standard, high_flow.")
		return nozzleSpec{}, false
	}
	return nozzleSpec{LeftNozzleMM: left, RightNozzleMM: right, LeftFlow: leftFlow, RightFlow: rightFlow}, true
}

func parseNozzleMM(c *gin.Context, raw, field string) (float64, bool) {
	mm, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !validNozzleMM[mm] {
		detail(c, http.StatusUnprocessableEntity, field+" must be one of 0.2, 0.4, 0.6, 0.8.")
		return 0, false
	}
	return mm, true
}

func parseUnits(c *gin.Context, raw string) (int32, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 100 {
		detail(c, http.StatusUnprocessableEntity, "Units per bed must be a whole number from 1 to 100.")
		return 0, false
	}
	return int32(n), true
}

func parseInfill(c *gin.Context, raw string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || f < 0 || f > 100 {
		detail(c, http.StatusUnprocessableEntity, "Infill must be a percentage from 0 to 100.")
		return 0, false
	}
	return f, true
}

// validateModelFile checks the extension and size, returning the lower-cased ext.
func validateModelFile(c *gin.Context, fh *multipart.FileHeader) (string, bool) {
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !allowedModelExt[ext] {
		detail(c, http.StatusUnprocessableEntity, "The file must be an STL, 3MF or STEP model.")
		return "", false
	}
	if fh.Size <= 0 {
		detail(c, http.StatusUnprocessableEntity, "The uploaded file is empty.")
		return "", false
	}
	if fh.Size > maxUploadBytes {
		detail(c, http.StatusRequestEntityTooLarge, "The model is larger than the 60 MB limit.")
		return "", false
	}
	return ext, true
}

// uploadModel streams the upload straight to object storage.
func (s *Server) uploadModel(c *gin.Context, key string, fh *multipart.FileHeader) bool {
	f, err := fh.Open()
	if err != nil {
		detail(c, http.StatusBadRequest, "Could not read the uploaded file.")
		return false
	}
	defer func() { _ = f.Close() }()
	if err := s.storage.Put(c.Request.Context(), key, f, fh.Size, "application/octet-stream"); err != nil {
		detail(c, http.StatusInternalServerError, "Could not store the uploaded model.")
		return false
	}
	return true
}

// insertDesignAndJob creates the design (queued), its first slice job, and the
// River slice job - all in one transaction. Because the River enqueue joins the
// same tx, a design can never be committed without its slice being queued (and a
// rollback leaves neither), which removes the old post-commit dual-write bug.
func (s *Server) insertDesignAndJob(
	c *gin.Context, designID uuid.UUID, stlKey, createdBy string, form designForm,
) (gen.InsertDesignRow, bool) {
	ctx := c.Request.Context()
	var row gen.InsertDesignRow
	jobID := uuid.New()
	err := s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		machineID, err := s.findOrCreateMachineProfile(ctx, q, form.Machine)
		if err != nil {
			return err
		}
		d, err := q.InsertDesign(ctx, gen.InsertDesignParams{
			ID: designID, BrandSlug: form.Brand, Name: form.Name, CreatedBy: createdBy,
			Status: designQueued, StlKey: stlKey, Material: form.Material, Colour: form.Colour,
			Finish: form.Finish, UnitsPerBed: form.UnitsPerBed, Quality: form.Quality,
			InfillPct: form.InfillPct, MachineID: &machineID,
		})
		if err != nil {
			return err
		}
		row = d
		if _, err := q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
			ID: jobID, DesignID: designID, Status: jobQueued, Attempt: 1,
		}); err != nil {
			return err
		}
		return s.enqueueSliceTx(ctx, tx, jobID, designID, stlKey, form)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the design.")
		return gen.InsertDesignRow{}, false
	}
	return row, true
}

// enqueueSliceTx inserts the River slice job on the given transaction. It carries
// everything the worker needs; the worker flips the design to "slicing" itself.
func (s *Server) enqueueSliceTx(
	ctx context.Context, tx pgx.Tx, jobID, designID uuid.UUID, stlKey string, form designForm,
) error {
	return s.enqueuer.EnqueueTx(ctx, tx, slicing.SliceArgs{
		JobID: jobID, DesignID: designID, BrandSlug: form.Brand, StlKey: stlKey,
		Material: form.Material, Quality: form.Quality,
		UnitsPerBed: int(form.UnitsPerBed), InfillPct: form.InfillPct,
	})
}

func (s *Server) resubmitDesign(c *gin.Context) {
	if !s.pipelineReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	d, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	form, ok := s.parseResubmit(c, d.BrandSlug)
	if !ok {
		return
	}
	if !s.applyResubmit(c, id, d.StlKey, form) {
		return
	}

	updated, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the design.")
		return
	}
	c.JSON(http.StatusOK, designDTO(updated.ID, updated.BrandSlug, updated.Name, updated.CreatedBy,
		updated.Status, updated.Material, updated.Colour, updated.Finish, updated.UnitsPerBed,
		updated.Quality, updated.InfillPct, updated.Sku, updated.CreatedAt, updated.UpdatedAt))
}

type resubmitRequest struct {
	Material    string  `json:"material" binding:"required"`
	Quality     string  `json:"quality" binding:"required"`
	Finish      string  `json:"finish" binding:"required"`
	Colour      *string `json:"colour"`
	UnitsPerBed int     `json:"units_per_bed" binding:"required,min=1,max=100"`
	InfillPct   float64 `json:"infill_pct" binding:"gte=0,lte=100"`
}

// parseResubmit binds the new answers (the STL is reused) and validates them.
func (s *Server) parseResubmit(c *gin.Context, brand string) (designForm, bool) {
	var req resubmitRequest
	if !bindJSON(c, &req) {
		return designForm{}, false
	}
	material := strings.TrimSpace(req.Material)
	finish := strings.ToLower(strings.TrimSpace(req.Finish))
	quality := strings.ToLower(strings.TrimSpace(req.Quality))
	if !validateSpecValues(c, material, finish, quality) {
		return designForm{}, false
	}
	form := designForm{
		Brand: brand, Material: material, Finish: finish, Quality: quality,
		UnitsPerBed: int32(req.UnitsPerBed), InfillPct: req.InfillPct,
	}
	if req.Colour != nil {
		if colour := strings.TrimSpace(*req.Colour); colour != "" {
			form.Colour = &colour
		}
	}
	return form, true
}

// applyResubmit updates the design's specs, resets it to queued, opens a new slice
// job at the next attempt number, and enqueues the River slice job - all in one
// transaction, so the re-slice is queued exactly when the design is reset.
func (s *Server) applyResubmit(c *gin.Context, id uuid.UUID, stlKey string, form designForm) bool {
	ctx := c.Request.Context()
	jobID := uuid.New()
	// The next attempt number is read inside the transaction, and a unique
	// (design_id, attempt) constraint backs it, so two concurrent resubmits of the
	// same design cannot both claim the same attempt: one commits, the other's
	// insert conflicts and rolls the whole re-slice back.
	err := s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		attempt, err := q.NextAttemptForDesign(ctx, id)
		if err != nil {
			return err
		}
		if err := q.UpdateDesignSpecs(ctx, gen.UpdateDesignSpecsParams{
			Material: form.Material, Colour: form.Colour, Finish: form.Finish,
			UnitsPerBed: form.UnitsPerBed, Quality: form.Quality, InfillPct: form.InfillPct,
			Status: designQueued, ID: id,
		}); err != nil {
			return err
		}
		if _, err := q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
			ID: jobID, DesignID: id, Status: jobQueued, Attempt: attempt,
		}); err != nil {
			return err
		}
		return s.enqueueSliceTx(ctx, tx, jobID, id, stlKey, form)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not queue the re-slice.")
		return false
	}
	return true
}
