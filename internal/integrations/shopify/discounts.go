package shopify

// Creating a single-use percentage discount code.
//
// NEEDS write_discounts, which is the only write scope in this file and the
// reason it is separate: everything else Tensor asks of Shopify is a read or a
// product write, and a shop that has not granted this one must still be able
// to do all of that.
//
// ONE CODE PER CUSTOMER PER CART, expiring in an hour. A shared code is a
// public coupon the moment one customer forwards the message; a code that
// belongs to one person, lasts one hour and can be used once is only worth
// what it was meant to be worth.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DiscountCode is one code to create.
type DiscountCode struct {
	// Code is what the customer types, and what goes in the link. Shopify
	// matches it case-insensitively but stores it as given.
	Code string
	// Title is what the merchant sees in Shopify's Discounts list. It is not
	// the code, and making it descriptive is the difference between a readable
	// list and a thousand rows of random letters.
	Title string
	// Percentage as a FRACTION: 0.10 is ten per cent. Shopify rejects 10.
	Percentage float64
	StartsAt   time.Time
	// EndsAt is when it stops working. Required here - this package will not
	// create a discount that lasts forever, because the only caller is a
	// win-back message that promises an hour.
	EndsAt time.Time
}

const createDiscountMutation = `mutation CreateDiscount($d: DiscountCodeBasicInput!) {
  discountCodeBasicCreate(basicCodeDiscount: $d) {
    codeDiscountNode { id }
    userErrors { field code message }
  }
}`

type createDiscountResponse struct {
	Data struct {
		DiscountCodeBasicCreate struct {
			CodeDiscountNode *struct {
				ID string `json:"id"`
			} `json:"codeDiscountNode"`
			UserErrors []struct {
				Field   []string `json:"field"`
				Code    string   `json:"code"`
				Message string   `json:"message"`
			} `json:"userErrors"`
		} `json:"discountCodeBasicCreate"`
	} `json:"data"`
}

// ErrDiscountCodeTaken is returned when the code already exists on the shop.
//
// Its own error because the caller's answer is to pick another name rather
// than to give up: a second customer called Seetha must still get a code, and
// an expired SEETHA10 still occupies the name.
var ErrDiscountCodeTaken = fmt.Errorf("that discount code already exists")

// CreateDiscountCode creates a single-use percentage code and returns its id.
func (c *Client) CreateDiscountCode(
	ctx context.Context, shop, token string, d DiscountCode,
) (string, error) {
	code := strings.TrimSpace(d.Code)
	if code == "" {
		return "", fmt.Errorf("no discount code to create")
	}
	if d.Percentage <= 0 || d.Percentage >= 1 {
		// Guarded because the units are the easy mistake: Shopify wants a
		// fraction, and 10 would be a 1000% discount if it were accepted.
		return "", fmt.Errorf("discount percentage must be a fraction between 0 and 1, got %v",
			d.Percentage)
	}
	if !d.EndsAt.After(d.StartsAt) {
		return "", fmt.Errorf("a discount that ends before it starts is not a discount")
	}

	title := strings.TrimSpace(d.Title)
	if title == "" {
		title = code
	}

	var resp createDiscountResponse
	err := c.do(ctx, shop, token, createDiscountMutation, map[string]any{
		"d": map[string]any{
			"title":    title,
			"code":     code,
			"startsAt": d.StartsAt.UTC().Format(time.RFC3339),
			"endsAt":   d.EndsAt.UTC().Format(time.RFC3339),
			"customerSelection": map[string]any{
				"all": true,
			},
			"customerGets": map[string]any{
				"value": map[string]any{
					"percentage": d.Percentage,
				},
				"items": map[string]any{"all": true},
			},
			// BOTH, and they are not the same guard. usageLimit caps the code
			// across everybody, appliesOncePerCustomer caps it per person.
			// With one code per customer the first is what matters; the second
			// costs nothing and is what stops a forwarded message being worth
			// anything to the person who received it.
			"appliesOncePerCustomer": true,
			"usageLimit":             1,
		},
	}, &resp)
	if err != nil {
		return "", err
	}

	result := resp.Data.DiscountCodeBasicCreate
	if len(result.UserErrors) > 0 {
		first := result.UserErrors[0]
		// Shopify reports a duplicate as a userError, not an HTTP failure, so
		// without this the caller sees "something went wrong" for the one
		// case it can actually recover from.
		if strings.EqualFold(first.Code, "TAKEN") ||
			strings.Contains(strings.ToLower(first.Message), "already exists") ||
			strings.Contains(strings.ToLower(first.Message), "has already been taken") {
			return "", ErrDiscountCodeTaken
		}
		return "", apiErr("Shopify refused the discount: %s", first.Message)
	}
	if result.CodeDiscountNode == nil || result.CodeDiscountNode.ID == "" {
		return "", apiErr("Shopify accepted the discount but returned no id")
	}
	return result.CodeDiscountNode.ID, nil
}

// AccessScopes is what a token is actually allowed to do on a shop.
//
// The cheapest possible proof that a credential works: it needs no scope of
// its own, so it answers for a token that has been granted nothing, and it
// fails loudly for one that is wrong. Used to check a credential at the
// moment somebody enters it, rather than discovering it on a page that reads
// orders three screens later.
//
// REST, not GraphQL - access_scopes has no GraphQL equivalent.
func (c *Client) AccessScopes(ctx context.Context, shop, token string) ([]string, error) {
	base := c.baseURL
	if base == "" {
		base = "https://" + shop
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/admin/oauth/access_scopes.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Shopify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := readAll(resp)
	if err != nil {
		return nil, fmt.Errorf("could not read Shopify's response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var refusal struct {
			Errors any `json:"errors"`
		}
		if json.Unmarshal(raw, &refusal) == nil && refusal.Errors != nil {
			return nil, fmt.Errorf("%v", refusal.Errors)
		}
		return nil, fmt.Errorf("Shopify answered %d", resp.StatusCode)
	}

	var body struct {
		AccessScopes []struct {
			Handle string `json:"handle"`
		} `json:"access_scopes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("could not decode Shopify's response: %w", err)
	}
	out := make([]string, 0, len(body.AccessScopes))
	for _, s := range body.AccessScopes {
		out = append(out, s.Handle)
	}
	return out, nil
}
