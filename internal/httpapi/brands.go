package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

type brandResponse struct {
	ID                string    `json:"id"`
	Key               string    `json:"key"`
	Name              string    `json:"name"`
	StartingPrice     float64   `json:"starting_price"`
	ShopifyURL        *string   `json:"shopify_url"`
	Description       *string   `json:"description"`
	IsActive          bool      `json:"is_active"`
	Ladder            []int     `json:"ladder"`
	CPGreenMax        float64   `json:"cp_green_max"`
	CPYellowMax       float64   `json:"cp_yellow_max"`
	EntryMachineHours *float64  `json:"entry_machine_hours"`
	EntryRung         *int      `json:"entry_rung"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func brandDTO(
	id uuid.UUID, key, name string, startingPrice float64, shopifyURL, description *string,
	isActive bool, ladderRaw []byte, green, yellow float64, emh pgtype.Numeric,
	entryRung *int32, created, updated pgtype.Timestamptz,
) (brandResponse, error) {
	var ladder []int
	if err := json.Unmarshal(ladderRaw, &ladder); err != nil {
		return brandResponse{}, err
	}
	var er *int
	if entryRung != nil {
		v := int(*entryRung)
		er = &v
	}
	return brandResponse{
		ID: id.String(), Key: key, Name: name, StartingPrice: startingPrice,
		ShopifyURL: shopifyURL, Description: description, IsActive: isActive, Ladder: ladder,
		CPGreenMax: green, CPYellowMax: yellow,
		EntryMachineHours: db.NumFloatPtr(emh), EntryRung: er,
		CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}, nil
}

func (s *Server) registerBrands(r *gin.Engine) {
	g := r.Group("/brands")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.BrandRead.Key()), s.listBrands)
	g.GET("/:key", s.guards.RequirePermission(auth.BrandRead.Key()), s.getBrand)
	g.PATCH("/:key", s.guards.RequirePermission(auth.BrandManage.Key()), s.updateBrand)
}

func notConfigured(key pricing.Brand) string {
	return fmt.Sprintf("Brand '%s' is not configured yet.", key)
}

func (s *Server) listBrands(c *gin.Context) {
	rows, err := s.store.Q.ListBrands(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list brands.")
		return
	}
	out := make([]brandResponse, 0, len(rows))
	for _, r := range rows {
		dto, err := brandDTO(r.ID, r.Key, r.Name, r.StartingPrice, r.ShopifyUrl, r.Description,
			r.IsActive, r.Ladder, r.CpGreenMax, r.CpYellowMax, r.EntryMachineHours, r.EntryRung,
			r.CreatedAt, r.UpdatedAt)
		if err != nil {
			detail(c, http.StatusInternalServerError, "A brand ladder is corrupt.")
			return
		}
		out = append(out, dto)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getBrand(c *gin.Context) {
	key, ok := brandKeyParam(c)
	if !ok {
		return
	}
	r, err := s.store.Q.GetBrandByKey(c.Request.Context(), string(key))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, notConfigured(key))
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the brand.")
		return
	}
	dto, err := brandDTO(r.ID, r.Key, r.Name, r.StartingPrice, r.ShopifyUrl, r.Description,
		r.IsActive, r.Ladder, r.CpGreenMax, r.CpYellowMax, r.EntryMachineHours, r.EntryRung,
		r.CreatedAt, r.UpdatedAt)
	if err != nil {
		detail(c, http.StatusInternalServerError, "The brand ladder is corrupt.")
		return
	}
	c.JSON(http.StatusOK, dto)
}

func (s *Server) updateBrand(c *gin.Context) {
	key, ok := brandKeyParam(c)
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

	current, err := s.store.Q.GetBrandByKey(c.Request.Context(), string(key))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, notConfigured(key))
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the brand.")
		return
	}

	params := gen.UpdateBrandParams{Key: string(key)}
	if !s.applyBrandUpdate(c, raw, &params) {
		return
	}

	// green must be at or below yellow, using the effective values.
	effGreen, effYellow := current.CpGreenMax, current.CpYellowMax
	if params.CpGreenMax != nil {
		effGreen = *params.CpGreenMax
	}
	if params.CpYellowMax != nil {
		effYellow = *params.CpYellowMax
	}
	if effGreen > effYellow {
		detail(c, http.StatusBadRequest, "The green threshold must be at or below the yellow threshold.")
		return
	}

	r, err := s.store.Q.UpdateBrand(c.Request.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, notConfigured(key))
			return
		}
		detail(c, http.StatusInternalServerError, "Could not update the brand.")
		return
	}
	dto, err := brandDTO(r.ID, r.Key, r.Name, r.StartingPrice, r.ShopifyUrl, r.Description,
		r.IsActive, r.Ladder, r.CpGreenMax, r.CpYellowMax, r.EntryMachineHours, r.EntryRung,
		r.CreatedAt, r.UpdatedAt)
	if err != nil {
		detail(c, http.StatusInternalServerError, "The brand ladder is corrupt.")
		return
	}
	c.JSON(http.StatusOK, dto)
}

// applyBrandUpdate fills params from the raw fields that were present, validating
// each. Presence is what drives the set-flags, so a field sent as null clears it
// while an absent field is left unchanged. Returns false after writing an error.
func (s *Server) applyBrandUpdate(c *gin.Context, raw map[string]json.RawMessage, params *gen.UpdateBrandParams) bool {
	if v, ok := raw["name"]; ok {
		var name string
		if !decodeField(c, v, &name) {
			return false
		}
		if len(name) < 1 || len(name) > 120 {
			detail(c, http.StatusUnprocessableEntity, "Name must be 1 to 120 characters.")
			return false
		}
		params.Name = &name
	}
	if v, ok := raw["starting_price"]; ok {
		var price float64
		if !decodeField(c, v, &price) {
			return false
		}
		if price <= 0 {
			detail(c, http.StatusUnprocessableEntity, "Starting price must be positive.")
			return false
		}
		params.StartingPrice = &price
	}
	if v, ok := raw["shopify_url"]; ok {
		var url *string
		if !decodeField(c, v, &url) {
			return false
		}
		params.SetShopifyUrl = true
		params.ShopifyUrl = url
	}
	if v, ok := raw["description"]; ok {
		var desc *string
		if !decodeField(c, v, &desc) {
			return false
		}
		params.SetDescription = true
		params.Description = desc
	}
	if v, ok := raw["is_active"]; ok {
		var active bool
		if !decodeField(c, v, &active) {
			return false
		}
		params.IsActive = &active
	}
	if v, ok := raw["ladder"]; ok {
		ladderJSON, ok := validateLadder(c, v)
		if !ok {
			return false
		}
		params.Ladder = ladderJSON
	}
	if v, ok := raw["cp_green_max"]; ok {
		f, ok := decodeFraction(c, v, "Green threshold")
		if !ok {
			return false
		}
		params.CpGreenMax = &f
	}
	if v, ok := raw["cp_yellow_max"]; ok {
		f, ok := decodeFraction(c, v, "Yellow threshold")
		if !ok {
			return false
		}
		params.CpYellowMax = &f
	}
	if v, ok := raw["entry_machine_hours"]; ok {
		var hours *float64
		if !decodeField(c, v, &hours) {
			return false
		}
		if hours != nil && *hours <= 0 {
			detail(c, http.StatusUnprocessableEntity, "Entry machine-hours must be positive.")
			return false
		}
		params.SetEntryMachineHours = true
		params.EntryMachineHours = hours
	}
	if v, ok := raw["entry_rung"]; ok {
		var rung *int32
		if !decodeField(c, v, &rung) {
			return false
		}
		if rung != nil && *rung <= 0 {
			detail(c, http.StatusUnprocessableEntity, "Entry rung must be positive.")
			return false
		}
		params.SetEntryRung = true
		params.EntryRung = rung
	}
	return true
}

func decodeField(c *gin.Context, raw json.RawMessage, dst any) bool {
	if err := json.Unmarshal(raw, dst); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return false
	}
	return true
}

func decodeFraction(c *gin.Context, raw json.RawMessage, label string) (float64, bool) {
	var f float64
	if !decodeField(c, raw, &f) {
		return 0, false
	}
	if f <= 0 || f > 1 {
		detail(c, http.StatusUnprocessableEntity, label+" must be a fraction between 0 and 1.")
		return 0, false
	}
	return f, true
}

// validateLadder decodes and validates a ladder, returning it re-encoded as JSON
// for storage. Messages match the frontend/Python validators.
func validateLadder(c *gin.Context, raw json.RawMessage) ([]byte, bool) {
	var ladder []int
	if err := json.Unmarshal(raw, &ladder); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return nil, false
	}
	if len(ladder) == 0 {
		detail(c, http.StatusUnprocessableEntity, "The ladder needs at least one price.")
		return nil, false
	}
	for i, price := range ladder {
		if price <= 0 {
			detail(c, http.StatusUnprocessableEntity, "Ladder prices must be positive.")
			return nil, false
		}
		if i > 0 && price <= ladder[i-1] {
			detail(c, http.StatusUnprocessableEntity, "Ladder prices must be strictly ascending.")
			return nil, false
		}
	}
	out, err := json.Marshal(ladder)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not encode the ladder.")
		return nil, false
	}
	return out, true
}
