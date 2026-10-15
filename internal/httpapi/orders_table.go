package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// The Orders table, one page at a time: GET /orders?page=N.
//
// Before this the page fetched every order and two more whole lists (the
// no-jobs and non-DNP sets) just to draw fifteen rows and five tab badges.
// Now the filters, the search and the page are all applied in Postgres, and
// the response carries one page plus the counts.

// Tab values, exactly as the frontend's Orders tabs send them.
const (
	ordersTabUnfulfilled = "unfulfilled"
	ordersTabDone        = "done"
	ordersTabNoJobs      = "no-jobs"
	ordersTabNonDnp      = "non-dnp"
)

// Payment statuses, as the frontend's toOrderStatus folds them.
var (
	refundedStatuses  = []string{"refunded", "partially_refunded"}
	cancelledStatuses = []string{"voided", "cancelled"}
)

// ordersTableFilter is a tab, expressed as CountOrdersTable's optional filters.
type ordersTableFilter struct {
	fulfilled      *bool
	withoutJobs    *bool
	onlyIDs        []uuid.UUID
	financialIn    []string
	financialNotIn []string
}

// orderTabFilter maps a tab to its filter. nonDnp is only consulted for the
// Non-DNP tab. An unknown tab selects nothing, which is what the table did
// with one: it compared it to each row's status and matched none.
func orderTabFilter(tab string, nonDnp []uuid.UUID) ordersTableFilter {
	yes, no := true, false
	switch tab {
	case "":
		return ordersTableFilter{}
	case ordersTabUnfulfilled:
		return ordersTableFilter{fulfilled: &no}
	case ordersTabDone:
		return ordersTableFilter{fulfilled: &yes}
	case ordersTabNoJobs:
		return ordersTableFilter{withoutJobs: &yes}
	case ordersTabNonDnp:
		return ordersTableFilter{onlyIDs: nonEmpty(nonDnp)}
	case "paid":
		return ordersTableFilter{financialIn: []string{"paid"}}
	case "refunded":
		return ordersTableFilter{financialIn: refundedStatuses}
	case "cancelled":
		return ordersTableFilter{financialIn: cancelledStatuses}
	case "pending":
		notPending := append(append([]string{"paid"}, refundedStatuses...), cancelledStatuses...)
		return ordersTableFilter{financialNotIn: notPending}
	default:
		return ordersTableFilter{onlyIDs: []uuid.UUID{}}
	}
}

// nonEmpty keeps an empty set an EMPTY ARRAY rather than nil: nil is NULL in
// the query, and NULL means "no filter" - the Non-DNP tab with no non-DNP
// orders must show none, not all.
func nonEmpty(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// wantsCounts is false only for an explicit counts=false: a client paging
// through a list it already has the badges for need not pay for them again.
func wantsCounts(c *gin.Context) bool { return c.Query("counts") != "false" }

func (s *Server) listOrdersTable(c *gin.Context, p offsetPage, source *string) {
	window, ok := parseTimeRange(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	tab := c.Query("tab")
	withCounts := wantsCounts(c)

	// The non-DNP set comes from Go's IsGeneratedProduct, never a second copy
	// of that rule in SQL. Read only when the tab or the badges need it.
	var nonDnp []uuid.UUID
	if tab == ordersTabNonDnp || withCounts {
		ids, err := s.orderIDsWithNonGeneratedItems(ctx, source)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list orders.")
			return
		}
		nonDnp = idList(ids)
	}

	f := orderTabFilter(tab, nonDnp)
	params := gen.CountOrdersTableParams{
		Source: source, Search: searchParam(c),
		ImportedFrom: window.From, ImportedTo: window.To,
		Fulfilled: f.fulfilled, WithoutJobs: f.withoutJobs, OnlyIds: f.onlyIDs,
		FinancialIn: f.financialIn, FinancialNotIn: f.financialNotIn,
	}
	out, err := s.ordersTablePage(ctx, params, p)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list orders.")
		return
	}
	if withCounts {
		counts, err := s.store.Q.CountOrdersByTab(ctx, gen.CountOrdersByTabParams{
			NonDnpIds: nonEmpty(nonDnp), Source: source,
		})
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not count orders.")
			return
		}
		out.Counts = map[string]int64{
			"": counts.AllOrders, ordersTabUnfulfilled: counts.Unfulfilled,
			ordersTabNoJobs: counts.NoJobs, ordersTabNonDnp: counts.NonDnp, ordersTabDone: counts.Done,
		}
	}
	c.JSON(http.StatusOK, out)
}

// ordersTablePage counts the filtered orders, clamps the page to what exists,
// and reads that page.
func (s *Server) ordersTablePage(
	ctx context.Context, f gen.CountOrdersTableParams, p offsetPage,
) (pageEnvelope[orderResponse], error) {
	total, err := s.store.Q.CountOrdersTable(ctx, f)
	if err != nil {
		return pageEnvelope[orderResponse]{}, err
	}
	p = p.within(total)
	rows, err := s.store.Q.ListOrdersTable(ctx, gen.ListOrdersTableParams{
		Source: f.Source, Search: f.Search, ImportedFrom: f.ImportedFrom, ImportedTo: f.ImportedTo,
		Fulfilled: f.Fulfilled, WithoutJobs: f.WithoutJobs, OnlyIds: f.OnlyIds,
		FinancialIn: f.FinancialIn, FinancialNotIn: f.FinancialNotIn,
		PageLimit: p.Size, PageOffset: p.Offset(),
	})
	if err != nil {
		return pageEnvelope[orderResponse]{}, err
	}
	items := make([]orderResponse, 0, len(rows))
	for _, o := range rows {
		items = append(items, orderDTO(o))
	}
	return newPageEnvelope(items, total, p), nil
}

func idList(set map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
