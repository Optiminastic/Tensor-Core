package shopify

// The fields of a bundle an order line was sold in.
//
// A Shopify bundle keeps what the customer typed on the bundle, not on its
// component lines. Asking for that on every line of every order made the
// order list too slow for Shopify to answer - every sync of 1,600 orders
// timed out - so the list asks only WHETHER a line is part of a bundle
// (lineItemGroup { id }), and the handful of orders that have one are asked
// for the bundle's fields here, one small query each.

import (
	"context"
	"log/slog"
)

// lineItemGroupNode is the bundle a line belongs to. The list query fills ID
// alone; fillBundleGroups adds the rest.
type lineItemGroupNode struct {
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	CustomAttributes []attributeNode `json:"customAttributes"`
}

const orderBundlesQuery = `query OrderBundles($id: ID!) {
  order(id: $id) {
    lineItems(first: 50) {
      nodes { lineItemGroup { id title customAttributes { key value } } }
    }
  }
}`

type orderBundlesResponse struct {
	Data struct {
		Order *struct {
			LineItems struct {
				Nodes []struct {
					LineItemGroup *lineItemGroupNode `json:"lineItemGroup"`
				} `json:"nodes"`
			} `json:"lineItems"`
		} `json:"order"`
	} `json:"data"`
}

// fillBundleGroups gives an order's bundle lines their bundle's title and
// fields. Best effort: a failure leaves the lines as they came, which is how
// every order was imported before bundles were read at all, rather than
// failing a sync of hundreds of orders over one.
func (c *Client) fillBundleGroups(ctx context.Context, shop, token string, n *orderNode) {
	if !hasBundleLine(n) {
		return
	}
	var out orderBundlesResponse
	if err := c.do(ctx, shop, token, orderBundlesQuery, map[string]any{"id": n.ID}, &out); err != nil {
		slog.Warn("could not read an order's bundle fields", "order", n.Name, "error", err)
		return
	}
	if out.Data.Order == nil {
		return
	}
	groups := map[string]*lineItemGroupNode{}
	for _, li := range out.Data.Order.LineItems.Nodes {
		if g := li.LineItemGroup; g != nil && g.ID != "" {
			groups[g.ID] = g
		}
	}
	for i := range n.LineItems.Nodes {
		line := &n.LineItems.Nodes[i]
		if line.LineItemGroup == nil {
			continue
		}
		if full, ok := groups[line.LineItemGroup.ID]; ok {
			line.LineItemGroup = full
		}
	}
}

func hasBundleLine(n *orderNode) bool {
	for _, li := range n.LineItems.Nodes {
		if li.LineItemGroup != nil && li.LineItemGroup.ID != "" {
			return true
		}
	}
	return false
}
