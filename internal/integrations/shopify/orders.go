package shopify

import (
	"context"
	"strconv"
	"strings"
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
}

// OrderLineProp is a Shopify line-item custom attribute (equivalent to the
// REST webhook's "properties" - print facts and personalisation are read
// from these).
type OrderLineProp struct {
	Name  string
	Value string
}

// maxBackfillOrders bounds the one-time catch-up import to a single GraphQL
// page (Shopify's own per-page cap) - fast enough to run inline in the OAuth
// callback. It backfills only the most recently created orders; deeper
// history is out of scope for this best-effort catch-up. Note Shopify's
// `read_orders` scope itself only returns orders from the last 60 days
// unless the app has been granted `read_all_orders` protected-data access.
const maxBackfillOrders = 250

// customer is deliberately omitted: it requires the read_customers scope,
// which apps only granted read_orders/read_products don't have - requesting
// an inaccessible field fails the entire GraphQL call (see Client.do), not
// just that field, so synced orders carry no customer name/email/phone.
const listOrdersQuery = `query ListOrders($first: Int!) {
  orders(first: $first, sortKey: CREATED_AT, reverse: true) {
    nodes {
      id
      name
      displayFinancialStatus
      totalPriceSet { shopMoney { amount currencyCode } }
      lineItems(first: 50) {
        nodes {
          sku
          name
          title
          quantity
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
			Nodes []orderNode `json:"nodes"`
		} `json:"orders"`
	} `json:"data"`
}

type orderNode struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	DisplayFinancialStatus string `json:"displayFinancialStatus"`
	TotalPriceSet          struct {
		ShopMoney struct {
			Amount       string `json:"amount"`
			CurrencyCode string `json:"currencyCode"`
		} `json:"shopMoney"`
	} `json:"totalPriceSet"`
	LineItems struct {
		Nodes []struct {
			SKU      string `json:"sku"`
			Name     string `json:"name"`
			Title    string `json:"title"`
			Quantity int    `json:"quantity"`
			Product  *struct {
				ID string `json:"id"`
			} `json:"product"`
			CustomAttributes []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"customAttributes"`
		} `json:"nodes"`
	} `json:"lineItems"`
}

// ListRecentOrders fetches the store's most recently created orders (every
// financial status - a COD order sitting at "pending" is still a real
// order), for the one-time backfill that runs after connecting. limit is
// capped at maxBackfillOrders; zero/negative uses the cap.
func (c *Client) ListRecentOrders(ctx context.Context, shop, token string, limit int) ([]OrderSummary, error) {
	if limit <= 0 || limit > maxBackfillOrders {
		limit = maxBackfillOrders
	}
	var out ordersResponse
	if err := c.do(ctx, shop, token, listOrdersQuery, map[string]any{"first": limit}, &out); err != nil {
		return nil, err
	}

	summaries := make([]OrderSummary, 0, len(out.Data.Orders.Nodes))
	for _, n := range out.Data.Orders.Nodes {
		summaries = append(summaries, toOrderSummary(n))
	}
	return summaries, nil
}

// toOrderSummary leaves Customer unset - the query above can't request it
// without the read_customers scope.
func toOrderSummary(n orderNode) OrderSummary {
	s := OrderSummary{
		ID:              gidNumericID(n.ID),
		Name:            n.Name,
		FinancialStatus: strings.ToLower(n.DisplayFinancialStatus),
		TotalPrice:      n.TotalPriceSet.ShopMoney.Amount,
		Currency:        n.TotalPriceSet.ShopMoney.CurrencyCode,
	}
	s.LineItems = make([]OrderLineItem, 0, len(n.LineItems.Nodes))
	for _, li := range n.LineItems.Nodes {
		item := OrderLineItem{SKU: li.SKU, Name: li.Name, Title: li.Title, Quantity: li.Quantity}
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
