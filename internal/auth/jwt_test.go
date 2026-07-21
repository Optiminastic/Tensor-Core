package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	testIssuer   = "http://localhost:3001"
	testAudience = "tensor-core"
	testKID      = "test-key-1"
)

type jwtEnv struct {
	priv     jwk.Key // private signing key (kid = testKID)
	verifier *Verifier
	server   *httptest.Server
}

func newJWTEnv(t *testing.T) *jwtEnv {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	privKey, err := jwk.FromRaw(priv)
	if err != nil {
		t.Fatalf("priv jwk: %v", err)
	}
	_ = privKey.Set(jwk.KeyIDKey, testKID)
	_ = privKey.Set(jwk.AlgorithmKey, jwa.EdDSA)

	pubKey, err := jwk.FromRaw(pub)
	if err != nil {
		t.Fatalf("pub jwk: %v", err)
	}
	_ = pubKey.Set(jwk.KeyIDKey, testKID)
	_ = pubKey.Set(jwk.AlgorithmKey, jwa.EdDSA)

	set := jwk.NewSet()
	_ = set.AddKey(pubKey)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(server.Close)

	v := NewVerifier(context.Background(), server.URL, testIssuer, testAudience, time.Minute)
	return &jwtEnv{priv: privKey, verifier: v, server: server}
}

// sign builds a token from a builder configured by fn and signs it with EdDSA.
func (e *jwtEnv) sign(t *testing.T, build func(*jwt.Builder) *jwt.Builder) string {
	t.Helper()
	b := jwt.NewBuilder().
		Subject("usr_admin").
		Issuer(testIssuer).
		Audience([]string{testAudience}).
		Expiration(time.Now().Add(15*time.Minute)).
		Claim("email", "admin@opti.com")
	tok, err := build(b).Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, e.priv))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

func TestVerifyValidToken(t *testing.T) {
	e := newJWTEnv(t)
	token := e.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.
			Claim("roles", []string{"PROJECT_LEAD"}).
			Claim("permissions", []string{"design:read", "pricing:read"}).
			Claim("permissionsVersion", 3).
			Claim("sessionId", "ses_xyz")
	})

	claims, err := e.verifier.VerifyToken(context.Background(), token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Sub != "usr_admin" || claims.Email != "admin@opti.com" {
		t.Errorf("sub/email = %q/%q", claims.Sub, claims.Email)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != RoleProjectLead {
		t.Errorf("roles = %v", claims.Roles)
	}
	if claims.PermissionsVersion != 3 {
		t.Errorf("permissionsVersion = %d, want 3", claims.PermissionsVersion)
	}
	if claims.SessionID == nil || *claims.SessionID != "ses_xyz" {
		t.Errorf("sessionId = %v", claims.SessionID)
	}
}

func TestVerifyEmptyRolesAndPermissions(t *testing.T) {
	e := newJWTEnv(t)
	token := e.sign(t, func(b *jwt.Builder) *jwt.Builder { return b })
	claims, err := e.verifier.VerifyToken(context.Background(), token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(claims.Roles) != 0 || len(claims.Permissions) != 0 {
		t.Errorf("expected empty roles/permissions, got %v / %v", claims.Roles, claims.Permissions)
	}
}

func TestVerifyExpired(t *testing.T) {
	e := newJWTEnv(t)
	token := e.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Expiration(time.Now().Add(-time.Minute))
	})
	_, err := e.verifier.VerifyToken(context.Background(), token)
	assertInvalid(t, err, "Access token has expired")
}

func TestVerifyWrongAudience(t *testing.T) {
	e := newJWTEnv(t)
	token := e.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Audience([]string{"someone-else"})
	})
	_, err := e.verifier.VerifyToken(context.Background(), token)
	assertInvalid(t, err, "Access token was not issued for this service")
}

func TestVerifyWrongIssuer(t *testing.T) {
	e := newJWTEnv(t)
	token := e.sign(t, func(b *jwt.Builder) *jwt.Builder {
		return b.Issuer("http://evil.example")
	})
	_, err := e.verifier.VerifyToken(context.Background(), token)
	assertInvalid(t, err, "Access token has an unexpected issuer")
}

func TestVerifyMissingExp(t *testing.T) {
	e := newJWTEnv(t)
	// Build a token with no expiration by zeroing it after the helper sets it.
	tok, _ := jwt.NewBuilder().
		Subject("usr_admin").Issuer(testIssuer).Audience([]string{testAudience}).
		Claim("email", "admin@opti.com").Build()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, e.priv))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = e.verifier.VerifyToken(context.Background(), string(signed))
	assertInvalid(t, err, "Access token is invalid")
}

func TestVerifyRejectsHS256(t *testing.T) {
	e := newJWTEnv(t)
	tok, _ := jwt.NewBuilder().
		Subject("usr_admin").Issuer(testIssuer).Audience([]string{testAudience}).
		Expiration(time.Now().Add(time.Minute)).Claim("email", "a@b.com").Build()
	// An attacker downgrades to HMAC. The alg allowlist must reject it.
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.HS256, []byte("public-key-as-secret")))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = e.verifier.VerifyToken(context.Background(), string(signed))
	assertInvalid(t, err, "Access token is invalid")
}

func TestVerifyWrongSigningKey(t *testing.T) {
	e := newJWTEnv(t)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherKey, _ := jwk.FromRaw(otherPriv)
	_ = otherKey.Set(jwk.KeyIDKey, "unknown-kid")
	_ = otherKey.Set(jwk.AlgorithmKey, jwa.EdDSA)

	tok, _ := jwt.NewBuilder().
		Subject("usr_admin").Issuer(testIssuer).Audience([]string{testAudience}).
		Expiration(time.Now().Add(time.Minute)).Claim("email", "a@b.com").Build()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, otherKey))

	_, err := e.verifier.VerifyToken(context.Background(), string(signed))
	assertInvalid(t, err, "Access token is invalid")
}

func TestVerifyConfigErrors(t *testing.T) {
	// No JWKS URL.
	noJWKS := NewVerifier(context.Background(), "", testIssuer, testAudience, time.Minute)
	if _, err := noJWKS.VerifyToken(context.Background(), "x"); err == nil {
		t.Error("expected config error for missing JWKS URL")
	} else if _, ok := err.(ConfigError); !ok {
		t.Errorf("want ConfigError, got %T", err)
	}

	// JWKS URL set but issuer empty.
	e := newJWTEnv(t)
	noIss := NewVerifier(context.Background(), e.server.URL, "", testAudience, time.Minute)
	if _, err := noIss.VerifyToken(context.Background(), "x"); err == nil {
		t.Error("expected config error for missing issuer")
	} else if _, ok := err.(ConfigError); !ok {
		t.Errorf("want ConfigError, got %T", err)
	}
}

func assertInvalid(t *testing.T, err error, wantMsg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", wantMsg)
	}
	ite, ok := err.(InvalidTokenError)
	if !ok {
		t.Fatalf("want InvalidTokenError, got %T: %v", err, err)
	}
	if ite.Msg != wantMsg {
		t.Errorf("message = %q, want %q", ite.Msg, wantMsg)
	}
}
