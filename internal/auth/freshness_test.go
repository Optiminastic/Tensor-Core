package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func freshGuards(loader VersionLoader, ttl time.Duration) *Guards {
	g := &Guards{}
	g.EnablePermissionFreshness(loader, ttl)
	return g
}

func freshCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return c, w
}

func TestPermissionsFreshDisabledWithoutLoader(t *testing.T) {
	g := &Guards{} // enforcement not enabled
	c, _ := freshCtx()
	if !g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 0}) {
		t.Fatal("expected pass when enforcement is disabled")
	}
}

func TestPermissionsFreshCurrentTokenPasses(t *testing.T) {
	g := freshGuards(func(context.Context, string) (int, error) { return 3, nil }, time.Minute)
	c, _ := freshCtx()
	if !g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 3}) {
		t.Fatal("token version equal to db version should pass")
	}
}

func TestPermissionsFreshStaleTokenRejected(t *testing.T) {
	g := freshGuards(func(context.Context, string) (int, error) { return 5, nil }, time.Minute)
	c, w := freshCtx()
	if g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 4}) {
		t.Fatal("a token older than the db version must be rejected")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for a stale token, got %d", w.Code)
	}
}

func TestPermissionsFreshFailsClosedOnLoaderError(t *testing.T) {
	g := freshGuards(func(context.Context, string) (int, error) { return 0, errors.New("db down") }, time.Minute)
	c, w := freshCtx()
	if g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 0}) {
		t.Fatal("must fail closed when the current version cannot be read")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 on loader error, got %d", w.Code)
	}
}

func TestVersionCacheHitAvoidsLoader(t *testing.T) {
	calls := 0
	g := freshGuards(func(context.Context, string) (int, error) { calls++; return 2, nil }, time.Minute)
	c, _ := freshCtx()
	for i := 0; i < 3; i++ {
		g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 2})
	}
	if calls != 1 {
		t.Fatalf("expected the loader to be called once (then cached), got %d", calls)
	}
}

func TestVersionCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	g := freshGuards(func(context.Context, string) (int, error) { calls++; return 2, nil }, 30*time.Second)
	base := time.Unix(1000, 0)
	g.versionCache.now = func() time.Time { return base }
	c, _ := freshCtx()

	g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 2}) // loads, caches until base+30s
	base = base.Add(31 * time.Second)                                        // advance past the TTL
	g.permissionsFresh(c, AuthenticatedUser{ID: "u", PermissionsVersion: 2}) // cache expired, reloads

	if calls != 2 {
		t.Fatalf("expected the loader re-called after the TTL, got %d", calls)
	}
}
