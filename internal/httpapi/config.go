package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

func (s *Server) registerConfig(r *gin.Engine) {
	g := r.Group("/config")
	g.Use(s.guards.RequireUser())
	read := s.guards.RequirePermission(auth.ConfigRead.Key())
	manage := s.guards.RequirePermission(auth.ConfigManage.Key())

	g.GET("/materials", read, s.listMaterials)
	g.POST("/materials", manage, s.createMaterial)
	g.GET("/materials/:id", read, s.getMaterial)
	g.PATCH("/materials/:id", manage, s.updateMaterial)

	g.GET("/machines", read, s.listMachines)
	g.POST("/machines", manage, s.createMachine)
	g.PATCH("/machines/:id", manage, s.updateMachineCost)

	g.GET("/cost-assumptions", read, s.listCostAssumptions)
	g.POST("/cost-assumptions", manage, s.createCostAssumption)
	g.GET("/cost-assumptions/:id", read, s.getCostAssumption)
	g.PATCH("/cost-assumptions/:id", manage, s.updateCostAssumption)
}

// --- materials ---------------------------------------------------------------

type materialResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	MaterialType string    `json:"material_type"`
	CostPerKg    float64   `json:"cost_per_kg"`
	Colour       *string   `json:"colour"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func materialDTO(id uuid.UUID, name, materialType string, cost float64, colour *string, active bool, created, updated pgtype.Timestamptz) materialResponse {
	return materialResponse{
		ID: id.String(), Name: name, MaterialType: materialType, CostPerKg: cost,
		Colour: colour, IsActive: active, CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}
}

func (s *Server) listMaterials(c *gin.Context) {
	rows, err := s.store.Q.ListMaterials(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list materials.")
		return
	}
	out := make([]materialResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, materialDTO(r.ID, r.Name, r.MaterialType, r.CostPerKg, r.Colour, r.IsActive, r.CreatedAt, r.UpdatedAt))
	}
	c.JSON(http.StatusOK, out)
}

type materialCreateRequest struct {
	Name         string  `json:"name" binding:"required,min=1,max=120"`
	MaterialType string  `json:"material_type" binding:"required,min=1,max=40"`
	CostPerKg    float64 `json:"cost_per_kg" binding:"required,gt=0"`
	Colour       *string `json:"colour"`
	IsActive     *bool   `json:"is_active"`
}

func (s *Server) createMaterial(c *gin.Context) {
	var req materialCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	r, err := s.store.Q.InsertMaterial(c.Request.Context(), gen.InsertMaterialParams{
		ID: uuid.New(), Name: req.Name, MaterialType: req.MaterialType,
		CostPerKg: req.CostPerKg, Colour: req.Colour, IsActive: active,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the material.")
		return
	}
	c.JSON(http.StatusCreated, materialDTO(r.ID, r.Name, r.MaterialType, r.CostPerKg, r.Colour, r.IsActive, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) getMaterial(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	r, err := s.store.Q.GetMaterial(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "MaterialProfile not found", "Could not load the material.")
		return
	}
	c.JSON(http.StatusOK, materialDTO(r.ID, r.Name, r.MaterialType, r.CostPerKg, r.Colour, r.IsActive, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) updateMaterial(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	body, ok := readBody(c)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return
	}
	params := gen.UpdateMaterialParams{ID: id}
	if !patchField(c, raw, "material_type",
		func(mt string) string {
			if len(mt) < 1 || len(mt) > 40 {
				return "Material type must be 1 to 40 characters."
			}
			return ""
		},
		func(mt string) { params.MaterialType = &mt }) {
		return
	}
	if !patchField(c, raw, "cost_per_kg",
		func(cost float64) string {
			if cost <= 0 {
				return "Cost per kg must be positive."
			}
			return ""
		},
		func(cost float64) { params.CostPerKg = &cost }) {
		return
	}
	if !patchField(c, raw, "colour", noValidation[*string], func(colour *string) {
		params.SetColour = true
		params.Colour = colour
	}) {
		return
	}
	if !patchField(c, raw, "is_active", noValidation[bool], func(active bool) { params.IsActive = &active }) {
		return
	}
	r, err := s.store.Q.UpdateMaterial(c.Request.Context(), params)
	if err != nil {
		dbError(c, err, "MaterialProfile not found", "Could not update the material.")
		return
	}
	c.JSON(http.StatusOK, materialDTO(r.ID, r.Name, r.MaterialType, r.CostPerKg, r.Colour, r.IsActive, r.CreatedAt, r.UpdatedAt))
}

// --- machines ----------------------------------------------------------------

type machineResponse struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	MachineHourCost float64   `json:"machine_hour_cost"`
	IsActive        bool      `json:"is_active"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (s *Server) listMachines(c *gin.Context) {
	rows, err := s.store.Q.ListMachines(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list machines.")
		return
	}
	out := make([]machineResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, machineResponse{
			ID: r.ID.String(), Name: r.Name, MachineHourCost: r.MachineHourCost,
			IsActive: r.IsActive, CreatedAt: db.Time(r.CreatedAt), UpdatedAt: db.Time(r.UpdatedAt),
		})
	}
	c.JSON(http.StatusOK, out)
}

type machineCreateRequest struct {
	Name            string  `json:"name" binding:"required,min=1,max=120"`
	MachineHourCost float64 `json:"machine_hour_cost" binding:"required,gt=0"`
	IsActive        *bool   `json:"is_active"`
}

func (s *Server) createMachine(c *gin.Context) {
	var req machineCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	r, err := s.store.Q.InsertMachine(c.Request.Context(), gen.InsertMachineParams{
		ID: uuid.New(), Name: req.Name, MachineHourCost: req.MachineHourCost, IsActive: active,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the machine.")
		return
	}
	c.JSON(http.StatusCreated, machineResponse{
		ID: r.ID.String(), Name: r.Name, MachineHourCost: r.MachineHourCost,
		IsActive: r.IsActive, CreatedAt: db.Time(r.CreatedAt), UpdatedAt: db.Time(r.UpdatedAt),
	})
}

type machineCostRequest struct {
	MachineHourCost float64 `json:"machine_hour_cost" binding:"gte=0"`
}

// updateMachineCost sets a machine's hourly cost. This is the ONLY machine field
// on the cost surface (config:manage); its slicing/ops config lives on /machines.
func (s *Server) updateMachineCost(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req machineCostRequest
	if !bindJSON(c, &req) {
		return
	}
	if _, err := s.store.Q.GetMachineConfig(c.Request.Context(), id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	m, err := s.store.Q.UpdateMachineCost(c.Request.Context(), gen.UpdateMachineCostParams{
		ID: id, MachineHourCost: req.MachineHourCost,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the machine cost.")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": m.ID.String(), "name": m.Name,
		"machine_hour_cost": m.MachineHourCost, "is_active": m.IsActive,
	})
}

// --- cost assumptions --------------------------------------------------------

type costAssumptionResponse struct {
	ID                     string                     `json:"id"`
	Name                   string                     `json:"name"`
	Brand                  *string                    `json:"brand"`
	FilamentCostPerKg      float64                    `json:"filament_cost_per_kg"`
	ElectricityCostPerUnit float64                    `json:"electricity_cost_per_unit"`
	MachineHourCost        float64                    `json:"machine_hour_cost"`
	FinishingLabour        float64                    `json:"finishing_labour"`
	Consumables            float64                    `json:"consumables"`
	FailurePct             float64                    `json:"failure_pct"`
	FixedCosts             pricing.FixedVariableCosts `json:"fixed_costs"`
	Margins                pricing.MarginAssumptions  `json:"margins"`
	IsDefault              bool                       `json:"is_default"`
	CreatedAt              time.Time                  `json:"created_at"`
	UpdatedAt              time.Time                  `json:"updated_at"`
}

// costAssumptionDTO maps a cost-assumption row (wide-param, since the sqlc row
// types differ per query) to the wire shape. fixedCostsJSON / marginsJSON are the
// raw json columns; the engine structs apply their own defaults for an empty '{}'.
func costAssumptionDTO(
	id uuid.UUID, name string, brand *string, filament, electricity, machine, finishing, consumables, failure float64,
	fixedCostsJSON, marginsJSON []byte, isDefault bool, created, updated pgtype.Timestamptz,
) costAssumptionResponse {
	var fixed pricing.FixedVariableCosts
	_ = json.Unmarshal(nonEmptyJSON(fixedCostsJSON), &fixed)
	var margins pricing.MarginAssumptions
	_ = json.Unmarshal(nonEmptyJSON(marginsJSON), &margins)
	return costAssumptionResponse{
		ID: id.String(), Name: name, Brand: brand,
		FilamentCostPerKg: filament, ElectricityCostPerUnit: electricity, MachineHourCost: machine,
		FinishingLabour: finishing, Consumables: consumables, FailurePct: failure,
		FixedCosts: fixed, Margins: margins,
		IsDefault: isDefault, CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}
}

func (s *Server) listCostAssumptions(c *gin.Context) {
	rows, err := s.store.Q.ListCostAssumptions(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list cost assumptions.")
		return
	}
	out := make([]costAssumptionResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, costAssumptionDTO(r.ID, r.Name, r.Brand, r.FilamentCostPerKg, r.ElectricityCostPerUnit,
			r.MachineHourCost, r.FinishingLabour, r.Consumables, r.FailurePct, r.FixedCosts, r.Margins, r.IsDefault, r.CreatedAt, r.UpdatedAt))
	}
	c.JSON(http.StatusOK, out)
}

type costAssumptionCreateRequest struct {
	Name                   string                      `json:"name" binding:"required,min=1,max=120"`
	Brand                  *string                     `json:"brand" binding:"omitempty,min=1,max=120"`
	FilamentCostPerKg      *float64                    `json:"filament_cost_per_kg" binding:"omitempty,gt=0"`
	ElectricityCostPerUnit *float64                    `json:"electricity_cost_per_unit" binding:"omitempty,gte=0"`
	MachineHourCost        *float64                    `json:"machine_hour_cost" binding:"omitempty,gte=0"`
	FinishingLabour        *float64                    `json:"finishing_labour" binding:"omitempty,gte=0"`
	Consumables            *float64                    `json:"consumables" binding:"omitempty,gte=0"`
	FailurePct             *float64                    `json:"failure_pct" binding:"omitempty,gte=0,lte=1"`
	FixedCosts             *pricing.FixedVariableCosts `json:"fixed_costs"`
	Margins                *pricing.MarginAssumptions  `json:"margins"`
	IsDefault              *bool                       `json:"is_default"`
}

func floatOr(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}

// nonEmptyJSON guards json.Unmarshal against an empty column value, treating it
// as the empty object so the engine structs apply their defaults.
func nonEmptyJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// marshalOrEmptyObject serialises v to json, or returns '{}' when v is nil or a
// nil pointer (which marshals to "null"), so the fixed_costs / margins columns
// always hold a valid object.
func marshalOrEmptyObject(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return []byte("{}")
	}
	return b
}

func (s *Server) createCostAssumption(c *gin.Context) {
	var req costAssumptionCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	isDefault := false
	if req.IsDefault != nil {
		isDefault = *req.IsDefault
	}
	if req.Margins != nil && pricing.RemainingMargin(*req.Margins, nil) <= 0 {
		detail(c, http.StatusUnprocessableEntity,
			"Margins leave no room for cost; ad spend, team, overhead and target profit must sum to under 100%.")
		return
	}
	r, err := s.store.Q.InsertCostAssumption(c.Request.Context(), gen.InsertCostAssumptionParams{
		ID: uuid.New(), Name: req.Name, Brand: req.Brand,
		FilamentCostPerKg:      floatOr(req.FilamentCostPerKg, 1000),
		ElectricityCostPerUnit: floatOr(req.ElectricityCostPerUnit, 12),
		MachineHourCost:        floatOr(req.MachineHourCost, 45),
		FinishingLabour:        floatOr(req.FinishingLabour, 30),
		Consumables:            floatOr(req.Consumables, 15),
		FailurePct:             floatOr(req.FailurePct, 0.06),
		FixedCosts:             marshalOrEmptyObject(req.FixedCosts),
		Margins:                marshalOrEmptyObject(req.Margins),
		IsDefault:              isDefault,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the cost assumption set.")
		return
	}
	c.JSON(http.StatusCreated, costAssumptionDTO(r.ID, r.Name, r.Brand, r.FilamentCostPerKg, r.ElectricityCostPerUnit,
		r.MachineHourCost, r.FinishingLabour, r.Consumables, r.FailurePct, r.FixedCosts, r.Margins, r.IsDefault, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) getCostAssumption(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	r, err := s.store.Q.GetCostAssumption(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "CostAssumptionSet not found", "Could not load the cost assumption set.")
		return
	}
	c.JSON(http.StatusOK, costAssumptionDTO(r.ID, r.Name, r.Brand, r.FilamentCostPerKg, r.ElectricityCostPerUnit,
		r.MachineHourCost, r.FinishingLabour, r.Consumables, r.FailurePct, r.FixedCosts, r.Margins, r.IsDefault, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) updateCostAssumption(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	body, ok := readBody(c)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return
	}
	params := gen.UpdateCostAssumptionParams{ID: id}
	if !patchField(c, raw, "name",
		func(name string) string {
			if len(name) < 1 || len(name) > 120 {
				return "Name must be 1 to 120 characters."
			}
			return ""
		},
		func(name string) { params.Name = &name }) {
		return
	}
	if !patchField(c, raw, "brand",
		func(brand *string) string {
			if brand != nil && (len(*brand) < 1 || len(*brand) > 120) {
				return "Brand must be 1 to 120 characters."
			}
			return ""
		},
		func(brand *string) {
			params.SetBrand = true
			params.Brand = brand
		}) {
		return
	}
	numeric := []struct {
		key    string
		dst    **float64
		floor  float64
		strict bool
	}{
		{"filament_cost_per_kg", &params.FilamentCostPerKg, 0, true},
		{"electricity_cost_per_unit", &params.ElectricityCostPerUnit, 0, false},
		{"machine_hour_cost", &params.MachineHourCost, 0, false},
		{"finishing_labour", &params.FinishingLabour, 0, false},
		{"consumables", &params.Consumables, 0, false},
	}
	for _, n := range numeric {
		if v, ok := raw[n.key]; ok {
			var f float64
			if !decodeField(c, v, &f) {
				return
			}
			if (n.strict && f <= n.floor) || (!n.strict && f < n.floor) {
				detail(c, http.StatusUnprocessableEntity, "Cost values must be non-negative.")
				return
			}
			val := f
			*n.dst = &val
		}
	}
	if !patchField(c, raw, "failure_pct",
		func(f float64) string {
			if f < 0 || f > 1 {
				return "Failure percentage must be a fraction between 0 and 1."
			}
			return ""
		},
		func(f float64) { params.FailurePct = &f }) {
		return
	}
	if !patchField(c, raw, "fixed_costs", noValidation[pricing.FixedVariableCosts],
		func(fixed pricing.FixedVariableCosts) { params.FixedCosts = marshalOrEmptyObject(fixed) }) {
		return
	}
	if !patchField(c, raw, "margins",
		func(margins pricing.MarginAssumptions) string {
			if pricing.RemainingMargin(margins, nil) <= 0 {
				return "Margins leave no room for cost; ad spend, team, overhead and target profit must sum to under 100%."
			}
			return ""
		},
		func(margins pricing.MarginAssumptions) { params.Margins = marshalOrEmptyObject(margins) }) {
		return
	}
	if !patchField(c, raw, "is_default", noValidation[bool], func(d bool) { params.IsDefault = &d }) {
		return
	}

	r, err := s.store.Q.UpdateCostAssumption(c.Request.Context(), params)
	if err != nil {
		dbError(c, err, "CostAssumptionSet not found", "Could not update the cost assumption set.")
		return
	}
	c.JSON(http.StatusOK, costAssumptionDTO(r.ID, r.Name, r.Brand, r.FilamentCostPerKg, r.ElectricityCostPerUnit,
		r.MachineHourCost, r.FinishingLabour, r.Consumables, r.FailurePct, r.FixedCosts, r.Margins, r.IsDefault, r.CreatedAt, r.UpdatedAt))
}
