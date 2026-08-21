package shopify

import "strings"

// operationName extracts the first selection-set field from a GraphQL document,
// e.g. "productCreate" from `mutation productCreate($i: X!) { productCreate(...) }`
// or "products" from `query { products(first: 50) { ... } }`. That first field is
// the operation Tensor invoked on the store's Admin API. An unparseable document
// yields "unknown".
func operationName(query string) string {
	_, rest, found := strings.Cut(query, "{")
	if !found {
		return "unknown"
	}
	start := 0
	for start < len(rest) && !isIdentStart(rest[start]) {
		start++
	}
	end := start
	for end < len(rest) && isIdentPart(rest[end]) {
		end++
	}
	if end == start {
		return "unknown"
	}
	return rest[start:end]
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentPart(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

// operationScopes maps each Admin API operation Tensor sends to the access scope
// it exercises, so the API activity log can show which permission each request
// used. Operations not listed here return an empty scope.
var operationScopes = map[string]string{
	"product":                   "read_products",
	"products":                  "read_products",
	"productCreate":             "write_products",
	"productUpdate":             "write_products",
	"productVariantsBulkUpdate": "write_products",
	"productCreateMedia":        "write_products",
	"productDeleteMedia":        "write_products",
	"stagedUploadsCreate":       "write_products",
	"locations":                 "read_inventory",
	"inventoryActivate":         "write_inventory",
	"inventorySetQuantities":    "write_inventory",
	"order":                     "read_orders",
	"orders":                    "read_orders",
}

// scopeForOperation returns the access scope an operation exercises, or "" when
// the operation is not one Tensor is known to send.
func scopeForOperation(op string) string {
	return operationScopes[op]
}
