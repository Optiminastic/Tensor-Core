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
	ID       string  `json:"id"`
	Material string  `json:"material"`
	Colour   *string `json:"colour"`
	// ColourHex is the swatch for Colour, e.g. "#1A1A1A". Null on rows entered
	// by hand: a made-up default would render a confidently wrong colour.
	ColourHex         *string   `json:"colour_hex"`
	GramsAvailable    float64   `json:"grams_available"`
	ReorderLevelGrams float64   `json:"reorder_level_grams"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func filamentDTO(f gen.FilamentInventory) filamentResponse {
	return filamentResponse{
		ID: f.ID.String(), Material: f.Material, Colour: f.Colour, ColourHex: f.ColourHex,
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
	// What a colour NAME means to the printers - see colour_map.go. It lives
	// under filament because that is what it describes, and because the floor
	// already has the filament permissions.
	s.registerColourMap(g)
}

func (s *Server) listFilament(c *gin.Context) {
	if numbered, paged, ok := parseOffsetPage(c); !ok {
		return
	} else if paged {
		s.listFilamentTable(c, numbered)
		return
	}
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

// filamentPage is one page of spools with the Material select's options.
type filamentPage struct {
	pageEnvelope[filamentResponse]
	Materials []string `json:"materials,omitempty"`
}

// listFilamentTable answers ?page=N for the Inventory page's Filament table:
// the stock tab (tab=in_stock|low), the material select, search and the
// period, all in Postgres before LIMIT/OFFSET.
func (s *Server) listFilamentTable(c *gin.Context, p offsetPage) {
	window, ok := parseTimeRange(c)
	if !ok {
		return
	}
	f := gen.CountFilamentTableParams{
		Material: nilIfEmpty(c.Query("material")), Search: searchParam(c),
		UpdatedFrom: window.From, UpdatedTo: window.To,
	}
	yes, no := true, false
	switch c.Query("tab") {
	case "":
	case "low":
		f.Low = &yes
	case "in_stock":
		f.Low = &no
	default:
		detail(c, http.StatusUnprocessableEntity, "'tab' must be in_stock or low.")
		return
	}
	ctx := c.Request.Context()
	total, err := s.store.Q.CountFilamentTable(ctx, f)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list filament inventory.")
		return
	}
	p = p.within(total)
	rows, err := s.store.Q.ListFilamentTable(ctx, gen.ListFilamentTableParams{
		Low: f.Low, Material: f.Material, Search: f.Search,
		UpdatedFrom: f.UpdatedFrom, UpdatedTo: f.UpdatedTo,
		PageLimit: p.Size, PageOffset: p.Offset(),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list filament inventory.")
		return
	}
	items := make([]filamentResponse, 0, len(rows))
	for _, r := range rows {
		items = append(items, filamentDTO(r))
	}
	out := filamentPage{pageEnvelope: newPageEnvelope(items, total, p)}
	if wantsCounts(c) {
		counts, err := s.store.Q.CountFilamentByTab(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not count filament inventory.")
			return
		}
		out.Counts = map[string]int64{"": counts.AllSpools, "in_stock": counts.InStock, "low": counts.Low}
		if out.Materials, err = s.store.Q.ListFilamentMaterials(ctx); err != nil {
			detail(c, http.StatusInternalServerError, "Could not list filament materials.")
			return
		}
	}
	c.JSON(http.StatusOK, out)
}
