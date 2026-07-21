package auth

import "testing"

func has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func TestAdminHasEveryPermission(t *testing.T) {
	admin := PermissionsFor(RoleAdmin)
	if len(admin) != len(AllPermissions) || len(AllPermissions) != 21 {
		t.Fatalf("admin has %d permissions, catalog has %d, want 21 each", len(admin), len(AllPermissions))
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
	if !has(op, "design:read") || !has(op, "production:read") {
		t.Error("operator should have design:read and production:read")
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

func TestProjectAndBrandAreAdminOnly(t *testing.T) {
	adminOnly := []string{"project:read", "project:manage", "brand:read", "brand:manage"}
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
	// 21 (admin) + 4 (designer) + 9 (project lead) + 2 (marketer) + 2 (operator) = 38.
	total := 0
	for _, role := range AllRoles {
		total += len(GrantsFor(role))
	}
	if total != 38 {
		t.Fatalf("total grants = %d, want 38", total)
	}
}
