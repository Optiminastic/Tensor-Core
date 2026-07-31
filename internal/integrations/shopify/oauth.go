package shopify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// OrderImportScopes are the scopes the inbound order-import app requests.
const OrderImportScopes = "read_orders,read_products"

// shopDomainPattern validates a store domain before it is used in a URL, so a
// crafted value cannot redirect the OAuth flow elsewhere.
var shopDomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.myshopify\.com$`)

// ValidShopDomain reports whether shop is a well-formed myshopify.com domain.
func ValidShopDomain(shop string) bool { return shopDomainPattern.MatchString(shop) }

// AuthorizeURL builds the Shopify OAuth authorize URL to redirect the admin to.
func AuthorizeURL(shop, clientID, scopes, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("scope", scopes)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return fmt.Sprintf("https://%s/admin/oauth/authorize?%s", shop, q.Encode())
}

// VerifyCallbackHMAC checks the `hmac` query parameter Shopify appends to the
// OAuth callback: the other params sorted and joined key=value with &, HMAC-SHA256
// hex with the app secret. A constant-time compare guards against timing attacks.
func VerifyCallbackHMAC(query url.Values, secret string) bool {
	provided := query.Get("hmac")
	if provided == "" {
		return false
	}
	keys := make([]string, 0, len(query))
	for k := range query {
		if k == "hmac" || k == "signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+query.Get(k))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.Join(parts, "&")))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(provided))
}

// VerifyWebhookHMAC checks the X-Shopify-Hmac-SHA256 header (base64) against the
// raw request body using the app secret.
func VerifyWebhookHMAC(body []byte, headerB64, secret string) bool {
	provided, err := base64.StdEncoding.DecodeString(headerB64)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

// statePayload is the signed OAuth state carried through the redirect: the shop it
// was issued for, a nonce, and an expiry.
type statePayload struct {
	Shop  string `json:"shop"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"`
}

// SignState returns an opaque, HMAC-signed state token binding the flow to a shop
// until expiresAt. nonce should be random per authorize call.
func SignState(shop, nonce, secret string, expiresAt time.Time) string {
	payload, _ := json.Marshal(statePayload{Shop: shop, Nonce: nonce, Exp: expiresAt.Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + signStateBody(body, secret)
}

// VerifyState checks a state token's signature and expiry and returns the shop it
// was issued for.
func VerifyState(token, secret string, now time.Time) (string, bool) {
	body, sig, found := strings.Cut(token, ".")
	if !found || !hmac.Equal([]byte(sig), []byte(signStateBody(body, secret))) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", false
	}
	var p statePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", false
	}
	if now.Unix() > p.Exp {
		return "", false
	}
	return p.Shop, true
}

func signStateBody(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ExchangeCode swaps an OAuth authorization code for an offline access token,
// returning the token and the granted scopes.
func (c *Client) ExchangeCode(ctx context.Context, shop, clientID, clientSecret, code string) (token, scopes string, err error) {
	base := c.baseURL
	if base == "" {
		base = "https://" + shop
	}
	body, _ := json.Marshal(map[string]string{
		"client_id": clientID, "client_secret": clientSecret, "code": code,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/admin/oauth/access_token", strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", apiErr("could not reach Shopify: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", apiErr("Shopify rejected the OAuth code (%d)", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	raw, err := readAll(resp)
	if err != nil {
		return "", "", apiErr("could not read Shopify token response: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", "", apiErr("Shopify did not return an access token")
	}
	return out.AccessToken, out.Scope, nil
}

const webhookCreateMutation = `mutation RegisterWebhook($topic: WebhookSubscriptionTopic!, $sub: WebhookSubscriptionInput!) {
  webhookSubscriptionCreate(topic: $topic, webhookSubscription: $sub) {
    webhookSubscription { id }
    userErrors { field message }
  }
}`

// RegisterOrdersPaidWebhook subscribes the store to the ORDERS_PAID topic pointing
// at callbackURL, returning the subscription id.
func (c *Client) RegisterOrdersPaidWebhook(ctx context.Context, shop, token, callbackURL string) (string, error) {
	variables := map[string]any{
		"topic": "ORDERS_PAID",
		"sub":   map[string]any{"callbackUrl": callbackURL, "format": "JSON"},
	}
	var out struct {
		Data struct {
			WebhookSubscriptionCreate struct {
				WebhookSubscription struct {
					ID string `json:"id"`
				} `json:"webhookSubscription"`
				UserErrors []userError `json:"userErrors"`
			} `json:"webhookSubscriptionCreate"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, webhookCreateMutation, variables, &out); err != nil {
		return "", err
	}
	if msg := firstUserError(out.Data.WebhookSubscriptionCreate.UserErrors); msg != "" {
		return "", apiErr("Shopify rejected the webhook: %s", msg)
	}
	return out.Data.WebhookSubscriptionCreate.WebhookSubscription.ID, nil
}

const webhookDeleteMutation = `mutation DeleteWebhook($id: ID!) {
  webhookSubscriptionDelete(id: $id) {
    deletedWebhookSubscriptionId
    userErrors { field message }
  }
}`

// DeleteWebhook removes a webhook subscription by id. It is best-effort: the
// caller disconnects the store regardless of the result.
func (c *Client) DeleteWebhook(ctx context.Context, shop, token, id string) error {
	var out struct {
		Data struct {
			WebhookSubscriptionDelete struct {
				UserErrors []userError `json:"userErrors"`
			} `json:"webhookSubscriptionDelete"`
		} `json:"data"`
	}
	if err := c.do(ctx, shop, token, webhookDeleteMutation, map[string]any{"id": id}, &out); err != nil {
		return err
	}
	if msg := firstUserError(out.Data.WebhookSubscriptionDelete.UserErrors); msg != "" {
		return apiErr("Shopify rejected the webhook delete: %s", msg)
	}
	return nil
}
