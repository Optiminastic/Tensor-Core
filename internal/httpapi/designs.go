package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
)

// Design lifecycle statuses (the `designs.status` column).
const (
	designQueued  = "queued"
	designSlicing = "slicing"
	designPriced  = "priced"
	designFailed  = "failed"
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
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type metricsResponse struct {
	PrintTimeHr            float64 `json:"print_time_hr"`
	EffectiveMachineTimeHr float64 `json:"effective_machine_time_hr"`
	FilamentG              float64 `json:"filament_g"`
	PurgeG                 float64 `json:"purge_g"`
	SupportG               float64 `json:"support_g"`
	ColourChanges          int     `json:"colour_changes"`
	ElectricityKwh         float64 `json:"electricity_kwh"`
	UnitsPerBed            int     `json:"units_per_bed"`
	LayerHeightMm          float64 `json:"layer_height_mm"`
	InfillDensityPct       float64 `json:"infill_density_pct"`
	WallLoops              int     `json:"wall_loops"`
	SupportUsed            bool    `json:"support_used"`
	FilamentLengthMm       float64 `json:"filament_length_mm"`
	GcodeKey               string  `json:"gcode_key"`
}

type pricingResponse struct {
	DesignCP      float64         `json:"design_cp"`
	Breakdown     json.RawMessage `json:"breakdown"`
	Verdict       string          `json:"verdict"`
	CPPct         float64         `json:"cp_pct"`
	RecommendedSP *int            `json:"recommended_sp"`
	Reasons       []string        `json:"reasons"`
	Suggestions   []string        `json:"suggestions"`
}

type jobResponse struct {
	Status  string  `json:"status"`
	Attempt int     `json:"attempt"`
	Error   *string `json:"error"`
}

type designDetailResponse struct {
	designResponse
	Job     *jobResponse     `json:"job"`
	Metrics *metricsResponse `json:"metrics"`
	Pricing *pricingResponse `json:"pricing"`
}

// designDTO maps the shared design columns (identical across the insert/get/list
// rows) to the wire shape. Wide-param like brandDTO, and for the same reason.
func designDTO(
	id uuid.UUID, brandSlug, name, createdBy, status, material string, colour *string,
	finish string, unitsPerBed int32, quality string, infillPct float64,
	created, updated pgtype.Timestamptz,
) designResponse {
	return designResponse{
		ID: id.String(), BrandSlug: brandSlug, Name: name, CreatedBy: createdBy, Status: status,
		Material: material, Colour: colour, Finish: finish, UnitsPerBed: int(unitsPerBed),
		Quality: quality, InfillPct: infillPct, CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}
}

func (s *Server) registerDesigns(r *gin.Engine) {
	g := r.Group("/designs")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.DesignRead.Key()), s.listDesigns)
	g.POST("", s.guards.RequirePermission(auth.DesignCreate.Key()), s.createDesign)
	g.GET("/:id", s.guards.RequirePermission(auth.DesignRead.Key()), s.getDesign)
	g.POST("/:id/resubmit", s.guards.RequirePermission(auth.DesignCreate.Key()), s.resubmitDesign)
}

// listDesigns returns a brand's designs, newest first. The brand is a required
// query parameter so the frontend lists per brand-scoped page.
func (s *Server) listDesigns(c *gin.Context) {
	slug := c.Query("brand")
	if !slugPattern.MatchString(slug) {
		detail(c, http.StatusUnprocessableEntity, "A valid 'brand' query parameter is required.")
		return
	}
	rows, err := s.store.Q.ListDesignsByBrand(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list designs.")
		return
	}
	out := make([]designResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, designDTO(r.ID, r.BrandSlug, r.Name, r.CreatedBy, r.Status, r.Material,
			r.Colour, r.Finish, r.UnitsPerBed, r.Quality, r.InfillPct, r.CreatedAt, r.UpdatedAt))
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
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, "That design does not exist.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the design.")
		return
	}

	resp := designDetailResponse{
		designResponse: designDTO(d.ID, d.BrandSlug, d.Name, d.CreatedBy, d.Status, d.Material,
			d.Colour, d.Finish, d.UnitsPerBed, d.Quality, d.InfillPct, d.CreatedAt, d.UpdatedAt),
		Job:     s.latestJob(ctx, id),
		Metrics: s.latestMetrics(ctx, id),
		Pricing: s.designPricing(ctx, id),
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) latestJob(ctx context.Context, id uuid.UUID) *jobResponse {
	j, err := s.store.Q.GetLatestJobForDesign(ctx, id)
	if err != nil {
		return nil
	}
	return &jobResponse{Status: j.Status, Attempt: int(j.Attempt), Error: j.Error}
}

func (s *Server) latestMetrics(ctx context.Context, id uuid.UUID) *metricsResponse {
	m, err := s.store.Q.GetLatestMetricsForDesign(ctx, id)
	if err != nil {
		return nil
	}
	return &metricsResponse{
		PrintTimeHr: m.PrintTimeHr, EffectiveMachineTimeHr: m.EffectiveMachineTimeHr,
		FilamentG: m.FilamentG, PurgeG: m.PurgeG, SupportG: m.SupportG,
		ColourChanges: int(m.ColourChanges), ElectricityKwh: m.ElectricityKwh,
		UnitsPerBed: int(m.UnitsPerBed), LayerHeightMm: m.LayerHeightMm,
		InfillDensityPct: m.InfillDensityPct, WallLoops: int(m.WallLoops),
		SupportUsed: m.SupportUsed, FilamentLengthMm: m.FilamentLengthMm, GcodeKey: m.GcodeKey,
	}
}

func (s *Server) designPricing(ctx context.Context, id uuid.UUID) *pricingResponse {
	p, err := s.store.Q.GetDesignPricing(ctx, id)
	if err != nil {
		return nil
	}
	var reasons, suggestions []string
	_ = json.Unmarshal(p.Reasons, &reasons)
	_ = json.Unmarshal(p.Suggestions, &suggestions)
	var recommended *int
	if p.RecommendedSp != nil {
		v := int(*p.RecommendedSp)
		recommended = &v
	}
	return &pricingResponse{
		DesignCP: p.DesignCp, Breakdown: json.RawMessage(p.Breakdown), Verdict: p.Verdict,
		CPPct: p.CpPct, RecommendedSP: recommended, Reasons: reasons, Suggestions: suggestions,
	}
}
