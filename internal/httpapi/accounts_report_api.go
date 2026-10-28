package httpapi

// The sales register's settings screen, and the Send now button behind it.
//
// ADMIN ONLY, through accounts:manage, which no other role holds. The
// recipient list is an outbound channel for the shop's complete sales figures
// - adding an address to it is exfiltration with one text box - and the GST
// rate is an assertion the business files on.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/accounts"
	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxRecipients bounds each of the three lists.
//
// Not a technical limit - SMTP would take far more - but a blast radius. A
// register carries every sale the shop made; a list long enough to need
// scrolling is one nobody is reading carefully.
const maxRecipients = 25

func (s *Server) registerAccountsReport(r *gin.Engine) {
	g := r.Group("/brands/:slug/accounts-report")
	g.Use(s.guards.RequireUser(), s.guards.RequirePermission(auth.AccountsManage.Key()))
	g.GET("", s.getAccountsReport)
	g.PUT("", s.saveAccountsReport)
	// Its own path rather than a flag on the save: pressing Send now mails a
	// real accountant, and that must never be a side effect of editing a
	// recipient list.
	g.POST("/send", s.sendAccountsReportNow)
}

type accountsReportResponse struct {
	Enabled bool     `json:"enabled"`
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	BCC     []string `json:"bcc"`

	GSTPercent  float64 `json:"gst_percent"`
	SellerState string  `json:"seller_state"`

	// Configured is false when nobody has ever saved this screen, which is
	// different from saved-and-switched-off.
	Configured bool `json:"configured"`
	// MailboxReady is false when the service has no SMTP credentials. The
	// page says so up front rather than letting somebody fill in a recipient
	// list that can never be used.
	MailboxReady bool   `json:"mailbox_ready"`
	MailboxFrom  string `json:"mailbox_from"`

	LastWindowEnd string `json:"last_window_end"`
	LastSentAt    string `json:"last_sent_at"`
	LastRowCount  int    `json:"last_row_count"`
	LastError     string `json:"last_error"`

	// The window the next run will cover, so the screen can say exactly what
	// "daily" means here rather than leaving it to be guessed.
	NextWindowStart string `json:"next_window_start"`
	NextWindowEnd   string `json:"next_window_end"`
}

func (s *Server) getAccountsReport(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()

	out := accountsReportResponse{
		GSTPercent:  accounts.DefaultGSTPercent,
		SellerState: accounts.DefaultSellerState,
		To:          []string{}, CC: []string{}, BCC: []string{},
	}
	mail := s.mailer()
	out.MailboxReady = mail.Configured()
	out.MailboxFrom = s.cfg.SMTPFrom

	from, to := accounts.Window(time.Now(), reportZone)
	out.NextWindowStart = from.Format(time.RFC3339)
	out.NextWindowEnd = to.Format(time.RFC3339)

	row, err := s.store.Q.GetAccountsReportSettings(ctx, brand)
	if err == nil {
		out.Configured = true
		out.Enabled = row.Enabled
		out.To = orEmpty(row.ToAddresses)
		out.CC = orEmpty(row.CcAddresses)
		out.BCC = orEmpty(row.BccAddresses)
		out.GSTPercent = numericToFloat(row.GstPercent)
		out.SellerState = row.SellerState
		out.LastWindowEnd = timeOrEmpty(row.LastWindowEnd)
		out.LastSentAt = timeOrEmpty(row.LastSentAt)
		if row.LastRowCount != nil {
			out.LastRowCount = int(*row.LastRowCount)
		}
		out.LastError = stringOrEmpty(row.LastError)
	}
	c.JSON(http.StatusOK, out)
}

type accountsReportRequest struct {
	Enabled     bool     `json:"enabled"`
	To          []string `json:"to"`
	CC          []string `json:"cc"`
	BCC         []string `json:"bcc"`
	GSTPercent  float64  `json:"gst_percent"`
	SellerState string   `json:"seller_state"`
}

func (s *Server) saveAccountsReport(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	var req accountsReportRequest
	if !bindJSON(c, &req) {
		return
	}

	to, refusal := cleanRecipients(req.To, "To")
	if refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}
	cc, refusal := cleanRecipients(req.CC, "Cc")
	if refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}
	bcc, refusal := cleanRecipients(req.BCC, "Bcc")
	if refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}
	// Refused on the way IN, not when the worker wakes at midday to find it
	// has nobody to mail. The switch is the thing being turned on, so it is
	// the thing that has to be complete.
	if req.Enabled && len(to) == 0 {
		detail(c, http.StatusUnprocessableEntity,
			"Add at least one To address before switching the report on.")
		return
	}

	rate := req.GSTPercent
	if rate <= 0 || rate > 100 {
		detail(c, http.StatusUnprocessableEntity,
			"The GST rate must be between 0 and 100.")
		return
	}
	state := strings.TrimSpace(req.SellerState)
	if state == "" {
		state = accounts.DefaultSellerState
	}

	ctx := c.Request.Context()
	saved, err := s.store.Q.UpsertAccountsReportSettings(ctx, gen.UpsertAccountsReportSettingsParams{
		BrandSlug: brand, Enabled: req.Enabled,
		ToAddresses: to, CcAddresses: cc, BccAddresses: bcc,
		GstPercent: floatToNumeric(rate), SellerState: state,
		UpdatedBy: ptr(currentUserID(c)),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the report settings.")
		return
	}

	// WHO CHANGED THE RECIPIENTS, recorded loudly. This list decides who
	// receives the shop's complete sales figures, and the only trace of a
	// change otherwise is the row itself.
	obs.FromContext(ctx).Info("accounts report settings saved",
		"brand", brand, "enabled", saved.Enabled,
		"to", len(saved.ToAddresses), "cc", len(saved.CcAddresses),
		"bcc", len(saved.BccAddresses), "by", currentUserID(c))

	s.getAccountsReport(c)
}

func (s *Server) sendAccountsReportNow(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()

	settings, err := s.store.Q.GetAccountsReportSettings(ctx, brand)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Save the report settings before sending one.")
		return
	}
	if len(settings.ToAddresses) == 0 {
		detail(c, http.StatusUnprocessableEntity,
			"There is nobody to send it to. Add a To address first.")
		return
	}

	from, to := accounts.Window(time.Now(), reportZone)
	out, sendErr := s.SendAccountsReport(ctx, settings, from, to)
	if sendErr != nil {
		_ = s.store.Q.RecordAccountsReportFailure(ctx, gen.RecordAccountsReportFailureParams{
			BrandSlug: brand, LastError: ptr(sendErr.Error()),
		})
		obs.FromContext(ctx).Warn("accounts report send failed",
			"brand", brand, "by", currentUserID(c), "error", sendErr)
		detail(c, http.StatusBadGateway, sendErr.Error())
		return
	}

	// A MANUAL SEND CLOSES THE WINDOW TOO. Leaving it open would have the
	// worker mail the same day again at noon, which is how an accountant
	// ends up with two registers for one day and has to work out which.
	_ = s.store.Q.RecordAccountsReportSent(ctx, gen.RecordAccountsReportSentParams{
		BrandSlug: brand, WindowEnd: timestamptzAt(to), RowCount: ptr(int32(out.Rows)),
	})
	obs.FromContext(ctx).Info("accounts report sent by hand",
		"brand", brand, "orders", out.Orders, "rows", out.Rows,
		"recipients", out.Recipients, "by", currentUserID(c))

	c.JSON(http.StatusOK, gin.H{
		"sent": true, "orders": out.Orders, "rows": out.Rows,
		"recipients": out.Recipients, "filename": out.Filename,
		"window_start": from.Format(time.RFC3339),
		"window_end":   to.Format(time.RFC3339),
	})
}

// cleanRecipients trims, drops blanks, de-duplicates and sanity-checks.
//
// SHAPE ONLY, not deliverability. A typo that is still a valid address cannot
// be caught here; what can be caught is the paste that brought a name, a
// newline or a stray semicolon along with it.
func cleanRecipients(in []string, label string) ([]string, string) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		// One box may hold several, separated however somebody typed them.
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
		}) {
			address := strings.TrimSpace(part)
			if address == "" {
				continue
			}
			at := strings.Index(address, "@")
			if at <= 0 || at == len(address)-1 || strings.Contains(address, "..") {
				return nil, label + ": " + address + " is not an email address."
			}
			if !strings.Contains(address[at:], ".") {
				return nil, label + ": " + address + " is not an email address."
			}
			key := strings.ToLower(address)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, address)
		}
	}
	if len(out) > maxRecipients {
		return nil, label + ": that is more than " + strconv.Itoa(maxRecipients) +
			" addresses. This report carries every sale the shop made."
	}
	return out, ""
}

func timeOrEmpty(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format(time.RFC3339)
}

func floatToNumeric(v float64) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(formatRate(v)); err != nil {
		_ = n.Scan("18.00")
	}
	return n
}

// formatRate renders the rate for the numeric column. Via a string because
// pgtype.Numeric parses one exactly, where a float64 round-trip can land on
// 17.999999999999996 and store a rate nobody typed.
func formatRate(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}
