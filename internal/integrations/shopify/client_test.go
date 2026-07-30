package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(url string) *Client {
	c := New("2024-10", 0)
	c.baseURL = url
	return c
}

func TestCreateDraftProductSuccess(t *testing.T) {
	var seenPrice string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Shopify-Access-Token") != "tok" {
			t.Errorf("missing/incorrect access token header")
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "productCreate"):
			_, _ = w.Write([]byte(`{"data":{"productCreate":{"product":{"id":"gid://shopify/Product/999","handle":"dragon","variants":{"nodes":[{"id":"gid://shopify/ProductVariant/111"}]}},"userErrors":[]}}}`))
		case strings.Contains(req.Query, "productVariantsBulkUpdate"):
			variants, _ := req.Variables["variants"].([]any)
			if len(variants) == 1 {
				if v, ok := variants[0].(map[string]any); ok {
					seenPrice, _ = v["price"].(string)
				}
			}
			_, _ = w.Write([]byte(`{"data":{"productVariantsBulkUpdate":{"productVariants":[{"id":"gid://shopify/ProductVariant/111"}],"userErrors":[]}}}`))
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	}))
	defer server.Close()

	ref, err := testClient(server.URL).CreateDraftProduct(context.Background(), "8nuv4p-1v.myshopify.com", "tok", ProductDraft{
		Title: "Dragon", Vendor: "Decor", ProductType: "Home decor", Tags: []string{"3dprint"},
		PriceINR: 3999, Metafields: []Metafield{{Namespace: "tensor", Key: "design_cp", Type: "number_decimal", Value: "111.40"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.GID != "gid://shopify/Product/999" || ref.Handle != "dragon" {
		t.Errorf("unexpected ref: %+v", ref)
	}
	if ref.AdminURL != "https://admin.shopify.com/store/8nuv4p-1v/products/999" {
		t.Errorf("unexpected admin url: %s", ref.AdminURL)
	}
	if seenPrice != "3999" {
		t.Errorf("expected variant price 3999, got %q", seenPrice)
	}
}

func TestCreateDraftProductUserError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"productCreate":{"product":null,"userErrors":[{"field":["title"],"message":"Title can't be blank"}]}}}`))
	}))
	defer server.Close()

	_, err := testClient(server.URL).CreateDraftProduct(context.Background(), "shop.myshopify.com", "tok", ProductDraft{Title: ""})
	if err == nil {
		t.Fatal("expected a userError to surface")
	}
	if !strings.Contains(err.Error(), "Title can't be blank") {
		t.Errorf("expected the Shopify message, got: %v", err)
	}
}

func TestCreateDraftProductBadToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := testClient(server.URL).CreateDraftProduct(context.Background(), "shop.myshopify.com", "bad", ProductDraft{Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "rejected the access token") {
		t.Errorf("expected a token-rejection error, got: %v", err)
	}
}

func TestCreateDraftProductRetriesOnRateLimit(t *testing.T) {
	var createCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		switch {
		case strings.Contains(req.Query, "productCreate"):
			createCalls++
			if createCalls == 1 {
				w.Header().Set("Retry-After", "0.05")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"productCreate":{"product":{"id":"gid://shopify/Product/1","handle":"h","variants":{"nodes":[{"id":"gid://shopify/ProductVariant/2"}]}},"userErrors":[]}}}`))
		case strings.Contains(req.Query, "productVariantsBulkUpdate"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"productVariantsBulkUpdate":{"productVariants":[{"id":"x"}],"userErrors":[]}}}`))
		}
	}))
	defer server.Close()

	ref, err := testClient(server.URL).CreateDraftProduct(context.Background(), "shop.myshopify.com", "tok",
		ProductDraft{Title: "x", PriceINR: 100})
	if err != nil {
		t.Fatalf("expected success after one retry, got: %v", err)
	}
	if createCalls != 2 {
		t.Fatalf("expected 2 productCreate calls (429 then success), got %d", createCalls)
	}
	if ref.GID == "" {
		t.Error("expected a product ref after the retry")
	}
}

func TestRateLimitExhaustedSurfacesSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0.01")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := testClient(server.URL).CreateDraftProduct(context.Background(), "shop.myshopify.com", "tok",
		ProductDraft{Title: "x", PriceINR: 100})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected the ErrRateLimited sentinel through the wrap, got: %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":    0,
		"0":   0,
		"2":   2 * time.Second,
		"0.5": 500 * time.Millisecond,
		"abc": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
