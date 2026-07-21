package auth

import (
	"context"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// ConfigError is a server-side misconfiguration (no JWKS URL, no issuer, or an
// unreachable JWKS). The HTTP layer maps it to 500 -- it is our fault, not the
// caller's bad token.
type ConfigError struct{ Msg string }

func (e ConfigError) Error() string { return e.Msg }

// InvalidTokenError is a bad or unacceptable token. The HTTP layer maps it to
// 401 and surfaces Msg as the detail. The four messages reproduce the Python
// backend verbatim so the frontend sees identical text.
type InvalidTokenError struct{ Msg string }

func (e InvalidTokenError) Error() string { return e.Msg }

var (
	errExpired       = InvalidTokenError{"Access token has expired"}
	errWrongAudience = InvalidTokenError{"Access token was not issued for this service"}
	errWrongIssuer   = InvalidTokenError{"Access token has an unexpected issuer"}
	errInvalid       = InvalidTokenError{"Access token is invalid"}
)

// allowedAlgs pins the acceptable signature algorithms. Better Auth signs with
// EdDSA; RS256 and ES256 are accepted for forward compatibility. Anything else
// -- including "none" and any HMAC alg -- is rejected, which defends against
// alg-substitution attacks.
var allowedAlgs = map[jwa.SignatureAlgorithm]struct{}{
	jwa.EdDSA: {},
	jwa.RS256: {},
	jwa.ES256: {},
}

// Verifier verifies access tokens against the frontend's JWKS. It is safe for
// concurrent use; the underlying jwk.Cache refreshes keys in the background.
type Verifier struct {
	jwksURL  string
	issuer   string
	audience string
	cache    *jwk.Cache
	now      func() time.Time
}

// NewVerifier builds a verifier. It never fails: configuration is checked lazily
// at VerifyToken so a missing JWKS URL or issuer becomes a per-request 500 (as
// in the Python backend) rather than a boot failure. ctx should live for the
// process; it drives the JWKS cache's background refresh.
func NewVerifier(ctx context.Context, jwksURL, issuer, audience string, cacheTTL time.Duration) *Verifier {
	v := &Verifier{
		jwksURL:  jwksURL,
		issuer:   issuer,
		audience: audience,
		now:      time.Now,
	}
	if jwksURL != "" {
		cache := jwk.NewCache(ctx)
		// The min refresh interval bounds how often we refetch; the cache also
		// honours the JWKS endpoint's Cache-Control. On an unknown kid we force a
		// refresh below, matching PyJWKClient's rotate-without-restart behaviour.
		_ = cache.Register(jwksURL, jwk.WithMinRefreshInterval(cacheTTL))
		v.cache = cache
	}
	return v
}

// VerifyToken verifies a compact JWS access token and returns its claims. The
// validation order and error messages mirror the Python verify_token exactly.
func (v *Verifier) VerifyToken(ctx context.Context, token string) (TokenClaims, error) {
	if v.jwksURL == "" || v.cache == nil {
		return TokenClaims{}, ConfigError{"AUTH_JWKS_URL is not set; cannot verify access tokens"}
	}
	if v.issuer == "" {
		return TokenClaims{}, ConfigError{"AUTH_ISSUER is not set; refusing to verify tokens without it"}
	}

	raw := []byte(token)

	// Inspect the protected header first: reject any algorithm outside the
	// allowlist before touching keys.
	msg, err := jws.Parse(raw)
	if err != nil || len(msg.Signatures()) == 0 {
		return TokenClaims{}, errInvalid
	}
	header := msg.Signatures()[0].ProtectedHeaders()
	if _, ok := allowedAlgs[header.Algorithm()]; !ok {
		return TokenClaims{}, errInvalid
	}

	set, err := v.cache.Get(ctx, v.jwksURL)
	if err != nil {
		// The endpoint is set but unreachable/unusable: a server problem, not a
		// bad token.
		return TokenClaims{}, ConfigError{"Cannot fetch the signing keys (JWKS)"}
	}
	if kid := header.KeyID(); kid != "" {
		if _, found := set.LookupKeyID(kid); !found {
			if refreshed, rerr := v.cache.Refresh(ctx, v.jwksURL); rerr == nil {
				set = refreshed
			}
		}
	}

	// Verify the signature and parse claims. Claim validation is done by hand
	// below so the error precedence and messages match PyJWT.
	tok, err := jwt.Parse(raw,
		jwt.WithKeySet(set, jws.WithInferAlgorithmFromKey(true)),
		jwt.WithValidate(false),
	)
	if err != nil {
		return TokenClaims{}, errInvalid
	}

	// Required claims present first (missing -> generic invalid), matching
	// PyJWT's require=[exp,iss,aud,sub].
	if tok.Expiration().IsZero() || len(tok.Audience()) == 0 ||
		tok.Issuer() == "" || tok.Subject() == "" {
		return TokenClaims{}, errInvalid
	}
	// Then value checks, in PyJWT order: exp, then aud, then iss. Leeway is 0.
	if v.now().After(tok.Expiration()) {
		return TokenClaims{}, errExpired
	}
	if !containsString(tok.Audience(), v.audience) {
		return TokenClaims{}, errWrongAudience
	}
	if tok.Issuer() != v.issuer {
		return TokenClaims{}, errWrongIssuer
	}

	return claimsFromToken(tok), nil
}

func claimsFromToken(tok jwt.Token) TokenClaims {
	priv := tok.PrivateClaims()
	email, _ := priv["email"].(string)

	var sessionID *string
	if s, ok := priv["sessionId"].(string); ok {
		sessionID = &s
	}

	return TokenClaims{
		Sub:                tok.Subject(),
		Email:              email,
		Roles:              rolesFromClaim(priv["roles"]),
		Permissions:        stringsFromClaim(priv["permissions"]),
		PermissionsVersion: intFromClaim(priv["permissionsVersion"]),
		SessionID:          sessionID,
	}
}

func rolesFromClaim(v any) []RoleName {
	items := stringsFromClaim(v)
	roles := make([]RoleName, 0, len(items))
	for _, s := range items {
		roles = append(roles, RoleName(s))
	}
	return roles
}

func stringsFromClaim(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func intFromClaim(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
