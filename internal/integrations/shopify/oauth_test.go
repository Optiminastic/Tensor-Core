package shopify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"testing"
	"time"
)

func TestValidShopDomain(t *testing.T) {
	if !ValidShopDomain("acme-store.myshopify.com") {
		t.Error("a normal store domain should be valid")
	}
	for _, bad := range []string{"acme.com", "evil.com/x.myshopify.com", "", "UPPER.myshopify.com"} {
		if ValidShopDomain(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestVerifyWebhookHMAC(t *testing.T) {
	secret := "app-secret"
	body := []byte(`{"id":123}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookHMAC(body, header, secret) {
		t.Error("a correctly-signed body should verify")
	}
	if VerifyWebhookHMAC(body, header, "wrong-secret") {
		t.Error("a wrong secret must not verify")
	}
	if VerifyWebhookHMAC([]byte(`{"id":456}`), header, secret) {
		t.Error("a tampered body must not verify")
	}
}

func TestVerifyCallbackHMAC(t *testing.T) {
	secret := "app-secret"
	q := url.Values{}
	q.Set("code", "abc")
	q.Set("shop", "acme.myshopify.com")
	q.Set("timestamp", "1700000000")
	// Compute the expected hmac the same way the verifier does.
	msg := "code=abc&shop=acme.myshopify.com&timestamp=1700000000"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	q.Set("hmac", hex.EncodeToString(mac.Sum(nil)))

	if !VerifyCallbackHMAC(q, secret) {
		t.Error("a correctly-signed callback should verify")
	}
	q.Set("code", "tampered")
	if VerifyCallbackHMAC(q, secret) {
		t.Error("a tampered callback must not verify")
	}
}

func TestSignVerifyState(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token := SignState("acme.myshopify.com", "nonce123", "secret", now.Add(10*time.Minute))

	shop, ok := VerifyState(token, "secret", now)
	if !ok || shop != "acme.myshopify.com" {
		t.Errorf("verify = (%q, %v), want acme.myshopify.com/true", shop, ok)
	}
	if _, ok := VerifyState(token, "other-secret", now); ok {
		t.Error("a wrong secret must not verify state")
	}
	if _, ok := VerifyState(token, "secret", now.Add(11*time.Minute)); ok {
		t.Error("an expired state must not verify")
	}
}
