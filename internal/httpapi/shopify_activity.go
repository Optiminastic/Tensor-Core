package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// registerShopifyActivity mounts the read-only Shopify API activity log. It is a
// global (all-stores) view, guarded by integration:manage since it exposes what
// the app does to connected stores.
func (s *Server) registerShopifyActivity(r *gin.Engine) {
	g := r.Group("/shopify")
	// RequireUser must run before RequirePermission, or there is no identity on the
	// context and the permission guard rejects with "Missing bearer token".
	g.Use(s.guards.RequireUser())
	g.GET("/api-calls",
		s.guards.RequirePermission(auth.IntegrationManage.Key()),
		s.listShopifyApiCalls)
}

// shopifyRecorder persists each physical Admin API request to shopify_api_calls.
// It satisfies shopify.Recorder. Writes are asynchronous and best-effort: a
// failed insert is logged, never returned, so the activity log can never slow or
// fail a publish.
type shopifyRecorder struct {
	store *db.Store
	log   *slog.Logger
}

func newShopifyRecorder(store *db.Store, log *slog.Logger) *shopifyRecorder {
	if log == nil {
		log = slog.Default()
	}
	return &shopifyRecorder{store: store, log: log}
}

// RecordCall inserts one call row in the background. It uses a fresh context, not
// the request context, because the request may complete before the insert runs.
func (r *shopifyRecorder) RecordCall(_ context.Context, call shopify.Call) {
	params := gen.InsertShopifyApiCallParams{
		ID:         uuid.New(),
		ShopDomain: call.Shop,
		Method:     call.Method,
		Operation:  call.Operation,
		Scope:      call.Scope,
		StatusCode: int32(call.StatusCode),
		Ok:         call.OK,
		LatencyMs:  int32(call.Latency.Milliseconds()),
	}
	if call.Err != nil {
		msg := call.Err.Error()
		params.ErrorMessage = &msg
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.store.Q.InsertShopifyApiCall(ctx, params); err != nil {
			r.log.Warn("shopify api call log insert failed", "error", err)
		}
	}()
}

// shopifyCallResponse is one row of the activity log on the wire (snake_case).
type shopifyCallResponse struct {
	ID           string    `json:"id"`
	ShopDomain   string    `json:"shop_domain"`
	BrandSlug    *string   `json:"brand_slug"`
	Method       string    `json:"method"`
	Operation    string    `json:"operation"`
	Scope        string    `json:"scope"`
	StatusCode   int32     `json:"status_code"`
	Ok           bool      `json:"ok"`
	ErrorMessage *string   `json:"error_message"`
	LatencyMs    int32     `json:"latency_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

func shopifyCallDTO(r gen.ShopifyApiCall) shopifyCallResponse {
	return shopifyCallResponse{
		ID:           r.ID.String(),
		ShopDomain:   r.ShopDomain,
		BrandSlug:    r.BrandSlug,
		Method:       r.Method,
		Operation:    r.Operation,
		Scope:        r.Scope,
		StatusCode:   r.StatusCode,
		Ok:           r.Ok,
		ErrorMessage: r.ErrorMessage,
		LatencyMs:    r.LatencyMs,
		CreatedAt:    db.Time(r.CreatedAt),
	}
}

// listShopifyApiCalls returns one keyset page of the Shopify API activity log,
// newest first, with the next-page cursor in the X-Next-Cursor header. Query
// params: shop (filter by store domain), only_errors=true (failed calls only),
// limit (1..200, default 50), cursor (opaque).
func (s *Server) listShopifyApiCalls(c *gin.Context) {
	ctx := c.Request.Context()

	limit := int32(50)
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageLimit {
			detail(c, http.StatusUnprocessableEntity, "'limit' must be an integer between 1 and 200.")
			return
		}
		limit = int32(n)
	}

	var cursorTS pgtype.Timestamptz
	var cursorID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		ts, id, err := decodeCursor(raw)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "The 'cursor' parameter is not valid.")
			return
		}
		cursorTS = pgtype.Timestamptz{Time: ts, Valid: true}
		cursorID = &id
	}

	rows, err := s.store.Q.ListShopifyApiCallsPage(ctx, gen.ListShopifyApiCallsPageParams{
		ShopDomain:      strings.TrimSpace(c.Query("shop")),
		OnlyErrors:      c.Query("only_errors") == "true",
		CursorCreatedAt: cursorTS,
		CursorID:        cursorID,
		PageSize:        limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list Shopify API calls.")
		return
	}

	out := make([]shopifyCallResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, shopifyCallDTO(r))
	}
	if n := len(rows); n > 0 {
		last := rows[n-1]
		setNextCursor(c, n, limit, last.CreatedAt.Time, last.ID)
	}
	c.JSON(http.StatusOK, out)
}
