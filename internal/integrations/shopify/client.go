// Package shopify is a minimal Admin GraphQL client for the one write the product
// needs: creating a draft product from an approved design. It mirrors the request
// shape proven in the frontend onboarding client (POST /admin/api/<v>/graphql.json
// with an X-Shopify-Access-Token header). The token is an argument, never logged.
package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/retry"
)

const defaultRequestTimeout = 15 * time.Second

// ErrRateLimited is wrapped by the APIError returned when Shopify answers 429.
// Callers can errors.Is against it; the retry loop uses it to back off.
var ErrRateLimited = errors.New("shopify rate limited")

// retryPolicy governs the automatic retry on rate limiting. Only 429 is retried
// (the request was rejected before execution, so retrying cannot duplicate a
// mutation); transport and 5xx errors are surfaced immediately.
var retryPolicy = retry.Policy{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second}

// Metafield is a custom metafield to attach to the product (namespace "tensor").
type Metafield struct {
	Namespace string
	Key       string
	Type      string
	Value     string
}

// ProductDraft is the set of details Tensor sends; the merchant finishes the rest
// (images, description, variants, collections) in Shopify.
type ProductDraft struct {
	Title       string
	Vendor      string
	ProductType string
	Tags        []string
	PriceINR    int
	Metafields  []Metafield
}

// ProductRef points back at the created draft so the UI can link to it. VariantGID
// is the default variant, persisted so a publish retry can re-set its price on the
// existing product instead of creating a duplicate.
type ProductRef struct {
	GID        string
	VariantGID string
	Handle     string
	AdminURL   string
}

// Client talks to one store's Admin GraphQL API.
type Client struct {
	http       *http.Client
	apiVersion string
	// baseURL overrides the scheme+host (tests point it at an httptest server).
	// Empty means the real https://<shop> endpoint.
	baseURL string
}

// New builds a client for the given Admin API version (e.g. "2024-10") with the
// given per-request timeout (zero uses the default). It carries a keep-alive
// transport so publishes reuse connections; construct it once and share it.
func New(apiVersion string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	return &Client{
		http:       &http.Client{Timeout: timeout, Transport: transport},
		apiVersion: apiVersion,
	}
}

// APIError is a Shopify-side failure (transport, HTTP status, or userErrors),
// safe to surface to the caller without leaking the token. It can carry a
// wrapped cause (for errors.Is/As) and rate-limit retry hints.
type APIError struct {
	msg        string
	err        error // wrapped cause, never rendered to the client
	retryable  bool
	retryAfter time.Duration
}

func (e *APIError) Error() string             { return e.msg }
func (e *APIError) Unwrap() error             { return e.err }
func (e *APIError) Retryable() bool           { return e.retryable }
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }

func apiErr(format string, args ...any) *APIError {
	return &APIError{msg: fmt.Sprintf(format, args...)}
}

// CreateProduct creates a DRAFT product with the core details + metafields and
// returns its ref (including the default variant GID) without setting a price.
// Splitting create from pricing lets a caller persist the product first, so a
// later failure and retry reuse the same draft instead of duplicating it.
// shop is "<handle>.myshopify.com".
func (c *Client) CreateProduct(ctx context.Context, shop, token string, draft ProductDraft) (ProductRef, error) {
	created, err := c.productCreate(ctx, shop, token, draft)
	if err != nil {
		return ProductRef{}, err
	}
	return ProductRef{
		GID:        created.productGID,
		VariantGID: created.variantGID,
		Handle:     created.handle,
		AdminURL:   adminURL(shop, created.productGID),
	}, nil
}

// SetPrice sets the default variant's price. It is idempotent: setting the same
// price again is a harmless no-op, so a publish retry can safely call it.
func (c *Client) SetPrice(ctx context.Context, shop, token, productGID, variantGID string, priceINR int) error {
	return c.setVariantPrice(ctx, shop, token, productGID, variantGID, priceINR)
}

// CreateDraftProduct creates a DRAFT product and sets its price in one call. It is
// the create-then-price convenience used where idempotent reuse is not needed.
func (c *Client) CreateDraftProduct(ctx context.Context, shop, token string, draft ProductDraft) (ProductRef, error) {
	ref, err := c.CreateProduct(ctx, shop, token, draft)
	if err != nil {
		return ProductRef{}, err
	}
	if draft.PriceINR > 0 && ref.VariantGID != "" {
		if err := c.SetPrice(ctx, shop, token, ref.GID, ref.VariantGID, draft.PriceINR); err != nil {
			return ProductRef{}, err
		}
	}
	return ref, nil
}

const productCreateMutation = `mutation CreateDraftProduct($product: ProductCreateInput!) {
  productCreate(product: $product) {
    product { id handle variants(first: 1) { nodes { id } } }
    userErrors { field message }
  }
}`

type createdProduct struct {
	productGID string
	handle     string
	variantGID string
}

func (c *Client) productCreate(ctx context.Context, shop, token string, draft ProductDraft) (createdProduct, error) {
	metafields := make([]map[string]string, 0, len(draft.Metafields))
	for _, m := range draft.Metafields {
		metafields = append(metafields, map[string]string{
			"namespace": m.Namespace, "key": m.Key, "type": m.Type, "value": m.Value,
		})
	}
	product := map[string]any{
		"title":       draft.Title,
		"vendor":      draft.Vendor,
		"productType": draft.ProductType,
		"tags":        draft.Tags,
		"status":      "DRAFT",
		"metafields":  metafields,
	}

	var out struct {
		Data struct {
			ProductCreate struct {
				Product struct {
					ID       string `json:"id"`
					Handle   string `json:"handle"`
					Variants struct {
						Nodes []struct {
							ID string `json:"id"`
						} `json:"nodes"`
					} `json:"variants"`
				} `json:"product"`
				UserErrors []userError `json:"userErrors"`
			} `json:"productCreate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, productCreateMutation, map[string]any{"product": product}, &out); err != nil {
		return createdProduct{}, err
	}
	if msg := firstUserError(out.Data.ProductCreate.UserErrors); msg != "" {
		return createdProduct{}, apiErr("Shopify rejected the product: %s", msg)
	}
	p := out.Data.ProductCreate.Product
	if p.ID == "" {
		return createdProduct{}, apiErr("Shopify did not return a created product")
	}
	created := createdProduct{productGID: p.ID, handle: p.Handle}
	if len(p.Variants.Nodes) > 0 {
		created.variantGID = p.Variants.Nodes[0].ID
	}
	return created, nil
}

const variantPriceMutation = `mutation SetVariantPrice($productId: ID!, $variants: [ProductVariantsBulkInput!]!) {
  productVariantsBulkUpdate(productId: $productId, variants: $variants) {
    productVariants { id }
    userErrors { field message }
  }
}`

func (c *Client) setVariantPrice(ctx context.Context, shop, token, productGID, variantGID string, priceINR int) error {
	variables := map[string]any{
		"productId": productGID,
		"variants":  []map[string]string{{"id": variantGID, "price": fmt.Sprintf("%d", priceINR)}},
	}
	var out struct {
		Data struct {
			ProductVariantsBulkUpdate struct {
				UserErrors []userError `json:"userErrors"`
			} `json:"productVariantsBulkUpdate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, variantPriceMutation, variables, &out); err != nil {
		return err
	}
	if msg := firstUserError(out.Data.ProductVariantsBulkUpdate.UserErrors); msg != "" {
		return apiErr("Shopify rejected the price: %s", msg)
	}
	return nil
}

type userError struct {
	Field   []string `json:"field"`
	Message string   `json:"message"`
}

func firstUserError(errs []userError) string {
	if len(errs) == 0 {
		return ""
	}
	return errs[0].Message
}

// do runs one GraphQL request and decodes data into out, retrying only on a 429
// rate-limit response (honouring Retry-After). Transport failures and 5xx are
// surfaced immediately rather than retried, so a create mutation that may have
// executed is never sent twice.
func (c *Client) do(ctx context.Context, shop, token, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	base := c.baseURL
	if base == "" {
		base = "https://" + shop
	}
	endpoint := fmt.Sprintf("%s/admin/api/%s/graphql.json", base, c.apiVersion)

	return retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		return c.doOnce(ctx, endpoint, token, body, out)
	})
}

// doOnce performs a single GraphQL request. A 429 becomes a retryable APIError
// carrying the server's Retry-After; every other failure is terminal.
func (c *Client) doOnce(ctx context.Context, endpoint, token string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)

	resp, err := c.http.Do(req)
	if err != nil {
		return &APIError{msg: fmt.Sprintf("could not reach Shopify: %v", err), err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return &APIError{
			msg:        "Shopify is rate limiting requests; please retry shortly",
			err:        ErrRateLimited,
			retryable:  true,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return apiErr("Shopify rejected the access token (check the token and the app's write_products scope)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiErr("Shopify request failed (%d)", resp.StatusCode)
	}

	// Decode into a shape that carries both top-level errors and the data.
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	raw, err := readAll(resp)
	if err != nil {
		return apiErr("could not read Shopify response: %v", err)
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Errors) > 0 {
		return apiErr("Shopify GraphQL error: %s", envelope.Errors[0].Message)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return apiErr("could not decode Shopify response: %v", err)
	}
	return nil
}

// parseRetryAfter reads Shopify's Retry-After header, expressed in (possibly
// fractional) seconds. An absent or unparseable value yields zero, letting the
// retry loop fall back to its own backoff.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func readAll(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// adminURL builds the store admin link to the product from the shop domain and
// the product gid (gid://shopify/Product/<numeric>).
func adminURL(shop, productGID string) string {
	handle := strings.TrimSuffix(shop, ".myshopify.com")
	numeric := productGID
	if i := strings.LastIndex(productGID, "/"); i >= 0 {
		numeric = productGID[i+1:]
	}
	return fmt.Sprintf("https://admin.shopify.com/store/%s/products/%s", handle, numeric)
}
