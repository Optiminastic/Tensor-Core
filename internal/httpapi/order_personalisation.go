package httpapi

// What the orders page says about the customer's thirty minutes.
//
// The window is the one part of an order's life the customer is inside, and
// until now the orders table said nothing about it: an operator taking a phone
// call about "the link you sent me" had to open the order, find a job, and ask
// that job for the link. The table now answers it directly - a live link while
// the window is open, and the fact that it closed once it has.
//
// THE LINK ITSELF IS NOT IN THE LIST. The list carries the state and the
// closing instant; the token is fetched only when somebody actually opens one.
// A page of fifty orders would otherwise put fifty live customer links into a
// response, a browser cache and every log between here and the operator, to
// serve the one that gets clicked.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// The states an order's window can be in, as the orders page reads them.
const (
	// personalisationWindowOpen - the customer can still change this order,
	// and the row offers the link they are looking at.
	personalisationWindowOpen = "open"
	// personalisationWindowDone - they were asked and the window has closed,
	// whether they answered or let it run out. Either way the values are
	// settled and the order has moved on.
	personalisationWindowDone = "done"
	// personalisationWindowNotSent - a link was claimed and the message
	// permanently refused. Said plainly rather than as "done", because
	// nobody asked this customer anything and the row should not imply
	// otherwise.
	personalisationWindowNotSent = "not_sent"
	// personalisationWindowChanged - the window closed AND the customer
	// corrected something while it was open.
	//
	// Split out of "done" because the two call for different care from the
	// person about to print. "Done" means the values are the ones the
	// customer typed at checkout; "Changed" means somebody edited a name
	// after ordering, and the plank will carry whatever they changed it to.
	// Both are closed windows, and reading them as the same thing is how a
	// correction gets printed as though nothing had happened.
	personalisationWindowChanged = "changed"
)

// stampPersonalisationWindows tells each order on the page where its window
// stands.
//
// Keyed off the responses rather than the rows, because the three list paths
// in listOrders produce three different sqlc row types and all three end up
// here. One query for the page, not one per order.
func (s *Server) stampPersonalisationWindows(ctx context.Context, out []orderResponse) {
	if len(out) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(out))
	index := make(map[uuid.UUID]int, len(out))
	for i := range out {
		id, err := uuid.Parse(out[i].ID)
		if err != nil {
			continue
		}
		ids = append(ids, id)
		index[id] = i
	}
	if len(ids) == 0 {
		return
	}

	rows, err := s.store.Q.ListPersonalisationLinkStatesForOrders(ctx, ids)
	if err != nil {
		// Not fatal. The orders page is still the orders page, and a missing
		// column costs an operator a click rather than correctness.
		obs.FromContext(ctx).Warn("could not read the personalisation windows",
			"error", err)
		return
	}

	for _, r := range rows {
		i, ok := index[r.OrderID]
		if !ok {
			continue
		}
		out[i].Personalisation = windowStateFor(r)
		if out[i].Personalisation == personalisationWindowOpen && r.ExpiresAt.Valid {
			when := r.ExpiresAt.Time.UTC().Format(time.RFC3339)
			out[i].PersonalisationWindowEndsAt = &when
		}
		// Only beside a dash. An order that WAS asked carries its own answer
		// in the pill, and a stale reason beside it would contradict it.
		if out[i].Personalisation == "" && r.SkipReason != nil {
			out[i].PersonalisationSkipReason = r.SkipReason
		}
	}
}

// windowStateFor is what the orders page column shows.
//
// Only a CLOSED window becomes "changed". While one is still open the row's
// job is to hand over the link, and the customer may yet edit again - the
// tally is not final until the window is.
func windowStateFor(r gen.ListPersonalisationLinkStatesForOrdersRow) string {
	state := windowStateOf(r)
	if state == personalisationWindowDone && r.Changed {
		return personalisationWindowChanged
	}
	return state
}

// windowStateOf is the one place "is this window open?" is decided.
//
// Deliberately the same test GetOpenPersonalisationLinkForOrder makes in SQL -
// sent or opened, and not yet expired - because the row that says "open" and
// the query that hands out the token have to agree. A row that says open and
// a token endpoint that returns nothing is a button that does nothing.
//
// 'pending' is NOT open: the row is claimed but the message has not left, so
// there is no link in anybody's hand yet.
func windowStateOf(r gen.ListPersonalisationLinkStatesForOrdersRow) string {
	switch strings.TrimSpace(r.Status) {
	case "":
		// No link row at all. The query is a LEFT JOIN from orders, so every
		// order asked about comes back whether or not anybody messaged it -
		// and an order nobody messaged must not fall through to the default
		// below and read as a window that closed.
		return ""
	case "sent", "opened":
		if r.ExpiresAt.Valid && r.ExpiresAt.Time.After(time.Now()) {
			return personalisationWindowOpen
		}
		// Past its instant but not yet swept. The customer cannot use it, so
		// it reads as closed rather than as a link that will refuse.
		return personalisationWindowDone
	case "failed":
		return personalisationWindowNotSent
	case "pending":
		// Claimed, not sent. Nothing to show and nothing to promise.
		return ""
	default:
		// confirmed, expired, and anything added later.
		return personalisationWindowDone
	}
}

// orderPersonalisationLink hands back the live link for ONE order.
//
// Its own request, reached only when somebody opens a link, for the reason in
// the file comment. order:read rather than production:read - this is the
// orders page, and whoever can see the customer's name and number on that row
// can see the link that was sent to them.
func (s *Server) orderPersonalisationLink(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.openLinkForOrder(c.Request.Context(), id))
}

// openLinkForOrder unseals an order's live link, or returns nothing.
//
// Shared with the per-job route, which asks the same question from the
// production table. Every "no" is the same empty answer rather than an error:
// a closed window, a service with no encryption key and a key that has since
// changed are all, to the person holding the mouse, simply no link.
func (s *Server) openLinkForOrder(
	ctx context.Context, orderID uuid.UUID,
) personalisationLinkResponse {
	link, err := s.store.Q.GetOpenPersonalisationLinkForOrder(ctx, orderID)
	if err != nil || link.TokenSealed == nil || s.secrets == nil {
		return personalisationLinkResponse{}
	}
	token, err := s.secrets.Open(*link.TokenSealed)
	if err != nil || strings.TrimSpace(token) == "" {
		return personalisationLinkResponse{}
	}
	out := personalisationLinkResponse{URL: personalisationLinkURL(s.customerLinkBase(), token)}
	if link.ExpiresAt.Valid {
		out.ExpiresAt = link.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	return out
}
