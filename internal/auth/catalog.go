// Package auth owns identity and RBAC: verifying the frontend-issued JWT against
// its JWKS, the permission catalog, resolving a user's roles and permissions
// from the database, invites, and the Gin guards. The catalog and models here
// are pure; the database-backed pieces live alongside them.
package auth

import "sort"

// RoleName is one of the five fixed roles. The value equals the name and is what
// the database `roles.name` column and the JWT `roles` claim carry.
type RoleName string

const (
	RoleAdmin               RoleName = "ADMIN"
	RoleDesigner            RoleName = "DESIGNER"
	RoleProjectLead         RoleName = "PROJECT_LEAD"
	RolePerformanceMarketer RoleName = "PERFORMANCE_MARKETER"
	RoleOperator            RoleName = "OPERATOR"
	// RolePackagingQc runs the QC and packaging stations. It maps the print-queue
	// packaging_qc role: it records QC/assembly/packaging but never advances a job
	// directly (no production:update) and never sees costs.
	RolePackagingQc RoleName = "PACKAGING_QC"
)

// AllRoles lists every role in a stable order.
var AllRoles = []RoleName{
	RoleAdmin, RoleDesigner, RoleProjectLead, RolePerformanceMarketer, RoleOperator, RolePackagingQc,
}

// Valid reports whether r is one of the known roles.
func (r RoleName) Valid() bool {
	for _, known := range AllRoles {
		if r == known {
			return true
		}
	}
	return false
}

// PermissionSpec is a single permission: a resource, an action, and a human
// description. Key is the wire form used everywhere else ("resource:action").
type PermissionSpec struct {
	Resource    string
	Action      string
	Description string
}

// Key returns the "resource:action" string.
func (p PermissionSpec) Key() string { return p.Resource + ":" + p.Action }

// The permission catalog. Order matters: it fixes the seed order and ADMIN's
// full set. A new permission added to AllPermissions automatically joins ADMIN.
var (
	DesignCreate  = PermissionSpec{"design", "create", "Upload a new design"}
	DesignRead    = PermissionSpec{"design", "read", "View a design and its pre-check report"}
	DesignUpdate  = PermissionSpec{"design", "update", "Edit a design that is not yet approved"}
	DesignDelete  = PermissionSpec{"design", "delete", "Delete a design"}
	DesignSubmit  = PermissionSpec{"design", "submit", "Submit a design for review"}
	DesignApprove = PermissionSpec{"design", "approve", "Approve a design and its selling price"}
	DesignReject  = PermissionSpec{"design", "reject", "Send a design back to the designer"}

	PricingRead     = PermissionSpec{"pricing", "read", "See Design CP, selling price and margins"}
	PricingGenerate = PermissionSpec{"pricing", "generate", "Generate a selling price from the ladder"}
	PricingOverride = PermissionSpec{"pricing", "override", "Override a generated selling price"}

	ConfigRead   = PermissionSpec{"config", "read", "View cost assumptions, materials and machines"}
	ConfigManage = PermissionSpec{"config", "manage", "Edit cost assumptions, materials and machines"}

	// RegistryRead splits reading the product registry away from reading cost
	// configuration, which used to be the same permission.
	//
	// It exists because the shop wants an Operator on every Production page,
	// and the Registry is one - it says which parts a SKU needs, which is
	// exactly what somebody assembling it has to know. config:read was the
	// wrong key for that: it also opens cost assumptions, and an Operator
	// never sees costs (TestOperatorNeverSeesCosts, which still stands).
	//
	// Reading the registry is therefore registry:read. WRITING it is still
	// config:manage - authoring the catalogue is a configuration act, and
	// nobody has asked for an editor who cannot see costs. The money on the
	// registry's own responses (parts_cost, line_cost) is withheld from a
	// caller without config:read rather than hidden by the page, so the
	// separation holds at the API and not merely in the UI.
	RegistryRead = PermissionSpec{"registry", "read", "View the product registry: SKUs, parts and templates"}

	ShopifyPublish = PermissionSpec{"shopify", "publish", "Publish an approved SKU to Shopify"}

	// The production pipeline (ported from print-queue-be). production:read is the
	// existing view permission; the rest gate the order -> job -> batch -> QC ->
	// packaging -> dispatch flow.
	ProductionRead   = PermissionSpec{"production", "read", "View production jobs and the print queue"}
	ProductionCreate = PermissionSpec{"production", "create", "Create production jobs from an order"}
	ProductionUpdate = PermissionSpec{"production", "update", "Advance a production job through its lifecycle"}
	ProductionFail   = PermissionSpec{"production", "fail", "Fail a production job and queue a reprint"}

	OrderRead = PermissionSpec{"order", "read", "View imported Shopify orders"}

	BatchRead   = PermissionSpec{"batch", "read", "View print batches"}
	BatchManage = PermissionSpec{"batch", "manage", "Create, plan and approve print batches"}

	DispatchRead   = PermissionSpec{"dispatch", "read", "View dispatch orders"}
	DispatchManage = PermissionSpec{"dispatch", "manage", "Create and mark dispatch orders"}

	FinishingSubmit = PermissionSpec{"finishing", "submit", "Record a finishing check"}
	QcSubmit        = PermissionSpec{"qc", "submit", "Record a quality-control check"}
	AssemblySubmit  = PermissionSpec{"assembly", "submit", "Record an assembly check"}
	PackagingSubmit = PermissionSpec{"packaging", "submit", "Record packaging details"}

	MachineRead   = PermissionSpec{"machine", "read", "View operational machine status"}
	MachineManage = PermissionSpec{"machine", "manage", "Create machines and set their status"}

	FilamentRead   = PermissionSpec{"filament", "read", "View filament inventory"}
	FilamentManage = PermissionSpec{"filament", "manage", "Adjust filament inventory levels"}

	IntegrationManage = PermissionSpec{"integration", "manage", "Connect and disconnect external stores (Shopify)"}

	UserRead   = PermissionSpec{"user", "read", "View users and their roles"}
	UserManage = PermissionSpec{"user", "manage", "Create users and assign roles"}

	ProjectRead   = PermissionSpec{"project", "read", "View projects"}
	ProjectManage = PermissionSpec{"project", "manage", "Create, edit and archive projects"}

	BrandRead   = PermissionSpec{"brand", "read", "View brands and their pricing policy"}
	BrandManage = PermissionSpec{"brand", "manage", "Edit brand identity and pricing ladders"}

	AuditRead = PermissionSpec{"audit", "read", "Read the audit trail"}
)

// AllPermissions is the full catalog in seed order (38 permissions).
var AllPermissions = []PermissionSpec{
	DesignCreate, DesignRead, DesignUpdate, DesignDelete, DesignSubmit, DesignApprove, DesignReject,
	PricingRead, PricingGenerate, PricingOverride,
	ConfigRead, ConfigManage,
	RegistryRead,
	ShopifyPublish,
	ProductionRead, ProductionCreate, ProductionUpdate, ProductionFail,
	OrderRead,
	BatchRead, BatchManage,
	DispatchRead, DispatchManage,
	QcSubmit, AssemblySubmit, FinishingSubmit, PackagingSubmit,
	MachineRead, MachineManage,
	FilamentRead, FilamentManage,
	IntegrationManage,
	UserRead, UserManage,
	ProjectRead, ProjectManage,
	BrandRead, BrandManage,
	AuditRead,
}

// roleGrants is the exact role -> permissions matrix. ADMIN is every permission
// by construction. The operational roles are deliberately narrow: OPERATOR never
// sees costs (no config:read / pricing:read); project:* and brand:* are
// ADMIN-only.
//
// The shop restated the shape of three of these, and the grants below are that
// statement rather than an inference from it:
//
//   - ADMIN sees everything, and may make other admins (the invite route
//     accepts ADMIN, so this is not only a permission but a capability).
//   - OPERATOR sees Production and EVERY page under it - Orders, Jobs, Batches,
//     Machines, Packaging, Inventory and the Registry - and nothing else. Not
//     Designs, which it used to see, and not Costing.
//   - DESIGNER sees Designs and Costing, and nothing else.
//
// "Sees a page" is a claim about the nav gates in the frontend's nav-config.ts,
// so each grant below is tied to the permission that file reads. Changing one
// without the other produces a role that can open a page it cannot load, or
// one hidden from data it is allowed to have.
//
// brand:read is held by EVERY role, and that is not a loosening. Designs,
// Costing and Production all live under /dashboard/<brand>/..., and reaching
// any of them means first listing the brands you may work in - so while
// brand:read was admin-only, no non-admin could enter the product at all: the
// switcher called GET /brands, got a 403, and showed nothing to pick.
//
// WHICH brands a member sees is a separate question from whether they may ask,
// and it is answered by user_brand_access (migration 0086) rather than by a
// permission: listBrands returns every brand to an admin and only the granted
// ones to everybody else. brand:MANAGE - creating, editing and deleting a brand
// - stays admin-only, which is what TestProjectAndBrandAreAdminOnly still
// pins.
var roleGrants = map[RoleName][]PermissionSpec{
	RoleAdmin: AllPermissions,
	// Designs AND Costing. PricingRead is what puts the Costing area in the nav
	// (nav-config.ts gates it on pricing:read); a designer who cannot see what
	// their model costs cannot act on the CP thresholds the costing page exists
	// to show. Still cannot approve, generate or override a price.
	RoleDesigner: {
		DesignCreate, DesignRead, DesignUpdate, DesignSubmit,
		PricingRead,
		BrandRead,
	},
	RoleProjectLead: {
		DesignRead, DesignApprove, DesignReject,
		PricingRead, PricingGenerate, PricingOverride,
		ShopifyPublish, ConfigRead, RegistryRead, AuditRead,
		// Runs the whole production pipeline.
		OrderRead,
		ProductionRead, ProductionCreate, ProductionUpdate, ProductionFail,
		BatchRead, BatchManage,
		DispatchRead, DispatchManage,
		QcSubmit, AssemblySubmit, FinishingSubmit, PackagingSubmit,
		MachineRead, MachineManage,
		FilamentRead, FilamentManage,
		BrandRead,
	},
	RolePerformanceMarketer: {DesignRead, PricingRead, BrandRead},
	// Machine operator: the whole Production area and nothing outside it.
	//
	// Every read below maps to a nav gate, so the area is complete rather than
	// mostly there: order:read (Orders), batch:read (Batch Management),
	// machine:read (Machine Management), filament:read (Inventory),
	// registry:read (Registry), production:read (Overview, Jobs, Packaging).
	//
	// DesignRead is gone. It put the whole Designs area in an operator's nav,
	// which is not their job and was never asked for - it predates anyone
	// stating what this role should see.
	//
	// Still no costs, and that is deliberate rather than an oversight to tidy
	// up later: no config:read, no pricing:read, and the Registry's own
	// parts_cost/line_cost are withheld from a caller lacking config:read.
	// Still no QC or packaging SUBMIT - reading the station page is not the
	// same as signing off work at it.
	//
	// batch:read without batch:manage: an operator watches the beds, and the
	// dispatcher sends them. Grant BatchManage here if they should press Queue.
	RoleOperator: {
		ProductionRead, ProductionUpdate, ProductionFail, AssemblySubmit, FinishingSubmit,
		OrderRead, BatchRead,
		MachineRead, MachineManage, FilamentRead, RegistryRead,
		BrandRead,
	},
	// QC/packaging station: records QC, assembly and packaging through their
	// dedicated endpoints. No production:update (cannot advance a job directly),
	// no cost or pricing visibility.
	RolePackagingQc: {
		ProductionRead, QcSubmit, AssemblySubmit, FinishingSubmit, PackagingSubmit,
		BrandRead,
	},
}

// RoleDescriptions is the one-line description of each role.
var RoleDescriptions = map[RoleName]string{
	RoleAdmin:               "Full access, including user management and cost configuration",
	RoleDesigner:            "Uploads and revises designs; cannot approve or price",
	RoleProjectLead:         "Approves designs, generates prices, publishes to Shopify",
	RolePerformanceMarketer: "Reads pricing and margins to plan ad spend",
	RoleOperator:            "Runs production jobs and machines; cannot see cost assumptions",
	RolePackagingQc:         "Runs the QC and packaging stations; cannot see cost assumptions",
}

// GrantsFor returns the permission specs granted to a role (nil for unknown).
func GrantsFor(role RoleName) []PermissionSpec { return roleGrants[role] }

// PermissionsFor returns the set of permission keys granted to a single role.
func PermissionsFor(role RoleName) map[string]struct{} {
	out := make(map[string]struct{})
	for _, p := range roleGrants[role] {
		out[p.Key()] = struct{}{}
	}
	return out
}

// PermissionsForRoles returns the union of permission keys across roles. An empty
// role list yields an empty set.
func PermissionsForRoles(roles []RoleName) map[string]struct{} {
	out := make(map[string]struct{})
	for _, r := range roles {
		for _, p := range roleGrants[r] {
			out[p.Key()] = struct{}{}
		}
	}
	return out
}

// SortedKeys returns the keys of a permission set sorted lexicographically, for
// stable token payloads and responses.
func SortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
