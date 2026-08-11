package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// slugPattern is the shape a brand slug must take: lowercase alphanumerics
// separated by single hyphens. It doubles as the {slug} path validator.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// reservedBrandSlugs would collide with the frontend's static dashboard routes
// (/dashboard/<slug> is dynamic, but these segments resolve to their own pages
// first), so a brand may not take them. "all" is the frontend's sentinel for the
// global "all brands" view, which rides the same /dashboard/<slug> routes, so a
// real brand named "all" would shadow it.
var reservedBrandSlugs = map[string]bool{
	"users":    true,
	"settings": true,
	"brands":   true,
	"projects": true,
	"all":      true,
}

type brandResponse struct {
	ID                string    `json:"id"`
	Slug              string    `json:"slug"`
	Name              string    `json:"name"`
	LogoURL           *string   `json:"logo_url"`
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
	id uuid.UUID, slug, name string, logoURL *string, startingPrice float64, shopifyURL, description *string,
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
		ID: id.String(), Slug: slug, Name: name, LogoURL: logoURL, StartingPrice: startingPrice,
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
	g.POST("", s.guards.RequirePermission(auth.BrandManage.Key()), s.createBrand)
	g.GET("/:slug", s.guards.RequirePermission(auth.BrandRead.Key()), s.getBrand)
	g.PATCH("/:slug", s.guards.RequirePermission(auth.BrandManage.Key()), s.updateBrand)
	g.DELETE("/:slug", s.guards.RequirePermission(auth.BrandManage.Key()), s.deleteBrand)
}

func notConfigured(slug pricing.Brand) string {
	return fmt.Sprintf("Brand '%s' is not configured yet.", slug)
}

// brandSlugParam reads and validates the {slug} path segment. Any well-formed
// slug is accepted; brands are free-form, so validity is about shape only.
func brandSlugParam(c *gin.Context) (string, bool) {
	slug := c.Param("slug")
	if !slugPattern.MatchString(slug) {
		detail(c, http.StatusUnprocessableEntity, "The brand slug in the URL is not valid.")
		return "", false
	}
	return slug, true
}

// slugify turns a human name into a slug: lowercased, non-alphanumerics folded
// to single hyphens, trimmed. Used when a create request omits an explicit slug.
func slugify(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevHyphen := false
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen && b.Len() > 0 {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// listBrands returns the brands the caller may access: every brand for an admin
// (brand:manage), otherwise only the ones an admin assigned via user_brands. The
// two queries return the same row shape, so both feed the same DTO mapping.
func (s *Server) listBrands(c *gin.Context) {
	ctx := c.Request.Context()
	user, _ := auth.UserFrom(c)

	var rows []gen.ListBrandsRow
	if user.Has(auth.BrandManage.Key()) {
		all, err := s.store.Q.ListBrands(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list brands.")
			return
		}
		rows = all
	} else {
		scoped, err := s.store.Q.ListBrandsForUser(ctx, user.ID)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list brands.")
			return
		}
		rows = make([]gen.ListBrandsRow, len(scoped))
		for i, r := range scoped {
			rows[i] = gen.ListBrandsRow(r)
		}
	}

	out := make([]brandResponse, 0, len(rows))
	for _, r := range rows {
		dto, err := brandDTO(r.ID, r.Slug, r.Name, r.LogoUrl, r.StartingPrice, r.ShopifyUrl, r.Description,
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
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	// A member may only load a brand an admin assigned them. Answer 404 (not 403)
	// so an unassigned brand is indistinguishable from a non-existent one.
	if user, ok := auth.UserFrom(c); !ok || !s.canAccessBrand(c.Request.Context(), user, slug) {
		detail(c, http.StatusNotFound, notConfigured(pricing.Brand(slug)))
		return
	}
	r, err := s.store.Q.GetBrandBySlug(c.Request.Context(), slug)
	if err != nil {
		dbError(c, err, notConfigured(pricing.Brand(slug)), "Could not load the brand.")
		return
	}
	dto, err := brandDTO(r.ID, r.Slug, r.Name, r.LogoUrl, r.StartingPrice, r.ShopifyUrl, r.Description,
		r.IsActive, r.Ladder, r.CpGreenMax, r.CpYellowMax, r.EntryMachineHours, r.EntryRung,
		r.CreatedAt, r.UpdatedAt)
	if err != nil {
		detail(c, http.StatusInternalServerError, "The brand ladder is corrupt.")
		return
	}
	c.JSON(http.StatusOK, dto)
}

type brandCreateRequest struct {
	Slug              *string  `json:"slug"`
	Name              string   `json:"name" binding:"required,min=1,max=120"`
	LogoURL           *string  `json:"logo_url"`
	StartingPrice     float64  `json:"starting_price" binding:"required,gt=0"`
	ShopifyURL        *string  `json:"shopify_url"`
	Description       *string  `json:"description" binding:"omitempty,max=500"`
	IsActive          *bool    `json:"is_active"`
	Ladder            []int    `json:"ladder" binding:"required,min=1"`
	CPGreenMax        float64  `json:"cp_green_max" binding:"required,gt=0,lte=1"`
	CPYellowMax       float64  `json:"cp_yellow_max" binding:"required,gt=0,lte=1"`
	EntryMachineHours *float64 `json:"entry_machine_hours" binding:"omitempty,gt=0"`
	EntryRung         *int32   `json:"entry_rung" binding:"omitempty,gt=0"`
}

func (s *Server) createBrand(c *gin.Context) {
	var req brandCreateRequest
	if !bindJSON(c, &req) {
		return
	}

	slug := slugify(req.Name)
	if req.Slug != nil && *req.Slug != "" {
		slug = strings.ToLower(strings.TrimSpace(*req.Slug))
	}
	if !slugPattern.MatchString(slug) {
		detail(c, http.StatusUnprocessableEntity, "The brand slug is not valid.")
		return
	}
	if reservedBrandSlugs[slug] {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf("'%s' is reserved. Choose another name or slug.", slug))
		return
	}

	if !validateLadderSlice(c, req.Ladder) {
		return
	}
	if req.CPGreenMax > req.CPYellowMax {
		detail(c, http.StatusBadRequest, "The green threshold must be at or below the yellow threshold.")
		return
	}

	exists, err := s.store.Q.BrandExists(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not verify the brand.")
		return
	}
	if exists {
		detail(c, http.StatusConflict, fmt.Sprintf("A brand with the slug '%s' already exists.", slug))
		return
	}

	ladderJSON, err := json.Marshal(req.Ladder)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not encode the ladder.")
		return
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}

	r, err := s.store.Q.InsertBrand(c.Request.Context(), gen.InsertBrandParams{
		ID: uuid.New(), Slug: slug, Name: req.Name, LogoUrl: req.LogoURL,
		StartingPrice: req.StartingPrice, ShopifyUrl: req.ShopifyURL, Description: req.Description,
		IsActive: active, Ladder: ladderJSON, CpGreenMax: req.CPGreenMax, CpYellowMax: req.CPYellowMax,
		EntryMachineHours: req.EntryMachineHours, EntryRung: req.EntryRung,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the brand.")
		return
	}
	dto, err := brandDTO(r.ID, r.Slug, r.Name, r.LogoUrl, r.StartingPrice, r.ShopifyUrl, r.Description,
		r.IsActive, r.Ladder, r.CpGreenMax, r.CpYellowMax, r.EntryMachineHours, r.EntryRung,
		r.CreatedAt, r.UpdatedAt)
	if err != nil {
		detail(c, http.StatusInternalServerError, "The brand ladder is corrupt.")
		return
	}
	c.JSON(http.StatusCreated, dto)
}

func (s *Server) deleteBrand(c *gin.Context) {
	slug, ok := brandSlugParam(c)
	if !ok {
		return
	}
	rows, err := s.store.Q.DeleteBrand(c.Request.Context(), slug)
	if err != nil {
		// A project still references this brand (FK RESTRICT).
		detail(c, http.StatusConflict, "This brand still has projects and cannot be deleted.")
		return
	}
	if rows == 0 {
		detail(c, http.StatusNotFound, notConfigured(pricing.Brand(slug)))
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) updateBrand(c *gin.Context) {
	slug, ok := brandSlugParam(c)
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

	current, err := s.store.Q.GetBrandBySlug(c.Request.Context(), slug)
	if err != nil {
		dbError(c, err, notConfigured(pricing.Brand(slug)), "Could not load the brand.")
		return
	}

	params := gen.UpdateBrandParams{Slug: slug}
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
		dbError(c, err, notConfigured(pricing.Brand(slug)), "Could not update the brand.")
		return
	}
	dto, err := brandDTO(r.ID, r.Slug, r.Name, r.LogoUrl, r.StartingPrice, r.ShopifyUrl, r.Description,
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
	if !patchField(c, raw, "logo_url", noValidation[*string], func(url *string) {
		params.SetLogoUrl = true
		params.LogoUrl = url
	}) {
		return false
	}
	if !patchField(c, raw, "starting_price",
		func(price float64) string {
			if price <= 0 {
				return "Starting price must be positive."
			}
			return ""
		},
		func(price float64) { params.StartingPrice = &price }) {
		return false
	}
	if !patchField(c, raw, "shopify_url", noValidation[*string], func(url *string) {
		params.SetShopifyUrl = true
		params.ShopifyUrl = url
	}) {
		return false
	}
	if !patchField(c, raw, "description", noValidation[*string], func(desc *string) {
		params.SetDescription = true
		params.Description = desc
	}) {
		return false
	}
	if !patchField(c, raw, "is_active", noValidation[bool], func(active bool) { params.IsActive = &active }) {
		return false
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
	if !patchField(c, raw, "entry_machine_hours",
		func(hours *float64) string {
			if hours != nil && *hours <= 0 {
				return "Entry machine-hours must be positive."
			}
			return ""
		},
		func(hours *float64) {
			params.SetEntryMachineHours = true
			params.EntryMachineHours = hours
		}) {
		return false
	}
	if !patchField(c, raw, "entry_rung",
		func(rung *int32) string {
			if rung != nil && *rung <= 0 {
				return "Entry rung must be positive."
			}
			return ""
		},
		func(rung *int32) {
			params.SetEntryRung = true
			params.EntryRung = rung
		}) {
		return false
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
	if !validateLadderSlice(c, ladder) {
		return nil, false
	}
	out, err := json.Marshal(ladder)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not encode the ladder.")
		return nil, false
	}
	return out, true
}

// validateLadderSlice enforces the ladder invariants: non-empty, positive, and
// strictly ascending. It writes a 422 and returns false on the first violation.
func validateLadderSlice(c *gin.Context, ladder []int) bool {
	if len(ladder) == 0 {
		detail(c, http.StatusUnprocessableEntity, "The ladder needs at least one price.")
		return false
	}
	for i, price := range ladder {
		if price <= 0 {
			detail(c, http.StatusUnprocessableEntity, "Ladder prices must be positive.")
			return false
		}
		if i > 0 && price <= ladder[i-1] {
			detail(c, http.StatusUnprocessableEntity, "Ladder prices must be strictly ascending.")
			return false
		}
	}
	return true
}
