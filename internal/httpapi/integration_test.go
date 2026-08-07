package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// setupStore migrates and truncates a throwaway database. It skips when
// TENSOR_TEST_DB is unset, so unit-only runs stay fast.
func setupStore(t *testing.T) *db.Store {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := os.Getenv("TENSOR_TEST_DB")
	if dsn == "" {
		t.Skip("set TENSOR_TEST_DB to run integration tests")
	}
	ctx := context.Background()
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.MigrateRiver(ctx, dsn); err != nil {
		t.Fatalf("river migrate: %v", err)
	}
	store, err := db.Open(ctx, dsn, db.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(store.Close)
	_, err = store.Pool.Exec(ctx, `TRUNCATE brands, projects, user_invites, user_authz_state,
		user_roles, role_permissions, permissions, roles, cost_assumption_sets,
		material_profiles, machine_profiles,
		orders, production_jobs, batches, filament_inventory, dispatch_orders,
		file_assets, shopify_connections RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return store
}

func seedAll(t *testing.T, store *db.Store) {
	t.Helper()
	ctx := context.Background()
	res, err := auth.SyncAll(ctx, store)
	if err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	if res.Permissions != 37 || res.Roles != 6 || res.Grants != 80 {
		t.Fatalf("seed counts = %+v, want 37/6/80", res)
	}
	seedGiftingBrand(t, store)
}

// seedGiftingBrand inserts the one brand the integration tests price against.
// Brands are user-created in production (no built-in seed), so the fixture lives
// with the tests. Its ladder and thresholds come from the engine constants that
// also back the pure-engine fallback, keeping the priced numbers stable.
func seedGiftingBrand(t *testing.T, store *db.Store) {
	t.Helper()
	ctx := context.Background()
	ladderJSON, err := json.Marshal(pricing.GiftingLadder)
	if err != nil {
		t.Fatalf("marshal ladder: %v", err)
	}
	green, yellow := pricing.CPThresholds(pricing.BrandGifting)
	entryHours := pricing.GiftingEntryMachineHours
	entryRung := int32(pricing.GiftingEntryRung)
	if _, err := store.Q.InsertBrand(ctx, gen.InsertBrandParams{
		ID:                uuid.New(),
		Slug:              string(pricing.BrandGifting),
		Name:              "Test Gifting",
		StartingPrice:     999,
		IsActive:          true,
		Ladder:            ladderJSON,
		CpGreenMax:        green,
		CpYellowMax:       yellow,
		EntryMachineHours: &entryHours,
		EntryRung:         &entryRung,
	}); err != nil {
		t.Fatalf("seed gifting brand: %v", err)
	}
}

func TestIntegrationSeedAndAuthz(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	// Re-seeding is idempotent.
	res, err := auth.SyncAll(ctx, store)
	if err != nil || res.Grants != 80 {
		t.Fatalf("re-seed: %+v err=%v", res, err)
	}

	adminRole, err := store.Q.GetRoleIDByName(ctx, "ADMIN")
	if err != nil {
		t.Fatalf("get admin role: %v", err)
	}
	if err := store.Q.InsertUserRole(ctx, gen.InsertUserRoleParams{UserID: "usr_admin", RoleID: adminRole}); err != nil {
		t.Fatalf("insert user role: %v", err)
	}

	authz, err := auth.ResolveUserAuthz(ctx, store.Q, "usr_admin")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(authz.Roles) != 1 || authz.Roles[0] != auth.RoleAdmin {
		t.Errorf("roles = %v, want [ADMIN]", authz.Roles)
	}
	if len(authz.Permissions) != 37 {
		t.Errorf("permissions = %d, want 37", len(authz.Permissions))
	}

	// Unknown user resolves to empty, version 0.
	empty, err := auth.ResolveUserAuthz(ctx, store.Q, "nobody")
	if err != nil {
		t.Fatalf("resolve unknown: %v", err)
	}
	if len(empty.Roles) != 0 || len(empty.Permissions) != 0 || empty.PermissionsVersion != 0 {
		t.Errorf("unknown user authz = %+v, want empty/0", empty)
	}
}

func TestIntegrationInviteLifecycle(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	invite, token, err := auth.IssueInvite(ctx, store.Q, "  Designer@Opti.com ", auth.RoleDesigner, "usr_admin", auth.DefaultInviteTTL)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if invite.Email != "designer@opti.com" {
		t.Errorf("email = %q, want normalised", invite.Email)
	}

	if _, err := auth.ValidateInvite(ctx, store.Q, token); err != nil {
		t.Fatalf("validate: %v", err)
	}

	err = store.InTx(ctx, func(q *gen.Queries) error {
		_, err := auth.AcceptInvite(ctx, q, token, "usr_new")
		return err
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	authz, _ := auth.ResolveUserAuthz(ctx, store.Q, "usr_new")
	if len(authz.Roles) != 1 || authz.Roles[0] != auth.RoleDesigner {
		t.Errorf("after accept, roles = %v, want [DESIGNER]", authz.Roles)
	}
	if authz.PermissionsVersion < 1 {
		t.Errorf("permissions version = %d, want >= 1", authz.PermissionsVersion)
	}

	// The link works exactly once.
	err = store.InTx(ctx, func(q *gen.Queries) error {
		_, err := auth.AcceptInvite(ctx, q, token, "usr_other")
		return err
	})
	if _, ok := err.(auth.InviteError); !ok {
		t.Errorf("second accept err = %v, want InviteError", err)
	}
}

func TestIntegrationBootstrapOneShot(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	if exists, _ := store.Q.AdminExists(ctx); exists {
		t.Fatal("admin should not exist on a clean db")
	}
	adminRole, _ := store.Q.GetRoleIDByName(ctx, "ADMIN")
	if err := store.Q.InsertUserRole(ctx, gen.InsertUserRoleParams{UserID: "usr_first", RoleID: adminRole}); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
	if exists, _ := store.Q.AdminExists(ctx); !exists {
		t.Fatal("admin should exist after grant")
	}
}

func testServer(t *testing.T, store *db.Store, guards *auth.Guards) http.Handler {
	t.Helper()
	cfg := config.Settings{
		Environment:  "development",
		AuthAudience: "tensor-core",
		CORSOrigins:  []string{"http://localhost:3001"},
	}
	return NewServer(cfg, store, guards, nil).Router()
}

func doJSON(router http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestIntegrationPricingFromDB(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	// Pricing routes have no guard; a verifier with no JWKS is fine.
	guards := auth.NewGuards(auth.NewVerifier(ctx, "", "", "tensor-core", time.Minute), "")
	router := testServer(t, store, guards)

	specBody := map[string]any{
		"design_cp": 240,
		"brand":     "gifting",
		"fixed_costs": map[string]any{
			"packaging": 90, "shipping": 25, "rto_cod": 15, "payment_gateway": 40,
		},
		"margins": map[string]any{
			"ad_spend_pct": 0.30, "team_pct": 0.12, "overhead_pct": 0.05, "target_profit_pct": 0.15,
		},
	}

	var out struct {
		RecommendedSP *int `json:"recommended_sp"`
	}
	rr := doJSON(router, http.MethodPost, "/pricing/selling-price", "", specBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("selling-price status = %d body=%s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.RecommendedSP == nil || *out.RecommendedSP != 1199 {
		t.Fatalf("recommended = %v, want 1199", out.RecommendedSP)
	}

	// Drop the 1199 rung from the gifting ladder; the engine must now recommend
	// 1299, proving it reads the ladder from the database.
	newLadder, _ := json.Marshal([]int{999, 1299, 1499, 1799, 1999, 2499, 2999, 3499, 3999})
	if _, err := store.Q.UpdateBrand(ctx, gen.UpdateBrandParams{Slug: "gifting", Ladder: newLadder}); err != nil {
		t.Fatalf("update ladder: %v", err)
	}

	rr = doJSON(router, http.MethodPost, "/pricing/selling-price", "", specBody)
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.RecommendedSP == nil || *out.RecommendedSP != 1299 {
		t.Fatalf("after ladder edit, recommended = %v, want 1299", out.RecommendedSP)
	}
}

// --- JWT-guarded route ---------------------------------------------------------

type tokenMinter struct {
	priv     jwk.Key
	verifier *auth.Verifier
}

func newTokenMinter(t *testing.T) *tokenMinter {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privKey, _ := jwk.FromRaw(priv)
	_ = privKey.Set(jwk.KeyIDKey, "itest")
	_ = privKey.Set(jwk.AlgorithmKey, jwa.EdDSA)
	pubKey, _ := jwk.FromRaw(pub)
	_ = pubKey.Set(jwk.KeyIDKey, "itest")
	_ = pubKey.Set(jwk.AlgorithmKey, jwa.EdDSA)
	set := jwk.NewSet()
	_ = set.AddKey(pubKey)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(server.Close)

	v := auth.NewVerifier(context.Background(), server.URL, "http://issuer", "tensor-core", time.Minute)
	return &tokenMinter{priv: privKey, verifier: v}
}

func (m *tokenMinter) mint(t *testing.T, permissions []string) string {
	t.Helper()
	tok, _ := jwt.NewBuilder().
		Subject("usr_admin").Issuer("http://issuer").Audience([]string{"tensor-core"}).
		Expiration(time.Now().Add(15*time.Minute)).
		Claim("email", "a@b.com").
		Claim("roles", []string{"ADMIN"}).
		Claim("permissions", permissions).
		Build()
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, m.priv))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return string(signed)
}

func TestIntegrationBrandGuard(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)

	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)

	// Anonymous -> 401.
	if rr := doJSON(router, http.MethodGet, "/brands", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("anon GET /brands = %d, want 401", rr.Code)
	}

	// Token without brand:read -> 403.
	noPerm := minter.mint(t, []string{"design:read"})
	if rr := doJSON(router, http.MethodGet, "/brands", noPerm, nil); rr.Code != http.StatusForbidden {
		t.Errorf("no-perm GET /brands = %d, want 403", rr.Code)
	}

	// Token with brand:read -> 200 and the one seeded fixture brand.
	withPerm := minter.mint(t, []string{"brand:read"})
	rr := doJSON(router, http.MethodGet, "/brands", withPerm, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("brand:read GET /brands = %d body=%s", rr.Code, rr.Body.String())
	}
	var brands []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &brands)
	if len(brands) != 1 {
		t.Errorf("brands = %d, want 1", len(brands))
	}
}

func TestIntegrationBrandCreateAndDelete(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)

	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	manage := minter.mint(t, []string{"brand:read", "brand:manage"})

	// A free-form brand is created with a derived slug and a valid ladder.
	create := map[string]any{
		"name":           "Festive Editions",
		"starting_price": 1499,
		"ladder":         []int{1499, 1999, 2499},
		"cp_green_max":   0.25,
		"cp_yellow_max":  0.30,
	}
	rr := doJSON(router, http.MethodPost, "/brands", manage, create)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create brand = %d body=%s", rr.Code, rr.Body.String())
	}
	var brand map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &brand)
	if brand["slug"] != "festive-editions" {
		t.Fatalf("slug = %v, want festive-editions", brand["slug"])
	}

	// green above yellow is a 400 with the exact message.
	bad := map[string]any{
		"name": "Bad", "starting_price": 999, "ladder": []int{999},
		"cp_green_max": 0.40, "cp_yellow_max": 0.30,
	}
	if rr := doJSON(router, http.MethodPost, "/brands", manage, bad); rr.Code != http.StatusBadRequest {
		t.Errorf("green>yellow = %d, want 400", rr.Code)
	}

	// Delete the new brand; it has no projects, so it goes cleanly.
	if rr := doJSON(router, http.MethodDelete, "/brands/festive-editions", manage, nil); rr.Code != http.StatusNoContent {
		t.Errorf("delete brand = %d, want 204", rr.Code)
	}
	if rr := doJSON(router, http.MethodGet, "/brands/festive-editions", manage, nil); rr.Code != http.StatusNotFound {
		t.Errorf("get deleted brand = %d, want 404", rr.Code)
	}
}

func TestIntegrationBrandConnections(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)

	minter := newTokenMinter(t)
	guards := auth.NewGuards(minter.verifier, "")
	router := testServer(t, store, guards)
	manage := minter.mint(t, []string{"brand:read", "brand:manage"})

	// No connections on a fresh brand.
	rr := doJSON(router, http.MethodGet, "/brands/gifting/connections", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list connections = %d body=%s", rr.Code, rr.Body.String())
	}
	var conns []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &conns)
	if len(conns) != 0 {
		t.Fatalf("connections = %d, want 0", len(conns))
	}

	// Upsert a google_ads connection; the token must never be echoed back.
	up := map[string]any{
		"status": "connected", "external_account_id": "acct-123",
		"access_token": "secret-token",
	}
	rr = doJSON(router, http.MethodPut, "/brands/gifting/connections/google_ads", manage, up)
	if rr.Code != http.StatusOK {
		t.Fatalf("upsert connection = %d body=%s", rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("secret-token")) {
		t.Fatal("access token leaked in the connection response")
	}

	// An unknown provider is a 422.
	if rr := doJSON(router, http.MethodPut, "/brands/gifting/connections/tiktok_ads", manage, up); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown provider = %d, want 422", rr.Code)
	}

	// The connection now lists, then disconnects.
	rr = doJSON(router, http.MethodGet, "/brands/gifting/connections", manage, nil)
	_ = json.Unmarshal(rr.Body.Bytes(), &conns)
	if len(conns) != 1 || conns[0]["provider"] != "google_ads" {
		t.Fatalf("connections = %+v, want one google_ads", conns)
	}
	if rr := doJSON(router, http.MethodDelete, "/brands/gifting/connections/google_ads", manage, nil); rr.Code != http.StatusNoContent {
		t.Errorf("delete connection = %d, want 204", rr.Code)
	}
}
