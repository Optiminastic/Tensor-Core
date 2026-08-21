package auth

import "testing"

func has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func TestAdminHasEveryPermission(t *testing.T) {
	admin := PermissionsFor(RoleAdmin)
	if len(admin) != len(AllPermissions) || len(AllPermissions) != 39 {
		t.Fatalf("admin has %d permissions, catalog has %d, want 39 each", len(admin), len(AllPermissions))
	}
	for _, p := range AllPermissions {
		if !has(admin, p.Key()) {
			t.Errorf("admin missing %s", p.Key())
		}
	}
}

func TestOperatorNeverSeesCosts(t *testing.T) {
	op := PermissionsFor(RoleOperator)
	for _, forbidden := range []string{"config:read", "config:manage", "pricing:read"} {
		if has(op, forbidden) {
			t.Errorf("operator must not have %s", forbidden)
		}
	}
	for _, expected := range []string{"design:read", "production:read", "production:update", "production:fail", "machine:manage"} {
		if !has(op, expected) {
			t.Errorf("operator should have %s", expected)
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

func TestDesignerCannotApproveOrPrice(t *testing.T) {
	d := PermissionsFor(RoleDesigner)
	for _, forbidden := range []string{"design:approve", "pricing:read", "user:manage"} {
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

func TestProjectAndBrandManageAreAdminOnly(t *testing.T) {
	// brand:read is granted to the working roles (scoped per user via user_brands);
	// only project:* and brand:manage stay admin-only.
	adminOnly := []string{"project:read", "project:manage", "brand:manage"}
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
	// The working roles can read brands (to see their assigned ones).
	for _, role := range []RoleName{
		RoleDesigner, RoleProjectLead, RolePerformanceMarketer, RoleOperator, RoleMarketingHead,
	} {
		if !has(PermissionsFor(role), "brand:read") {
			t.Errorf("%s should have brand:read", role)
		}
	}
}

func TestMarketingHeadWritesCopyOnly(t *testing.T) {
	mh := PermissionsFor(RoleMarketingHead)
	for _, expected := range []string{"brand:read", "design:read", "design:content"} {
		if !has(mh, expected) {
			t.Errorf("marketing head should have %s", expected)
		}
	}
	// Copy only: never approves, edits, prices, publishes or manages users.
	for _, forbidden := range []string{
		"design:approve", "design:update", "design:delete",
		"pricing:read", "pricing:generate", "shopify:publish", "config:read", "user:manage",
	} {
		if has(mh, forbidden) {
			t.Errorf("marketing head must not have %s", forbidden)
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
	// 39 (admin) + 6 (designer) + 29 (project lead) + 3 (marketer) + 14 (operator)
	// + 4 (packaging_qc) + 3 (marketing head) = 98. Project lead and operator each
	// gained print:dispatch (sending a sliced batch to a printer); admin gains
	// every permission by construction. Admin also gained design:content;
	// marketing head is brand:read + design:read + design:content.
	total := 0
	for _, role := range AllRoles {
		total += len(GrantsFor(role))
	}
	if total != 98 {
		t.Fatalf("total grants = %d, want 98", total)
	}
}
