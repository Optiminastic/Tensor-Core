package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// Machine slicing configuration handlers. This surface is Operator-visible, so it
// deliberately never reads or writes machine_hour_cost - that lives on the cost
// endpoints (ConfigManage), keeping the "Operator never sees cost" rule intact.

var validNozzles = map[float64]bool{0.2: true, 0.4: true, 0.6: true, 0.8: true}
var validFlows = map[string]bool{"standard": true, "high_flow": true}

// filamentOption is one filament a machine supports: the material label, the exact
// Bambu filament preset to load, its density (g/cm3), and whether it is the default.
type filamentOption struct {
	Material       string  `json:"material"`
	FilamentPreset string  `json:"filament_preset"`
	Density        float64 `json:"density"`
	IsDefault      bool    `json:"is_default"`
}

type machineConfigResponse struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Family             string           `json:"family"`
	NozzleMM           float64          `json:"nozzle_mm"`
	Flow               string           `json:"flow"`
	DefaultColour      *string          `json:"default_colour"`
	LayerHeightMinMM   float64          `json:"layer_height_min_mm"`
	LayerHeightMaxMM   float64          `json:"layer_height_max_mm"`
	SupportedFilaments []filamentOption `json:"supported_filaments"`
	Status             string           `json:"status"`
	IsActive           bool             `json:"is_active"`
	IsDefault          bool             `json:"is_default"`
}

func decodeFilaments(raw []byte) []filamentOption {
	out := []filamentOption{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// machineConfigDTO mirrors the shared column set of Get/List rows (identical
// fields, distinct sqlc types), so both handlers build the response the same way.
func machineConfigDTO(
	id uuid.UUID, name, family string, nozzle float64, flow string, colour *string,
	lhMin, lhMax float64, filaments []byte, status string, isActive, isDefault bool,
) machineConfigResponse {
	return machineConfigResponse{
		ID: id.String(), Name: name, Family: family, NozzleMM: nozzle, Flow: flow,
		DefaultColour: colour, LayerHeightMinMM: lhMin, LayerHeightMaxMM: lhMax,
		SupportedFilaments: decodeFilaments(filaments), Status: status,
		IsActive: isActive, IsDefault: isDefault,
	}
}

func (s *Server) listMachinesConfig(c *gin.Context) {
	rows, err := s.store.Q.ListMachinesConfig(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list machines.")
		return
	}
	out := make([]machineConfigResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, machineConfigDTO(m.ID, m.Name, m.Family, m.NozzleMm, m.Flow,
			m.DefaultColour, m.LayerHeightMinMm, m.LayerHeightMaxMm, m.SupportedFilaments,
			m.Status, m.IsActive, m.IsDefault))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getMachineConfig(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	m, err := s.store.Q.GetMachineConfig(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	c.JSON(http.StatusOK, machineConfigDTO(m.ID, m.Name, m.Family, m.NozzleMm, m.Flow,
		m.DefaultColour, m.LayerHeightMinMm, m.LayerHeightMaxMm, m.SupportedFilaments,
		m.Status, m.IsActive, m.IsDefault))
}

type machineConfigRequest struct {
	Name               string           `json:"name" binding:"required,max=120"`
	Family             string           `json:"family" binding:"required,max=16"`
	NozzleMM           float64          `json:"nozzle_mm"`
	Flow               string           `json:"flow"`
	DefaultColour      *string          `json:"default_colour"`
	LayerHeightMinMM   float64          `json:"layer_height_min_mm"`
	LayerHeightMaxMM   float64          `json:"layer_height_max_mm"`
	SupportedFilaments []filamentOption `json:"supported_filaments"`
	Status             string           `json:"status"`
	IsActive           *bool            `json:"is_active"`
	IsDefault          bool             `json:"is_default"`
}

// validateMachineConfig returns a user-facing error message, or "" when the config
// is valid. The values passed in are the effective (defaults-applied) values.
func validateMachineConfig(
	nozzle float64, flow, status string, lhMin, lhMax float64, filaments []filamentOption,
) string {
	if !validNozzles[nozzle] {
		return "Nozzle must be 0.2, 0.4, 0.6 or 0.8 mm."
	}
	if !validFlows[flow] {
		return "Flow must be standard or high_flow."
	}
	if !production.ValidMachineStatus(status) {
		return "Status must be online, busy, offline or maintenance."
	}
	if lhMin <= 0 || lhMax <= 0 || lhMin > lhMax || lhMax > 0.6 {
		return "Layer-height range is invalid (need 0 < min <= max <= 0.6)."
	}
	if len(filaments) == 0 {
		return "At least one supported filament is required."
	}
	defaults := 0
	for _, f := range filaments {
		if f.Material == "" || f.FilamentPreset == "" || f.Density <= 0 {
			return "Each filament needs a material, a preset and a positive density."
		}
		if f.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return "Only one filament can be the default."
	}
	return ""
}

func (s *Server) createMachineConfig(c *gin.Context) {
	var req machineConfigRequest
	if !bindJSON(c, &req) {
		return
	}
	flow := orDefault(req.Flow, "standard")
	status := orDefault(req.Status, "offline")
	if msg := validateMachineConfig(req.NozzleMM, flow, status, req.LayerHeightMinMM,
		req.LayerHeightMaxMM, req.SupportedFilaments); msg != "" {
		detail(c, http.StatusUnprocessableEntity, msg)
		return
	}
	filaments, _ := json.Marshal(req.SupportedFilaments)
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	m, err := s.store.Q.InsertMachineConfig(c.Request.Context(), gen.InsertMachineConfigParams{
		ID: uuid.New(), Name: req.Name, Family: req.Family, NozzleMm: req.NozzleMM, Flow: flow,
		DefaultColour: req.DefaultColour, LayerHeightMinMm: req.LayerHeightMinMM,
		LayerHeightMaxMm: req.LayerHeightMaxMM, SupportedFilaments: filaments, Status: status,
		IsActive: isActive, IsDefault: req.IsDefault,
	})
	if err != nil {
		detail(c, http.StatusConflict, "Could not create the machine (the name may already be in use).")
		return
	}
	c.JSON(http.StatusCreated, machineConfigDTO(m.ID, m.Name, m.Family, m.NozzleMm, m.Flow,
		m.DefaultColour, m.LayerHeightMinMm, m.LayerHeightMaxMm, m.SupportedFilaments,
		m.Status, m.IsActive, m.IsDefault))
}

type machineConfigPatch struct {
	Name               *string           `json:"name"`
	Family             *string           `json:"family"`
	NozzleMM           *float64          `json:"nozzle_mm"`
	Flow               *string           `json:"flow"`
	SetDefaultColour   bool              `json:"set_default_colour"`
	DefaultColour      *string           `json:"default_colour"`
	LayerHeightMinMM   *float64          `json:"layer_height_min_mm"`
	LayerHeightMaxMM   *float64          `json:"layer_height_max_mm"`
	SupportedFilaments *[]filamentOption `json:"supported_filaments"`
	Status             *string           `json:"status"`
	IsActive           *bool             `json:"is_active"`
	IsDefault          *bool             `json:"is_default"`
}

// validatePatch checks only the fields the caller actually sent.
func (p machineConfigPatch) validate() string {
	if p.NozzleMM != nil && !validNozzles[*p.NozzleMM] {
		return "Nozzle must be 0.2, 0.4, 0.6 or 0.8 mm."
	}
	if p.Flow != nil && !validFlows[*p.Flow] {
		return "Flow must be standard or high_flow."
	}
	if p.Status != nil && !production.ValidMachineStatus(*p.Status) {
		return "Status must be online, busy, offline or maintenance."
	}
	if p.SupportedFilaments != nil {
		if msg := validateMachineConfig(0.4, "standard", "offline", 0.1, 0.2, *p.SupportedFilaments); msg != "" &&
			len(*p.SupportedFilaments) > 0 {
			// reuse only the filament checks: run the shared validator with valid
			// placeholder scalars so any failure is filament-specific.
			return msg
		}
		if len(*p.SupportedFilaments) == 0 {
			return "At least one supported filament is required."
		}
	}
	return ""
}

func (s *Server) updateMachineConfig(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var p machineConfigPatch
	if !bindJSON(c, &p) {
		return
	}
	if msg := p.validate(); msg != "" {
		detail(c, http.StatusUnprocessableEntity, msg)
		return
	}
	if _, err := s.store.Q.GetMachineConfig(c.Request.Context(), id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}

	var filaments []byte
	if p.SupportedFilaments != nil {
		filaments, _ = json.Marshal(*p.SupportedFilaments)
	}
	m, err := s.store.Q.UpdateMachineConfig(c.Request.Context(), gen.UpdateMachineConfigParams{
		Name: p.Name, Family: p.Family, NozzleMm: p.NozzleMM, Flow: p.Flow,
		SetDefaultColour: p.SetDefaultColour, DefaultColour: p.DefaultColour,
		LayerHeightMinMm: p.LayerHeightMinMM, LayerHeightMaxMm: p.LayerHeightMaxMM,
		SupportedFilaments: filaments, Status: p.Status, IsActive: p.IsActive,
		IsDefault: p.IsDefault, ID: id,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the machine.")
		return
	}
	c.JSON(http.StatusOK, machineConfigDTO(m.ID, m.Name, m.Family, m.NozzleMm, m.Flow,
		m.DefaultColour, m.LayerHeightMinMm, m.LayerHeightMaxMm, m.SupportedFilaments,
		m.Status, m.IsActive, m.IsDefault))
}

func (s *Server) deleteMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if _, err := s.store.Q.GetMachineConfig(c.Request.Context(), id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	if err := s.store.Q.DeleteMachine(c.Request.Context(), id); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the machine.")
		return
	}
	c.Status(http.StatusNoContent)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
