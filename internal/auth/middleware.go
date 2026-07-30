package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const contextUserKey = "tensor.user"

// Guards builds the Gin middlewares that enforce authentication and RBAC.
type Guards struct {
	verifier       *Verifier
	internalSecret string
	// Optional permission-freshness enforcement (see freshness.go). Nil unless
	// EnablePermissionFreshness has been called.
	versionLoader VersionLoader
	versionCache  *versionCache
}

// NewGuards wires the guards to the token verifier and the internal shared
// secret.
func NewGuards(verifier *Verifier, internalSecret string) *Guards {
	return &Guards{verifier: verifier, internalSecret: internalSecret}
}

func abortDetail(c *gin.Context, status int, detail string) {
	c.AbortWithStatusJSON(status, gin.H{"detail": detail})
}

// RequireUser verifies the bearer token and stores the identity on the context.
// A missing token is 401; a misconfigured service is 500; a bad token is 401
// with the verifier's exact message. All carry WWW-Authenticate: Bearer except
// the 500.
func (g *Guards) RequireUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			c.Header("WWW-Authenticate", "Bearer")
			abortDetail(c, http.StatusUnauthorized, "Missing bearer token")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))

		claims, err := g.verifier.VerifyToken(c.Request.Context(), token)
		if err != nil {
			var cfg ConfigError
			if errors.As(err, &cfg) {
				abortDetail(c, http.StatusInternalServerError, "Authentication is not configured on this service")
				return
			}
			c.Header("WWW-Authenticate", "Bearer")
			var invalid InvalidTokenError
			if errors.As(err, &invalid) {
				abortDetail(c, http.StatusUnauthorized, invalid.Msg)
				return
			}
			abortDetail(c, http.StatusUnauthorized, "Access token is invalid")
			return
		}

		user := NewAuthenticatedUser(claims)
		// Reject a token whose grants have since changed (fails closed when the
		// current version cannot be read). No-op unless enforcement is enabled.
		if !g.permissionsFresh(c, user) {
			return
		}
		c.Set(contextUserKey, user)
		c.Next()
	}
}

// RequirePermission rejects a request whose identity lacks the given permission
// key. It must be chained after RequireUser; an absent identity is a 401 so an
// anonymous caller never reaches a 403.
func (g *Guards) RequirePermission(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFrom(c)
		if !ok {
			c.Header("WWW-Authenticate", "Bearer")
			abortDetail(c, http.StatusUnauthorized, "Missing bearer token")
			return
		}
		if !user.Has(key) {
			abortDetail(c, http.StatusForbidden, "Missing required permission: "+key)
			return
		}
		c.Next()
	}
}

// RequireInternalSecret guards the service-to-service endpoints with a
// constant-time secret compare. An unconfigured secret fails closed with 503.
func (g *Guards) RequireInternalSecret() gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.internalSecret == "" {
			abortDetail(c, http.StatusServiceUnavailable, "Internal API is not configured")
			return
		}
		provided := c.GetHeader("X-Internal-Secret")
		if provided == "" ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(g.internalSecret)) != 1 {
			abortDetail(c, http.StatusForbidden, "Invalid internal secret")
			return
		}
		c.Next()
	}
}

// UserFrom returns the authenticated identity stored by RequireUser.
func UserFrom(c *gin.Context) (AuthenticatedUser, bool) {
	v, ok := c.Get(contextUserKey)
	if !ok {
		return AuthenticatedUser{}, false
	}
	u, ok := v.(AuthenticatedUser)
	return u, ok
}
