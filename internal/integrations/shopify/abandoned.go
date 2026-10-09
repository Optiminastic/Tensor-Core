package shopify

// Checkouts a customer started and did not finish.
//
// WHAT COUNTS AS ABANDONED is Shopify's definition, not ours: a checkout only
// becomes one once the customer entered contact details. Everybody who bounced
// before that step is invisible here - they are in the storefront's analytics,
// not in this API - so this list is always much shorter than "carts that did
// not convert", and that is correct rather than a bug to chase.
//
// NO CUSTOMER OBJECT, DELIBERATELY. AbandonedCheckout.customer needs the
// read_customers scope, which this app does not hold, and asking for it fails
// the whole field with ACCESS_DENIED. The name and phone come off the
// addresses instead, which read_orders already covers - measured against the
// live store rather than assumed. There is no `email` field on the type at
// all, so an address is the only contact detail available without widening the
// app's scopes and reinstalling it on the shop.

import (
	"context"
	"strings"
	"time"
)

// abandonedPageSize is how many checkouts one request asks for. Shopify costs
// a GraphQL query by the fields it returns, and a page of 50 with line items
// is comfortably inside the bucket.
const abandonedPageSize = 50

// maxAbandonedCheckouts caps a single listing. The page shows recent activity,
// not an archive, and an unbounded walk over a busy store is a slow request
// nobody asked for.
const maxAbandonedCheckouts = 250

// AbandonedLineItem is one thing left in the basket.
type AbandonedLineItem struct {
	Title        string `json:"title"`
	VariantTitle string `json:"variant_title"`
	SKU          string `json:"sku"`
	Quantity     int    `json:"quantity"`
}

// AbandonedCheckout is one unfinished checkout.
type AbandonedCheckout struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// RecoveryURL restores the customer's exact basket. It is the one genuinely
	// actionable field here, which is why it is not optional in the UI.
	RecoveryURL string `json:"recovery_url"`
	// CustomerName and Phone come off the billing address, falling back to the
	// shipping one. Either may be empty: a customer can reach the contact step
	// having given an email only, and the phone comes back null for those.
	CustomerName string              `json:"customer_name"`
	Phone        string              `json:"phone"`
	City         string              `json:"city"`
	Province     string              `json:"province"`
	TotalAmount  string              `json:"total_amount"`
	Currency     string              `json:"currency"`
	ItemCount    int                 `json:"item_count"`
	LineItems    []AbandonedLineItem `json:"line_items"`
	// CompletedAt is set once the buyer finished the checkout after all, which
	// happens later than it sounds: measured on the live store, one cart
	// converted six days after it was abandoned.
	//
	// Recovered checkouts are NOT dropped from Shopify's list, so without this
	// field every one of them reads "ready to call" and the button rings
	// somebody about a basket they already paid for. Nil while still open.
	CompletedAt *time.Time `json:"completed_at"`
}

const listAbandonedCheckoutsQuery = `
query AbandonedCheckouts($first: Int!, $after: String, $query: String) {
  abandonedCheckouts(first: $first, after: $after, query: $query, sortKey: CREATED_AT, reverse: true) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      name
      createdAt
      completedAt
      abandonedCheckoutUrl
      lineItemsQuantity
      totalPriceSet { shopMoney { amount currencyCode } }
      billingAddress { firstName lastName phone city province }
      shippingAddress { firstName lastName phone city province }
      lineItems(first: 10) { nodes { title quantity sku variantTitle } }
    }
  }
}`

type abandonedAddress struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Phone     string `json:"phone"`
	City      string `json:"city"`
	Province  string `json:"province"`
}

type abandonedNode struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	CreatedAt            string `json:"createdAt"`
	CompletedAt          string `json:"completedAt"`
	AbandonedCheckoutURL string `json:"abandonedCheckoutUrl"`
	LineItemsQuantity    int    `json:"lineItemsQuantity"`
	TotalPriceSet        struct {
		ShopMoney struct {
			Amount       string `json:"amount"`
			CurrencyCode string `json:"currencyCode"`
		} `json:"shopMoney"`
	} `json:"totalPriceSet"`
	BillingAddress  *abandonedAddress `json:"billingAddress"`
	ShippingAddress *abandonedAddress `json:"shippingAddress"`
	LineItems       struct {
		Nodes []struct {
			Title        string `json:"title"`
			Quantity     int    `json:"quantity"`
			SKU          string `json:"sku"`
			VariantTitle string `json:"variantTitle"`
		} `json:"nodes"`
	} `json:"lineItems"`
}

type abandonedResponse struct {
	Data struct {
		AbandonedCheckouts struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []abandonedNode `json:"nodes"`
		} `json:"abandonedCheckouts"`
	} `json:"data"`
}

// ListAbandonedCheckouts returns a store's unfinished checkouts, newest first.
//
// createdAtMin is a floor on age, in the store's own day: an operator asking
// for "since Monday" means the store's Monday, and a UTC timestamp would cut
// the day at the wrong hour. Zero fetches whatever Shopify still retains.
func (c *Client) ListAbandonedCheckouts(
	ctx context.Context, shop, token string, limit int, createdAtMin time.Time,
) ([]AbandonedCheckout, error) {
	if limit <= 0 || limit > maxAbandonedCheckouts {
		limit = maxAbandonedCheckouts
	}

	var (
		out   []AbandonedCheckout
		after string
	)
	for len(out) < limit {
		page := limit - len(out)
		if page > abandonedPageSize {
			page = abandonedPageSize
		}

		vars := map[string]any{"first": page}
		if !createdAtMin.IsZero() {
			vars["query"] = "created_at:>=" + createdAtMin.Format("2006-01-02")
		}
		if after != "" {
			vars["after"] = after
		}

		var resp abandonedResponse
		if err := c.do(ctx, shop, token, listAbandonedCheckoutsQuery, vars, &resp); err != nil {
			// Pages already fetched are NOT returned beside the error: a partial
			// list is indistinguishable from a complete one to the caller, and
			// this one renders a page that says "these are your abandoned
			// checkouts".
			return nil, err
		}

		for _, n := range resp.Data.AbandonedCheckouts.Nodes {
			out = append(out, toAbandonedCheckout(n))
		}

		info := resp.Data.AbandonedCheckouts.PageInfo
		if !info.HasNextPage || info.EndCursor == "" || info.EndCursor == after {
			// EndCursor == after guards a server that keeps claiming another
			// page while returning the same one, which would spin to the cap.
			break
		}
		after = info.EndCursor
	}
	return out, nil
}

func toAbandonedCheckout(n abandonedNode) AbandonedCheckout {
	// Billing first, shipping as the fallback - resolved FIELD BY FIELD, not
	// by picking one address and taking everything from it.
	//
	// Measured on the live store: a customer typically fills the shipping
	// address and leaves billing partly empty, so a row can carry a billing
	// NAME with a null billing phone while the shipping address holds the
	// number. Choosing the address on the strength of its name, then reading
	// the phone off the same one, loses a reachable customer to "No phone" -
	// which on this page means never being called.
	pick := func(get func(*abandonedAddress) string) string {
		if n.BillingAddress != nil {
			if v := strings.TrimSpace(get(n.BillingAddress)); v != "" {
				return v
			}
		}
		if n.ShippingAddress != nil {
			return strings.TrimSpace(get(n.ShippingAddress))
		}
		return ""
	}

	out := AbandonedCheckout{
		ID:          n.ID,
		Name:        n.Name,
		RecoveryURL: n.AbandonedCheckoutURL,
		ItemCount:   n.LineItemsQuantity,
		TotalAmount: n.TotalPriceSet.ShopMoney.Amount,
		Currency:    n.TotalPriceSet.ShopMoney.CurrencyCode,
	}
	out.CustomerName = strings.TrimSpace(
		pick(func(a *abandonedAddress) string { return a.FirstName }) + " " +
			pick(func(a *abandonedAddress) string { return a.LastName }),
	)
	out.Phone = pick(func(a *abandonedAddress) string { return a.Phone })
	out.City = pick(func(a *abandonedAddress) string { return a.City })
	out.Province = pick(func(a *abandonedAddress) string { return a.Province })
	if t, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
		out.CreatedAt = t
	}
	// Left nil rather than zeroed when absent. A zero time.Time renders as a
	// real date, and "completed in year 1" is a worse lie than "not completed".
	if n.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, n.CompletedAt); err == nil {
			out.CompletedAt = &t
		}
	}
	for _, li := range n.LineItems.Nodes {
		out.LineItems = append(out.LineItems, AbandonedLineItem{
			Title: li.Title, VariantTitle: li.VariantTitle, SKU: li.SKU, Quantity: li.Quantity,
		})
	}
	return out
}
