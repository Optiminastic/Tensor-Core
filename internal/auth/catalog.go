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
	// RoleMarketingHead writes the product marketing description. It cannot approve,
	// price or publish - copy only, kept separate from the Project Lead.
	RoleMarketingHead RoleName = "MARKETING_HEAD"
)

// AllRoles lists every role in a stable order.
var AllRoles = []RoleName{
	RoleAdmin, RoleDesigner, RoleProjectLead, RolePerformanceMarketer, RoleOperator, RolePackagingQc,
	RoleMarketingHead,
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
	DesignContent = PermissionSpec{"design", "content", "Write the product marketing description"}

	PricingRead     = PermissionSpec{"pricing", "read", "See Design CP, selling price and margins"}
	PricingGenerate = PermissionSpec{"pricing", "generate", "Generate a selling price from the ladder"}
	PricingOverride = PermissionSpec{"pricing", "override", "Override a generated selling price"}

	ConfigRead   = PermissionSpec{"config", "read", "View cost assumptions, materials and machines"}
	ConfigManage = PermissionSpec{"config", "manage", "Edit cost assumptions, materials and machines"}

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

	// PrintDispatch sends a sliced plate to a printer. Deliberately separate from
	// batch:manage: approving a batch is a planning act, while dispatching commits
	// a physical machine and real filament.
	PrintDispatch = PermissionSpec{"print", "dispatch", "Send a sliced batch to a printer"}

	DispatchRead   = PermissionSpec{"dispatch", "read", "View dispatch orders"}
	DispatchManage = PermissionSpec{"dispatch", "manage", "Create and mark dispatch orders"}

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

// AllPermissions is the full catalog in seed order (39 permissions).
var AllPermissions = []PermissionSpec{
	DesignCreate, DesignRead, DesignUpdate, DesignDelete, DesignSubmit, DesignApprove, DesignReject,
	DesignContent,
	PricingRead, PricingGenerate, PricingOverride,
	ConfigRead, ConfigManage,
	ShopifyPublish,
	ProductionRead, ProductionCreate, ProductionUpdate, ProductionFail,
	OrderRead,
	BatchRead, BatchManage,
	PrintDispatch,
	DispatchRead, DispatchManage,
	QcSubmit, AssemblySubmit, PackagingSubmit,
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
// sees costs (no config:read / pricing:read); project:* and brand:manage are
// ADMIN-only. brand:read is granted to the working roles so they can see the
// brands an admin assigned them (scoped per user via user_brands at the handler).
var roleGrants = map[RoleName][]PermissionSpec{
	RoleAdmin:    AllPermissions,
	RoleDesigner: {BrandRead, DesignCreate, DesignRead, DesignUpdate, DesignSubmit, MachineRead},
	RoleProjectLead: {
		BrandRead,
		DesignRead, DesignApprove, DesignReject, DesignDelete,
		PricingRead, PricingGenerate, PricingOverride,
		ShopifyPublish, ConfigRead, AuditRead,
		// Can view the team roster and remove junior members (not admins or other
		// leads); user:manage (invite / assign brands) stays ADMIN-only.
		UserRead,
		// Runs the whole production pipeline.
		OrderRead,
		ProductionRead, ProductionCreate, ProductionUpdate, ProductionFail,
		BatchRead, BatchManage, PrintDispatch,
		DispatchRead, DispatchManage,
		QcSubmit, AssemblySubmit, PackagingSubmit,
		MachineRead, MachineManage,
		FilamentRead, FilamentManage,
	},
	RolePerformanceMarketer: {BrandRead, DesignRead, PricingRead},
	// Marketing head: writes the product marketing copy on a design and reads
	// designs to do it. Never approves, prices or publishes - separation from the
	// Project Lead, who approves and pushes the copy to Shopify.
	RoleMarketingHead: {BrandRead, DesignRead, DesignContent},
	// Machine operator: runs the whole Production tab - jobs, orders, batches,
	// machines and filament inventory. Records assembly, fails/advances a job,
	// manages machine status, and reads orders + manages batches/inventory. Still
	// never sees costs or pricing, and never does QC/packaging.
	RoleOperator: {
		BrandRead,
		DesignRead,
		OrderRead,
		ProductionRead, ProductionUpdate, ProductionFail, AssemblySubmit,
		BatchRead, BatchManage, PrintDispatch,
		MachineRead, MachineManage,
		FilamentRead, FilamentManage,
	},
	// QC/packaging station: records QC, assembly and packaging through their
	// dedicated endpoints. No production:update (cannot advance a job directly),
	// no cost or pricing visibility.
	RolePackagingQc: {
		ProductionRead, QcSubmit, AssemblySubmit, PackagingSubmit,
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
	RoleMarketingHead:       "Writes product marketing copy; cannot approve, price or publish",
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
