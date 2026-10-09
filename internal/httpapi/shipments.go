package httpapi

// Every parcel the store has shipped, and what the courier has done with it.
//
// EVERY delivery, not only the failed ones. The reason the shop cares about
// most is "Consignee unavailable" - Delhivery went to the door, nobody could
// take the parcel, and somebody has to ring the customer before it turns around
// and comes back - so those sort to the top and have their own filter. But the
// rest of the list is here too, because a page that only ever shows problems
// cannot be told apart from a page that is broken.
//
// HOW IT IS ASSEMBLED, and why it is not one call:
//
//	Delhivery CANNOT be asked which shipments are stuck. Its tracking endpoint
//	answers {"Error": "parameter ref_ids/ref_nos or waybill is required"} with
//	or without a status filter, and /api/cmu/get/ndr, /api/ndr/ and /api/v1/ndr/
//	all return its login page rather than JSON. Measured on the live account.
//
// So: Shopify for the waybills (every fulfilment carries trackingInfo),
// Delhivery to track them fifty at a time, and the filtering here. Two
// upstreams, one list.
//
// LIVE, NOT STORED. There is no Tensor-side state here yet - nothing marks a
// delivery as chased - so a table and a poller would buy staleness for
// nothing. The day this page grows a "called back" column is the day it
// earns storage.
//
// EVERY ROW CARRIES DELHIVERY'S OWN SENTENCE. The reason bucket is a filter
// over Status.Instructions, and Delhivery varies that wording; a phrase the
// classifier has not met shows up under "Other" with its text intact rather
// than vanishing or being mislabelled. See internal/integrations/delhivery/ndr.go.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// defaultShipmentWindowDays bounds the listing.
//
// Thirty days is well past the point where a parcel is still deliverable -
// Delhivery returns one to origin long before that - so anything older is
// history, and tracking it costs a slot in a carrier request to render a row
// nobody can act on.
const defaultShipmentWindowDays = 30

// deliveryCarrier is the courier Tensor reads, matched against the company name
// Shopify records on each fulfilment.
//
// THE STORE USES MORE THAN ONE. Measured: it also ships through "Blue Dart
// Surface" and "Bluedart" (booked via Shiprocket), with eleven-digit waybills
// that Delhivery does not recognise. Tracking those against a Delhivery key
// returns nothing, so without this filter every Blue Dart parcel would arrive
// on the page as an untrackable shipment - a dozen false alarms.
//
// They are not silently dropped either: the response counts them by carrier, so
// the page can say what it does not cover.
const deliveryCarrier = "delhivery"

// reasonUntracked is a Tensor-side bucket, not one of Delhivery's.
//
// It is its own bucket rather than folded into "other" because the two are
// opposite problems: "other" is a delivery Delhivery explained in words this
// code has not met, and this is a parcel Delhivery has never heard of. Counting
// them together would hide a broken booking inside a list of odd NDR wording.
const reasonUntracked = "untracked"

func (s *Server) registerShipments(r *gin.Engine) {
	g := r.Group("/brands/:slug/shipments")
	g.Use(s.guards.RequireUser())
	// order:read. This is an order's parcel and the customer's address and
	// phone number; the roles that may see orders are exactly the ones who
	// would ring somebody about a failed delivery.
	g.GET("", s.guards.RequirePermission(auth.OrderRead.Key()), s.listShipments)
}

type shipmentScanResponse struct {
	At          string `json:"at"`
	Status      string `json:"status"`
	Instruction string `json:"instruction"`
	Location    string `json:"location"`
	StatusCode  string `json:"status_code"`
	ScanType    string `json:"scan_type"`
}

type shipmentRowResponse struct {
	// The parcel.
	Waybill     string `json:"waybill"`
	Carrier     string `json:"carrier"`
	TrackingURL string `json:"tracking_url"`

	// Why it has not arrived. Reason is the bucket; Instruction is Delhivery's
	// own sentence and is always present.
	Reason      string `json:"reason"`
	ReasonLabel string `json:"reason_label"`
	Instruction string `json:"instruction"`
	// Status and StatusType are Delhivery's coarse state ("Pending", "UD").
	// StatusCode is its internal code, carried because it is the only stable
	// handle on a reason whose wording changes.
	Status     string `json:"status"`
	StatusType string `json:"status_type"`
	StatusCode string `json:"status_code"`
	StatusAt   string `json:"status_at"`
	Location   string `json:"location"`
	// Attempts is how many times the courier has already been out. A parcel on
	// its third attempt is a different conversation from one on its first.
	Attempts       int    `json:"attempts"`
	FirstAttemptAt string `json:"first_attempt_at"`
	PromisedAt     string `json:"promised_at"`
	ExpectedAt     string `json:"expected_at"`
	// Returning says the parcel has started back to the warehouse. Shown, not
	// hidden: it is the deadline on every row above it.
	Returning bool `json:"returning"`
	// Actionable is whether this row is somebody's work. The backend decides
	// it, not the page: it is the sort key, and the rule behind it is a
	// measurement about this carrier (see delhivery.Reason.Actionable), not a
	// presentation choice.
	Actionable bool `json:"actionable"`

	// Who to reach, and where it was going.
	CustomerName string `json:"customer_name"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	Address      string `json:"address"`
	City         string `json:"city"`
	State        string `json:"state"`
	Pincode      string `json:"pincode"`

	// The order behind it.
	OrderName   string `json:"order_name"`
	OrderID     int64  `json:"order_id"`
	AdminURL    string `json:"admin_url"`
	OrderedAt   string `json:"ordered_at"`
	ShippedAt   string `json:"shipped_at"`
	TotalAmount string `json:"total_amount"`
	Currency    string `json:"currency"`
	// CODAmount is what the courier still has to collect. Zero on a pre-paid
	// order, and it changes what somebody says on the phone.
	CODAmount float64                    `json:"cod_amount"`
	LineItems []shopify.ShipmentLineItem `json:"line_items"`

	// Scans is the full carrier trail, for the detail panel.
	Scans []shipmentScanResponse `json:"scans"`

	// ShopifyStatus is what the store last heard from the carrier, which lags
	// by hours. Carried so the page can show the disagreement rather than
	// quietly preferring one side.
	ShopifyStatus string `json:"shopify_status"`
	// Tracked is false when Shopify handed out a waybill that Delhivery does
	// not recognise - a parcel booked under another account, or a tracking
	// number typed in by hand. Those are a real problem and are NOT silently
	// dropped.
	Tracked bool `json:"tracked"`
}

type shipmentsListResponse struct {
	Items []shipmentRowResponse `json:"items"`
	Shop  string                `json:"shop"`
	// Counts is every reason bucket with at least one parcel in it - computed
	// over the WHOLE window, not over the filtered list, so the chips keep
	// their numbers while one of them is selected.
	Counts map[string]int `json:"counts"`
	// Actionable is how many of those are somebody's work: the number the page
	// leads with, and the one that is zero on a good day.
	Actionable int `json:"actionable"`
	// Tracked and Shipped say how much was looked at. A page showing nothing is
	// then readable: "0 of 112 tracked" is a working integration with good
	// news, "0 of 0" is a window with no parcels in it.
	Shipped int `json:"shipped"`
	Tracked int `json:"tracked"`
	// OtherCarriers is every courier in the window that is NOT Delhivery, with
	// its parcel count. The store ships some orders by Blue Dart, and this page
	// cannot see where those are; saying so is better than a page that looks
	// complete and is not.
	OtherCarriers map[string]int `json:"other_carriers"`
	WindowDays    int            `json:"window_days"`
	GeneratedAt   string         `json:"generated_at"`
}

func (s *Server) listShipments(c *gin.Context) {
	ctx := c.Request.Context()
	slug := c.Param("slug")

	days := defaultShipmentWindowDays
	if raw := c.Query("days"); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed <= 0 || parsed > 180 {
			detail(c, http.StatusUnprocessableEntity,
				"days must be a whole number between 1 and 180.")
			return
		}
		days = parsed
	}

	// ASSEMBLED ONCE PER WINDOW, FILTERED PER REQUEST. The filter chips on the
	// page are server-rendered links, so without this split every chip click
	// re-read three Shopify pages and fifteen Delhivery requests - which is
	// what earned an HTTP 429 from the carrier. See shipment_cache.go.
	snapshot, err := s.shipmentCache.get(ctx,
		shipmentCacheKey(slug, days),
		c.Query("refresh") != "",
		func(fetchCtx context.Context) (*shipmentSnapshot, error) {
			return s.assembleShipments(fetchCtx, slug, days)
		})
	if err != nil {
		status := http.StatusBadGateway
		var notConnected *shipmentsNotConnectedError
		if errors.As(err, &notConnected) {
			status = notConnected.status
		}
		// The upstream's own sentence. Shopify names the scope it refused and
		// Delhivery says when it is throttling, and that is what the reader
		// can act on - "failed (429)" is not.
		detail(c, status, err.Error())
		return
	}

	wanted := c.Query("reason")
	items := make([]shipmentRowResponse, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		// Nothing is filtered out by default. A bucket asked for explicitly
		// narrows the list, which is what the page's chips do.
		if wanted != "" && wanted != row.Reason {
			continue
		}
		items = append(items, row)
	}

	c.JSON(http.StatusOK, shipmentsListResponse{
		Items:         items,
		Shop:          snapshot.Shop,
		Counts:        snapshot.Counts,
		Actionable:    snapshot.Actionable,
		Shipped:       snapshot.Shipped,
		Tracked:       snapshot.Tracked,
		OtherCarriers: snapshot.OtherCarriers,
		WindowDays:    days,
		// The moment the CARRIER was read, not the moment this request was
		// served. The page says "as of" from this, and a timestamp that
		// refreshed on every view while the data did not would be a lie.
		GeneratedAt: snapshot.At.UTC().Format(time.RFC3339),
	})
}

// shipmentsNotConnectedError is a missing integration rather than an upstream
// failure, and carries the status that says so.
//
// It exists because the assembly now runs behind a cache, which only returns an
// error - the handler can no longer write its own 409 or 503 inline.
type shipmentsNotConnectedError struct {
	status  int
	message string
}

func (e *shipmentsNotConnectedError) Error() string { return e.message }

// assembleShipments reads both upstreams and classifies the result.
//
// Shopify for the waybills, Delhivery to track them, the classifier over the
// carrier's own sentence. Nothing here is filtered: the snapshot holds every
// parcel, and the handler narrows it.
func (s *Server) assembleShipments(
	ctx context.Context, slug string, days int,
) (*shipmentSnapshot, error) {
	conn, err := s.store.Q.GetConnectionWithToken(ctx,
		gen.GetConnectionWithTokenParams{BrandSlug: slug, Provider: shopifyProvider})
	shop, token, connected := shopifyCredentials(conn, err)
	if !connected {
		return nil, &shipmentsNotConnectedError{
			status: http.StatusConflict,
			message: "This brand has no connected Shopify store, and Shopify is where " +
				"the waybills come from. Connect one in Settings first.",
		}
	}

	courier := s.delhiveryFor(ctx, slug)
	if !courier.Configured() {
		return nil, &shipmentsNotConnectedError{
			status: http.StatusServiceUnavailable,
			message: "Delhivery is not connected for this brand, so Tensor cannot read " +
				"where any parcel is. Add the API key in Settings → Integrations.",
		}
	}

	// Every carrier, then split - rather than asking Shopify for Delhivery
	// only. The parcels this page cannot speak for are worth counting, and
	// they cost nothing extra: the filtering is local, the query is the same.
	allParcels, err := s.shopify.ListShipments(ctx, shop, token,
		time.Now().AddDate(0, 0, -days), "")
	if err != nil {
		return nil, err
	}

	parcels := make([]shopify.Shipment, 0, len(allParcels))
	otherCarriers := map[string]int{}
	for _, p := range allParcels {
		if strings.Contains(strings.ToLower(p.Carrier), deliveryCarrier) {
			parcels = append(parcels, p)
			continue
		}
		name := strings.TrimSpace(p.Carrier)
		if name == "" {
			name = "Unnamed carrier"
		}
		otherCarriers[name]++
	}

	waybills := make([]string, 0, len(parcels))
	for _, p := range parcels {
		waybills = append(waybills, p.Waybill)
	}

	tracked, err := courier.Track(ctx, waybills)
	if err != nil {
		return nil, err
	}
	byWaybill := make(map[string]delhivery.Shipment, len(tracked))
	for _, t := range tracked {
		byWaybill[t.AWB.String()] = t
	}

	counts := map[string]int{}
	actionable := 0
	rows := make([]shipmentRowResponse, 0, len(parcels))

	for _, parcel := range parcels {
		carrierView, found := byWaybill[parcel.Waybill]
		if !found {
			// Shopify says this shipped; Delhivery has never heard of the
			// waybill. Reported rather than dropped - it means the parcel is
			// untrackable, which is worse than a failed delivery, not better.
			row := untrackedRow(parcel)
			counts[row.Reason]++
			actionable++
			rows = append(rows, row)
			continue
		}

		reason := delhivery.Classify(carrierView)
		counts[string(reason)]++
		if reason.Actionable() {
			actionable++
		}
		rows = append(rows, shipmentRow(parcel, carrierView, reason))
	}

	// PROBLEMS FIRST, then everything else - because the list holds every
	// delivery, and a page sorted purely by date buries the one parcel
	// somebody has to act on today under forty that are simply in transit.
	//
	// Within the problems, OLDEST first: a parcel stuck for four days is
	// closer to turning around than one that failed this morning, so it is the
	// one to ring about first. Within the rest, newest first, which is the
	// ordering every other list in this product uses.
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Actionable, rows[j].Actionable
		if left != right {
			return left
		}
		if left {
			return rows[i].StatusAt < rows[j].StatusAt
		}
		return rows[i].StatusAt > rows[j].StatusAt
	})

	return &shipmentSnapshot{
		Shop: shop, Rows: rows, Counts: counts, Actionable: actionable,
		Shipped: len(parcels), Tracked: len(tracked),
		OtherCarriers: otherCarriers, At: time.Now(),
	}, nil
}

// stamp renders one of Delhivery's timestamps, or "" when the event has not
// happened. Empty rather than a zero date: "attempted in year 1" is a worse lie
// than "not attempted".
func stamp(t delhivery.Stamp) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func instant(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// shipmentRow merges the store's half and the carrier's half into one row.
//
// The CARRIER wins on the consignee, the STORE wins on the order. Delhivery's
// consignee is the name and address the parcel is physically labelled with,
// which is what the courier knocked on; Shopify's shipping address is protected
// customer data and can come back null entirely. Each side is the fallback for
// the other, field by field, so a row is never blank where either knows the
// answer.
func shipmentRow(
	parcel shopify.Shipment, carrierView delhivery.Shipment, reason delhivery.Reason,
) shipmentRowResponse {
	or := func(primary, fallback string) string {
		if primary != "" {
			return primary
		}
		return fallback
	}

	row := shipmentRowResponse{
		Waybill:     parcel.Waybill,
		Carrier:     or(parcel.Carrier, "Delhivery"),
		TrackingURL: parcel.TrackingURL,

		Reason:      string(reason),
		ReasonLabel: reason.Label(),
		Instruction: carrierView.Status.Instructions.String(),
		Actionable:  reason.Actionable() && !carrierView.Delivered(),
		Status:      carrierView.Status.Status.String(),
		StatusType:  carrierView.Status.StatusType.String(),
		StatusCode:  carrierView.Status.StatusCode.String(),
		StatusAt:    stamp(carrierView.Status.StatusDateTime),
		Location:    carrierView.Status.StatusLocation.String(),

		Attempts:       carrierView.Attempts(),
		FirstAttemptAt: stamp(carrierView.FirstAttemptDate),
		PromisedAt:     stamp(carrierView.PromisedDeliveryDate),
		ExpectedAt:     stamp(carrierView.ExpectedDeliveryDate),
		Returning:      carrierView.Returning(),

		CustomerName: or(carrierView.Consignee.Name.String(), parcel.CustomerName),
		Phone:        or(parcel.Phone, carrierView.Consignee.Phone()),
		Email:        parcel.Email,
		Address:      or(carrierView.Consignee.Address(), parcel.Address),
		City:         or(carrierView.Consignee.City.String(), parcel.City),
		State:        or(carrierView.Consignee.State.String(), parcel.State),
		Pincode:      or(carrierView.Consignee.PinCode.String(), parcel.Pincode),

		OrderName:     or(parcel.OrderName, carrierView.ReferenceNo.String()),
		OrderID:       parcel.OrderID,
		AdminURL:      parcel.AdminURL,
		OrderedAt:     instant(parcel.OrderedAt),
		ShippedAt:     instant(parcel.ShippedAt),
		TotalAmount:   parcel.TotalAmount,
		Currency:      parcel.Currency,
		CODAmount:     carrierView.CODAmount,
		LineItems:     parcel.LineItems,
		ShopifyStatus: parcel.ShopifyStatus,
		Tracked:       true,
	}

	for _, scan := range carrierView.History() {
		row.Scans = append(row.Scans, shipmentScanResponse{
			At:          stamp(scan.ScanDateTime),
			Status:      scan.Scan.String(),
			Instruction: scan.Instructions.String(),
			Location:    scan.ScannedLocation.String(),
			StatusCode:  scan.StatusCode.String(),
			ScanType:    scan.ScanType.String(),
		})
	}
	// Newest scan first in the detail panel - the opposite of the row order,
	// deliberately. On a list you look for the oldest problem; inside one
	// parcel you look for the latest event.
	sort.SliceStable(row.Scans, func(i, j int) bool { return row.Scans[i].At > row.Scans[j].At })
	return row
}

// untrackedRow is a waybill Shopify handed out that Delhivery does not know.
func untrackedRow(parcel shopify.Shipment) shipmentRowResponse {
	return shipmentRowResponse{
		Waybill:     parcel.Waybill,
		Carrier:     parcel.Carrier,
		TrackingURL: parcel.TrackingURL,

		Reason:      reasonUntracked,
		ReasonLabel: "Not tracked",
		Actionable:  true,
		// MEASURED: Delhivery stops recognising a waybill after roughly six
		// weeks. Waybills from 15-19 August were gone on 9 October while ones
		// from 25-28 August still tracked, so on a long window this bucket
		// fills with parcels that simply aged out - which is why the sentence
		// says so rather than implying somebody made a mistake.
		Instruction: "Delhivery does not recognise this waybill under the connected " +
			"account. It may have been booked elsewhere, typed in by hand, or aged " +
			"out of the carrier's tracking window.",
		// Dated from the fulfilment so the row sorts with the rest rather than
		// collecting at one end of the list on an empty timestamp.
		StatusAt: instant(parcel.ShippedAt),

		CustomerName: parcel.CustomerName,
		Phone:        parcel.Phone,
		Email:        parcel.Email,
		Address:      parcel.Address,
		City:         parcel.City,
		State:        parcel.State,
		Pincode:      parcel.Pincode,

		OrderName:     parcel.OrderName,
		OrderID:       parcel.OrderID,
		AdminURL:      parcel.AdminURL,
		OrderedAt:     instant(parcel.OrderedAt),
		ShippedAt:     instant(parcel.ShippedAt),
		TotalAmount:   parcel.TotalAmount,
		Currency:      parcel.Currency,
		LineItems:     parcel.LineItems,
		ShopifyStatus: parcel.ShopifyStatus,
		Tracked:       false,
	}
}
