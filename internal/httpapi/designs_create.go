package httpapi

import (
	"context"
	"encoding/json"
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

// maxPreviewBytes caps the preview image. allowedImageExt is the set the designer
// may upload as the design's cover.
const maxPreviewBytes = 10 << 20 // 10 MB

var allowedImageExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
}

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
	Notes       *string
	// Advanced slice overrides. Empty on upload; a re-slice can carry them.
	Settings slicing.SliceSettings
	// The machine to slice on (nil = legacy H2S 0.4) and, optionally, the exact
	// machine filament to use.
	MachineID      *uuid.UUID
	FilamentPreset string
	// Upload metadata (spec Step 1), stored as jsonb; nil when none supplied.
	Attributes []byte
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
	previewHeader, previewExt, ok := parsePreview(c)
	if !ok {
		return
	}

	user, _ := auth.UserFrom(c)
	designID := uuid.New()
	stlKey := fmt.Sprintf("designs/%s/model%s", designID, ext)
	previewKey := fmt.Sprintf("designs/%s/preview%s", designID, previewExt)
	if !s.uploadModel(c, stlKey, fileHeader) {
		return
	}
	if !s.uploadModel(c, previewKey, previewHeader) {
		return
	}

	row, ok := s.insertDesignAndJob(c, designID, stlKey, previewKey, user.ID, form)
	if !ok {
		return
	}
	c.JSON(http.StatusCreated, designDTO(row.ID, row.BrandSlug, row.Name, row.CreatedBy, row.Status,
		row.Material, row.Colour, row.Finish, row.UnitsPerBed, row.Quality, row.InfillPct,
		row.PreviewKey, row.Sku, row.CreatedAt, row.UpdatedAt))
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
	if notes := strings.TrimSpace(c.PostForm("notes")); notes != "" {
		if len(notes) > 2000 {
			detail(c, http.StatusUnprocessableEntity, "Notes must be 2000 characters or fewer.")
			return f, nil, false
		}
		f.Notes = &notes
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

	if mid := strings.TrimSpace(c.PostForm("machine_id")); mid != "" {
		id, err := uuid.Parse(mid)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "Invalid machine id.")
			return f, nil, false
		}
		f.MachineID = &id
	}
	f.FilamentPreset = strings.TrimSpace(c.PostForm("filament_preset"))
	f.Attributes = parseAttributes(c)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "An STL, 3MF or STEP file is required.")
		return f, nil, false
	}
	return f, fileHeader, true
}

// parseAttributes reads the optional upload-metadata fields (spec Step 1) into a
// jsonb blob, or nil when none were supplied. Free-form: the backend stores what
// the designer entered; costing can read it later.
func parseAttributes(c *gin.Context) []byte {
	productType := strings.TrimSpace(c.PostForm("product_type"))
	personalisationType := strings.TrimSpace(c.PostForm("personalisation_type"))
	packagingType := strings.TrimSpace(c.PostForm("packaging_type"))
	colourCount := 0
	if v := strings.TrimSpace(c.PostForm("colour_count")); v != "" {
		colourCount, _ = strconv.Atoi(v)
	}
	var addOns []string
	for _, a := range strings.Split(c.PostForm("add_ons"), ",") {
		if t := strings.TrimSpace(a); t != "" {
			addOns = append(addOns, t)
		}
	}

	attrs := map[string]any{}
	if productType != "" {
		attrs["product_type"] = productType
	}
	if personalisationType != "" {
		attrs["personalisation_type"] = personalisationType
	}
	if packagingType != "" {
		attrs["packaging_type"] = packagingType
	}
	if colourCount > 0 {
		attrs["colour_count"] = colourCount
	}
	if len(addOns) > 0 {
		attrs["add_ons"] = addOns
	}
	if len(attrs) == 0 {
		return nil
	}
	b, _ := json.Marshal(attrs)
	return b
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

// parsePreview reads and validates the required cover image, returning its header
// and lower-cased extension.
func parsePreview(c *gin.Context) (*multipart.FileHeader, string, bool) {
	fh, err := c.FormFile("preview")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "A preview image is required.")
		return nil, "", false
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !allowedImageExt[ext] {
		detail(c, http.StatusUnprocessableEntity, "The preview must be a PNG, JPG, WEBP or GIF image.")
		return nil, "", false
	}
	if fh.Size <= 0 {
		detail(c, http.StatusUnprocessableEntity, "The preview image is empty.")
		return nil, "", false
	}
	if fh.Size > maxPreviewBytes {
		detail(c, http.StatusRequestEntityTooLarge, "The preview image is larger than the 10 MB limit.")
		return nil, "", false
	}
	return fh, ext, true
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
	c *gin.Context, designID uuid.UUID, stlKey, previewKey, createdBy string, form designForm,
) (gen.InsertDesignRow, bool) {
	ctx := c.Request.Context()
	var row gen.InsertDesignRow
	jobID := uuid.New()
	err := s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		// Every design gets a permanent, system-generated SKU at creation - no one
		// types one in. The sequence guarantees the numeric suffix is unique.
		seq, err := q.NextDesignSkuSeq(ctx)
		if err != nil {
			return err
		}
		sku := generateSKU(form.Brand, form.Material, form.Colour, seq)
		d, err := q.InsertDesign(ctx, gen.InsertDesignParams{
			ID: designID, BrandSlug: form.Brand, Name: form.Name, CreatedBy: createdBy,
			Status: designQueued, StlKey: stlKey, Material: form.Material, Colour: form.Colour,
			Sku: &sku, MachineID: form.MachineID,
			Finish: form.Finish, UnitsPerBed: form.UnitsPerBed, Quality: form.Quality,
			InfillPct: form.InfillPct, Notes: form.Notes, PreviewKey: previewKey,
			Attributes: form.Attributes,
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
		Settings: form.Settings, MachineID: form.MachineID, FilamentPreset: form.FilamentPreset,
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
		updated.Quality, updated.InfillPct, updated.PreviewKey, updated.Sku, updated.CreatedAt, updated.UpdatedAt))
}

type resubmitRequest struct {
	Material    string  `json:"material" binding:"required"`
	Quality     string  `json:"quality" binding:"required"`
	Finish      string  `json:"finish" binding:"required"`
	Colour      *string `json:"colour"`
	UnitsPerBed int     `json:"units_per_bed" binding:"required,min=1,max=100"`
	InfillPct   float64 `json:"infill_pct" binding:"gte=0,lte=100"`
	// Advanced slice overrides (all optional). Clamped/allowlisted server-side by
	// SliceSettings.Normalize before they can reach the slicer CLI.
	LayerHeightMM  *float64 `json:"layer_height_mm"`
	WallLoops      *int     `json:"wall_loops"`
	InfillPattern  *string  `json:"infill_pattern"`
	Support        *bool    `json:"support"`
	SupportDeg     *int     `json:"support_threshold_deg"`
	MachineID      *string  `json:"machine_id"`
	FilamentPreset *string  `json:"filament_preset"`
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
		Settings: slicing.SliceSettings{
			LayerHeightMM: req.LayerHeightMM, WallLoops: req.WallLoops,
			InfillPattern: req.InfillPattern, Support: req.Support, SupportDeg: req.SupportDeg,
		}.Normalize(),
	}
	if req.Colour != nil {
		if colour := strings.TrimSpace(*req.Colour); colour != "" {
			form.Colour = &colour
		}
	}
	if req.MachineID != nil {
		if mid := strings.TrimSpace(*req.MachineID); mid != "" {
			id, err := uuid.Parse(mid)
			if err != nil {
				detail(c, http.StatusUnprocessableEntity, "Invalid machine id.")
				return designForm{}, false
			}
			form.MachineID = &id
		}
	}
	if req.FilamentPreset != nil {
		form.FilamentPreset = strings.TrimSpace(*req.FilamentPreset)
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
			MachineID: form.MachineID, Status: designQueued, ID: id,
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
