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
	"github.com/Optiminastic/tensor-core/internal/db/gen"
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

// A member sees the brands an admin granted them, and no others.
//
// The permission says they may ask; user_brand_access says which answers come
// back. Both halves are needed: without the permission every non-admin got a
// 403 and an empty switcher, and without the scoping they would all see every
// brand the workspace has.
func TestIntegrationBrandListIsScopedToGrantedBrands(t *testing.T) {
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

	// The admin sees both, with no grants of their own - an admin is not in
	// user_brand_access at all. Asserted as "contains", not as a count: the
	// seed ships a fixture brand of its own and this test is about scoping, not
	// about what else is in the workspace.
	adminSees := brandSlugsFrom(t, router, adminToken)
	for _, want := range []string{"alpha-brand", "beta-brand"} {
		if !contains(adminSees, want) {
			t.Errorf("admin does not see %s (sees %v)", want, adminSees)
		}
	}

	// A designer with no grants sees none. Deliberate: access is given, not
	// assumed, so a brand-new member starts with nothing rather than the lot.
	designerID := "usr_" + string(auth.RoleDesigner)
	designerToken := tokenFor(t, minter, auth.RoleDesigner, designerID)
	if got := brandSlugsFrom(t, router, designerToken); len(got) != 0 {
		t.Errorf("an ungranted designer sees %v, want nothing", got)
	}

	// Make them a member, then grant one brand through the route the People
	// page calls - which did not exist at all until now.
	roleID, err := store.Q.GetRoleIDByName(t.Context(), string(auth.RoleDesigner))
	if err != nil {
		t.Fatalf("look up the designer role: %v", err)
	}
	if err := store.Q.InsertUserRole(t.Context(), gen.InsertUserRoleParams{
		UserID: designerID, RoleID: roleID,
	}); err != nil {
		t.Fatalf("make the designer a member: %v", err)
	}
	rr := doJSON(router, http.MethodPut, "/admin/users/"+designerID+"/brands", adminToken,
		map[string][]string{"brand_slugs": {"alpha-brand"}})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("PUT brands = %d, want 204: %s", rr.Code, rr.Body.String())
	}

	got := brandSlugsFrom(t, router, designerToken)
	if len(got) != 1 || got[0] != "alpha-brand" {
		t.Errorf("after the grant the designer sees %v, want [alpha-brand]", got)
	}

	// And cannot reach the other one by typing its slug, which the switcher
	// would otherwise merely hide.
	if rr := doJSON(router, http.MethodGet, "/brands/beta-brand", designerToken, nil); rr.Code != http.StatusNotFound {
		t.Errorf("GET an ungranted brand = %d, want 404 - a 403 would confirm it exists", rr.Code)
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
