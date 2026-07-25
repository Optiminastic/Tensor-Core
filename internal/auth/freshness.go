package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Permission freshness closes the staleness window in the token model. A token
// carries the permissions_version it was minted with; anything that changes a
// user's grants calls BumpPermissionsVersion. Enforcing the version here means a
// revoked permission takes effect within the cache TTL instead of lingering until
// the token expires.
//
// The check is only as expensive as one database read per user per TTL: the
// current version is cached, so the hot path stays an in-memory comparison.

// VersionLoader returns a user's current permissions version from the source of
// truth (the database). ErrNoRows must be mapped to version 0 by the loader.
type VersionLoader func(ctx context.Context, userID string) (int, error)

type cachedVersion struct {
	version int
	expiry  time.Time
}

// versionCache is a small TTL cache of per-user permissions versions. It is
// safe for concurrent use.
type versionCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]cachedVersion
	now   func() time.Time
}

func newVersionCache(ttl time.Duration) *versionCache {
	return &versionCache{ttl: ttl, items: make(map[string]cachedVersion), now: time.Now}
}

func (c *versionCache) get(userID string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[userID]
	if !ok || c.now().After(it.expiry) {
		return 0, false
	}
	return it.version, true
}

func (c *versionCache) set(userID string, v int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[userID] = cachedVersion{version: v, expiry: c.now().Add(c.ttl)}
}

// EnablePermissionFreshness turns on per-request permission-version enforcement,
// backed by a TTL cache over loader. Left unset, RequireUser skips the check
// (the previous behaviour), which keeps unit/integration wiring unchanged.
func (g *Guards) EnablePermissionFreshness(loader VersionLoader, ttl time.Duration) {
	g.versionLoader = loader
	g.versionCache = newVersionCache(ttl)
}

// permissionsFresh reports whether the token's permissions version is still
// current, writing the appropriate response and returning false when it is not.
// It fails closed: an inability to confirm freshness denies the request rather
// than trusting a possibly-stale token.
func (g *Guards) permissionsFresh(c *gin.Context, user AuthenticatedUser) bool {
	if g.versionLoader == nil {
		return true // enforcement not enabled
	}
	current, err := g.currentVersion(c.Request.Context(), user.ID)
	if err != nil {
		abortDetail(c, http.StatusServiceUnavailable, "Authorization is temporarily unavailable")
		return false
	}
	if user.PermissionsVersion < current {
		// The token was minted before a grant change. Force a re-mint; the
		// frontend refreshes the token rather than signing the user out.
		c.Header("WWW-Authenticate", "Bearer")
		abortDetail(c, http.StatusUnauthorized, "Your access has changed; please retry")
		return false
	}
	return true
}

// currentVersion returns the user's current permissions version, from the cache
// when warm, otherwise from the loader (and caches it).
func (g *Guards) currentVersion(ctx context.Context, userID string) (int, error) {
	if v, ok := g.versionCache.get(userID); ok {
		return v, nil
	}
	v, err := g.versionLoader(ctx, userID)
	if err != nil {
		return 0, err
	}
	g.versionCache.set(userID, v)
	return v, nil
}
