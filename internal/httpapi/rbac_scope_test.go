package httpapi

// The three role scopes the shop asked for, asserted where they are ENFORCED -
// at the HTTP boundary, with a real token and a real router.
//
// The catalog tests in internal/auth prove which permission keys a role holds.
// That is a claim about a Go map, and it is not the same as a claim about what
// the product does: a role can hold exactly the right keys and still be locked
// out, which is precisely what was happening - every non-admin held no
// brand:read, so GET /brands answered 403 and the whole brand-scoped area
// (Designs, Costing, Production) was unreachable however correct the matrix
// looked.
//
// So these mint a token carrying EXACTLY one role's permissions, as
// ResolveUserAuthz would, and ask the router.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// tokenFor mints a token carrying exactly the permissions of one role.
//
// auth.PermissionsFor is the same function the seed projects into the database,
// so a test that passes here cannot pass for a role whose grants differ from
// the catalog.
func tokenFor(t *testing.T, m *tokenMinter, role auth.RoleName, subject string) string {
	t.Helper()
	keys := auth.SortedKeys(auth.PermissionsFor(role))
	tok, err := jwt.NewBuilder().
		Subject(subject).Issuer("http://issuer").Audience([]string{"tensor-core"}).
		Expiration(time.Now().Add(15*time.Minute)).
		Claim("email", string(role)+"@example.test").
		Claim("roles", []string{string(role)}).
		Claim("permissions", keys).
		Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, m.priv))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return string(signed)
}

// rbacCase is one role asking for one endpoint, and whether the guard must let
// it through.
//
// allowed is a claim about AUTHORIZATION only, so it is asserted as "not 401 and
// not 403" rather than as 200. Several of these endpoints then reject the
// request for reasons that have nothing to do with the role - /designs wants a
// ?brand= and answers 422 without one - and pinning 200 would make this test
// fail whenever unrelated request validation changed, which is how a security
// test stops being trusted.
type rbacCase struct {
	role    auth.RoleName
	path    string
	allowed bool
	why     string
}

// Each role reaches exactly its own area and is refused the others.
//
// 403 is the assertion that matters; 200 only proves the guard let it through,
// since an empty workspace answers 200 with an empty list either way.
func TestIntegrationRoleScopesAtTheHTTPBoundary(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	cases := []rbacCase{
		// OPERATOR: the whole Production area.
		{auth.RoleOperator, "/production-jobs", true, "Production Jobs"},
		{auth.RoleOperator, "/orders", true, "Orders"},
		{auth.RoleOperator, "/batches", true, "Batch Management"},
		{auth.RoleOperator, "/inventory-items", true, "Inventory"},
		{auth.RoleOperator, "/registry/products", true, "Registry"},
		{auth.RoleOperator, "/brands", true, "the brand switcher, or no page is reachable"},
		// ...and nothing else. These are the refusals the shop asked for.
		{auth.RoleOperator, "/designs", false, "Designs is not an operator's area"},
		{auth.RoleOperator, "/config/cost-assumptions", false, "an operator never sees costs"},
		{auth.RoleOperator, "/config/materials", false, "an operator never sees costs"},
		{auth.RoleOperator, "/admin/users", false, "the team is an admin's"},

		// DESIGNER: Designs and Costing.
		{auth.RoleDesigner, "/designs", true, "Designs"},
		{auth.RoleDesigner, "/brands", true, "the brand switcher"},
		// ...and nothing else.
		{auth.RoleDesigner, "/production-jobs", false, "Production is not a designer's area"},
		{auth.RoleDesigner, "/batches", false, "Production is not a designer's area"},
		{auth.RoleDesigner, "/registry/products", false, "the Registry is a Production page"},
		{auth.RoleDesigner, "/config/cost-assumptions", false, "cost CONFIG is not costing"},
		{auth.RoleDesigner, "/admin/users", false, "the team is an admin's"},

		// ADMIN: everything.
		{auth.RoleAdmin, "/designs", true, "admin sees everything"},
		{auth.RoleAdmin, "/production-jobs", true, "admin sees everything"},
		{auth.RoleAdmin, "/registry/products", true, "admin sees everything"},
		{auth.RoleAdmin, "/config/cost-assumptions", true, "admin sees everything"},
		{auth.RoleAdmin, "/admin/users", true, "admin manages the team"},
		{auth.RoleAdmin, "/brands", true, "admin sees every brand"},
	}

	for _, tc := range cases {
		token := tokenFor(t, minter, tc.role, "usr_"+string(tc.role))
		rr := doJSON(router, http.MethodGet, tc.path, token, nil)
		refused := rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden
		switch {
		case tc.allowed && refused:
			t.Errorf("%s GET %s = %d, want the guard to ALLOW it (%s)\n  body: %s",
				tc.role, tc.path, rr.Code, tc.why, rr.Body.String())
		case !tc.allowed && rr.Code != http.StatusForbidden:
			t.Errorf("%s GET %s = %d, want 403 (%s)\n  body: %s",
				tc.role, tc.path, rr.Code, tc.why, rr.Body.String())
		}
	}
}

// The Registry is readable by an operator and carries no money for them.
//
// This is the whole reason registry:read exists. The page used to be gated on
// config:read, so the only way to show an operator which parts a SKU takes was
// to show them the cost assumptions too.
func TestIntegrationRegistryCarriesNoCostsForAnOperator(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	for _, role := range []auth.RoleName{auth.RoleOperator, auth.RoleAdmin} {
		token := tokenFor(t, minter, role, "usr_"+string(role))
		rr := doJSON(router, http.MethodGet, "/registry/products", token, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s GET /registry/products = %d, want 200", role, rr.Code)
		}
	}

	// The stripping itself, on the shape the handlers build. An integration
	// fixture with a priced bill of materials would assert the same thing
	// through more machinery; this asserts it where the decision is made.
	lines := []bomLineResponse{
		{ItemCode: strPtr("LED-01"), Quantity: 2, UnitPrice: f64(12.5), LineCost: f64(25)},
		{ItemCode: strPtr("BOX-02"), Quantity: 1, UnitPrice: f64(4), LineCost: f64(4)},
	}
	stripBomCosts(lines)
	for _, l := range lines {
		code := deref(l.ItemCode)
		if l.UnitPrice != nil || l.LineCost != nil {
			t.Errorf("%s kept a price after stripping", code)
		}
		if l.Quantity == 0 {
			t.Errorf("%s lost its quantity; the parts list is what the page is FOR", code)
		}
	}
}

func f64(v float64) *float64 { return &v }

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// Every role sees every store.
//
// This replaces a test that pinned the opposite - an admin saw all brands, an
// ungranted member saw none, and a grant through the People page let one
// through. The shop's instruction is that store-level access does not exist, so
// what is worth pinning now is that NO role is filtered: the mistake this
// catches is scoping creeping back in for one role and not another.
func TestIntegrationEveryRoleSeesEveryStore(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	adminToken := tokenFor(t, minter, auth.RoleAdmin, "usr_admin")
	for _, slug := range []string{"alpha-brand", "beta-brand"} {
		rr := doJSON(router, http.MethodPost, "/brands", adminToken, newBrandPayload(slug))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", slug, rr.Code, rr.Body.String())
		}
	}

	want := brandSlugsFrom(t, router, adminToken)
	if len(want) < 2 {
		t.Fatalf("the admin sees %v, want at least the two just created", want)
	}

	// Every other role sees exactly the same list, with no grant anywhere.
	for _, role := range []auth.RoleName{
		auth.RoleDesigner, auth.RoleOperator, auth.RoleProjectLead,
		auth.RolePerformanceMarketer, auth.RolePackagingQc,
	} {
		token := tokenFor(t, minter, role, "usr_"+string(role))
		got := brandSlugsFrom(t, router, token)
		if len(got) != len(want) {
			t.Errorf("%s sees %v, want the same %v the admin sees", role, got, want)
			continue
		}
		for _, slug := range want {
			if !contains(got, slug) {
				t.Errorf("%s cannot see %s", role, slug)
			}
		}
	}

	// And can open one by slug, which is the half a list-only filter would miss.
	designer := tokenFor(t, minter, auth.RoleDesigner, "usr_designer_detail")
	if rr := doJSON(router, http.MethodGet, "/brands/beta-brand", designer, nil); rr.Code != http.StatusOK {
		t.Errorf("a designer opening a store by slug = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// brandSlugsFrom reads the slugs out of GET /brands for one token.
func brandSlugsFrom(t *testing.T, router http.Handler, token string) []string {
	t.Helper()
	rr := doJSON(router, http.MethodGet, "/brands", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /brands = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var rows []struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode brands: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Slug)
	}
	return out
}

// newBrandPayload is the minimum a brand needs to be created.
func newBrandPayload(slug string) map[string]any {
	return map[string]any{
		"slug": slug, "name": slug, "starting_price": 499,
		"ladder": []int{499, 999, 1499}, "is_active": true,
		"cp_green_max": 0.25, "cp_yellow_max": 0.3,
	}
}
