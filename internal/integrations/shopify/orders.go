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
	ProductID *int64
	SKU       string
	Name      string
	Title     string
	Quantity  int
	// VariantTitle is what the customer picked from the product's own options,
	// as one string - "BABY PINK / NO LIGHT". VariantOptions is the same thing
	// split into named options ("Colour" -> "BABY PINK"), which is what tells
	// the shop which filament to load. VariantOptions is empty when the app
	// cannot read products (see listOrdersQuery), leaving VariantTitle as the
	// only record of the choice.
	VariantTitle   string
	VariantOptions []OrderLineProp
	Properties     []OrderLineProp
}

// OrderLineProp is a Shopify line-item custom attribute (equivalent to the
// REST webhook's "properties" - print facts and personalisation are read
// from these).
type OrderLineProp struct {
	Name  string
	Value string
}

// ordersPageSize is Shopify's own per-page cap for the orders connection. A
// request for more than this is rejected, so a larger limit is walked page by
// page instead.
const ordersPageSize = 250

// defaultOrderLimit applies when a caller asks for no particular number.
const defaultOrderLimit = 50

// maxOrderPages bounds a deep backfill so a misconfigured limit cannot walk a
// large store forever - 20 pages is 5,000 orders, past any catch-up this is
// meant for. Note Shopify's `read_orders` scope only returns orders from the
// last 60 days unless the app has `read_all_orders` protected-data access.
const maxOrderPages = 20

// customer is deliberately omitted: it requires the read_customers scope,
// which apps only granted read_orders/read_products don't have - requesting
// an inaccessible field fails the entire GraphQL call (see Client.do), not
// just that field, so synced orders carry no customer name/email/phone.
// listOrdersQuery reads the variant the customer chose. `variant` resolves a
// ProductVariant, which needs read_products - and one inaccessible field fails
// the whole GraphQL call rather than just that field (the same trap that keeps
// `customer` out of these queries). A store whose app has only read_orders
// falls back to listOrdersQueryLean below.
const listOrdersQuery = `query ListOrders($first: Int!, $after: String) {
  orders(first: $first, after: $after, sortKey: CREATED_AT, reverse: true) {
    pageInfo { hasNextPage endCursor }
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
          variantTitle
          variant { selectedOptions { name value } }
          customAttributes { key value }
        }
      }
    }
  }
}`

// listOrdersQueryLean is listOrdersQuery without the variant block, for a store
// whose app cannot read products. variantTitle is a scalar on the line item
// itself, so it survives here and the colour is still recoverable by eye.
const listOrdersQueryLean = `query ListOrders($first: Int!, $after: String) {
  orders(first: $first, after: $after, sortKey: CREATED_AT, reverse: true) {
    pageInfo { hasNextPage endCursor }
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
          variantTitle
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
			VariantTitle string `json:"variantTitle"`
			Variant      *struct {
				SelectedOptions []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"selectedOptions"`
			} `json:"variant"`
			CustomAttributes []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"customAttributes"`
		} `json:"nodes"`
	} `json:"lineItems"`
}

// ListRecentOrders fetches the store's most recently created orders, newest
// first (every financial status - a COD order sitting at "pending" is still a
// real order). A limit above Shopify's per-page cap is walked page by page, so
// a deep backfill of a whole store and a routine 50-order sweep are the same
// call with a different number. Zero or negative uses defaultOrderLimit.
//
// Pages are fetched in sequence, not in parallel: Shopify's GraphQL cost
// budget is per-shop, and a burst of concurrent order queries gets throttled
// rather than served faster.
func (c *Client) ListRecentOrders(ctx context.Context, shop, token string, limit int) ([]OrderSummary, error) {
	if limit <= 0 {
		limit = defaultOrderLimit
	}

	summaries := make([]OrderSummary, 0, min(limit, ordersPageSize))
	cursor := ""
	leanOnly := false
	for page := 0; page < maxOrderPages && len(summaries) < limit; page++ {
		vars := map[string]any{"first": min(limit-len(summaries), ordersPageSize)}
		if cursor != "" {
			vars["after"] = cursor
		}
		var out ordersResponse
		query := listOrdersQuery
		if leanOnly {
			query = listOrdersQueryLean
		}
		err := c.do(ctx, shop, token, query, vars, &out)
		if err != nil && !leanOnly {
			// Most likely the app cannot read products. Drop the variant block
			// and retry this page rather than losing the whole sync over the
			// one field that is nice to have.
			leanOnly = true
			out = ordersResponse{}
			err = c.do(ctx, shop, token, listOrdersQueryLean, vars, &out)
		}
		if err != nil {
			// Whatever earlier pages returned is still worth importing, so a
			// failure deep into a backfill is reported with the partial result
			// rather than throwing it away.
			if len(summaries) > 0 {
				return summaries, nil
			}
			return nil, err
		}
		for _, n := range out.Data.Orders.Nodes {
			summaries = append(summaries, toOrderSummary(n))
		}
		if !out.Data.Orders.PageInfo.HasNextPage || out.Data.Orders.PageInfo.EndCursor == "" {
			break
		}
		cursor = out.Data.Orders.PageInfo.EndCursor
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
		item := OrderLineItem{
			SKU: li.SKU, Name: li.Name, Title: li.Title,
			Quantity: li.Quantity, VariantTitle: li.VariantTitle,
		}
		if li.Product != nil {
			id := gidNumericID(li.Product.ID)
			item.ProductID = &id
		}
		if li.Variant != nil {
			for _, o := range li.Variant.SelectedOptions {
				item.VariantOptions = append(item.VariantOptions, OrderLineProp{Name: o.Name, Value: o.Value})
			}
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
