package shopify

// One order, read live, and the two writes the WhatsApp bot makes to it.
//
// LIVE, NOT FROM THE ORDERS TABLE. Tensor's own copy is up to one sync interval
// stale and does not hold tracking, while a customer asking "where is my order"
// wants the courier's latest word. So the answer comes from Shopify itself.

import (
	"context"
	"sort"
	"strings"
	"time"
)

const orderByNameQuery = `query OrderByName($q: String!) {
  orders(first: 1, query: $q) {
    nodes {
      id name createdAt cancelledAt phone
      shippingLine { title }
      displayFulfillmentStatus
      shippingAddress { phone }
      billingAddress { phone }
      customer { defaultPhoneNumber { phoneNumber } }
      lineItems(first: 20) { nodes { customAttributes { key value } } }
      fulfillments(first: 5) { createdAt displayStatus trackingInfo(first: 3) { company number url } }
    }
  }
}`

const tagsAddMutation = `mutation AddTags($id: ID!, $tags: [String!]!) {
  tagsAdd(id: $id, tags: $tags) { userErrors { field message } }
}`

const orderNoteQuery = `query OrderNote($id: ID!) { order(id: $id) { note } }`

const orderNoteMutation = `mutation SetNote($input: OrderInput!) {
  orderUpdate(input: $input) { userErrors { field message } }
}`

// OrderTracking is one tracking number on a shipment.
type OrderTracking struct {
	Company string
	Number  string
	URL     string
}

// OrderFulfillment is one shipment. DisplayStatus is Shopify's
// FulfillmentDisplayStatus, e.g. IN_TRANSIT or DELIVERED.
type OrderFulfillment struct {
	CreatedAt     time.Time
	DisplayStatus string
	Tracking      []OrderTracking
}

// OrderAttribute is one custom attribute on a line item.
type OrderAttribute struct {
	Key   string
	Value string
}

// OrderStatus is what the bot needs to answer for one order.
type OrderStatus struct {
	ID                string // GID
	Name              string
	CreatedAt         time.Time
	CancelledAt       *time.Time
	ShippingTitle     string
	FulfillmentStatus string
	// Fulfillments oldest first, so the last one is the latest shipment.
	Fulfillments []OrderFulfillment
	// Phones are the order's own numbers: order, shipping, billing, customer.
	Phones []string
	// Attributes are every line item's custom attributes, flattened, which is
	// where a "WhatsApp Number" step lives.
	Attributes []OrderAttribute
}

// OrderByName finds an order by its exact name, e.g. "T3DPS-115766".
func (c *Client) OrderByName(ctx context.Context, shop, token, name string) (OrderStatus, bool, error) {
	var data struct {
		Orders struct {
			Nodes []lookupNode `json:"nodes"`
		} `json:"orders"`
	}
	if err := c.queryData(ctx, shop, token, orderByNameQuery, map[string]any{"q": "name:" + name}, &data); err != nil {
		return OrderStatus{}, false, err
	}
	// Shopify's name search matches loosely, so only an identical name counts.
	for _, n := range data.Orders.Nodes {
		if strings.EqualFold(n.Name, name) {
			return n.status(), true, nil
		}
	}
	return OrderStatus{}, false, nil
}

// AddOrderTags adds tags to an order, keeping the ones it has.
func (c *Client) AddOrderTags(ctx context.Context, shop, token, orderGID string, tags ...string) error {
	var data struct {
		TagsAdd struct {
			UserErrors []userError `json:"userErrors"`
		} `json:"tagsAdd"`
	}
	if err := c.queryData(ctx, shop, token, tagsAddMutation, map[string]any{"id": orderGID, "tags": tags}, &data); err != nil {
		return err
	}
	if msg := firstUserError(data.TagsAdd.UserErrors); msg != "" {
		return apiErr("Shopify refused the order tags: %s", msg)
	}
	return nil
}

// AppendOrderNote adds a line to the order note without losing what is there.
func (c *Client) AppendOrderNote(ctx context.Context, shop, token, orderGID, line string) error {
	var current struct {
		Order *struct {
			Note *string `json:"note"`
		} `json:"order"`
	}
	if err := c.queryData(ctx, shop, token, orderNoteQuery, map[string]any{"id": orderGID}, &current); err != nil {
		return err
	}
	if current.Order == nil {
		return apiErr("Shopify has no order %s", orderGID)
	}
	note := line
	if current.Order.Note != nil && strings.TrimSpace(*current.Order.Note) != "" {
		note = strings.TrimSpace(*current.Order.Note) + "\n" + line
	}

	var data struct {
		OrderUpdate struct {
			UserErrors []userError `json:"userErrors"`
		} `json:"orderUpdate"`
	}
	input := map[string]any{"id": orderGID, "note": note}
	if err := c.queryData(ctx, shop, token, orderNoteMutation, map[string]any{"input": input}, &data); err != nil {
		return err
	}
	if msg := firstUserError(data.OrderUpdate.UserErrors); msg != "" {
		return apiErr("Shopify refused the order note: %s", msg)
	}
	return nil
}

// queryData runs a GraphQL operation and decodes its "data" object into out,
// which do leaves to the caller.
func (c *Client) queryData(ctx context.Context, shop, token, query string, vars map[string]any, out any) error {
	envelope := struct {
		Data any `json:"data"`
	}{Data: out}
	return c.do(ctx, shop, token, query, vars, &envelope)
}

type lookupPhone struct {
	Phone *string `json:"phone"`
}

type lookupNode struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"createdAt"`
	CancelledAt  *time.Time `json:"cancelledAt"`
	Phone        *string    `json:"phone"`
	ShippingLine *struct {
		Title string `json:"title"`
	} `json:"shippingLine"`
	DisplayFulfillmentStatus string       `json:"displayFulfillmentStatus"`
	ShippingAddress          *lookupPhone `json:"shippingAddress"`
	BillingAddress           *lookupPhone `json:"billingAddress"`
	Customer                 *struct {
		DefaultPhoneNumber *struct {
			PhoneNumber string `json:"phoneNumber"`
		} `json:"defaultPhoneNumber"`
	} `json:"customer"`
	LineItems struct {
		Nodes []struct {
			CustomAttributes []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"customAttributes"`
		} `json:"nodes"`
	} `json:"lineItems"`
	Fulfillments []struct {
		CreatedAt     time.Time `json:"createdAt"`
		DisplayStatus string    `json:"displayStatus"`
		TrackingInfo  []struct {
			Company string `json:"company"`
			Number  string `json:"number"`
			URL     string `json:"url"`
		} `json:"trackingInfo"`
	} `json:"fulfillments"`
}

func (n lookupNode) status() OrderStatus {
	o := OrderStatus{
		ID: n.ID, Name: n.Name, CreatedAt: n.CreatedAt, CancelledAt: n.CancelledAt,
		FulfillmentStatus: n.DisplayFulfillmentStatus,
	}
	if n.ShippingLine != nil {
		o.ShippingTitle = n.ShippingLine.Title
	}
	o.Phones = n.phones()
	for _, li := range n.LineItems.Nodes {
		for _, a := range li.CustomAttributes {
			o.Attributes = append(o.Attributes, OrderAttribute{Key: a.Key, Value: a.Value})
		}
	}

	fulfillments := n.Fulfillments
	sort.SliceStable(fulfillments, func(i, j int) bool {
		return fulfillments[i].CreatedAt.Before(fulfillments[j].CreatedAt)
	})
	for _, f := range fulfillments {
		of := OrderFulfillment{CreatedAt: f.CreatedAt, DisplayStatus: f.DisplayStatus}
		for _, t := range f.TrackingInfo {
			of.Tracking = append(of.Tracking, OrderTracking{Company: t.Company, Number: t.Number, URL: t.URL})
		}
		o.Fulfillments = append(o.Fulfillments, of)
	}
	return o
}

func (n lookupNode) phones() []string {
	var phones []string
	add := func(p *string) {
		if p != nil && strings.TrimSpace(*p) != "" {
			phones = append(phones, *p)
		}
	}
	add(n.Phone)
	for _, h := range []*lookupPhone{n.ShippingAddress, n.BillingAddress} {
		if h != nil {
			add(h.Phone)
		}
	}
	if n.Customer != nil && n.Customer.DefaultPhoneNumber != nil {
		add(&n.Customer.DefaultPhoneNumber.PhoneNumber)
	}
	return phones
}
