package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type filamentResponse struct {
	ID                string    `json:"id"`
	Material          string    `json:"material"`
	Colour            *string   `json:"colour"`
	GramsAvailable    float64   `json:"grams_available"`
	ReorderLevelGrams float64   `json:"reorder_level_grams"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func filamentDTO(f gen.FilamentInventory) filamentResponse {
	return filamentResponse{
		ID: f.ID.String(), Material: f.Material, Colour: f.Colour,
		GramsAvailable: db.NumFloat(f.GramsAvailable), ReorderLevelGrams: db.NumFloat(f.ReorderLevelGrams),
		CreatedAt: db.Time(f.CreatedAt), UpdatedAt: db.Time(f.UpdatedAt),
	}
}

func (s *Server) registerFilament(r *gin.Engine) {
	g := r.Group("/filament-inventory")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.FilamentRead.Key()), s.listFilament)
	g.POST("", s.guards.RequirePermission(auth.FilamentManage.Key()), s.upsertFilament)
	// Mirrors BambuBuddy's shelf into Tensor's per-material buckets. Manage,
	// not read: it rewrites stock figures.
	g.POST("/sync", s.guards.RequirePermission(auth.FilamentManage.Key()), s.syncFilamentInventory)
}

func (s *Server) listFilament(c *gin.Context) {
	page, ok := parsePageParams(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !page.paginate {
		rows, err := s.store.Q.ListFilament(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list filament inventory.")
			return
		}
		out := make([]filamentResponse, 0, len(rows))
		for _, f := range rows {
			out = append(out, filamentDTO(f))
		}
		c.JSON(http.StatusOK, out)
		return
	}
	rows, err := s.store.Q.ListFilamentPage(ctx, gen.ListFilamentPageParams{
		CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list filament inventory.")
		return
	}
	out := make([]filamentResponse, 0, len(rows))
	for _, f := range rows {
		out = append(out, filamentDTO(f))
	}
	if n := len(rows); n > 0 {
		setNextCursor(c, n, page.limit, db.Time(rows[n-1].CreatedAt), rows[n-1].ID)
	}
	c.JSON(http.StatusOK, out)
}

type filamentUpsertRequest struct {
	Material          string  `json:"material" binding:"required,min=1,max=255"`
	Colour            *string `json:"colour"`
	GramsAvailable    float64 `json:"grams_available" binding:"gte=0"`
	ReorderLevelGrams float64 `json:"reorder_level_grams" binding:"gte=0"`
}

func (s *Server) upsertFilament(c *gin.Context) {
	var req filamentUpsertRequest
	if !bindJSON(c, &req) {
		return
	}
	colour := nonEmptyPtr(deref(req.Colour))
	ctx := c.Request.Context()

	existing, err := s.store.Q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{
		Material: req.Material, Colour: colour,
	})
	if err != nil && !isNoRows(err) {
		detail(c, http.StatusInternalServerError, "Could not read filament inventory.")
		return
	}

	var row gen.FilamentInventory
	if isNoRows(err) {
		row, err = s.store.Q.InsertFilament(ctx, gen.InsertFilamentParams{
			ID: uuid.New(), Material: req.Material, Colour: colour,
			GramsAvailable: req.GramsAvailable, ReorderLevelGrams: req.ReorderLevelGrams,
		})
	} else {
		row, err = s.store.Q.UpdateFilamentLevel(ctx, gen.UpdateFilamentLevelParams{
			ID: existing.ID, GramsAvailable: req.GramsAvailable, ReorderLevelGrams: req.ReorderLevelGrams,
		})
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save filament inventory.")
		return
	}
	c.JSON(http.StatusCreated, filamentDTO(row))
}

// filamentAvailable reports whether there is enough stock for the material. An
// untracked material (no row) is treated as available - the queue does not block
// on filament it is not tracking.
func (s *Server) filamentAvailable(ctx context.Context, q *gen.Queries, material *string, grams float64) bool {
	if material == nil || *material == "" {
		return true
	}
	row, err := q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{Material: *material})
	if err != nil {
		return true
	}
	return db.NumFloat(row.GramsAvailable) >= grams
}

// decrementFilament reduces a material's stock, best-effort: an untracked material
// is a no-op, and stock is allowed to go negative.
func (s *Server) decrementFilament(ctx context.Context, q *gen.Queries, material *string, grams float64) error {
	if material == nil || *material == "" || grams <= 0 {
		return nil
	}
	return q.AdjustFilamentStock(ctx, gen.AdjustFilamentStockParams{Delta: -grams, Material: *material})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
