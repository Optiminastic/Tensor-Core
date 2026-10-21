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
//
// WHICH IS WHY A ROW CAN READ "No phone" WHILE SHOPIFY'S OWN ADMIN SHOWS ONE.
// Shopify is reading the customer record; Tensor cannot see it. Two things
// follow, and only one is fixable here:
//
//   - The NAME has no other source. A checkout whose addresses are empty has
//     no name Tensor may read, and it will stay that way until the app is
//     granted read_customers and reinstalled on the shop.
//   - The PHONE usually does: this store asks for a WhatsApp number at the
//     personalisation step, so it sits in a line item's own properties. That
//     fallback is below, and it is what makes the ordinary abandoned
//     checkout - abandoned BEFORE the address step - reachable at all.

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

// CheckoutAttribute is one key/value the storefront attached to a checkout
// or a line - on this store, the personalisation a customer typed (the names
// on a plank), which is the part of the basket a call most needs to get right.
type CheckoutAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CheckoutAddress is one of the two addresses a checkout can carry, whole.
type CheckoutAddress struct {
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Address1 string `json:"address1"`
	Address2 string `json:"address2"`
	City     string `json:"city"`
	Province string `json:"province"`
	Zip      string `json:"zip"`
	Country  string `json:"country"`
}

// AbandonedLineItem is one thing left in the basket.
type AbandonedLineItem struct {
	Title        string `json:"title"`
	VariantTitle string `json:"variant_title"`
	SKU          string `json:"sku"`
	Quantity     int    `json:"quantity"`
	ImageURL     string `json:"image_url"`
	// UnitPrice is the list price; LineTotal is after any code discount, so
	// the two disagreeing is how a discounted line shows itself.
	UnitPrice  string              `json:"unit_price"`
	LineTotal  string              `json:"line_total"`
	Attributes []CheckoutAttribute `json:"attributes"`
}

// AbandonedCheckout is one unfinished checkout.
type AbandonedCheckout struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// RecoveryURL restores the customer's exact basket. It is the one genuinely
	// actionable field here, which is why it is not optional in the UI.
	RecoveryURL string `json:"recovery_url"`
	// CustomerName and Phone come off the CUSTOMER RECORD first and the
	// addresses second - the order Shopify's own admin reads them in. Either
	// may still be empty: a guest checkout has no customer record, and one
	// abandoned before the address step has no address.
	CustomerName string `json:"customer_name"`
	Phone        string `json:"phone"`
	// Email has ONE source. The abandoned-checkout type carries no email
	// field at all, so this is empty for every guest checkout and for every
	// shop still on a token issued without read_customers.
	Email       string              `json:"email"`
	City        string              `json:"city"`
	Province    string              `json:"province"`
	TotalAmount string              `json:"total_amount"`
	Currency    string              `json:"currency"`
	ItemCount   int                 `json:"item_count"`
	LineItems   []AbandonedLineItem `json:"line_items"`
	// CompletedAt is set once the buyer finished the checkout after all, which
	// happens later than it sounds: measured on the live store, one cart
	// converted six days after it was abandoned.
	//
	// Recovered checkouts are NOT dropped from Shopify's list, so without this
	// field every one of them reads "ready to call" and the button rings
	// somebody about a basket they already paid for. Nil while still open.
	CompletedAt *time.Time `json:"completed_at"`

	// What Shopify's own checkout page shows beyond the list: the money broken
	// down, the codes applied, the note and both addresses in full. All of it
	// is read_orders, the scope the list already needed.
	UpdatedAt       time.Time           `json:"updated_at"`
	Note            string              `json:"note"`
	DiscountCodes   []string            `json:"discount_codes"`
	TaxesIncluded   bool                `json:"taxes_included"`
	Attributes      []CheckoutAttribute `json:"attributes"`
	LineItemsAmount string              `json:"line_items_amount"`
	SubtotalAmount  string              `json:"subtotal_amount"`
	DiscountAmount  string              `json:"discount_amount"`
	TaxAmount       string              `json:"tax_amount"`
	// Nil when the customer never got as far as giving that address.
	ShippingAddress *CheckoutAddress `json:"shipping_address"`
	BillingAddress  *CheckoutAddress `json:"billing_address"`
}

const listAbandonedCheckoutsQuery = `
query AbandonedCheckouts($first: Int!, $after: String, $query: String) {
  abandonedCheckouts(first: $first, after: $after, query: $query, sortKey: CREATED_AT, reverse: true) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      name
      createdAt
      updatedAt
      completedAt
      abandonedCheckoutUrl
      lineItemsQuantity
      note
      discountCodes
      taxesIncluded
      customAttributes { key value }
      customer { firstName lastName displayName email phone }
      totalPriceSet { shopMoney { amount currencyCode } }
      subtotalPriceSet { shopMoney { amount } }
      totalLineItemsPriceSet { shopMoney { amount } }
      totalDiscountSet { shopMoney { amount } }
      totalTaxSet { shopMoney { amount } }
      billingAddress { firstName lastName phone address1 address2 city province zip country }
      shippingAddress { firstName lastName phone address1 address2 city province zip country }
      lineItems(first: 10) {
        nodes {
          title quantity sku variantTitle
          image { url }
          originalUnitPriceSet { shopMoney { amount } }
          discountedTotalPriceWithCodeDiscount { shopMoney { amount } }
          customAttributes { key value }
        }
      }
    }
  }
}`

// abandonedCustomer is the customer record behind a checkout.
//
// NEEDS read_customers. Asking for it without the scope fails the whole query
// with ACCESS_DENIED rather than returning a null field, so a shop still on an
// older token must be reinstalled - see OrderImportScopes.
//
// It is the only source of an EMAIL: the abandoned-checkout type carries none.
type abandonedCustomer struct {
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
}

type abandonedAddress struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Phone     string `json:"phone"`
	Address1  string `json:"address1"`
	Address2  string `json:"address2"`
	City      string `json:"city"`
	Province  string `json:"province"`
	Zip       string `json:"zip"`
	Country   string `json:"country"`
}

// moneyBag is Shopify's MoneyBag, read in the shop's currency only.
type moneyBag struct {
	ShopMoney struct {
		Amount string `json:"amount"`
	} `json:"shopMoney"`
}

type abandonedNode struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	CreatedAt            string              `json:"createdAt"`
	CompletedAt          string              `json:"completedAt"`
	AbandonedCheckoutURL string              `json:"abandonedCheckoutUrl"`
	LineItemsQuantity    int                 `json:"lineItemsQuantity"`
	UpdatedAt            string              `json:"updatedAt"`
	Note                 string              `json:"note"`
	DiscountCodes        []string            `json:"discountCodes"`
	TaxesIncluded        bool                `json:"taxesIncluded"`
	CustomAttributes     []CheckoutAttribute `json:"customAttributes"`
	// Nil for a GUEST checkout, which is most of them - and null too on a
	// token issued before read_customers was granted, though that case fails
	// the whole query rather than reaching here.
	Customer               *abandonedCustomer `json:"customer"`
	SubtotalPriceSet       moneyBag           `json:"subtotalPriceSet"`
	TotalLineItemsPriceSet moneyBag           `json:"totalLineItemsPriceSet"`
	TotalDiscountSet       moneyBag           `json:"totalDiscountSet"`
	// Nullable in Shopify's schema, unlike the three above.
	TotalTaxSet   *moneyBag `json:"totalTaxSet"`
	TotalPriceSet struct {
		ShopMoney struct {
			Amount       string `json:"amount"`
			CurrencyCode string `json:"currencyCode"`
		} `json:"shopMoney"`
	} `json:"totalPriceSet"`
	BillingAddress  *abandonedAddress `json:"billingAddress"`
	ShippingAddress *abandonedAddress `json:"shippingAddress"`
	LineItems       struct {
		Nodes []abandonedLineItemNode `json:"nodes"`
	} `json:"lineItems"`
}

// abandonedLineItemNode is named rather than inline because two things now
// read it: the conversion below, and the WhatsApp-number fallback that looks
// for the customer's own number among a line item's properties.
type abandonedLineItemNode struct {
	Title        string `json:"title"`
	Quantity     int    `json:"quantity"`
	SKU          string `json:"sku"`
	VariantTitle string `json:"variantTitle"`
	Image        *struct {
		URL string `json:"url"`
	} `json:"image"`
	OriginalUnitPriceSet                 moneyBag            `json:"originalUnitPriceSet"`
	DiscountedTotalPriceWithCodeDiscount moneyBag            `json:"discountedTotalPriceWithCodeDiscount"`
	CustomAttributes                     []CheckoutAttribute `json:"customAttributes"`
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

		Note:            strings.TrimSpace(n.Note),
		DiscountCodes:   nonNil(n.DiscountCodes),
		TaxesIncluded:   n.TaxesIncluded,
		Attributes:      nonNil(n.CustomAttributes),
		LineItemsAmount: n.TotalLineItemsPriceSet.ShopMoney.Amount,
		SubtotalAmount:  n.SubtotalPriceSet.ShopMoney.Amount,
		DiscountAmount:  n.TotalDiscountSet.ShopMoney.Amount,
		ShippingAddress: toCheckoutAddress(n.ShippingAddress),
		BillingAddress:  toCheckoutAddress(n.BillingAddress),
	}
	if n.TotalTaxSet != nil {
		out.TaxAmount = n.TotalTaxSet.ShopMoney.Amount
	}
	if t, err := time.Parse(time.RFC3339, n.UpdatedAt); err == nil {
		out.UpdatedAt = t
	}
	// THE CUSTOMER RECORD FIRST, the addresses second.
	//
	// This is the order Shopify's own admin reads them in, and it is why a row
	// could say "No phone" beside an admin screen showing one: a checkout
	// abandoned before the address step has no address to read, while the
	// customer record has had the number since they created the account.
	//
	// Still falls through to the addresses, and that is not belt-and-braces: a
	// guest checkout has no customer record at all, and those are a large part
	// of this list.
	out.CustomerName = strings.TrimSpace(nameOf(n.Customer))
	if out.CustomerName == "" {
		out.CustomerName = strings.TrimSpace(
			pick(func(a *abandonedAddress) string { return a.FirstName }) + " " +
				pick(func(a *abandonedAddress) string { return a.LastName }),
		)
	}
	out.Email = customerField(n.Customer, func(c abandonedCustomer) string { return c.Email })
	out.Phone = customerField(n.Customer, func(c abandonedCustomer) string { return c.Phone })
	if out.Phone == "" {
		out.Phone = pick(func(a *abandonedAddress) string { return a.Phone })
	}
	if out.Phone == "" {
		// THE NUMBER THE CUSTOMER TYPED THEMSELVES, when no address carries
		// one. This store asks for a WhatsApp number at the personalisation
		// step - "STEP 6 - WhatsApp Number" - so a cart abandoned before the
		// address step still has a reachable number on it, in a line item's
		// own properties.
		//
		// It is the commonest shape of an abandoned checkout, and without
		// this the row reads "No phone" and the win-back agent never calls
		// or messages that basket at all. The order path has always read
		// this attribute (whatsappbot/summary.WhatsAppNumber); the checkout
		// path did not, which is why the same customer was reachable after
		// they bought and unreachable before.
		// Read from the raw node: out.LineItems is not built until further
		// down, and an empty slice here would look like "no number given".
		out.Phone = whatsAppAttribute(n)
	}
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
		item := AbandonedLineItem{
			Title: li.Title, VariantTitle: li.VariantTitle, SKU: li.SKU, Quantity: li.Quantity,
			UnitPrice:  li.OriginalUnitPriceSet.ShopMoney.Amount,
			LineTotal:  li.DiscountedTotalPriceWithCodeDiscount.ShopMoney.Amount,
			Attributes: nonNil(li.CustomAttributes),
		}
		if li.Image != nil {
			item.ImageURL = li.Image.URL
		}
		out.LineItems = append(out.LineItems, item)
	}
	return out
}

// toCheckoutAddress keeps an address whole, as Shopify's page shows it.
// Unlike the name and phone above, nothing here is merged across the two:
// the shipping address is where the parcel goes, and borrowing its city for
// the billing one would describe an address nobody gave.
func toCheckoutAddress(a *abandonedAddress) *CheckoutAddress {
	if a == nil {
		return nil
	}
	return &CheckoutAddress{
		Name:     strings.TrimSpace(strings.TrimSpace(a.FirstName) + " " + strings.TrimSpace(a.LastName)),
		Phone:    strings.TrimSpace(a.Phone),
		Address1: strings.TrimSpace(a.Address1),
		Address2: strings.TrimSpace(a.Address2),
		City:     strings.TrimSpace(a.City),
		Province: strings.TrimSpace(a.Province),
		Zip:      strings.TrimSpace(a.Zip),
		Country:  strings.TrimSpace(a.Country),
	}
}

// nonNil turns an absent list into an empty one, so the JSON says [] and the
// page never has to tell "none" apart from "missing".
func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// customerField reads one field off the customer record, or "" when there is
// no record. Nil-safe because a guest checkout has none, and that is the
// ordinary case rather than an error.
func customerField(c *abandonedCustomer, get func(abandonedCustomer) string) string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(get(*c))
}

// nameOf is the customer's name as Shopify would show it.
//
// displayName FIRST, because Shopify builds it the way the merchant expects -
// including for a customer with only one name filled in, where joining first
// and last would leave a stray space. The parts are the fallback for a record
// that has them without a display name.
func nameOf(c *abandonedCustomer) string {
	if c == nil {
		return ""
	}
	if n := strings.TrimSpace(c.DisplayName); n != "" {
		return n
	}
	return strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
}

// whatsAppAttribute is the number the customer typed at checkout, if any.
//
// Checkout-level properties first, then each line item's. On this store the
// key is "STEP 6 - WhatsApp Number", so the match is on the word rather than
// the whole key: the step number moves when the product's form changes, and
// a number that stops being found because a form was reordered is a customer
// who stops being called.
//
// The same rule as whatsappbot/summary.isWhatsAppKey, deliberately restated
// rather than imported - an integration reaching into the bot's packages for
// a two-line predicate is a dependency the wrong way round.
func whatsAppAttribute(n abandonedNode) string {
	if v := firstWhatsApp(n.CustomAttributes); v != "" {
		return v
	}
	for _, li := range n.LineItems.Nodes {
		if v := firstWhatsApp(li.CustomAttributes); v != "" {
			return v
		}
	}
	return ""
}

func firstWhatsApp(attrs []CheckoutAttribute) string {
	for _, a := range attrs {
		if !strings.Contains(strings.ToLower(a.Key), "whatsapp") {
			continue
		}
		if v := strings.TrimSpace(a.Value); v != "" {
			return v
		}
	}
	return ""
}
