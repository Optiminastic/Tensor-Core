package httpapi

// Delhivery's own exception queue, with Delhivery's own tabs.
//
// A SEPARATE ROUTER GROUP from /brands/:slug/shipments, which already owns
// "/:waybill/ndr". A static "exceptions" segment beside that wildcard is the
// kind of route pair that works until gin's matcher is upgraded, and the cost
// of avoiding it is one word in a path.
//
// THE TABS ARE DELHIVERY'S, NOT OURS. Each row carries an "action" field
// holding the tab Delhivery files it under, and that string is passed straight
// through. The alternative - classifying Delhivery's status codes here - is
// exactly what produced the mismatch this endpoint exists to fix: Tensor
// listed three parcels as needing a decision while Delhivery's Need Action tab
// was empty, because Delhivery had already booked an appointment for one and
// owned the other two itself.
//
// THE ORDER NAME IS A BONUS, NOT A DEPENDENCY. Delhivery knows waybills; the
// shop thinks in order numbers. The existing cached shipment snapshot maps one
// to the other, so it is consulted - but a failure there leaves the waybill
// showing alone rather than failing the page. Delhivery's queue is the answer
// to the question; the order number is a convenience on top of it.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// exceptionTabs are the three tabs of open work, in the order Delhivery shows
// them. Closed is deliberately not here: it is history, and it is fetched only
// when somebody asks for it.
var exceptionTabs = []delhivery.Tab{
	delhivery.TabNeedAction,
	delhivery.TabAutoReattempt,
	delhivery.TabReturnRecommendation,
}

// exceptionEnrichWindow is how far back the order lookup goes. It only affects
// whether a row can show an order number, never whether the row appears.
const exceptionEnrichWindow = 90

func (s *Server) registerShipmentExceptions(r *gin.Engine) {
	g := r.Group("/brands/:slug/shipment-exceptions")
	g.Use(s.guards.RequireUser())
	// Reading is order:read, as on the shipments list: this is an order's
	// parcel and the people who may see orders are the ones who would ring
	// somebody about a failed delivery.
	g.GET("", s.guards.RequirePermission(auth.OrderRead.Key()), s.listShipmentExceptions)
	// Acting spends money and sends a van, or gives up on a parcel entirely.
	g.POST("/:id/cta",
		s.guards.RequirePermission(auth.DispatchManage.Key()), s.actOnShipmentException)
}

type exceptionResponse struct {
	ID      string `json:"id"`
	Waybill string `json:"waybill"`
	// Tab is Delhivery's own filing, spelled as they spell it.
	Tab        string `json:"tab"`
	Bucket     string `json:"bucket"`
	BucketName string `json:"bucket_name"`
	// Actions are the buttons, taken from the row's own cta_list so a bucket
	// whose actions differ still works without a code table here to go stale.
	Actions []exceptionActionResponse `json:"actions"`

	Reason     string `json:"reason"`
	Status     string `json:"status"`
	Payment    string `json:"payment"`
	Attempts   int    `json:"attempts"`
	Actionable string `json:"actionable"`

	RaisedAt    string `json:"raised_at"`
	SLAAt       string `json:"sla_at"`
	PromisedAt  string `json:"promised_at"`
	LastUpdate  string `json:"last_update"`
	SLABreached bool   `json:"sla_breached"`

	// The proofs that decide whether a re-attempt is worth paying for.
	Geofence         bool    `json:"geofence"`
	GeofenceDistance float64 `json:"geofence_distance"`
	CallAttempted    bool    `json:"call_attempted"`
	CallDuration     int     `json:"call_duration"`
	WhatsAppVerified bool    `json:"whatsapp_verified"`

	// From Shopify, when the snapshot happens to know this waybill. Blank is
	// normal and is not an error.
	OrderName    string `json:"order_name"`
	CustomerName string `json:"customer_name"`
	City         string `json:"city"`
	AdminURL     string `json:"admin_url"`
	TrackingURL  string `json:"tracking_url"`
}

type exceptionActionResponse struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type exceptionsListResponse struct {
	// Connected is false when the brand has no Delhivery One credential. The
	// page then says so instead of showing an empty queue, which would read
	// as "nothing is stuck".
	Connected bool                   `json:"connected"`
	Tabs      []exceptionTabResponse `json:"tabs"`
	Enriched  bool                   `json:"enriched"`
	FetchedAt string                 `json:"fetched_at"`
}

type exceptionTabResponse struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
	// Error is Delhivery's own words when THIS tab failed while others
	// succeeded. One bad tab does not empty the page.
	Error string              `json:"error,omitempty"`
	Items []exceptionResponse `json:"items"`
}

func (s *Server) listShipmentExceptions(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	ctx := c.Request.Context()

	one := s.delhiveryUCPFor(ctx, brand)
	if !one.Configured() {
		// 200, not an error: "not connected" is a real answer to "what is in
		// the queue", and the page renders a setup notice from it.
		c.JSON(http.StatusOK, exceptionsListResponse{
			Connected: false,
			Tabs:      []exceptionTabResponse{},
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	tabs := exceptionTabs
	if c.Query("include_closed") != "" {
		tabs = append(append([]delhivery.Tab{}, tabs...), delhivery.TabClosed)
	}

	// THE TABS IN PARALLEL. Delhivery has no "give me every tab" call, so a
	// page with three tabs is three requests; run end to end they are three
	// round trips to somebody else's server on a screen somebody opens to
	// answer one question.
	results := make([][]delhivery.Exception, len(tabs))
	failures := make([]string, len(tabs))
	var group errgroup.Group
	group.SetLimit(len(tabs))
	for i, tab := range tabs {
		i, tab := i, tab
		group.Go(func() error {
			rows, err := one.ListExceptions(ctx, delhivery.ExceptionQuery{Tab: tab})
			if err != nil {
				// Recorded against the tab rather than returned: one tab
				// Delhivery will not answer for should not blank the other
				// two, and the page shows which one is missing.
				failures[i] = err.Error()
				obs.FromContext(ctx).Warn("delhivery exception tab failed",
					"brand", brand, "tab", string(tab), "error", err)
				return nil
			}
			results[i] = rows
			return nil
		})
	}
	_ = group.Wait()

	orders := s.waybillLookup(ctx, brand, results)

	out := exceptionsListResponse{
		Connected: true,
		Enriched:  len(orders) > 0,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Tabs:      make([]exceptionTabResponse, 0, len(tabs)),
	}
	for i, tab := range tabs {
		items := make([]exceptionResponse, 0, len(results[i]))
		for _, row := range results[i] {
			items = append(items, exceptionRow(row, orders[row.Waybill]))
		}
		out.Tabs = append(out.Tabs, exceptionTabResponse{
			Key:   string(tab),
			Label: exceptionTabLabel(tab),
			Count: len(items),
			Error: failures[i],
			Items: items,
		})
	}
	c.JSON(http.StatusOK, out)
}

// waybillLookup maps the waybills in the queue to what Shopify knows, best
// effort.
//
// BEST EFFORT IS THE WHOLE POINT. This reads the cached shipment snapshot,
// which needs a connected Shopify store AND the tracking API. A brand may have
// Delhivery One and neither of those, and the queue is still the answer to the
// question the page asks - so a failure here returns an empty map and every row
// shows its waybill without an order number.
func (s *Server) waybillLookup(
	ctx context.Context, brand string, results [][]delhivery.Exception,
) map[string]shipmentRowResponse {
	wanted := map[string]bool{}
	for _, rows := range results {
		for _, row := range rows {
			if row.Waybill != "" {
				wanted[row.Waybill] = true
			}
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	// Never a forced refresh: this is a convenience lookup riding on a cache
	// that exists to keep Delhivery from returning 429, and refreshing it
	// here would re-read three Shopify pages per exception page load.
	snapshot, err := s.shipmentCache.get(ctx,
		shipmentCacheKey(brand, exceptionEnrichWindow), false,
		func(fetchCtx context.Context) (*shipmentSnapshot, error) {
			return s.assembleShipments(fetchCtx, brand, exceptionEnrichWindow)
		})
	if err != nil || snapshot == nil {
		return nil
	}

	found := make(map[string]shipmentRowResponse, len(wanted))
	for _, row := range snapshot.Rows {
		if wanted[row.Waybill] {
			found[row.Waybill] = row
		}
	}
	return found
}

func exceptionRow(e delhivery.Exception, order shipmentRowResponse) exceptionResponse {
	actions := make([]exceptionActionResponse, 0, len(e.CTAList))
	for code, label := range e.CTAList {
		actions = append(actions, exceptionActionResponse{Code: code, Label: label})
	}
	// cta_list is a map, so its order is random; sorted by code so the
	// buttons do not move between page loads.
	sort.Slice(actions, func(i, j int) bool { return actions[i].Code < actions[j].Code })

	return exceptionResponse{
		ID:         e.ID,
		Waybill:    e.Waybill,
		Tab:        e.Action,
		Bucket:     e.Bucket,
		BucketName: e.BucketName,
		Actions:    actions,

		Reason:     e.Reason,
		Status:     e.Status,
		Payment:    e.Payment,
		Attempts:   e.Attempts(),
		Actionable: e.Actionable,

		RaisedAt:   epochSeconds(e.ExceptionTime),
		SLAAt:      epochSeconds(e.SLA),
		PromisedAt: epochSeconds(e.PromiseDate),
		LastUpdate: epochSeconds(e.LastUpdate),
		// Computed here rather than trusting default_cta, which reads
		// "Overdue" on rows whose SLA has not passed.
		SLABreached: e.SLA > 0 && time.Now().Unix() > e.SLA,

		Geofence:         e.Geofence,
		GeofenceDistance: e.GeofenceDistance,
		CallAttempted:    e.CallAttempted,
		CallDuration:     e.CallDuration,
		WhatsAppVerified: e.WhatsAppVerified,

		OrderName:    order.OrderName,
		CustomerName: order.CustomerName,
		City:         order.City,
		AdminURL:     order.AdminURL,
		TrackingURL:  order.TrackingURL,
	}
}

// epochSeconds formats Delhivery's epoch-second timestamps, and returns "" for
// the zero they use to mean "never".
func epochSeconds(v int64) string {
	if v <= 0 {
		return ""
	}
	return time.Unix(v, 0).UTC().Format(time.RFC3339)
}

func exceptionTabLabel(t delhivery.Tab) string {
	switch t {
	case delhivery.TabNeedAction:
		return "Need Action"
	case delhivery.TabAutoReattempt:
		return "Auto Reattempt"
	case delhivery.TabReturnRecommendation:
		return "Return Recommendation"
	case delhivery.TabClosed:
		return "Closed"
	}
	return string(t)
}

type exceptionCTARequest struct {
	// Code is a CTA from the row's own action list, e.g. CTA023.
	Code string `json:"code" binding:"required"`
	// Bucket is the row's bucket. Sent by the client because it comes from
	// the row, and defaulted when absent.
	Bucket string `json:"bucket"`
}

func (s *Server) actOnShipmentException(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" || len(id) > maxExceptionIDLen {
		detail(c, http.StatusUnprocessableEntity, "That is not an exception id.")
		return
	}

	var req exceptionCTARequest
	if !bindJSON(c, &req) {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		detail(c, http.StatusUnprocessableEntity, "Pick what the courier should do.")
		return
	}

	ctx := c.Request.Context()
	one := s.delhiveryUCPFor(ctx, brand)
	if !one.Configured() {
		detail(c, http.StatusServiceUnavailable,
			"Delhivery One is not connected for this brand, so nothing can be sent to the courier. "+
				"Add the One credential in Settings → Integrations.")
		return
	}

	if err := one.ApplyCTA(ctx, delhivery.CTAAction{
		ExceptionID: id, Bucket: strings.TrimSpace(req.Bucket), Code: code,
	}); err != nil {
		obs.FromContext(ctx).Warn("delhivery refused an exception action",
			"brand", brand, "exception", id, "code", code, "error", err)
		detail(c, http.StatusBadGateway, err.Error())
		return
	}

	// RECORDED BECAUSE NOTHING ELSE IS. There is no Tensor row for a
	// shipment or for an exception - both lists are assembled live - so this
	// is the only trace that a person asked for a van or for a return.
	obs.FromContext(ctx).Info("exception action submitted to delhivery",
		"brand", brand, "exception", id, "code", code, "by", currentUserID(c))

	// SUBMITTED, not done. Delhivery acts on the parcel out of band and the
	// row's tab changes on their schedule, not on this response.
	c.JSON(http.StatusOK, gin.H{"exception_id": id, "code": code, "submitted": true})
}

// maxExceptionIDLen bounds the path segment: Delhivery's ids are UUIDs.
const maxExceptionIDLen = 64
