package httpapi

// Tensor's own slicing profiles: a copy of each BambuBuddy pipeline's printer,
// process and filament presets, plus Tensor's overrides on top of the process.
//
// The SKU mapping (registry_pipelines.go) still names a pipeline. A profile
// mirrors that pipeline by id, so a SKU reaches its profile through the
// mapping it already has, and importing changes nothing about what prints
// until the dispatch path is switched to slice with Tensor's own worker.
//
// Import is a refresh: re-running it picks up preset changes made in
// BambuBuddy and keeps Tensor's overrides. The version only moves when a
// preset or override actually changed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// profileSummaryKeys are the process settings shown at a glance, read after
// the overrides are applied.
var profileSummaryKeys = []string{
	"layer_height", "wall_loops", "sparse_infill_density", "sparse_infill_pattern",
	"top_shell_layers", "bottom_shell_layers", "enable_support", "support_type",
}

type slicingProfileResponse struct {
	ID                 uuid.UUID      `json:"id"`
	BambuPipelineID    *int32         `json:"bambu_pipeline_id"`
	Name               string         `json:"name"`
	MachineFamily      string         `json:"machine_family"`
	PrinterPresetName  string         `json:"printer_preset_name"`
	ProcessPresetName  string         `json:"process_preset_name"`
	FilamentPresetName string         `json:"filament_preset_name"`
	Overrides          map[string]any `json:"overrides"`
	// Settings are the summary keys as this profile slices them: the
	// imported process with the overrides applied.
	Settings   map[string]any `json:"settings"`
	Version    int32          `json:"version"`
	ImportedAt *time.Time     `json:"imported_at"`
	UpdatedBy  string         `json:"updated_by"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type slicingProfileDetailResponse struct {
	slicingProfileResponse
	// Process is every process setting, overrides applied, for the advanced
	// list where any key can be overridden.
	Process map[string]any `json:"process"`
}

type importSkip struct {
	PipelineID int    `json:"pipeline_id"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
}

type importProfilesResponse struct {
	Imported []slicingProfileResponse `json:"imported"`
	Skipped  []importSkip             `json:"skipped"`
}

type putOverridesRequest struct {
	Overrides map[string]any `json:"overrides"`
}

func (s *Server) registerSlicingProfiles(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/slicing-profiles", read, s.listSlicingProfiles)
	g.GET("/slicing-profiles/:id", read, s.getSlicingProfile)
	g.POST("/slicing-profiles/import", manage, s.importSlicingProfiles)
	g.PUT("/slicing-profiles/:id/overrides", manage, s.putSlicingProfileOverrides)
}

func (s *Server) listSlicingProfiles(c *gin.Context) {
	rows, err := s.store.Q.ListSlicingProfiles(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the slicing profiles.")
		return
	}
	out := make([]slicingProfileResponse, 0, len(rows))
	for _, r := range rows {
		resp, _, err := profileResponse(r)
		if err != nil {
			detail(c, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getSlicingProfile(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	row, err := s.store.Q.GetSlicingProfile(c.Request.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		detail(c, http.StatusNotFound, "No slicing profile has that id.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the slicing profile.")
		return
	}
	resp, process, err := profileResponse(row)
	if err != nil {
		detail(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, slicingProfileDetailResponse{slicingProfileResponse: resp, Process: process})
}

func (s *Server) importSlicingProfiles(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.bambu.Configured() {
		detail(c, http.StatusBadGateway, errBambuNotConfigured.Error())
		return
	}
	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		detail(c, http.StatusBadGateway, errPipelinesUnreadable.Error())
		return
	}
	out := importProfilesResponse{Imported: []slicingProfileResponse{}, Skipped: []importSkip{}}
	user := currentUserID(c)
	for _, p := range pipelines {
		row, reason := s.importPipeline(ctx, p, user)
		if reason != "" {
			out.Skipped = append(out.Skipped, importSkip{PipelineID: p.ID, Name: p.Name, Reason: reason})
			continue
		}
		resp, _, err := profileResponse(row)
		if err != nil {
			detail(c, http.StatusInternalServerError, err.Error())
			return
		}
		out.Imported = append(out.Imported, resp)
	}
	c.JSON(http.StatusOK, out)
}

// importPipeline stores one pipeline's presets as a profile, or says why it
// cannot. A pipeline is skipped rather than failing the import, so one broken
// pipeline does not stop the rest from being copied.
func (s *Server) importPipeline(ctx context.Context, p bambubuddy.Pipeline, user string) (gen.SlicingProfile, string) {
	family := strings.ToUpper(deref(p.TargetModelClass))
	if !validMachineFamily(family) {
		return gen.SlicingProfile{}, "it is not targeted at a machine class Tensor runs (H2C, A2L or P2S)"
	}
	if p.PrinterPreset == nil || p.ProcessPreset == nil || len(p.FilamentPresets) == 0 {
		return gen.SlicingProfile{}, "it has no printer, process or filament preset"
	}
	printer, err := s.readPreset(ctx, *p.PrinterPreset, "printer")
	if err != nil {
		return gen.SlicingProfile{}, err.Error()
	}
	process, err := s.readPreset(ctx, *p.ProcessPreset, "process")
	if err != nil {
		return gen.SlicingProfile{}, err.Error()
	}
	// The same filament preset dispatch slices with today (filamentPresetOf):
	// the first. One file per slot is made from it at slice time.
	filament, err := s.readPreset(ctx, p.FilamentPresets[0], "filament")
	if err != nil {
		return gen.SlicingProfile{}, err.Error()
	}
	pipelineID := int32(p.ID)
	row, err := s.store.Q.UpsertSlicingProfileFromPipeline(ctx, gen.UpsertSlicingProfileFromPipelineParams{
		ID: uuid.New(), BambuPipelineID: &pipelineID, Name: p.Name, MachineFamily: family,
		PrinterPresetName: printer.Name, PrinterPreset: printer.raw,
		ProcessPresetName: process.Name, ProcessPreset: process.raw,
		FilamentPresetName: filament.Name, FilamentPreset: filament.raw,
		UpdatedBy: user,
	})
	if err != nil {
		return gen.SlicingProfile{}, "it could not be saved"
	}
	return row, ""
}

type readPresetResult struct {
	Name string
	raw  []byte
}

// readPreset fetches one local preset's full settings from BambuBuddy.
// GetPrinterPreset reads any local preset, whatever its kind.
func (s *Server) readPreset(ctx context.Context, p bambubuddy.PresetVal, kind string) (readPresetResult, error) {
	preset, err := s.bambu.GetPrinterPreset(ctx, p)
	if err != nil {
		return readPresetResult{}, fmt.Errorf("its %s preset could not be read: %v", kind, err)
	}
	if len(preset.Setting) == 0 {
		return readPresetResult{}, fmt.Errorf("its %s preset has no settings", kind)
	}
	raw, err := json.Marshal(preset.Setting)
	if err != nil {
		return readPresetResult{}, err
	}
	name := preset.Name
	if name == "" {
		name, _ = preset.Setting["name"].(string)
	}
	return readPresetResult{Name: name, raw: raw}, nil
}

func (s *Server) putSlicingProfileOverrides(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req putOverridesRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Overrides == nil {
		req.Overrides = map[string]any{}
	}
	ctx := c.Request.Context()
	row, err := s.store.Q.GetSlicingProfile(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		detail(c, http.StatusNotFound, "No slicing profile has that id.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the slicing profile.")
		return
	}
	var process map[string]any
	if err := json.Unmarshal(row.ProcessPreset, &process); err != nil {
		detail(c, http.StatusInternalServerError, "This profile's process preset is unreadable; import it again.")
		return
	}
	if err := slicing.ValidateOverrides(process, req.Overrides); err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	raw, err := json.Marshal(req.Overrides)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The overrides are not valid JSON.")
		return
	}
	updated, err := s.store.Q.UpdateSlicingProfileOverrides(ctx, gen.UpdateSlicingProfileOverridesParams{
		ID: id, Overrides: raw, UpdatedBy: currentUserID(c),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the overrides.")
		return
	}
	resp, _, err := profileResponse(updated)
	if err != nil {
		detail(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, resp)
}

// profileResponse decodes a stored profile and returns it with the process
// settings it slices with (overrides applied).
func profileResponse(r gen.SlicingProfile) (slicingProfileResponse, map[string]any, error) {
	var process, overrides map[string]any
	if err := json.Unmarshal(r.ProcessPreset, &process); err != nil {
		return slicingProfileResponse{}, nil, fmt.Errorf("profile %q has an unreadable process preset", r.Name)
	}
	if err := json.Unmarshal(r.Overrides, &overrides); err != nil || overrides == nil {
		overrides = map[string]any{}
	}
	for k, v := range overrides {
		process[k] = v
	}
	summary := make(map[string]any, len(profileSummaryKeys))
	for _, k := range profileSummaryKeys {
		if v, ok := process[k]; ok {
			summary[k] = v
		}
	}
	resp := slicingProfileResponse{
		ID: r.ID, BambuPipelineID: r.BambuPipelineID, Name: r.Name, MachineFamily: r.MachineFamily,
		PrinterPresetName: r.PrinterPresetName, ProcessPresetName: r.ProcessPresetName,
		FilamentPresetName: r.FilamentPresetName, Overrides: overrides, Settings: summary,
		Version: r.Version, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.ImportedAt.Valid {
		t := r.ImportedAt.Time
		resp.ImportedAt = &t
	}
	return resp, process, nil
}
