package httpapi

// Building and sending one brand's daily sales register.
//
// THE DAY RUNS NOON TO NOON, which is the shop's own definition: the print
// floor works past midnight, so a calendar day would split one evening's
// trading across two reports.
//
// READ FROM TENSOR'S OWN MIRROR, not live from Shopify. Orders sync every ten
// minutes and the register is judged on Shopify's created_at, so the mirror is
// complete for a window that closed before the worker woke. Querying Shopify
// again would re-fetch a day of orders to produce figures Tensor already has,
// and would bring Shopify's rate limit onto a job nobody is watching.
//
// A QUIET DAY STILL SENDS. A report that silently does not arrive is
// indistinguishable from one nobody generated, and that ambiguity is what let
// the personalisation sweep fail unnoticed for hours. An empty register is a
// positive statement: no orders, and the system is working.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/accounts"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/email"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"

	"github.com/jackc/pgx/v5/pgtype"
)

// reportZone is the shop's clock. Noon means noon in Mumbai, not in UTC.
//
// A FIXED ZONE rather than time.LoadLocation: the worker image is a slim
// Debian with no tzdata, and LoadLocation there returns an error that a
// careless caller turns into UTC - which would move every window by five and
// a half hours and quietly put a day's orders on the wrong report.
var reportZone = time.FixedZone("IST", 5*60*60+30*60)

// ReportZone exposes the shop's clock to the worker, so the window it asks
// about is the same one the report is built for. A second zone constant in
// cmd/ is how two halves of one feature come to disagree about what noon is.
func ReportZone() *time.Location { return reportZone }

// MailboxReady reports whether this process could send at all.
//
// Exported so the worker can say so AT STARTUP rather than at midday: a
// deployment that forgot the credentials should be obvious on the first page
// of the logs, not in one line nobody is watching six hours later.
func (s *Server) MailboxReady() bool { return s.mailer().Configured() }

// ReportOutcome is what one send did, for the caller to log or show.
type ReportOutcome struct {
	BrandSlug   string
	WindowStart time.Time
	WindowEnd   time.Time
	Orders      int
	Rows        int
	Recipients  int
	Filename    string
}

// mailer builds the SMTP client from the process environment.
func (s *Server) mailer() *email.Client {
	return email.New(email.Config{
		Host:     s.cfg.SMTPHost,
		Port:     s.cfg.SMTPPort,
		Username: s.cfg.SMTPUsername,
		Password: s.cfg.SMTPPassword,
		From:     s.cfg.SMTPFrom,
		FromName: s.cfg.SMTPFromName,
	})
}

// SendAccountsReport builds and sends one brand's register for one window.
//
// Exported so the worker, an operator pressing Send now, and a test can all
// drive the same path - there is no second code path for "send it manually",
// which is how two behaviours drift apart.
func (s *Server) SendAccountsReport(
	ctx context.Context, settings gen.AccountsReportSetting, from, to time.Time,
) (ReportOutcome, error) {
	out := ReportOutcome{BrandSlug: settings.BrandSlug, WindowStart: from, WindowEnd: to}

	mail := s.mailer()
	if !mail.Configured() {
		return out, fmt.Errorf(
			"no mailbox is configured on this service, so the register cannot be sent " +
				"(set SMTP_HOST, SMTP_USERNAME, SMTP_PASSWORD and SMTP_FROM)")
	}

	rows, err := s.store.Q.ListOrdersForAccountsWindow(ctx, gen.ListOrdersForAccountsWindowParams{
		BrandSlug:   &settings.BrandSlug,
		WindowStart: timestamptzAt(from),
		WindowEnd:   timestamptzAt(to),
	})
	if err != nil {
		return out, fmt.Errorf("read the window's orders: %w", err)
	}
	out.Orders = len(rows)

	orders := make([]accounts.Order, 0, len(rows))
	for _, r := range rows {
		orders = append(orders, accountsOrderFrom(r))
	}

	register := accounts.Build(orders, accounts.Options{
		GSTPercent:  numericToFloat(settings.GstPercent),
		SellerState: settings.SellerState,
	})
	out.Rows = len(register)

	book, err := accounts.WriteXLSX(register, reportZone)
	if err != nil {
		return out, fmt.Errorf("write the spreadsheet: %w", err)
	}
	out.Filename = accounts.Filename(to, reportZone)

	message := email.Message{
		To:      settings.ToAddresses,
		CC:      settings.CcAddresses,
		BCC:     settings.BccAddresses,
		Subject: reportSubject(to),
		Body:    reportBody(from, to, out.Orders, out.Rows),
		Attachments: []email.Attachment{{
			Filename:    out.Filename,
			ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			Content:     book,
		}},
	}
	out.Recipients = len(message.Recipients())

	if err := mail.Send(message); err != nil {
		return out, err
	}
	return out, nil
}

// accountsOrderFrom narrows a stored order to what the register needs.
func accountsOrderFrom(r gen.ListOrdersForAccountsWindowRow) accounts.Order {
	order := accounts.Order{
		Number:   r.OrderNumber,
		Customer: stringOrEmpty(r.CustomerName),
	}
	if r.PlacedAt.Valid {
		order.PlacedAt = r.PlacedAt.Time
	}

	// The shipping province decides CGST+SGST against IGST: place of supply
	// is where the goods go, not where the card was billed.
	var address shopify.OrderAddress
	if len(r.ShippingAddress) > 0 {
		_ = json.Unmarshal(r.ShippingAddress, &address)
	}
	order.State = address.Province

	var lines []shopify.OrderLineItem
	if len(r.LineItems) > 0 {
		_ = json.Unmarshal(r.LineItems, &lines)
	}
	for _, line := range lines {
		order.Lines = append(order.Lines, accounts.Line{
			Title:    lineTitle(line),
			SKU:      line.SKU,
			Quantity: line.Quantity,
			Gross:    lineGross(line),
		})
	}

	// An order whose line items did not survive the import would otherwise
	// vanish from the register entirely. One row for the whole order is wrong
	// in detail but right in total, and a missing sale is wrong in both.
	if len(order.Lines) == 0 {
		order.Lines = []accounts.Line{{
			Title:    "(line items unavailable)",
			Quantity: 1,
			Gross:    numericOrZero(r.TotalPrice),
		}}
	}
	return order
}

// lineTitle prefers the name the customer saw, variant and all.
func lineTitle(line shopify.OrderLineItem) string {
	name := strings.TrimSpace(line.Name)
	if name == "" {
		name = strings.TrimSpace(line.Title)
	}
	if v := strings.TrimSpace(line.VariantTitle); v != "" && !strings.Contains(name, v) {
		name = name + " - " + v
	}
	if name == "" {
		return "(unnamed item)"
	}
	return name
}

// lineGross is what was actually taken for the line.
//
// LineTotal FIRST, because it is after any discount. Falling back to the unit
// price times quantity would report the list price on a discounted sale, and
// the register would not reconcile with the bank.
func lineGross(line shopify.OrderLineItem) float64 {
	if total := parseMoney(line.LineTotal); total > 0 {
		return total
	}
	unit := parseMoney(line.DiscountedUnitPrice)
	if unit == 0 {
		unit = parseMoney(line.UnitPrice)
	}
	qty := line.Quantity
	if qty < 1 {
		qty = 1
	}
	return unit * float64(qty)
}

func parseMoney(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return f
}

func reportSubject(windowEnd time.Time) string {
	return fmt.Sprintf("Sales register - %s", windowEnd.In(reportZone).Format("02 Jan 2006"))
}

func reportBody(from, to time.Time, orders, rows int) string {
	var b strings.Builder
	b.WriteString("The daily sales register is attached.\n\n")
	fmt.Fprintf(&b, "Period: %s to %s (IST)\n",
		from.In(reportZone).Format("02 Jan 2006, 3:04 PM"),
		to.In(reportZone).Format("02 Jan 2006, 3:04 PM"))
	fmt.Fprintf(&b, "Orders: %d\n", orders)
	fmt.Fprintf(&b, "Line items: %d\n\n", rows)
	if orders == 0 {
		// Said explicitly. An empty attachment with no explanation reads as a
		// broken report rather than a quiet day.
		b.WriteString("No orders were placed in this period. " +
			"The attached sheet has its headings and no rows.\n\n")
	}
	b.WriteString("Sent automatically by Tensor.\n")
	return b.String()
}

// numericToFloat reads the stored rate.
//
// Falls back to the package default rather than to zero: a zero rate would
// silently report every sale as untaxed, which is a plausible-looking register
// that is wrong in every column.
func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// numericOrZero reads a money column. Zero on anything unreadable, which
// shows up as a zero-value row rather than as a panic in a worker.
func numericOrZero(n pgtype.Numeric) float64 {
	v, err := n.Float64Value()
	if err != nil || !v.Valid {
		return 0
	}
	return v.Float64
}

func numericToFloat(n pgtype.Numeric) float64 {
	v, err := n.Float64Value()
	if err != nil || !v.Valid || v.Float64 <= 0 {
		return accounts.DefaultGSTPercent
	}
	return v.Float64
}
