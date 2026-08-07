package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/failurerisk"
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// Design lifecycle statuses (the `designs.status` column).
const (
	designQueued           = "queued"
	designSlicing          = "slicing"
	designPriced           = "priced"
	designFailed           = "failed"
	designSubmitted        = "submitted"
	designChangesRequested = "changes_requested"
	designApproved         = "approved"
	designPublished        = "published"
)

// Review event kinds recorded on the design_reviews thread.
const (
	reviewComment = "comment"
	reviewSubmit  = "submit"
	reviewApprove = "approve"
	reviewReject  = "reject"
)

// verdictGreen / verdictYellow are the submittable pre-check verdicts; a Red
// design must be revised before it can go to the Project Lead.
const (
	verdictGreen  = "green"
	verdictYellow = "yellow"
)

// Slice job statuses (the `slice_jobs.status` column).
const (
	jobQueued  = "queued"
	jobRunning = "running"
	jobDone    = "done"
	jobFailed  = "failed"
)

// Accepted answers. Materials/qualities must map to an H2S profile in the worker
// (internal/../profiles.py); finishes are cosmetic and advisory only.
var (
	validMaterials  = map[string]bool{"PLA": true, "PETG": true, "ABS": true}
	validQualities  = map[string]bool{"draft": true, "standard": true, "fine": true}
	validFinishes   = map[string]bool{"none": true, "sanded": true, "painted": true}
	allowedModelExt = map[string]bool{".stl": true, ".3mf": true, ".step": true, ".stp": true}
)

type designResponse struct {
	ID          string    `json:"id"`
	BrandSlug   string    `json:"brand_slug"`
	Name        string    `json:"name"`
	CreatedBy   string    `json:"created_by"`
	Status      string    `json:"status"`
	Material    string    `json:"material"`
	Colour      *string   `json:"colour"`
	Finish      string    `json:"finish"`
	UnitsPerBed int       `json:"units_per_bed"`
	Quality     string    `json:"quality"`
	InfillPct   float64   `json:"infill_pct"`
	SKU         *string   `json:"sku"`
	HasPreview  bool      `json:"has_preview"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type metricsResponse struct {
	PrintTimeHr            float64         `json:"print_time_hr"`
	EffectiveMachineTimeHr float64         `json:"effective_machine_time_hr"`
	FilamentG              float64         `json:"filament_g"`
	PurgeG                 float64         `json:"purge_g"`
	SupportG               float64         `json:"support_g"`
	ColourChanges          int             `json:"colour_changes"`
	ElectricityKwh         float64         `json:"electricity_kwh"`
	UnitsPerBed            int             `json:"units_per_bed"`
	LayerHeightMm          float64         `json:"layer_height_mm"`
	InfillDensityPct       float64         `json:"infill_density_pct"`
	WallLoops              int             `json:"wall_loops"`
	SupportUsed            bool            `json:"support_used"`
	FilamentLengthMm       float64         `json:"filament_length_mm"`
	GcodeKey               string          `json:"gcode_key"`
	Orientation            json.RawMessage `json:"orientation"`
}

type pricingResponse struct {
	DesignCP           float64         `json:"design_cp"`
	Breakdown          json.RawMessage `json:"breakdown"`
	Verdict            string          `json:"verdict"`
	CPPct              float64         `json:"cp_pct"`
	RecommendedSP      *int            `json:"recommended_sp"`
	RawSP              float64         `json:"raw_sp"`
	CPPctAtRecommended *float64        `json:"cp_pct_at_recommended"`
	PassesNormal       bool            `json:"passes_normal"`
	SurvivesStress     bool            `json:"survives_stress"`
	SPWarnings         []string        `json:"sp_warnings"`
	ApprovedSP         *int            `json:"approved_sp"`
	Reasons            []string        `json:"reasons"`
	Suggestions        []string        `json:"suggestions"`
}

// shopifyBlock is the reference to the design's Shopify draft product, present
// once it has been published.
type shopifyBlock struct {
	Status   string `json:"status"`
	Handle   string `json:"handle"`
	AdminURL string `json:"admin_url"`
}

type jobResponse struct {
	Status  string  `json:"status"`
	Attempt int     `json:"attempt"`
	Error   *string `json:"error"`
}

type designDetailResponse struct {
	designResponse
	Notes                *string                `json:"notes"`
	Job                  *jobResponse           `json:"job"`
	Metrics              *metricsResponse       `json:"metrics"`
	Pricing              *pricingResponse       `json:"pricing"`
	Shopify              *shopifyBlock          `json:"shopify"`
	Machine              *machineConfigResponse `json:"machine"`
	PersonalisationRules json.RawMessage        `json:"personalisation_rules"`
	// Upload metadata (product type, personalisation type, colour count, add-ons,
	// packaging type); null when none was supplied.
	Attributes json.RawMessage `json:"attributes"`
	// The applied personalisation spec (name + placement + colour); null when the
	// design has none, so the editor can rehydrate the saved state.
	Personalisation json.RawMessage `json:"personalisation"`
	// Advisory print-reliability score derived from the metrics; nil until sliced.
	FailureRisk *failurerisk.Assessment `json:"failure_risk"`
}

// designDTO maps the shared design columns (identical across the insert/get/list
// rows) to the wire shape. Wide-param like brandDTO, and for the same reason.
func designDTO(
	id uuid.UUID, brandSlug, name, createdBy, status, material string, colour *string,
	finish string, unitsPerBed int32, quality string, infillPct float64, previewKey string,
	sku *string, created, updated pgtype.Timestamptz,
) designResponse {
	return designResponse{
		ID: id.String(), BrandSlug: brandSlug, Name: name, CreatedBy: createdBy, Status: status,
		Material: material, Colour: colour, Finish: finish, UnitsPerBed: int(unitsPerBed),
		Quality: quality, InfillPct: infillPct, SKU: sku, HasPreview: previewKey != "",
		CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}
}

func (s *Server) registerDesigns(r *gin.Engine) {
	g := r.Group("/designs")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.DesignRead.Key()), s.listDesigns)
	g.POST("", s.guards.RequirePermission(auth.DesignCreate.Key()), s.createDesign)

	// Every /:id route also passes the brand-access gate, so a member can only
	// reach designs in a brand an admin assigned them (a 404 otherwise). Admins
	// (brand:manage) pass through. The gate runs after RequireUser (inherited).
	id := g.Group("/:id")
	id.Use(s.requireDesignBrandAccess)
	id.GET("", s.guards.RequirePermission(auth.DesignRead.Key()), s.getDesign)
	id.DELETE("", s.guards.RequirePermission(auth.DesignDelete.Key()), s.deleteDesign)
	id.GET("/model", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadModel)
	id.GET("/model-lite", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadModelLite)
	id.GET("/personalise-preview", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadPersonalisePreview)
	id.GET("/personalise-text", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadPersonaliseText)
	id.PATCH("/personalisation", s.guards.RequirePermission(auth.DesignUpdate.Key()), s.setDesignPersonalisation)
	id.POST("/optimize", s.guards.RequirePermission(auth.DesignRead.Key()), s.optimizeDesign)
	id.GET("/report.pdf", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadDesignReport)
	id.POST("/email-report", s.guards.RequirePermission(auth.DesignRead.Key()), s.emailDesignReport)
	id.GET("/gcode", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadGcode)
	id.GET("/gcode/plate", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadGcodePlate)
	id.GET("/preview", s.guards.RequirePermission(auth.DesignRead.Key()), s.downloadPreview)
	id.POST("/resubmit", s.guards.RequirePermission(auth.DesignCreate.Key()), s.resubmitDesign)
	id.POST("/submit", s.guards.RequirePermission(auth.DesignSubmit.Key()), s.submitDesign)
	id.POST("/approve", s.guards.RequirePermission(auth.DesignApprove.Key()), s.approveDesign)
	id.POST("/reject", s.guards.RequirePermission(auth.DesignReject.Key()), s.rejectDesign)
	id.POST("/comments", s.guards.RequirePermission(auth.DesignRead.Key()), s.commentOnDesign)
	id.GET("/reviews", s.guards.RequirePermission(auth.DesignRead.Key()), s.listDesignReviews)
	id.POST("/publish-shopify", s.guards.RequirePermission(auth.ShopifyPublish.Key()), s.publishDesignToShopify)
	id.PATCH("/sku", s.guards.RequirePermission(auth.ShopifyPublish.Key()), s.setDesignSku)
	id.PATCH("/personalisation-rules",
		s.guards.RequirePermission(auth.ShopifyPublish.Key()), s.setDesignPersonalisationRules)
	id.POST("/personalisation-estimate",
		s.guards.RequirePermission(auth.DesignRead.Key()), s.estimatePersonalisation)
}

// requireDesignBrandAccess is the /:id gate: it resolves the design's brand and
// denies (404, matching a missing design) when the caller may not access it. A
// missing/invalid id or unknown design is left to the handler's own load, which
// produces the same 404, so the gate only blocks the cross-brand case.
func (s *Server) requireDesignBrandAccess(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.Next()
		return
	}
	slug, err := s.store.Q.GetDesignBrandSlug(c.Request.Context(), id)
	if err != nil {
		c.Next()
		return
	}
	user, ok := auth.UserFrom(c)
	if !ok || !s.canAccessBrand(c.Request.Context(), user, slug) {
		detail(c, http.StatusNotFound, "That design does not exist.")
		c.Abort()
		return
	}
	c.Next()
}

// listDesigns returns a brand's designs, newest first. The brand is a required
// query parameter so the frontend lists per brand-scoped page.
func (s *Server) listDesigns(c *gin.Context) {
	slug := c.Query("brand")
	if !slugPattern.MatchString(slug) {
		detail(c, http.StatusUnprocessableEntity, "A valid 'brand' query parameter is required.")
		return
	}
	if !s.requireBrandAccess(c, slug) {
		return
	}
	ctx := c.Request.Context()
	page, ok := parsePageParams(c)
	if !ok {
		return
	}

	// Default (no ?limit): the full list, unchanged.
	if !page.paginate {
		rows, err := s.store.Q.ListDesignsByBrand(ctx, slug)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list designs.")
			return
		}
		out := make([]designResponse, 0, len(rows))
		for _, r := range rows {
			out = append(out, designDTO(r.ID, r.BrandSlug, r.Name, r.CreatedBy, r.Status, r.Material,
				r.Colour, r.Finish, r.UnitsPerBed, r.Quality, r.InfillPct, r.PreviewKey, r.Sku, r.CreatedAt, r.UpdatedAt))
		}
		c.JSON(http.StatusOK, out)
		return
	}

	// Paginated: one keyset page, same body shape, next cursor in the header.
	rows, err := s.store.Q.ListDesignsByBrandPage(ctx, gen.ListDesignsByBrandPageParams{
		BrandSlug: slug, CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list designs.")
		return
	}
	out := make([]designResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, designDTO(r.ID, r.BrandSlug, r.Name, r.CreatedBy, r.Status, r.Material,
			r.Colour, r.Finish, r.UnitsPerBed, r.Quality, r.InfillPct, r.PreviewKey, r.Sku, r.CreatedAt, r.UpdatedAt))
	}
	if n := len(rows); n > 0 {
		last := rows[n-1]
		setNextCursor(c, n, page.limit, last.CreatedAt.Time, last.ID)
	}
	c.JSON(http.StatusOK, out)
}

// getDesign returns a design with its latest job, metrics and pricing so the
// frontend can poll one endpoint through the whole slice -> price loop.
func (s *Server) getDesign(c *gin.Context) {
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

	// Each sub-resource may legitimately not exist yet (the frontend polls this
	// endpoint through the slice -> price loop), but a real database error must
	// surface as a 5xx rather than being rendered as an absent field.
	job, err := s.latestJob(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	metrics, err := s.latestMetrics(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	pricing, err := s.designPricing(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	shopifyBlk, err := s.designShopify(ctx, id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	machine, err := s.designMachine(ctx, d.MachineID)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}

	resp := designDetailResponse{
		designResponse: designDTO(d.ID, d.BrandSlug, d.Name, d.CreatedBy, d.Status, d.Material,
			d.Colour, d.Finish, d.UnitsPerBed, d.Quality, d.InfillPct, d.PreviewKey, d.Sku, d.CreatedAt, d.UpdatedAt),
		Notes:                d.Notes,
		Job:                  job,
		Metrics:              metrics,
		Pricing:              pricing,
		Shopify:              shopifyBlk,
		Machine:              machine,
		PersonalisationRules: json.RawMessage(d.PersonalisationRules),
		Attributes:           json.RawMessage(d.Attributes),
		Personalisation:      json.RawMessage(d.Personalisation),
		FailureRisk:          failureRiskFor(metrics),
	}
	c.JSON(http.StatusOK, resp)
}

// deleteDesign permanently removes a design and all its child rows (the FKs
// cascade). Guarded by design:delete and the /:id brand-access gate. Stored model
// / preview / g-code objects are left in object storage (harmless orphans); the
// catalog record is what the UI and pipeline key off. Does not touch Shopify.
func (s *Server) deleteDesign(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	rows, err := s.store.Q.DeleteDesign(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the design.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, "That design does not exist.")
		return
	}
	c.Status(http.StatusNoContent)
}

// failureRiskFor scores print reliability from the latest metrics (nil when the
// design has not been sliced). The overhang signal comes from the stored
// orientation recommendation.
func failureRiskFor(m *metricsResponse) *failurerisk.Assessment {
	if m == nil {
		return nil
	}
	a := failurerisk.Assess(failurerisk.Inputs{
		FilamentG:           m.FilamentG,
		SupportG:            m.SupportG,
		SupportUsed:         m.SupportUsed,
		WallLoops:           m.WallLoops,
		PurgeG:              m.PurgeG,
		ColourChanges:       m.ColourChanges,
		OverhangBaselineMM2: overhangBaseline(m.Orientation),
	})
	return &a
}

// overhangBaseline pulls the baseline overhang area from the orientation
// recommendation JSON, or 0 when absent.
func overhangBaseline(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var o struct {
		OverhangBaseline float64 `json:"overhang_area_baseline"`
	}
	_ = json.Unmarshal(raw, &o)
	return o.OverhangBaseline
}

// designMachine resolves the machine profile a design slices on, or nil when the
// design has no machine set (it uses the built-in default) or the chosen machine
// has since been deleted.
func (s *Server) designMachine(ctx context.Context, machineID *uuid.UUID) (*machineConfigResponse, error) {
	if machineID == nil {
		return nil, nil
	}
	m, err := s.store.Q.GetMachineConfig(ctx, *machineID)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	dto := machineConfigDTO(m.ID, m.Name, m.Family, m.NozzleMm, m.Flow, m.DefaultColour,
		m.LayerHeightMinMm, m.LayerHeightMaxMm, m.SupportedFilaments, m.Status, m.IsActive, m.IsDefault)
	return &dto, nil
}

// downloadModel streams the original model a designer uploaded (STL/3MF/STEP).
// This is the file to open in a slicer - unlike the G-code archive, which is a
// costing artifact sliced with the H2S profile and not meant to be re-opened.
func (s *Server) downloadModel(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	if d.StlKey == "" {
		detail(c, http.StatusNotFound, "No model file is available for this design.")
		return
	}
	// Keep the uploaded model's own extension so it opens as what it is.
	filename := safeName(d.Name) + strings.ToLower(filepath.Ext(d.StlKey))
	s.streamObject(c, d.StlKey, filename)
}

// downloadGcode streams the sliced G-code archive for a design's latest slice.
// The worker exports a Bambu 3MF project carrying the G-code and stores it in
// object storage; this hands those exact bytes back as an attachment download.
func (s *Server) downloadGcode(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	m, err := s.store.Q.GetLatestMetricsForDesign(c.Request.Context(), d.ID)
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "No G-code is available for this design yet.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the design's G-code.")
		return
	}
	if m.GcodeKey == "" {
		detail(c, http.StatusNotFound, "No G-code is available for this design yet.")
		return
	}
	s.streamObject(c, m.GcodeKey, safeName(d.Name)+".gcode.3mf")
}

// downloadGcodePlate streams the plate G-code as plain text, extracted from the
// sliced Bambu 3MF project. The browser's layer preview renders from this, so it
// does not have to unzip the archive itself.
func (s *Server) downloadGcodePlate(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	m, err := s.store.Q.GetLatestMetricsForDesign(c.Request.Context(), d.ID)
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "No G-code is available for this design yet.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the design's G-code.")
		return
	}
	if m.GcodeKey == "" {
		detail(c, http.StatusNotFound, "No G-code is available for this design yet.")
		return
	}

	plate, err := s.extractPlateGcode(c.Request.Context(), m.GcodeKey)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "That G-code is no longer available.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the G-code.")
		return
	}
	if plate == "" {
		detail(c, http.StatusNotFound, "This slice has no plate G-code.")
		return
	}

	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=300")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(plate))
}

// extractPlateGcode downloads the sliced .3mf for key to a temp file and returns
// its plate G-code text, reusing the worker's archive reader. The temp file is
// always removed before returning.
func (s *Server) extractPlateGcode(ctx context.Context, key string) (string, error) {
	tmp, err := os.CreateTemp("", "plate-*.gcode.3mf")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := s.storage.Download(ctx, key, tmpPath); err != nil {
		return "", err
	}
	return slicing.LoadPlateGcode(tmpPath)
}

// designForDownload runs the shared pre-flight for the file downloads: pipeline
// readiness, id parsing, and loading the design. It writes the error response
// and returns ok=false when the caller should stop.
func (s *Server) designForDownload(c *gin.Context) (gen.GetDesignByIDRow, bool) {
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "The design pipeline is not configured.")
		return gen.GetDesignByIDRow{}, false
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return gen.GetDesignByIDRow{}, false
	}
	d, err := s.store.Q.GetDesignByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return gen.GetDesignByIDRow{}, false
	}
	return d, true
}

// streamObject fetches an object and writes it to the response as a named
// attachment download. A missing object answers 404 rather than 500.
func (s *Server) streamObject(c *gin.Context, key, filename string) {
	obj, err := s.storage.Get(c.Request.Context(), key)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "That file is no longer available.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the file.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	disposition := fmt.Sprintf("attachment; filename=%q", filename)
	c.DataFromReader(http.StatusOK, obj.Size, "application/octet-stream", obj.Body,
		map[string]string{"Content-Disposition": disposition})
}

// downloadPreview streams a design's cover image inline (so an <img> renders it),
// unlike the model/gcode attachment downloads.
func (s *Server) downloadPreview(c *gin.Context) {
	d, ok := s.designForDownload(c)
	if !ok {
		return
	}
	if d.PreviewKey == "" {
		detail(c, http.StatusNotFound, "This design has no preview image.")
		return
	}
	s.streamInline(c, d.PreviewKey)
}

// streamInline serves an object inline with a content type inferred from its key,
// for images shown in the browser rather than downloaded.
func (s *Server) streamInline(c *gin.Context, key string) {
	obj, err := s.storage.Get(c.Request.Context(), key)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "That image is no longer available.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the image.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	ctype := mime.TypeByExtension(filepath.Ext(key))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	c.DataFromReader(http.StatusOK, obj.Size, ctype, obj.Body, map[string]string{
		"Content-Disposition": "inline",
		"Cache-Control":       "private, max-age=300",
	})
}

// safeName reduces a design name to filename-safe ASCII for downloads.
func safeName(name string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r == ' ':
			return '-'
		default:
			return -1
		}
	}, name)
	if safe == "" {
		return "design"
	}
	return safe
}

// latestJob returns the design's most recent slice job, or (nil, nil) when none
// exists yet. A real database error is returned so the caller surfaces a 5xx
// instead of rendering the field as absent.
func (s *Server) latestJob(ctx context.Context, id uuid.UUID) (*jobResponse, error) {
	j, err := s.store.Q.GetLatestJobForDesign(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &jobResponse{Status: j.Status, Attempt: int(j.Attempt), Error: j.Error}, nil
}

func (s *Server) latestMetrics(ctx context.Context, id uuid.UUID) (*metricsResponse, error) {
	m, err := s.store.Q.GetLatestMetricsForDesign(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &metricsResponse{
		PrintTimeHr: m.PrintTimeHr, EffectiveMachineTimeHr: m.EffectiveMachineTimeHr,
		FilamentG: m.FilamentG, PurgeG: m.PurgeG, SupportG: m.SupportG,
		ColourChanges: int(m.ColourChanges), ElectricityKwh: m.ElectricityKwh,
		UnitsPerBed: int(m.UnitsPerBed), LayerHeightMm: m.LayerHeightMm,
		InfillDensityPct: m.InfillDensityPct, WallLoops: int(m.WallLoops),
		SupportUsed: m.SupportUsed, FilamentLengthMm: m.FilamentLengthMm, GcodeKey: m.GcodeKey,
		// Stored as JSON (or NULL); a nil RawMessage marshals to `null`.
		Orientation: json.RawMessage(m.Orientation),
	}, nil
}

func (s *Server) designPricing(ctx context.Context, id uuid.UUID) (*pricingResponse, error) {
	p, err := s.store.Q.GetDesignPricing(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	var reasons, suggestions, warnings []string
	_ = json.Unmarshal(p.Reasons, &reasons)
	_ = json.Unmarshal(p.Suggestions, &suggestions)
	_ = json.Unmarshal(p.SpWarnings, &warnings)
	var recommended *int
	var cpPctAtRec *float64
	if p.RecommendedSp != nil {
		v := int(*p.RecommendedSp)
		recommended = &v
		c := p.CpPctAtRecommended
		cpPctAtRec = &c
	}
	var approved *int
	if p.ApprovedSp != nil {
		v := int(*p.ApprovedSp)
		approved = &v
	}
	return &pricingResponse{
		DesignCP: p.DesignCp, Breakdown: json.RawMessage(p.Breakdown), Verdict: p.Verdict,
		CPPct: p.CpPct, RecommendedSP: recommended, RawSP: p.RawSp,
		CPPctAtRecommended: cpPctAtRec, PassesNormal: p.PassesNormal,
		SurvivesStress: p.SurvivesStress, SPWarnings: warnings, ApprovedSP: approved,
		Reasons: reasons, Suggestions: suggestions,
	}, nil
}

func (s *Server) designShopify(ctx context.Context, id uuid.UUID) (*shopifyBlock, error) {
	p, err := s.store.Q.GetShopifyProduct(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &shopifyBlock{Status: p.Status, Handle: p.Handle, AdminURL: p.AdminUrl}, nil
}
