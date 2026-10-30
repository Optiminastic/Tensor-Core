package auth

import "testing"

func has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func TestAdminHasEveryPermission(t *testing.T) {
	admin := PermissionsFor(RoleAdmin)
	// 43: registry:read split reading the product registry away from reading
	// cost configuration, bulk_order:read/manage are admin-only by being
	// granted to no other role, design:job_read/job_supply give a designer
	// the production jobs waiting for a 3MF without giving them production:*,
	// and whatsapp:read/reply added the inbox. See catalog.go.
	// 46 with damage:resolve, which is admin-only by being granted to no
	// other role - the same construction as the bulk-order pair.
	if len(admin) != len(AllPermissions) || len(AllPermissions) != 49 {
		t.Fatalf("admin has %d permissions, catalog has %d, want 49 each", len(admin), len(AllPermissions))
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

// The designer's scope: Designs, Costing and Production - the last READ-ONLY.
//
// pricing:read is what puts Costing in the nav. It is a READ - a designer sees
// what a model costs and cannot generate, override or approve a price.
//
// THE LINE MOVED, AND IT MOVED FROM AREAS TO VERBS. This list used to forbid
// production:read, order:read, batch:read, machine:read and registry:read,
// because the role was Designs and Costing and nothing else. The shop then
// asked for a designer to see the Registry and the whole Production area -
// the same way pricing:read moved out of the forbidden list when Costing was
// put in their nav. What a designer still cannot do is CHANGE any of it, and
// that is what the forbidden list now says.
func TestDesignerSeesDesignsCostingAndProduction(t *testing.T) {
	d := PermissionsFor(RoleDesigner)
	for _, expected := range []string{
		"design:read", "design:create", "design:update", "design:submit",
		"pricing:read",
		// Every one of these maps to a nav gate, so the areas are whole.
		"registry:read", "production:read", "order:read", "batch:read",
		"machine:read", "filament:read",
	} {
		if !has(d, expected) {
			t.Errorf("designer should have %s", expected)
		}
	}
	// Designs and Production are WORKED, not watched: a designer approves,
	// deletes, publishes, advances a job, locks a bed, manages machines and
	// the shelf, and signs off at every station.
	for _, expected := range []string{
		"design:delete", "design:approve", "design:reject", "shopify:publish",
		"production:create", "production:update", "production:fail",
		"batch:manage", "dispatch:manage", "machine:manage", "filament:manage",
		"qc:submit", "assembly:submit", "finishing:submit", "packaging:submit",
	} {
		if !has(d, expected) {
			t.Errorf("designer should have %s", expected)
		}
	}
	// WHAT IS LEFT OUT IS MONEY AND PEOPLE. The shop asked for Designs and
	// Production, and for Costing to be VISIBLE - so the verbs that decide
	// what a customer pays stay elsewhere, and so does everything about the
	// workspace itself.
	for _, forbidden := range []string{
		// Reads prices; does not set them, and cannot touch the cost
		// assumptions the whole engine runs on - config:manage is the same
		// permission that writes the registry, which is why registry writing
		// is not here either. Splitting the two is the open question.
		"pricing:generate", "pricing:override", "config:read", "config:manage",
		// Not the workspace, and never customer chats.
		"user:read", "user:manage", "brand:manage", "whatsapp:read",
		// Admin only, each for its own reason: a sweep that advances the whole
		// board, and a leaf that stays invisible inside a Production section
		// everybody else can now see.
		"production:catchup", "bulk_order:read",
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
// This list has shed two entries, each when the shop said so. pricing:read
// left it when Costing went into the designer's nav; design:approve and
// design:reject left it when the shop asked for a designer to run the Designs
// tab outright. What stays is money: the two verbs that decide what a
// customer actually pays, and the config the engine computes it from.
func TestDesignerCannotPrice(t *testing.T) {
	d := PermissionsFor(RoleDesigner)
	for _, forbidden := range []string{
		"pricing:generate", "pricing:override",
		"config:read", "config:manage",
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
		// Declaring a whole board's stations done without anybody inspecting
		// anything. A Packaging-QC holds qc:submit so they can record a check
		// they PERFORMED; this is the power to say a hundred jobs were
		// checked when nobody checked them, and it belongs with whoever
		// answers for the shop when a customer asks who inspected their
		// order.
		"production:catchup",
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
	// 43 (admin) + 6 (designer) + 30 (project lead) + 3 (marketer) + 14 (operator)
	// + 6 (packaging_qc) = 102.
	//
	// Moved from 84 when the shop restated three roles: +1 admin and +1 project
	// lead for registry:read, +1 designer for pricing:read (Costing), and the
	// operator went 9 -> 12 by losing design:read and gaining order:read,
	// batch:read, registry:read and brand:read.
	//
	// brand:read then went to every role (+5), because without it no non-admin
	// could list the brands their own pages live under - see
	// TestProjectAndBrandManageAreAdminOnly.
	//
	// whatsapp:read and whatsapp:reply then went to admin, project lead and
	// operator (+6): the people who already see orders answer the customers
	// asking about them.
	//
	// The designer then went 8 -> 30 (+22) when the shop asked for that role to
	// RUN Designs and Production rather than watch them. Sight first
	// (registry:read, production:read, order:read, batch:read, machine:read,
	// filament:read), then the verbs: design delete/approve/reject,
	// shopify:publish, production create/update/fail, batch:manage,
	// dispatch read/manage, the four station sign-offs, machine:manage and
	// filament:manage - see TestDesignerSeesDesignsCostingAndProduction.
	//
	// Costing stays a READ for them, and the registry is still written with
	// config:manage, which is admin-only because the same permission opens the
	// cost assumptions the pricing engine runs on.
	total := 0
	for _, role := range AllRoles {
		total += len(GrantsFor(role))
	}
	// 108: design:job_read and design:job_supply each go to DESIGNER,
	// PROJECT_LEAD and ADMIN; whatsapp:read and whatsapp:reply each go to
	// ADMIN, PROJECT_LEAD and OPERATOR.
	//
	// 112 with CUSTOMER_SUPPORT, which adds four and no new permission:
	// order:read, whatsapp:read, whatsapp:reply and brand:read. A role that
	// needed a permission nobody else had would be a wider change than a row
	// in this table, and this number is how that shows up.
	// 114: damage:resolve goes to ADMIN and to CUSTOMER_SUPPORT. A third
	// holder of it would show up here before it showed up on anybody's
	// screen, which is the point of counting rather than spot-checking.
	if total != 139 {
		t.Fatalf("total grants = %d, want 139", total)
	}
}

// A designer can be handed the jobs waiting for a 3MF and can work the queue
// those jobs end up in.
//
// This assertion has inverted, in two steps and both times because the shop
// said so: production:read left the forbidden list when a designer was given
// sight of the Production area, and production:update followed when they were
// given the running of it. Worth stating what that last one carries, since it
// is the widest verb here: patching any job, failing one, and validating
// somebody's personalisation - which means answering a customer's
// confirmation on their behalf.
func TestDesignerSuppliesDesignFilesAndWorksTheQueue(t *testing.T) {
	designer := PermissionsFor(RoleDesigner)

	for _, p := range []PermissionSpec{
		DesignJobRead, DesignJobSupply, ProductionRead, ProductionUpdate,
	} {
		if !has(designer, p.Key()) {
			t.Errorf("designer cannot %s, so the Non-DNP queue is unreachable for them", p.Key())
		}
	}
	// What a designer still cannot do to the queue: sweep the whole board in
	// one press. Working a job and restating every job at once are different
	// acts, and only the first was asked for.
	for _, p := range []PermissionSpec{ProductionCatchUp} {
		if has(designer, p.Key()) {
			t.Errorf("designer holds %s; that sweep is admin-only", p.Key())
		}
	}
}

func TestWhatsAppInboxGoesToTheRolesThatSeeOrders(t *testing.T) {
	for _, role := range []RoleName{
		RoleAdmin, RoleProjectLead, RoleOperator, RoleCustomerSupport,
	} {
		perms := PermissionsFor(role)
		if !has(perms, WhatsAppRead.Key()) || !has(perms, WhatsAppReply.Key()) {
			t.Errorf("%s cannot use the WhatsApp inbox", role)
		}
	}
	for _, role := range []RoleName{RoleDesigner, RolePerformanceMarketer, RolePackagingQc} {
		if has(PermissionsFor(role), WhatsAppRead.Key()) {
			t.Errorf("%s must not read customer chats", role)
		}
	}
}

// Customer support sees the Agent area and Shipping, and nothing else.
//
// Asserted BOTH WAYS, because the second half is the whole point of a narrow
// role: a grant that quietly widened this one would otherwise pass every test
// in this file. The permissions listed are the nav gates from the frontend's
// nav-config.ts, so "sees the page" and "may load its data" stay the same
// claim - the thing the comment on roleGrants warns about.
func TestCustomerSupportSeesAgentAndShippingOnly(t *testing.T) {
	perms := PermissionsFor(RoleCustomerSupport)

	// Both areas are gated on order:read, and so is every endpoint behind
	// them - abandoned checkouts, call logs, message logs, shipments, and the
	// order a damage report opens in its drawer.
	// damage:resolve is here at the shop's instruction: the person reading a
	// damage report is the person who should be able to decide its outcome,
	// and a queue where every row waits for an admin is a queue that waits.
	for _, need := range []PermissionSpec{
		OrderRead, WhatsAppRead, WhatsAppReply, DamageResolve, BrandRead,
	} {
		if !has(perms, need.Key()) {
			t.Errorf("customer support cannot use %s, so part of the Agent or "+
				"Shipping area is closed to it", need.Key())
		}
	}

	// Nothing production-shaped. production:read alone would put the whole
	// Production area in the nav - Overview, Jobs, Packaging - which is not
	// this role's job and was never asked for.
	for _, denied := range []PermissionSpec{
		ProductionRead, ProductionUpdate, ProductionFail,
		BatchRead, MachineRead, FilamentRead, RegistryRead,
		QcSubmit, PackagingSubmit, AssemblySubmit,
		DesignRead, PricingRead, ConfigRead, AuditRead,
		UserRead, BrandManage, DispatchRead, BulkOrderRead,
	} {
		if has(perms, denied.Key()) {
			t.Errorf("customer support holds %s; this role is the Agent area and "+
				"Shipping, and nothing else", denied.Key())
		}
	}

	// Four and only four. A count rather than a spot check: the loop above can
	// only name permissions somebody thought to list, and this catches the one
	// nobody did.
	if n := len(GrantsFor(RoleCustomerSupport)); n != 5 {
		t.Errorf("customer support holds %d permissions, want 5", n)
	}
}
