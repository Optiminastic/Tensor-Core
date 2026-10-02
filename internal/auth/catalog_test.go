package auth

import "testing"

func has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func TestAdminHasEveryPermission(t *testing.T) {
	admin := PermissionsFor(RoleAdmin)
	// 41: registry:read split reading the product registry away from reading
	// cost configuration, and bulk_order:read/manage are admin-only by being
	// granted to no other role. See catalog.go.
	if len(admin) != len(AllPermissions) || len(AllPermissions) != 41 {
		t.Fatalf("admin has %d permissions, catalog has %d, want 41 each", len(admin), len(AllPermissions))
	}
	for _, p := range AllPermissions {
		if !has(admin, p.Key()) {
			t.Errorf("admin missing %s", p.Key())
		}
	}
}

// The operator's scope: the whole Production area, nothing outside it, no money.
//
// The cost half of this is unchanged and must stay that way - it is the oldest
// rule in the matrix. What changed is the other half: the shop asked for an
// operator on EVERY Production page, which added order:read, batch:read and the
// new registry:read, and for an operator on NO other area, which removed
// design:read.
//
// registry:read exists precisely so those two halves can both hold. The
// Registry is a Production page and used to be gated on config:read, so before
// it the only way to show an operator that page was to hand them cost
// assumptions as well.
func TestOperatorSeesAllOfProductionAndNoCosts(t *testing.T) {
	op := PermissionsFor(RoleOperator)
	for _, forbidden := range []string{"config:read", "config:manage", "pricing:read"} {
		if has(op, forbidden) {
			t.Errorf("operator must not have %s", forbidden)
		}
	}
	// One per page in the Production area, named as nav-config.ts gates them.
	for _, expected := range []string{
		"production:read",   // Overview, Production Jobs, Packaging
		"order:read",        // Orders
		"batch:read",        // Batch Management
		"machine:read",      // Machine Management
		"filament:read",     // Inventory
		"registry:read",     // Registry
		"production:update", // and the work itself
		"production:fail",
		"machine:manage",
	} {
		if !has(op, expected) {
			t.Errorf("operator should have %s", expected)
		}
	}
	// Outside Production. design:read was held until the shop said the operator
	// sees Production only; it put the entire Designs area in their nav.
	for _, forbidden := range []string{"design:read", "design:create", "user:read", "brand:manage"} {
		if has(op, forbidden) {
			t.Errorf("operator must not have %s - it is outside the Production area", forbidden)
		}
	}
}

// The designer's scope: Designs and Costing, and nothing else.
//
// pricing:read is what puts Costing in the nav. It is a READ - a designer sees
// what a model costs and cannot generate, override or approve a price.
func TestDesignerSeesDesignsAndCosting(t *testing.T) {
	d := PermissionsFor(RoleDesigner)
	for _, expected := range []string{
		"design:read", "design:create", "design:update", "design:submit",
		"pricing:read",
	} {
		if !has(d, expected) {
			t.Errorf("designer should have %s", expected)
		}
	}
	for _, forbidden := range []string{
		// Not Production, not the registry, not the workspace.
		"production:read", "order:read", "batch:read", "machine:read", "registry:read",
		"user:read", "brand:manage",
		// Reads prices; does not set or approve them.
		"pricing:generate", "pricing:override", "design:approve", "config:read",
	} {
		if has(d, forbidden) {
			t.Errorf("designer must not have %s", forbidden)
		}
	}
}

func TestPackagingQcScope(t *testing.T) {
	qc := PermissionsFor(RolePackagingQc)
	// Records QC, assembly and packaging; reads the queue.
	for _, expected := range []string{"production:read", "qc:submit", "assembly:submit", "packaging:submit"} {
		if !has(qc, expected) {
			t.Errorf("packaging_qc should have %s", expected)
		}
	}
	// Cannot advance a job directly, cannot see costs, cannot manage machines.
	for _, forbidden := range []string{"production:update", "production:fail", "config:read", "pricing:read", "order:read", "machine:manage"} {
		if has(qc, forbidden) {
			t.Errorf("packaging_qc must not have %s", forbidden)
		}
	}
}

// A designer READS prices and cannot set them.
//
// pricing:read moved out of this list when the shop put Costing in the
// designer's nav - see TestDesignerSeesDesignsAndCosting. What stays forbidden
// is every verb that CHANGES a price or a design's fate.
func TestDesignerCannotApproveOrPrice(t *testing.T) {
	d := PermissionsFor(RoleDesigner)
	for _, forbidden := range []string{
		"design:approve", "design:reject",
		"pricing:generate", "pricing:override",
		"user:manage",
	} {
		if has(d, forbidden) {
			t.Errorf("designer must not have %s", forbidden)
		}
	}
}

func TestProjectLeadCannotManageUsers(t *testing.T) {
	if has(PermissionsFor(RoleProjectLead), "user:manage") {
		t.Error("project lead must not have user:manage")
	}
}

// brand:read is NOT in this list any more, and that is deliberate.
//
// Designs, Costing and Production all live under /dashboard/<brand>/..., so a
// role that cannot list brands cannot reach any page it is otherwise entitled
// to - which is what happened: the switcher called GET /brands, got a 403, and
// every non-admin saw an empty workspace. Asking which brands exist is now
// allowed to everyone, and so is seeing all of them - store-level scoping was
// removed (migration 0087).
//
// brand:MANAGE - create, edit, delete - stays admin-only, which is the thing
// this test was protecting.
func TestProjectAndBrandManageAreAdminOnly(t *testing.T) {
	adminOnly := []string{
		"project:read", "project:manage", "brand:manage",
		// Bulk orders carry negotiated prices and can put a hundred jobs on the
		// floor from one upload. The shop asked for admin only, and this is
		// where that is enforced rather than hoped for.
		"bulk_order:read", "bulk_order:manage",
	}
	for _, role := range AllRoles {
		if role == RoleAdmin {
			continue
		}
		set := PermissionsFor(role)
		for _, key := range adminOnly {
			if has(set, key) {
				t.Errorf("%s must not have %s (admin-only)", role, key)
			}
		}
	}
}

func TestPermissionsForRolesUnion(t *testing.T) {
	union := PermissionsForRoles([]RoleName{RoleDesigner, RolePerformanceMarketer})
	// designer's design:create plus performance-marketer's pricing:read.
	if !has(union, "design:create") || !has(union, "pricing:read") {
		t.Errorf("union missing expected keys: %v", SortedKeys(union))
	}
	if len(PermissionsForRoles(nil)) != 0 {
		t.Error("empty role list must yield an empty permission set")
	}
}

func TestTotalGrantsMatchSpec(t *testing.T) {
	// 41 (admin) + 6 (designer) + 28 (project lead) + 3 (marketer) + 12 (operator)
	// + 6 (packaging_qc) = 96.
	//
	// Moved from 84 when the shop restated three roles: +1 admin and +1 project
	// lead for registry:read, +1 designer for pricing:read (Costing), and the
	// operator went 9 -> 12 by losing design:read and gaining order:read,
	// batch:read, registry:read and brand:read.
	//
	// brand:read then went to every role (+5), because without it no non-admin
	// could list the brands their own pages live under - see
	// TestProjectAndBrandManageAreAdminOnly.
	total := 0
	for _, role := range AllRoles {
		total += len(GrantsFor(role))
	}
	if total != 96 {
		t.Fatalf("total grants = %d, want 96", total)
	}
}
