package httpapi

// The damaged-products queue: every customer who reported damage, what they
// sent, and how far anyone has got with it.
//
// WHETHER THE PHOTOGRAPHS ARRIVED IS A FIRST-CLASS FACT HERE, not something
// the page infers. A report saying "3 photos" when Tensor holds none of them
// is a report nobody can act on, and the difference between "still
// downloading" and "never arrived" decides whether waiting helps. So the list
// carries the tally per report and the detail carries it per photograph.

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// damageReportRows caps one brand's list.
//
// Whole-set, like the inbox's own cap and for the same reason: the tables in
// this product filter, search and count their tabs over everything they were
// given, so paging on the server either breaks the counts or costs a second
// round trip. Damage reports are rare - tens a month, not thousands. If that
// ever stops being true, orders_table.go + CountOrdersByTab is the pattern to
// adopt, and the response already has a counts object to grow into.
const damageReportRows = 500

// The statuses a report moves between. Any may follow any: a report reopened
// after a refund is a real thing, and a one-way ladder would need a fourth
// status to say so.
const (
	damageNew      = "new"
	damageInReview = "in_review"
	damageResolved = "resolved"
)

func validDamageStatus(s string) bool {
	switch s {
	case damageNew, damageInReview, damageResolved:
		return true
	}
	return false
}

type damageReportJSON struct {
	ID              string  `json:"id"`
	WaID            string  `json:"wa_id"`
	CustomerName    string  `json:"customer_name"`
	OrderNumber     string  `json:"order_number"`
	ShopifyOrderGID string  `json:"shopify_order_gid"`
	OrderID         *string `json:"order_id"`
	// PhotoCount is how many the customer sent. The three that follow are how
	// many Tensor actually holds, is still fetching, and gave up on - which is
	// not the same number and is the one that decides whether anybody can work
	// the report.
	PhotoCount      int32      `json:"photo_count"`
	PhotosStored    int32      `json:"photos_stored"`
	PhotosPending   int32      `json:"photos_pending"`
	PhotosFailed    int32      `json:"photos_failed"`
	Status          string     `json:"status"`
	ResolutionNote  string     `json:"resolution_note"`
	ReportedAt      time.Time  `json:"reported_at"`
	StatusChangedAt *time.Time `json:"status_changed_at"`
	StatusChangedBy *string    `json:"status_changed_by"`
}

type damagePhotoJSON struct {
	MessageID string `json:"message_id"`
	Kind      string `json:"kind"`
	// Caption is what the customer typed alongside the photograph, which is
	// stored as the message body.
	Caption     string    `json:"caption"`
	MediaStatus string    `json:"media_status"`
	MimeType    string    `json:"mime_type"`
	MediaError  string    `json:"media_error"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Server) registerDamageReports(r *gin.Engine) {
	g := r.Group("/brands/:slug/damage-reports")
	g.Use(s.guards.RequireUser())
	// whatsapp:read and whatsapp:reply rather than a damage:* pair. The
	// audiences are already exactly right - ADMIN, PROJECT_LEAD, OPERATOR -
	// and a new permission would move catalog_test.go's totals for no
	// difference in who can do what. Adding damage:manage later is two lines
	// in catalog.go plus those two assertions.
	g.GET("", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.listDamageReports)
	g.GET("/:id", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.getDamageReport)
	g.PATCH("/:id", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.updateDamageReport)
}

func (s *Server) listDamageReports(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")

	rows, err := s.store.Q.ListDamageReports(ctx, gen.ListDamageReportsParams{
		BrandSlug: brand, RowLimit: damageReportRows,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the damage reports.")
		return
	}
	items := make([]damageReportJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, damageListJSON(row))
	}

	// "" is All, the same convention the orders table uses.
	counts := map[string]int32{"": int32(len(rows))}
	if tallies, err := s.store.Q.CountDamageReportsByStatus(ctx, brand); err == nil {
		for _, t := range tallies {
			counts[t.Status] = t.Total
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "counts": counts})
}

func (s *Server) getDamageReport(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	row, err := s.store.Q.GetDamageReport(ctx, gen.GetDamageReportParams{ID: id, BrandSlug: brand})
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not load the report.")
		return
	}

	photos := make([]damagePhotoJSON, 0, row.PhotoCount)
	if found, err := s.store.Q.ListDamageReportPhotos(ctx, id); err == nil {
		for _, p := range found {
			photos = append(photos, damagePhotoJSON{
				MessageID: p.MessageID.String(), Kind: p.Kind, Caption: p.Body,
				MediaStatus: p.MediaStatus, MimeType: p.MediaMimeType,
				MediaError: p.MediaError, CreatedAt: p.CreatedAt.Time,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"report": damageJSON(row, photoTally(photos)), "photos": photos})
}

func (s *Server) updateDamageReport(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var body struct {
		Status         *string `json:"status"`
		ResolutionNote *string `json:"resolution_note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Send a status, a resolution note, or both.")
		return
	}
	if body.Status == nil && body.ResolutionNote == nil {
		detail(c, http.StatusUnprocessableEntity, "Send a status, a resolution note, or both.")
		return
	}
	if body.Status != nil {
		next := strings.TrimSpace(*body.Status)
		if !validDamageStatus(next) {
			detail(c, http.StatusUnprocessableEntity,
				"A damage report is new, in_review or resolved.")
			return
		}
		body.Status = &next
	}

	by := currentUserID(c)
	row, err := s.store.Q.UpdateDamageReportStatus(ctx, gen.UpdateDamageReportStatusParams{
		Status: body.Status, ResolutionNote: body.ResolutionNote,
		StatusChangedBy: &by, ID: id, BrandSlug: brand,
	})
	if err != nil {
		dbError(c, err, "That damage report does not exist.", "Could not update the report.")
		return
	}
	c.JSON(http.StatusOK, damageJSON(row, photoCounts{}))
}

// photoCounts is how many of a report's photographs Tensor actually holds.
type photoCounts struct{ stored, pending, failed int32 }

func photoTally(photos []damagePhotoJSON) photoCounts {
	var out photoCounts
	for _, p := range photos {
		switch p.MediaStatus {
		case mediaStored:
			out.stored++
		case mediaPending:
			out.pending++
		case mediaFailed:
			out.failed++
		}
	}
	return out
}

func damageListJSON(row gen.ListDamageReportsRow) damageReportJSON {
	out := damageJSON(gen.DamageReport{
		ID: row.ID, WaID: row.WaID, CustomerName: row.CustomerName,
		OrderNumber: row.OrderNumber, ShopifyOrderGid: row.ShopifyOrderGid,
		OrderID: row.OrderID, PhotoCount: row.PhotoCount, Status: row.Status,
		ResolutionNote: row.ResolutionNote, ReportedAt: row.ReportedAt,
		StatusChangedAt: row.StatusChangedAt, StatusChangedBy: row.StatusChangedBy,
	}, photoCounts{
		stored: row.PhotosStored, pending: row.PhotosPending, failed: row.PhotosFailed,
	})
	return out
}

func damageJSON(row gen.DamageReport, counts photoCounts) damageReportJSON {
	out := damageReportJSON{
		ID: row.ID.String(), WaID: row.WaID, CustomerName: row.CustomerName,
		OrderNumber: row.OrderNumber, ShopifyOrderGID: row.ShopifyOrderGid,
		PhotoCount: row.PhotoCount, PhotosStored: counts.stored,
		PhotosPending: counts.pending, PhotosFailed: counts.failed,
		Status: row.Status, ResolutionNote: row.ResolutionNote,
		ReportedAt: row.ReportedAt.Time, StatusChangedBy: row.StatusChangedBy,
	}
	if row.OrderID != nil {
		id := row.OrderID.String()
		out.OrderID = &id
	}
	if row.StatusChangedAt.Valid {
		at := row.StatusChangedAt.Time
		out.StatusChangedAt = &at
	}
	return out
}
