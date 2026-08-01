package auth

import "testing"

func has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func TestAdminHasEveryPermission(t *testing.T) {
	admin := PermissionsFor(RoleAdmin)
	if len(admin) != len(AllPermissions) || len(AllPermissions) != 37 {
		t.Fatalf("admin has %d permissions, catalog has %d, want 37 each", len(admin), len(AllPermissions))
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
	// 37 (admin) + 5 (designer, +machine:read for the machine picker) + 25 (project
	// lead) + 2 (marketer) + 8 (operator) + 4 (packaging_qc) = 81.
	total := 0
	for _, role := range AllRoles {
		total += len(GrantsFor(role))
	}
	if total != 81 {
		t.Fatalf("total grants = %d, want 81", total)
	}
}
