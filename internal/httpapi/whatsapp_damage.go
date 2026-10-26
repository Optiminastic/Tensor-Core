package httpapi

// The damaged-products queue: every customer who filled in the damage form,
// what they sent, and how far anyone has got with it.
//
// ONE TALLY, NOT FOUR. The first version of this carried
// stored/pending/failed counts, because the photographs arrived as WhatsApp
// attachments that had to be fetched back out of Meta and could still be in
// flight or have failed outright. They are uploaded straight to Tensor now,
// so a file a report holds is simply there - and "3 photos, none of which we
// hold" has stopped being a state this screen has to describe.

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/storage"
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
	// FileCount is how many photographs and videos the report holds. It is
	// counted from the files themselves rather than read off the row, so it
	// cannot disagree with what the drawer then lists.
	FileCount      int32  `json:"file_count"`
	Status         string `json:"status"`
	ResolutionNote string `json:"resolution_note"`
	// ResolutionKind is "", "reprint" or "reverse", and ReverseState is how
	// far a reverse has got. Both reach the screen so the dropdown can show
	// what was already decided rather than offering the choice again - the
	// decision is made once, and the API refuses a second.
	ResolutionKind  string     `json:"resolution_kind"`
	ReverseState    string     `json:"reverse_state"`
	ReportedAt      time.Time  `json:"reported_at"`
	StatusChangedAt *time.Time `json:"status_changed_at"`
	StatusChangedBy *string    `json:"status_changed_by"`
}

// damageFileJSON is one uploaded photograph or video.
//
// No URL: the bytes come from this router's own route, which the dashboard
// builds from the report id and this id. A pre-signed link would be a second
// way in to a customer's photographs that outlives the permission check.
type damageFileJSON struct {
	ID          string    `json:"id"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
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
	// NOT /files/:id, which serves file_assets from a flat global id space on
	// production:read. A customer's photographs are not production data -
	// PACKAGING_QC holds the latter and has no business with the former - and
	// the id has to be checked against the report and the brand, or this
	// would hand out any file in the database.
	g.GET("/:id/files/:fileID", s.guards.RequirePermission(auth.WhatsAppRead.Key()), s.damageReportFile)
	g.PATCH("/:id", s.guards.RequirePermission(auth.WhatsAppReply.Key()), s.updateDamageReport)
	// Deciding the OUTCOME - reprint or reverse pickup - is its own
	// permission, held by admin and support alike. See damage_resolution.go.
	s.registerDamageResolution(g)
	// Confirming the Rs 100 and booking the van - see damage_reverse.go.
	s.registerDamageReverse(g)
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

	files := make([]damageFileJSON, 0, row.PhotoCount)
	if found, err := s.store.Q.ListDamageReportFiles(ctx, id); err == nil {
		for _, f := range found {
			files = append(files, damageFileJSON{
				ID: f.ID.String(), ContentType: f.ContentType,
				SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt.Time,
			})
		}
	}
	// SEPARATE FROM files, on purpose. Those are the photographs of the
	// breakage; this is a bank screenshot answering a different question, and
	// in the photo grid it would sit among pictures of a cracked product
	// looking like one of them.
	var proof *damageFileJSON
	if row.PaymentProofFileID != nil {
		if f, err := s.store.Q.GetDamagePaymentProofFile(ctx,
			gen.GetDamagePaymentProofFileParams{ReportID: id, BrandSlug: brand}); err == nil {
			proof = &damageFileJSON{
				ID: f.ID.String(), ContentType: f.ContentType,
				SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt.Time,
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"report": damageJSON(row, int32(len(files))),
		"files":  files,
		// null unless the customer sent one. The screen shows it beside the
		// confirm button, which is the only place anybody needs it.
		"payment_proof": proof,
		// The whole sequence, done and not: a timeline of only completed
		// steps reads "fine" right up until somebody notices it has been
		// fine for four days.
		"stages": s.damageTimeline(ctx, row),
	})
}

// damageReportFile hands one photograph or video to the dashboard.
func (s *Server) damageReportFile(c *gin.Context) {
	ctx := c.Request.Context()
	brand := c.Param("slug")
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	fileID, ok := parseUUIDParam(c, "fileID")
	if !ok {
		return
	}
	// The query joins report -> link -> file, so a file id belonging to
	// another report, or to another brand, simply does not resolve. Asked
	// BEFORE the storage check, so a file that is not on this report is
	// refused as such whatever state object storage is in.
	row, err := s.store.Q.GetDamageReportFile(ctx, gen.GetDamageReportFileParams{
		ReportID: id, BrandSlug: brand, FileID: fileID,
	})
	if isNoRows(err) {
		// THE PAYMENT SCREENSHOT IS NOT IN damage_files. It arrived over
		// WhatsApp rather than through the public form, so it hangs off the
		// report's own column - and without this second look the one image
		// support most needs to see is the one image this route refuses.
		//
		// Scoped exactly as tightly: the query joins report -> brand -> file,
		// so a file id that is not THIS report's proof still does not resolve.
		proof, perr := s.store.Q.GetDamagePaymentProofFile(ctx,
			gen.GetDamagePaymentProofFileParams{ReportID: id, BrandSlug: brand})
		if perr != nil || proof.ID != fileID {
			detail(c, http.StatusNotFound, "That file is not on this report.")
			return
		}
		row = gen.GetDamageReportFileRow{
			ID: proof.ID, Filename: proof.Filename, ContentType: proof.ContentType,
			SizeBytes: proof.SizeBytes, StorageKey: proof.StorageKey,
		}
	} else if err != nil {
		dbError(c, err, "That file is not on this report.", "Could not load the file.")
		return
	}
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}

	obj, err := s.storage.Get(ctx, row.StorageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "That file is no longer in storage.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the file.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	// Inline, because the point is to look at it in the page rather than to
	// save it. Cached hard and privately: the bytes never change once stored,
	// and they are one customer's photographs.
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "private, max-age=86400")
	// NEVER SNIFFED. These are bytes a stranger uploaded through an
	// unauthenticated form, served inline on the dashboard's own origin, and
	// the type beside them is derived from the filename's extension rather
	// than proven. Without this a .jpg full of markup can be sniffed as HTML
	// and run as a page here.
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, obj.Size, row.ContentType, obj.Body, nil)
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
	c.JSON(http.StatusOK, damageJSON(row, row.PhotoCount))
}

func damageListJSON(row gen.ListDamageReportsRow) damageReportJSON {
	return damageJSON(gen.DamageReport{
		ID: row.ID, WaID: row.WaID, CustomerName: row.CustomerName,
		OrderNumber: row.OrderNumber, ShopifyOrderGid: row.ShopifyOrderGid,
		OrderID: row.OrderID, PhotoCount: row.PhotoCount, Status: row.Status,
		ResolutionNote: row.ResolutionNote, ReportedAt: row.ReportedAt,
		ResolutionKind: row.ResolutionKind, ReverseState: row.ReverseState,
		StatusChangedAt: row.StatusChangedAt, StatusChangedBy: row.StatusChangedBy,
	}, row.FileCount)
}

func damageJSON(row gen.DamageReport, files int32) damageReportJSON {
	out := damageReportJSON{
		ID: row.ID.String(), WaID: row.WaID, CustomerName: row.CustomerName,
		OrderNumber: row.OrderNumber, ShopifyOrderGID: row.ShopifyOrderGid,
		FileCount: files, Status: row.Status, ResolutionNote: row.ResolutionNote,
		ResolutionKind: row.ResolutionKind, ReverseState: row.ReverseState,
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
