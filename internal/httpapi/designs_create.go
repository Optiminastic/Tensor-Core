package httpapi

import (
	"errors"
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
}

// pipelineReady fails closed with 503 when object storage or the dispatcher is
// not configured, so the upload routes never half-work.
func (s *Server) pipelineReady(c *gin.Context) bool {
	if s.storage == nil || s.dispatcher == nil {
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

	row, jobID, ok := s.insertDesignAndJob(c, designID, stlKey, user.ID, form)
	if !ok {
		return
	}
	if !s.enqueueSlice(c, designID, jobID, stlKey, form) {
		return
	}
	c.JSON(http.StatusCreated, designDTO(row.ID, row.BrandSlug, row.Name, row.CreatedBy, row.Status,
		row.Material, row.Colour, row.Finish, row.UnitsPerBed, row.Quality, row.InfillPct,
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
	f.Material = strings.ToUpper(strings.TrimSpace(c.PostForm("material")))
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
		detail(c, http.StatusUnprocessableEntity, "Material must be one of PLA, PETG, ABS.")
		return false
	}
	if !validFinishes[finish] {
		detail(c, http.StatusUnprocessableEntity, "Finish must be one of none, sanded, painted.")
		return false
	}
	if !validQualities[quality] {
		detail(c, http.StatusUnprocessableEntity, "Quality must be one of draft, standard, fine.")
		return false
	}
	return true
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

// insertDesignAndJob creates the design (queued) and its first slice job atomically.
func (s *Server) insertDesignAndJob(
	c *gin.Context, designID uuid.UUID, stlKey, createdBy string, form designForm,
) (gen.InsertDesignRow, uuid.UUID, bool) {
	ctx := c.Request.Context()
	var row gen.InsertDesignRow
	jobID := uuid.New()
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		d, err := q.InsertDesign(ctx, gen.InsertDesignParams{
			ID: designID, BrandSlug: form.Brand, Name: form.Name, CreatedBy: createdBy,
			Status: designQueued, StlKey: stlKey, Material: form.Material, Colour: form.Colour,
			Finish: form.Finish, UnitsPerBed: form.UnitsPerBed, Quality: form.Quality,
			InfillPct: form.InfillPct,
		})
		if err != nil {
			return err
		}
		row = d
		_, err = q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
			ID: jobID, DesignID: designID, Status: jobQueued, Attempt: 1,
		})
		return err
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the design.")
		return gen.InsertDesignRow{}, uuid.Nil, false
	}
	return row, jobID, true
}

// enqueueSlice hands the job to the dispatcher and moves the design to "slicing".
// On failure it marks the job and design failed so nothing sits stuck in "queued".
func (s *Server) enqueueSlice(c *gin.Context, designID, jobID uuid.UUID, stlKey string, form designForm) bool {
	ctx := c.Request.Context()
	specs := slicing.Specs{
		Material: form.Material, Quality: form.Quality,
		UnitsPerBed: int(form.UnitsPerBed), InfillPct: form.InfillPct,
	}
	if err := s.dispatcher.Enqueue(ctx, jobID.String(), stlKey, specs); err != nil {
		s.failJob(ctx, jobID, "could not reach the slicer")
		detail(c, http.StatusBadGateway, "Could not queue the design for slicing.")
		return false
	}
	_ = s.store.Q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{Status: designSlicing, ID: designID})
	return true
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
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, "That design does not exist.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the design.")
		return
	}

	form, ok := s.parseResubmit(c, d.BrandSlug)
	if !ok {
		return
	}
	jobID, ok := s.applyResubmit(c, id, form)
	if !ok {
		return
	}
	if !s.enqueueSlice(c, id, jobID, d.StlKey, form) {
		return
	}

	updated, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the design.")
		return
	}
	c.JSON(http.StatusOK, designDTO(updated.ID, updated.BrandSlug, updated.Name, updated.CreatedBy,
		updated.Status, updated.Material, updated.Colour, updated.Finish, updated.UnitsPerBed,
		updated.Quality, updated.InfillPct, updated.CreatedAt, updated.UpdatedAt))
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
	material := strings.ToUpper(strings.TrimSpace(req.Material))
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

// applyResubmit updates the design's specs, resets it to queued, and opens a new
// slice job at the next attempt number - all atomically.
func (s *Server) applyResubmit(c *gin.Context, id uuid.UUID, form designForm) (uuid.UUID, bool) {
	ctx := c.Request.Context()
	attempt, err := s.store.Q.NextAttemptForDesign(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not prepare the re-slice.")
		return uuid.Nil, false
	}
	jobID := uuid.New()
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.UpdateDesignSpecs(ctx, gen.UpdateDesignSpecsParams{
			Material: form.Material, Colour: form.Colour, Finish: form.Finish,
			UnitsPerBed: form.UnitsPerBed, Quality: form.Quality, InfillPct: form.InfillPct,
			Status: designQueued, ID: id,
		}); err != nil {
			return err
		}
		_, err := q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
			ID: jobID, DesignID: id, Status: jobQueued, Attempt: attempt,
		})
		return err
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not queue the re-slice.")
		return uuid.Nil, false
	}
	return jobID, true
}
