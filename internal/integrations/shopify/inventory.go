package shopify

import "context"

const locationsQuery = `query PrimaryLocation {
  locations(first: 1, includeInactive: false) { nodes { id } }
}`

// PrimaryLocationGID returns the store's first active location, the one a
// single-location store holds its stock at. Most Tensor stores are single-site,
// so the first active location is the right place to set a quantity.
func (c *Client) PrimaryLocationGID(ctx context.Context, shop, token string) (string, error) {
	var out struct {
		Data struct {
			Locations struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"locations"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, locationsQuery, map[string]any{}, &out); err != nil {
		return "", err
	}
	if len(out.Data.Locations.Nodes) == 0 {
		return "", apiErr("this store has no active location to hold inventory")
	}
	return out.Data.Locations.Nodes[0].ID, nil
}

const inventoryActivateMutation = `mutation ActivateInventory($inventoryItemId: ID!, $locationId: ID!) {
  inventoryActivate(inventoryItemId: $inventoryItemId, locationId: $locationId) {
    inventoryLevel { id }
    userErrors { field message }
  }
}`

const inventorySetMutation = `mutation SetInventory($input: InventorySetQuantitiesInput!) {
  inventorySetQuantities(input: $input) {
    userErrors { field message }
  }
}`

// SetInventoryQuantity sets the absolute on-hand "available" quantity for a
// variant's inventory item at a location. It first activates the item at the
// location (creating the stock level if absent, a no-op if already active), then
// sets the quantity, ignoring the optimistic compare-quantity check so a plain
// "set it to N" always applies.
func (c *Client) SetInventoryQuantity(
	ctx context.Context, shop, token, inventoryItemGID, locationGID string, qty int,
) error {
	if err := c.activateInventory(ctx, shop, token, inventoryItemGID, locationGID); err != nil {
		return err
	}
	input := map[string]any{
		"name":                  "available",
		"reason":                "correction",
		"ignoreCompareQuantity": true,
		"quantities": []map[string]any{{
			"inventoryItemId": inventoryItemGID,
			"locationId":      locationGID,
			"quantity":        qty,
		}},
	}
	var out struct {
		Data struct {
			InventorySetQuantities struct {
				UserErrors []userError `json:"userErrors"`
			} `json:"inventorySetQuantities"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, inventorySetMutation, map[string]any{"input": input}, &out); err != nil {
		return err
	}
	if msg := firstUserError(out.Data.InventorySetQuantities.UserErrors); msg != "" {
		return apiErr("Shopify rejected the inventory update: %s", msg)
	}
	return nil
}

// activateInventory ensures the item is stocked at the location. An already-active
// item comes back without error, so this is safe to call every time.
func (c *Client) activateInventory(ctx context.Context, shop, token, inventoryItemGID, locationGID string) error {
	var out struct {
		Data struct {
			InventoryActivate struct {
				UserErrors []userError `json:"userErrors"`
			} `json:"inventoryActivate"`
		} `json:"data"`
	}
	variables := map[string]any{"inventoryItemId": inventoryItemGID, "locationId": locationGID}
	if err := c.do(ctx, shop, token, inventoryActivateMutation, variables, &out); err != nil {
		return err
	}
	if msg := firstUserError(out.Data.InventoryActivate.UserErrors); msg != "" {
		return apiErr("Shopify could not stock this item at the location: %s", msg)
	}
	return nil
}
