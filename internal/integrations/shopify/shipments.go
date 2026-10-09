package shopify

// Which parcels the store has sent, and the waybill on each.
//
// WHY THIS EXISTS AS ITS OWN QUERY. Delhivery has no list-by-status endpoint:
// it answers "parameter ref_ids/ref_nos or waybill is required" to any attempt
// to ask which shipments are stuck. So the set of waybills to track has to come
// from the store, and Shopify is where it lives - every fulfilment carries
// trackingInfo { company, number }.
//
// It is NOT folded into listOrdersQuery. That query feeds the order sync and
// is already near Shopify's cost ceiling; adding fulfilments to it would make
// every sync pay for data the sync does not use, and this page would still need
// a different window and a different filter.

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// ShipmentPageSize is Shopify's per-page cap for an orders query.
const shipmentPageSize = 250

// maxShipmentOrders bounds one listing.
//
// Each tracked waybill costs a slot in a Delhivery request (50 per call), so
// this is also a ceiling on how many carrier calls one page load makes.
//
// MEASURED, and the first value was wrong: thirty days on this store is 500
// fulfilled orders, so a cap of 500 silently truncated the window - the page
// looked complete and was missing whatever fell off the end. 1500 is three
// times that, and the cost of the ceiling only appears if a store ever
// actually reaches it.
const maxShipmentOrders = 1500

// Shipment is one parcel the store has handed to a carrier, with the order it
// belongs to.
//
// Everything here is Shopify's half. The carrier's half - where the parcel is,
// why it has not arrived - is tracked separately and joined on Waybill.
type Shipment struct {
	OrderID   int64  `json:"order_id"`
	OrderName string `json:"order_name"`
	// Waybill is the carrier's tracking number, and the join key.
	Waybill     string     `json:"waybill"`
	Carrier     string     `json:"carrier"`
	TrackingURL string     `json:"tracking_url"`
	ShippedAt   *time.Time `json:"shipped_at"`
	OrderedAt   *time.Time `json:"ordered_at"`
	// ShopifyStatus is the carrier state Shopify last heard, which lags the
	// carrier by hours. It is carried so the page can say when the two
	// disagree rather than quietly preferring one.
	ShopifyStatus string `json:"shopify_status"`

	CustomerName string `json:"customer_name"`
	// Phone and Email are what somebody would use to reach the customer about
	// a parcel that could not be delivered. Protected customer data: Shopify
	// returns these as null until the app has protected-data access, without
	// failing the query, so they may simply be empty.
	Phone   string `json:"phone"`
	Email   string `json:"email"`
	Address string `json:"address"`
	City    string `json:"city"`
	State   string `json:"state"`
	Pincode string `json:"pincode"`

	TotalAmount string             `json:"total_amount"`
	Currency    string             `json:"currency"`
	LineItems   []ShipmentLineItem `json:"line_items"`
	AdminURL    string             `json:"admin_url"`
}

// ShipmentLineItem is what is in the parcel.
type ShipmentLineItem struct {
	Title        string `json:"title"`
	VariantTitle string `json:"variant_title"`
	SKU          string `json:"sku"`
	Quantity     int    `json:"quantity"`
}

// fulfillments is an ARRAY on Order, not a connection - it has no nodes or
// pageInfo, and asking for them is a query error. first:10 is the argument it
// does take. Verified against the live store.
//
// `customer` is deliberately omitted, for the same reason listOrdersQuery omits
// it: it needs read_customers, and asking without that scope puts an
// ACCESS_DENIED entry in the errors array, which fails the whole call rather
// than one field. The shipping address carries the name instead.
const listShipmentsQuery = `query ListShipments($first: Int!, $query: String, $after: String) {
  orders(first: $first, sortKey: CREATED_AT, reverse: true, query: $query, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      name
      createdAt
      email
      phone
      displayFulfillmentStatus
      totalPriceSet { shopMoney { amount currencyCode } }
      shippingAddress { name address1 address2 city province zip country phone }
      lineItems(first: 20) {
        nodes { title variantTitle sku quantity }
      }
      fulfillments(first: 10) {
        createdAt
        displayStatus
        trackingInfo { company number url }
      }
    }
  }
}`

type shipmentsResponse struct {
	Data struct {
		Orders struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []shipmentOrderNode `json:"nodes"`
		} `json:"orders"`
	} `json:"data"`
}

type shipmentOrderNode struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	// Shopify's own fulfilment rollup, not the carrier's.
	DisplayFulfillmentStatus string        `json:"displayFulfillmentStatus"`
	TotalPriceSet            money         `json:"totalPriceSet"`
	ShippingAddress          *OrderAddress `json:"shippingAddress"`
	LineItems                struct {
		Nodes []struct {
			Title        string `json:"title"`
			VariantTitle string `json:"variantTitle"`
			SKU          string `json:"sku"`
			Quantity     int    `json:"quantity"`
		} `json:"nodes"`
	} `json:"lineItems"`
	Fulfillments []struct {
		CreatedAt     string `json:"createdAt"`
		DisplayStatus string `json:"displayStatus"`
		TrackingInfo  []struct {
			Company string `json:"company"`
			Number  string `json:"number"`
			URL     string `json:"url"`
		} `json:"trackingInfo"`
	} `json:"fulfillments"`
}

// ListShipments returns every waybill the store has handed out since a date,
// newest order first. carrier filters on the tracking company, case-insensitively;
// empty returns every carrier.
//
// One Shipment PER WAYBILL, not per order: a split fulfilment ships two parcels
// under one order and each has its own tracking number and its own fate.
func (c *Client) ListShipments(
	ctx context.Context, shop, token string, since time.Time, carrier string,
) ([]Shipment, error) {
	query := "fulfillment_status:shipped"
	if !since.IsZero() {
		query = "created_at:>=" + since.Format("2006-01-02") + " AND " + query
	}

	var (
		out    []Shipment
		after  string
		orders int
	)
	for orders < maxShipmentOrders {
		page := maxShipmentOrders - orders
		if page > shipmentPageSize {
			page = shipmentPageSize
		}

		vars := map[string]any{"first": page, "query": query}
		if after != "" {
			vars["after"] = after
		}

		var resp shipmentsResponse
		if err := c.do(ctx, shop, token, listShipmentsQuery, vars, &resp); err != nil {
			// No partial list. This one renders a page that says "these are
			// the parcels that did not arrive", and a short answer there reads
			// as good news.
			return nil, err
		}

		for _, node := range resp.Data.Orders.Nodes {
			orders++
			out = append(out, shipmentsFrom(shop, node, carrier)...)
		}

		info := resp.Data.Orders.PageInfo
		if !info.HasNextPage || info.EndCursor == "" || info.EndCursor == after {
			// EndCursor == after guards a server that keeps promising another
			// page while returning the same one.
			break
		}
		after = info.EndCursor
	}
	return out, nil
}

// shipmentsFrom flattens one order into its parcels.
func shipmentsFrom(shop string, node shipmentOrderNode, carrier string) []Shipment {
	base := Shipment{
		OrderID:       gidNumericID(node.ID),
		OrderName:     node.Name,
		ShopifyStatus: node.DisplayFulfillmentStatus,
		Phone:         strings.TrimSpace(node.Phone),
		Email:         strings.TrimSpace(node.Email),
		TotalAmount:   node.TotalPriceSet.ShopMoney.Amount,
		Currency:      node.TotalPriceSet.ShopMoney.CurrencyCode,
	}
	if t, err := time.Parse(time.RFC3339, node.CreatedAt); err == nil {
		base.OrderedAt = &t
	}
	if a := node.ShippingAddress; a != nil {
		base.CustomerName = strings.TrimSpace(a.Name)
		base.Address = strings.TrimSpace(strings.Trim(a.Address1+", "+a.Address2, ", "))
		base.City, base.State, base.Pincode = a.City, a.Province, a.Zip
		if base.Phone == "" {
			base.Phone = strings.TrimSpace(a.Phone)
		}
	}
	for _, li := range node.LineItems.Nodes {
		base.LineItems = append(base.LineItems, ShipmentLineItem{
			Title: li.Title, VariantTitle: li.VariantTitle, SKU: li.SKU, Quantity: li.Quantity,
		})
	}
	if base.OrderID > 0 {
		base.AdminURL = "https://" + shop + "/admin/orders/" + strconv.FormatInt(base.OrderID, 10)
	}

	want := strings.ToLower(strings.TrimSpace(carrier))
	out := make([]Shipment, 0, len(node.Fulfillments))
	for _, f := range node.Fulfillments {
		for _, info := range f.TrackingInfo {
			number := strings.TrimSpace(info.Number)
			if number == "" {
				continue
			}
			if want != "" && !strings.Contains(strings.ToLower(info.Company), want) {
				continue
			}
			parcel := base
			parcel.Waybill = number
			parcel.Carrier = strings.TrimSpace(info.Company)
			parcel.TrackingURL = strings.TrimSpace(info.URL)
			if t, err := time.Parse(time.RFC3339, f.CreatedAt); err == nil {
				parcel.ShippedAt = &t
			}
			if f.DisplayStatus != "" {
				parcel.ShopifyStatus = f.DisplayStatus
			}
			out = append(out, parcel)
		}
	}
	return out
}
