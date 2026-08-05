package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// machineOpsResponse is the narrow view /machines/suggested answers with - kept
// separate from machineFullResponse so that endpoint's shape (id/name/status
// only) never has to change just because the full CRUD below grows fields.
type machineOpsResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	IsActive bool   `json:"is_active"`
}

// machineFilament is one entry in a machine's supported_filaments jsonb array.
type machineFilament struct {
	Material       string  `json:"material"`
	FilamentPreset string  `json:"filament_preset"`
	Density        float64 `json:"density"`
	IsDefault      bool    `json:"is_default"`
}

// machineFullResponse is the slicing-config view of a machine_profiles row,
// served by /machines (auth.MachineRead/MachineManage). machine_hour_cost is
// deliberately absent - that field lives on /config/machines only, gated by
// config:manage, so cost never reaches a role that shouldn't see it.
type machineFullResponse struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Family             string            `json:"family"`
	NozzleMm           float64           `json:"nozzle_mm"`
	RightNozzleMm      *float64          `json:"right_nozzle_mm"`
	Flow               string            `json:"flow"`
	RightFlow          *string           `json:"right_flow"`
	DefaultColour      *string           `json:"default_colour"`
	LayerHeightMinMm   float64           `json:"layer_height_min_mm"`
	LayerHeightMaxMm   float64           `json:"layer_height_max_mm"`
	SupportedFilaments []machineFilament `json:"supported_filaments"`
	Status             string            `json:"status"`
	IsActive           bool              `json:"is_active"`
	IsDefault          bool              `json:"is_default"`
}

func decodeFilaments(raw []byte) []machineFilament {
	var out []machineFilament
	if len(raw) == 0 {
		return []machineFilament{}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return []machineFilament{}
	}
	return out
}

func (s *Server) registerMachineOps(r *gin.Engine) {
	g := r.Group("/machines")
	g.Use(s.guards.RequireUser())
	read := s.guards.RequirePermission(auth.MachineRead.Key())
	manage := s.guards.RequirePermission(auth.MachineManage.Key())

	g.GET("", read, s.listMachineOps)
	g.GET("/suggested", read, s.suggestedMachine)
	g.GET("/profile-options", read, s.machineProfileOptions)
	g.GET("/:id", read, s.getMachineOps)
	g.POST("", manage, s.createMachineProfile)
	g.PATCH("/:id", manage, s.updateMachine)
	g.DELETE("/:id", manage, s.deleteMachine)
}

func (s *Server) listMachineOps(c *gin.Context) {
	rows, err := s.store.Q.ListMachineProfilesFull(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list machines.")
		return
	}
	out := make([]machineFullResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, machineFullResponse{
			ID: m.ID.String(), Name: m.Name, Family: m.Family, NozzleMm: m.NozzleMm,
			RightNozzleMm: db.NumFloatPtr(m.RightNozzleMm), Flow: m.Flow, RightFlow: m.RightFlow, DefaultColour: m.DefaultColour,
			LayerHeightMinMm: m.LayerHeightMinMm, LayerHeightMaxMm: m.LayerHeightMaxMm,
			SupportedFilaments: decodeFilaments(m.SupportedFilaments),
			Status:             m.Status, IsActive: m.IsActive, IsDefault: m.IsDefault,
		})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getMachineOps(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	m, err := s.store.Q.GetMachineProfileFull(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	c.JSON(http.StatusOK, machineFullResponse{
		ID: m.ID.String(), Name: m.Name, Family: m.Family, NozzleMm: m.NozzleMm,
		RightNozzleMm: db.NumFloatPtr(m.RightNozzleMm), Flow: m.Flow, RightFlow: m.RightFlow, DefaultColour: m.DefaultColour,
		LayerHeightMinMm: m.LayerHeightMinMm, LayerHeightMaxMm: m.LayerHeightMaxMm,
		SupportedFilaments: decodeFilaments(m.SupportedFilaments),
		Status:             m.Status, IsActive: m.IsActive, IsDefault: m.IsDefault,
	})
}

type machineProfileCreateRequest struct {
	Name               string            `json:"name" binding:"required,min=1,max=120"`
	Family             string            `json:"family" binding:"required,min=1,max=16"`
	NozzleMm           float64           `json:"nozzle_mm" binding:"required,gt=0"`
	RightNozzleMm      *float64          `json:"right_nozzle_mm"`
	Flow               string            `json:"flow" binding:"required"`
	RightFlow          *string           `json:"right_flow"`
	DefaultColour      *string           `json:"default_colour"`
	LayerHeightMinMm   float64           `json:"layer_height_min_mm" binding:"required,gt=0"`
	LayerHeightMaxMm   float64           `json:"layer_height_max_mm" binding:"required,gt=0"`
	SupportedFilaments []machineFilament `json:"supported_filaments" binding:"required,min=1"`
	Status             string            `json:"status"`
	IsActive           *bool             `json:"is_active"`
	IsDefault          bool              `json:"is_default"`
}

func (s *Server) createMachineProfile(c *gin.Context) {
	var req machineProfileCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	if !production.ValidMachineStatus(req.Status) {
		req.Status = production.MachineOffline
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	filaments, err := json.Marshal(req.SupportedFilaments)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Invalid supported_filaments.")
		return
	}
	m, err := s.store.Q.InsertMachineProfileFull(c.Request.Context(), gen.InsertMachineProfileFullParams{
		ID: uuid.New(), Name: req.Name, IsActive: active, Family: req.Family, NozzleMm: req.NozzleMm,
		RightNozzleMm: req.RightNozzleMm, Flow: req.Flow, RightFlow: req.RightFlow, DefaultColour: req.DefaultColour,
		LayerHeightMinMm: req.LayerHeightMinMm, LayerHeightMaxMm: req.LayerHeightMaxMm,
		SupportedFilaments: filaments, Status: req.Status, IsDefault: req.IsDefault,
	})
	if err != nil {
		if isUniqueViolation(err) {
			detail(c, http.StatusConflict, "A machine with that name already exists.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not create the machine.")
		return
	}
	c.JSON(http.StatusCreated, machineFullResponse{
		ID: m.ID.String(), Name: m.Name, Family: m.Family, NozzleMm: m.NozzleMm,
		RightNozzleMm: db.NumFloatPtr(m.RightNozzleMm), Flow: m.Flow, RightFlow: m.RightFlow, DefaultColour: m.DefaultColour,
		LayerHeightMinMm: m.LayerHeightMinMm, LayerHeightMaxMm: m.LayerHeightMaxMm,
		SupportedFilaments: decodeFilaments(m.SupportedFilaments),
		Status:             m.Status, IsActive: m.IsActive, IsDefault: m.IsDefault,
	})
}

// machineUpdateRequest mirrors the frontend's Partial<MachineInput> PATCH body -
// every field optional, only present ones change. set_right_nozzle_mm/
// set_default_colour distinguish "leave unchanged" from "clear to null" for
// the two nullable fields, matching the pattern used elsewhere in this repo
// (e.g. UpdateMaterial's set_colour).
type machineUpdateRequest struct {
	Name               string            `json:"name"`
	Family             string            `json:"family"`
	NozzleMm           *float64          `json:"nozzle_mm"`
	SetRightNozzleMm   bool              `json:"set_right_nozzle_mm"`
	RightNozzleMm      *float64          `json:"right_nozzle_mm"`
	Flow               string            `json:"flow"`
	SetRightFlow       bool              `json:"set_right_flow"`
	RightFlow          *string           `json:"right_flow"`
	SetDefaultColour   bool              `json:"set_default_colour"`
	DefaultColour      *string           `json:"default_colour"`
	LayerHeightMinMm   *float64          `json:"layer_height_min_mm"`
	LayerHeightMaxMm   *float64          `json:"layer_height_max_mm"`
	SupportedFilaments []machineFilament `json:"supported_filaments"`
	Status             string            `json:"status"`
	IsActive           *bool             `json:"is_active"`
	IsDefault          *bool             `json:"is_default"`
}

func (s *Server) updateMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req machineUpdateRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Status != "" && !production.ValidMachineStatus(req.Status) {
		detail(c, http.StatusUnprocessableEntity, "Status must be one of online, busy, offline, maintenance.")
		return
	}
	if _, err := s.store.Q.GetMachineProfileFull(c.Request.Context(), id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}

	params := gen.UpdateMachineProfileFullParams{
		ID: id, SetRightNozzleMm: req.SetRightNozzleMm, RightNozzleMm: req.RightNozzleMm,
		SetRightFlow: req.SetRightFlow, RightFlow: req.RightFlow,
		SetDefaultColour: req.SetDefaultColour, DefaultColour: req.DefaultColour,
		LayerHeightMinMm: req.LayerHeightMinMm, LayerHeightMaxMm: req.LayerHeightMaxMm,
		NozzleMm: req.NozzleMm, IsActive: req.IsActive, IsDefault: req.IsDefault,
	}
	if req.Name != "" {
		params.Name = &req.Name
	}
	if req.Family != "" {
		params.Family = &req.Family
	}
	if req.Flow != "" {
		params.Flow = &req.Flow
	}
	if req.Status != "" {
		params.Status = &req.Status
	}
	if req.SupportedFilaments != nil {
		filaments, err := json.Marshal(req.SupportedFilaments)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "Invalid supported_filaments.")
			return
		}
		params.SupportedFilaments = filaments
	}

	m, err := s.store.Q.UpdateMachineProfileFull(c.Request.Context(), params)
	if err != nil {
		if isUniqueViolation(err) {
			detail(c, http.StatusConflict, "A machine with that name already exists.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not update the machine.")
		return
	}
	c.JSON(http.StatusOK, machineFullResponse{
		ID: m.ID.String(), Name: m.Name, Family: m.Family, NozzleMm: m.NozzleMm,
		RightNozzleMm: db.NumFloatPtr(m.RightNozzleMm), Flow: m.Flow, RightFlow: m.RightFlow, DefaultColour: m.DefaultColour,
		LayerHeightMinMm: m.LayerHeightMinMm, LayerHeightMaxMm: m.LayerHeightMaxMm,
		SupportedFilaments: decodeFilaments(m.SupportedFilaments),
		Status:             m.Status, IsActive: m.IsActive, IsDefault: m.IsDefault,
	})
}

func (s *Server) deleteMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	rows, err := s.store.Q.DeleteMachineProfile(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the machine.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, "That machine does not exist.")
		return
	}
	c.Status(http.StatusNoContent)
}

// suggestedMachine returns the least-loaded online machine, or null when none is
// online.
func (s *Server) suggestedMachine(c *gin.Context) {
	m, err := s.store.Q.SuggestMachine(c.Request.Context())
	if err != nil {
		if isNoRows(err) {
			c.JSON(http.StatusOK, nil)
			return
		}
		detail(c, http.StatusInternalServerError, "Could not suggest a machine.")
		return
	}
	c.JSON(http.StatusOK, machineOpsResponse{ID: m.ID.String(), Name: m.Name, Status: m.Status, IsActive: true})
}

// machineProfileOptions answers the (family, nozzle) filament/layer-height
// catalog the admin form uses to keep a profile's fields mutually consistent.
// Backed by the bundled Bambu H2C profile set (internal/slicing/profiles.go) -
// the only family this shop's fleet runs.
func (s *Server) machineProfileOptions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"filaments":     slicing.AvailableFilaments(),
		"layer_heights": slicing.AvailableLayerHeights(),
	})
}
