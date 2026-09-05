package shopify

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// OrderSummary is one order pulled from Shopify's Admin GraphQL API for the
// one-time historical backfill that runs right after a store connects (see
// httpapi's shopifyCallback) - shaped independently of Shopify's REST webhook
// payload, which a caller converts an OrderSummary into before importing it
// the same way a live webhook delivery would be.
type OrderSummary struct {
	ID              int64
	Name            string
	FinancialStatus string
	TotalPrice      string
	Currency        string
	Customer        *OrderCustomer
	LineItems       []OrderLineItem

	// Everything Shopify's own order page shows beyond the header. Strings
	// rather than floats for money: Shopify sends decimal strings, and parsing
	// them here to re-render them later only adds a place to lose a paisa.
	CreatedAt         string
	Note              string
	Attributes        []OrderLineProp
	Tags              []string
	FulfillmentStatus string
	// DeliveryStatus is the carrier's view, from the first fulfilment. Empty
	// until something ships - an unfulfilled order has no fulfilments at all.
	DeliveryStatus  string
	ReturnStatus    string
	SourceName      string
	SubtotalPrice   string
	TotalDiscounts  string
	TotalShipping   string
	TotalReceived   string
	DiscountTitle   string
	ShippingTitle   string
	Email           string
	Phone           string
	ShippingAddress *OrderAddress
	BillingAddress  *OrderAddress
}

// OrderAddress is a shipping or billing address.
//
// Protected customer data: Shopify returns these as null until the app is
// granted protected-data access, WITHOUT failing the query. So they are
// requested unconditionally and simply arrive empty today - the day access is
// approved, a re-sync fills them in with no code change.
type OrderAddress struct {
	Name     string `json:"name,omitempty"`
	Address1 string `json:"address1,omitempty"`
	Address2 string `json:"address2,omitempty"`
	City     string `json:"city,omitempty"`
	Province string `json:"province,omitempty"`
	Zip      string `json:"zip,omitempty"`
	Country  string `json:"country,omitempty"`
	Phone    string `json:"phone,omitempty"`
}

// OrderCustomer is the customer attached to an OrderSummary, if any.
type OrderCustomer struct {
	ID        int64
	FirstName string
	LastName  string
	Email     string
	Phone     string
}

// OrderLineItem is one line item on an OrderSummary.
type OrderLineItem struct {
	ProductID  *int64
	SKU        string
	Name       string
	Title      string
	Quantity   int
	Properties []OrderLineProp

	// VariantTitle is the option string Shopify shows under the product name,
	// e.g. "BABY PINK / NO LIGHT". Empty on a product with no variants.
	VariantTitle string
	ImageURL     string
	// UnitPrice is the price before any discount - what Shopify strikes
	// through - and DiscountedUnitPrice is what was actually charged. Both are
	// kept so the page can show the same "was/now" the store does.
	UnitPrice           string
	DiscountedUnitPrice string
	LineTotal           string
}

// OrderLineProp is a Shopify line-item custom attribute (equivalent to the
// REST webhook's "properties" - print facts and personalisation are read
// from these).
type OrderLineProp struct {
	Name  string
	Value string
}

// ordersPageSize is Shopify's per-page cap for an orders query.
const ordersPageSize = 250

// maxSyncOrders bounds a whole sync, across pages.
//
// A ceiling rather than a page size: the sync is floored at a date (see
// shopifyImportSince), so it walks a bounded window - 177 orders on the store
// today - and paging stops as soon as Shopify says there is no next page. This
// only exists so that a misconfigured floor cannot turn one button press into a
// walk through the store's entire history.
//
// Note Shopify's `read_orders` scope itself only returns orders from the last
// 60 days unless the app has `read_all_orders` protected-data access.
const maxSyncOrders = 2000

// `customer` is deliberately omitted and must stay omitted: it needs the
// read_customers scope, and asking for it returns an ACCESS_DENIED entry in
// the response's errors array, which Client.do treats as a failed call - so
// one inaccessible field would cost the whole sync, not just that field.
// Verified against the live store.
//
// email, phone, shippingAddress and billingAddress are a different case and
// ARE requested: they are protected customer data, which Shopify answers with
// null rather than an error until the app has protected-data access. Also
// verified - the response carries no errors array. So they cost nothing today
// and start working the day access is granted, with no code change.
const listOrdersQuery = `query ListOrders($first: Int!, $query: String, $after: String) {
  orders(first: $first, sortKey: CREATED_AT, reverse: true, query: $query, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      name
      createdAt
      note
      displayFinancialStatus
      displayFulfillmentStatus
      returnStatus
      fulfillments(first: 1) { displayStatus }
      sourceName
      tags
      customAttributes { key value }
      email
      phone
      shippingAddress { name address1 address2 city province zip country phone }
      billingAddress { name address1 address2 city province zip country phone }
      subtotalPriceSet { shopMoney { amount } }
      totalDiscountsSet { shopMoney { amount } }
      totalShippingPriceSet { shopMoney { amount } }
      totalPriceSet { shopMoney { amount currencyCode } }
      totalReceivedSet { shopMoney { amount } }
      discountApplications(first: 1) {
        nodes {
          ... on DiscountCodeApplication { code }
          ... on AutomaticDiscountApplication { title }
          ... on ManualDiscountApplication { title }
          ... on ScriptDiscountApplication { title }
        }
      }
      shippingLines(first: 1) { nodes { title } }
      lineItems(first: 50) {
        nodes {
          sku
          name
          title
          variantTitle
          quantity
          image { url }
          originalUnitPriceSet { shopMoney { amount } }
          discountedUnitPriceSet { shopMoney { amount } }
          discountedTotalSet { shopMoney { amount } }
          product { id }
          customAttributes { key value }
        }
      }
    }
  }
}`

type ordersResponse struct {
	Data struct {
		Orders struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []orderNode `json:"nodes"`
		} `json:"orders"`
	} `json:"data"`
}

// money unwraps Shopify's {shopMoney:{amount}} envelope, which every monetary
// field on an order is wrapped in.
type money struct {
	ShopMoney struct {
		Amount       string `json:"amount"`
		CurrencyCode string `json:"currencyCode"`
	} `json:"shopMoney"`
}

type addressNode struct {
	Name     string `json:"name"`
	Address1 string `json:"address1"`
	Address2 string `json:"address2"`
	City     string `json:"city"`
	Province string `json:"province"`
	Zip      string `json:"zip"`
	Country  string `json:"country"`
	Phone    string `json:"phone"`
}

type attributeNode struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type orderNode struct {
	ID                       string `json:"id"`
	Name                     string `json:"name"`
	CreatedAt                string `json:"createdAt"`
	Note                     string `json:"note"`
	DisplayFinancialStatus   string `json:"displayFinancialStatus"`
	DisplayFulfillmentStatus string `json:"displayFulfillmentStatus"`
	ReturnStatus             string `json:"returnStatus"`
	Fulfillments             []struct {
		DisplayStatus string `json:"displayStatus"`
	} `json:"fulfillments"`
	SourceName            string          `json:"sourceName"`
	Tags                  []string        `json:"tags"`
	CustomAttributes      []attributeNode `json:"customAttributes"`
	Email                 string          `json:"email"`
	Phone                 string          `json:"phone"`
	ShippingAddress       *addressNode    `json:"shippingAddress"`
	BillingAddress        *addressNode    `json:"billingAddress"`
	SubtotalPriceSet      money           `json:"subtotalPriceSet"`
	TotalDiscountsSet     money           `json:"totalDiscountsSet"`
	TotalShippingPriceSet money           `json:"totalShippingPriceSet"`
	TotalPriceSet         money           `json:"totalPriceSet"`
	TotalReceivedSet      money           `json:"totalReceivedSet"`
	DiscountApplications  struct {
		Nodes []struct {
			// A discount is named by whichever of these its type carries: a
			// code discount has a code, every other kind has a title.
			Code  string `json:"code"`
			Title string `json:"title"`
		} `json:"nodes"`
	} `json:"discountApplications"`
	ShippingLines struct {
		Nodes []struct {
			Title string `json:"title"`
		} `json:"nodes"`
	} `json:"shippingLines"`
	LineItems struct {
		Nodes []struct {
			SKU          string `json:"sku"`
			Name         string `json:"name"`
			Title        string `json:"title"`
			VariantTitle string `json:"variantTitle"`
			Quantity     int    `json:"quantity"`
			Image        *struct {
				URL string `json:"url"`
			} `json:"image"`
			OriginalUnitPriceSet   money `json:"originalUnitPriceSet"`
			DiscountedUnitPriceSet money `json:"discountedUnitPriceSet"`
			DiscountedTotalSet     money `json:"discountedTotalSet"`
			Product                *struct {
				ID string `json:"id"`
			} `json:"product"`
			CustomAttributes []attributeNode `json:"customAttributes"`
		} `json:"nodes"`
	} `json:"lineItems"`
}

// ListRecentOrders fetches the store's most recently created orders (every
// financial status - a COD order sitting at "pending" is still a real order).
//
// createdAtMin, when non-zero, restricts the pull to orders placed on or after
// that date. Pressing "Sync from Shopify" repeatedly is the normal way orders
// enter Tensor, and without a floor every press re-imports the store's entire
// recent history to rediscover the handful of orders that are actually new.
// limit is capped at maxSyncOrders; zero/negative uses the cap, and pages are
// followed until Shopify reports no next page.
func (c *Client) ListRecentOrders(
	ctx context.Context, shop, token string, limit int, createdAtMin time.Time,
) ([]OrderSummary, error) {
	if limit <= 0 || limit > maxSyncOrders {
		limit = maxSyncOrders
	}

	var (
		summaries []OrderSummary
		after     string
	)
	for len(summaries) < limit {
		page := limit - len(summaries)
		if page > ordersPageSize {
			page = ordersPageSize
		}

		vars := map[string]any{"first": page}
		if !createdAtMin.IsZero() {
			// Shopify's search syntax, not a GraphQL argument. Date-only: the
			// store's own day boundary is what an operator means by "since
			// Wednesday", and a UTC timestamp would cut the day at the wrong
			// hour.
			vars["query"] = "created_at:>=" + createdAtMin.Format("2006-01-02")
		}
		if after != "" {
			vars["after"] = after
		}

		var out ordersResponse
		if err := c.do(ctx, shop, token, listOrdersQuery, vars, &out); err != nil {
			// Pages already fetched are NOT returned alongside the error. A
			// partial list looks exactly like a complete one to the caller, and
			// the caller's next step is to reconcile Tensor against it.
			return nil, err
		}

		for _, n := range out.Data.Orders.Nodes {
			summaries = append(summaries, toOrderSummary(n))
		}

		if !out.Data.Orders.PageInfo.HasNextPage || out.Data.Orders.PageInfo.EndCursor == "" {
			break
		}
		// Guards against a server that keeps claiming another page while
		// returning none, which would otherwise spin until the ceiling.
		if len(out.Data.Orders.Nodes) == 0 {
			break
		}
		after = out.Data.Orders.PageInfo.EndCursor
	}
	return summaries, nil
}

// toOrderSummary leaves Customer unset - the query above cannot request it
// without the read_customers scope. Addresses come through the protected-data
// fields instead, empty until that access is granted.
func toOrderSummary(n orderNode) OrderSummary {
	s := OrderSummary{
		ID:                gidNumericID(n.ID),
		Name:              n.Name,
		FinancialStatus:   strings.ToLower(n.DisplayFinancialStatus),
		TotalPrice:        n.TotalPriceSet.ShopMoney.Amount,
		Currency:          n.TotalPriceSet.ShopMoney.CurrencyCode,
		CreatedAt:         n.CreatedAt,
		Note:              n.Note,
		Tags:              n.Tags,
		FulfillmentStatus: strings.ToLower(n.DisplayFulfillmentStatus),
		ReturnStatus:      strings.ToLower(n.ReturnStatus),
		SourceName:        n.SourceName,
		SubtotalPrice:     n.SubtotalPriceSet.ShopMoney.Amount,
		TotalDiscounts:    n.TotalDiscountsSet.ShopMoney.Amount,
		TotalShipping:     n.TotalShippingPriceSet.ShopMoney.Amount,
		TotalReceived:     n.TotalReceivedSet.ShopMoney.Amount,
		Email:             n.Email,
		Phone:             n.Phone,
		ShippingAddress:   toOrderAddress(n.ShippingAddress),
		BillingAddress:    toOrderAddress(n.BillingAddress),
	}
	// The first fulfilment only. An order split across several shipments has
	// no single delivery status, and inventing one - "partly delivered" - would
	// be Tensor stating something Shopify never said.
	if len(n.Fulfillments) > 0 {
		s.DeliveryStatus = strings.ToLower(n.Fulfillments[0].DisplayStatus)
	}
	for _, a := range n.CustomAttributes {
		s.Attributes = append(s.Attributes, OrderLineProp{Name: a.Key, Value: a.Value})
	}
	if len(n.DiscountApplications.Nodes) > 0 {
		d := n.DiscountApplications.Nodes[0]
		s.DiscountTitle = firstNonEmpty(d.Title, d.Code)
	}
	if len(n.ShippingLines.Nodes) > 0 {
		s.ShippingTitle = n.ShippingLines.Nodes[0].Title
	}

	s.LineItems = make([]OrderLineItem, 0, len(n.LineItems.Nodes))
	for _, li := range n.LineItems.Nodes {
		item := OrderLineItem{
			SKU: li.SKU, Name: li.Name, Title: li.Title, Quantity: li.Quantity,
			VariantTitle:        li.VariantTitle,
			UnitPrice:           li.OriginalUnitPriceSet.ShopMoney.Amount,
			DiscountedUnitPrice: li.DiscountedUnitPriceSet.ShopMoney.Amount,
			LineTotal:           li.DiscountedTotalSet.ShopMoney.Amount,
		}
		if li.Image != nil {
			item.ImageURL = li.Image.URL
		}
		if li.Product != nil {
			id := gidNumericID(li.Product.ID)
			item.ProductID = &id
		}
		for _, a := range li.CustomAttributes {
			item.Properties = append(item.Properties, OrderLineProp{Name: a.Key, Value: a.Value})
		}
		s.LineItems = append(s.LineItems, item)
	}
	return s
}

// toOrderAddress maps an address node, treating an all-empty one as absent.
// Shopify returns a populated-looking object with every field blank when the
// app lacks protected-data access, and a panel of empty labels reads as a bug.
func toOrderAddress(a *addressNode) *OrderAddress {
	if a == nil {
		return nil
	}
	out := OrderAddress{
		Name: a.Name, Address1: a.Address1, Address2: a.Address2, City: a.City,
		Province: a.Province, Zip: a.Zip, Country: a.Country, Phone: a.Phone,
	}
	if out == (OrderAddress{}) {
		return nil
	}
	return &out
}

// firstNonEmpty returns the first non-blank argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// gidNumericID extracts the trailing numeric id from a Shopify GID
// (gid://shopify/Order/123 -> 123), matching the plain int64 ids Shopify's
// REST webhook payloads use. Returns 0 for an empty/malformed gid.
func gidNumericID(gid string) int64 {
	i := strings.LastIndex(gid, "/")
	if i < 0 {
		return 0
	}
	id, _ := strconv.ParseInt(gid[i+1:], 10, 64)
	return id
}
