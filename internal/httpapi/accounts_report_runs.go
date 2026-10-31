package httpapi

// The register's own history: every one that went, and every one that did not.
//
// accounts_report_settings keeps last_sent_at, last_row_count and last_error -
// one row per brand, overwritten each day. That answers "did today's go?" and
// nothing else. An accountant asking for Tuesday's file, or anybody asking why
// Thursday's never arrived, had nowhere to look, and the only copy of the
// spreadsheet was in somebody's inbox.
//
// So each send writes a row and keeps its file, and the page reads them back.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// reportRunSource says whether a register went out on the schedule or because
// somebody pressed Send now, so a test send is never mistaken for the day's.
const (
	reportRunScheduled = "scheduled"
	reportRunManual    = "manual"
	reportRunSent      = "sent"
	reportRunFailed    = "failed"
)

// recordReportRun writes down one send, successful or not.
//
// BEST EFFORT, ALWAYS. The mail has already gone by the time this runs, and a
// failure to write the history must never turn a delivered register into a
// reported failure - the worker would then retry and the accountant would get
// it twice. Errors are logged and swallowed for that reason.
func (s *Server) recordReportRun(
	ctx context.Context, settings gen.AccountsReportSetting, out ReportOutcome,
	source string, book []byte, sendErr error,
) {
	log := obs.FromContext(ctx)

	status, detail := reportRunSent, (*string)(nil)
	if sendErr != nil {
		status = reportRunFailed
		msg := sendErr.Error()
		detail = &msg
	}

	// The spreadsheet is stored only for a send that happened. A failed run
	// has a reason worth keeping and no file worth keeping: whatever it would
	// have attached was never seen by anybody.
	var fileID *uuid.UUID
	if sendErr == nil && len(book) > 0 {
		if id, err := s.storeReportBook(ctx, settings.BrandSlug, out, book); err != nil {
			// The register WAS delivered. A missing attachment here is a gap
			// in the history, not a failed report, and saying otherwise on
			// the page would be worse than the gap.
			log.Warn("the register was sent but its spreadsheet could not be stored",
				"brand", settings.BrandSlug, "error", err)
		} else {
			fileID = &id
		}
	}

	if _, err := s.store.Q.InsertAccountsReportRun(ctx, gen.InsertAccountsReportRunParams{
		ID:          uuid.New(),
		BrandSlug:   settings.BrandSlug,
		WindowStart: timestamptzAt(out.WindowStart),
		WindowEnd:   timestamptzAt(out.WindowEnd),
		Status:      status,
		Detail:      detail,
		RowCount:    int32(out.Rows),
		OrderCount:  int32(out.Orders),
		// NIL IS NOT NULL HERE. The columns are NOT NULL DEFAULT '{}', and a
		// Go nil slice reaches Postgres as NULL rather than as the default -
		// so a brand with no Cc list would fail the insert and lose the
		// record of a register that had already gone out.
		ToAddresses:  orEmpty(settings.ToAddresses),
		CcAddresses:  orEmpty(settings.CcAddresses),
		BccAddresses: orEmpty(settings.BccAddresses),
		FileID:       fileID,
		Source:       source,
	}); err != nil {
		log.Warn("could not record the register's run", "brand", settings.BrandSlug, "error", err)
	}
}

// storeReportBook puts the spreadsheet where the download route can find it.
//
// The same file_assets row and the same object storage every other stored
// artefact uses, so nothing new has to be taught how to serve it.
func (s *Server) storeReportBook(
	ctx context.Context, brand string, out ReportOutcome, book []byte,
) (uuid.UUID, error) {
	if s.storage == nil {
		return uuid.Nil, fmt.Errorf("object storage is not configured on this service")
	}
	id := uuid.New()
	key := fmt.Sprintf("accounts-reports/%s/%s.xlsx", brand, id)
	if err := s.storage.Put(ctx, key, bytes.NewReader(book),
		int64(len(book)), xlsxContentType); err != nil {
		return uuid.Nil, fmt.Errorf("store the spreadsheet: %w", err)
	}
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: id, Filename: out.Filename, ContentType: xlsxContentType,
		SizeBytes: int64(len(book)), StorageKey: key, UploadedBy: "accounts report",
	}); err != nil {
		return uuid.Nil, fmt.Errorf("record the spreadsheet: %w", err)
	}
	return id, nil
}

// xlsxContentType is what a browser needs to open the file rather than
// download it as bytes of nothing in particular.
const xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// accountsReportRunResponse is one line of the register's history.
type accountsReportRunResponse struct {
	ID          string   `json:"id"`
	WindowStart string   `json:"window_start"`
	WindowEnd   string   `json:"window_end"`
	SentAt      string   `json:"sent_at"`
	Status      string   `json:"status"`
	Detail      string   `json:"detail"`
	Rows        int32    `json:"rows"`
	Orders      int32    `json:"orders"`
	To          []string `json:"to"`
	CC          []string `json:"cc"`
	BCC         []string `json:"bcc"`
	Source      string   `json:"source"`
	// HasFile rather than a URL: the page builds its own download link, and a
	// run whose spreadsheet predates this history - or could not be stored -
	// is still a run worth showing.
	HasFile  bool   `json:"has_file"`
	Filename string `json:"filename"`
	Size     int64  `json:"size_bytes"`
}

// runHistoryLimit is how far back the page looks. A register a day means
// roughly four months, which is further than anybody scrolls and small enough
// to send in one response.
const runHistoryLimit = 120

func (s *Server) listAccountsReportRuns(c *gin.Context) {
	brand := c.Param("slug")
	rows, err := s.store.Q.ListAccountsReportRuns(c.Request.Context(),
		gen.ListAccountsReportRunsParams{BrandSlug: brand, Lim: runHistoryLimit})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the register's history.")
		return
	}
	out := make([]accountsReportRunResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, accountsReportRunResponse{
			ID:          r.ID.String(),
			WindowStart: db.Time(r.WindowStart).Format(time.RFC3339),
			WindowEnd:   db.Time(r.WindowEnd).Format(time.RFC3339),
			SentAt:      db.Time(r.SentAt).Format(time.RFC3339),
			Status:      r.Status,
			Detail:      deref(r.Detail),
			Rows:        r.RowCount,
			Orders:      r.OrderCount,
			To:          r.ToAddresses,
			CC:          r.CcAddresses,
			BCC:         r.BccAddresses,
			Source:      r.Source,
			HasFile:     r.FileID != nil,
			Filename:    deref(r.Filename),
			Size:        derefInt64(r.SizeBytes),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// downloadAccountsReportRun serves one run's spreadsheet.
//
// Streamed from object storage rather than redirected to it: the bucket is
// private, and a signed URL would be a second way to reach a sales register.
func (s *Server) downloadAccountsReportRun(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	// Scoped by BRAND as well as id, so a token for one shop cannot fetch
	// another's sales day by guessing a uuid.
	run, err := s.store.Q.GetAccountsReportRun(ctx, gen.GetAccountsReportRunParams{
		ID: id, BrandSlug: c.Param("slug"),
	})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That register has no spreadsheet stored.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read that register.")
		return
	}
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable,
			"Object storage is not configured on this service, so the file cannot be served.")
		return
	}
	obj, err := s.storage.Get(ctx, run.StorageKey)
	if err != nil {
		obs.FromContext(ctx).Error("could not read a stored register",
			"run", id, "key", run.StorageKey, "error", err)
		detail(c, http.StatusBadGateway, "Could not read that spreadsheet.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	c.Header("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", run.Filename))
	c.DataFromReader(http.StatusOK, obj.Size, run.ContentType, obj.Body, nil)
}

// derefInt64 reads a nullable count as zero.
func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
