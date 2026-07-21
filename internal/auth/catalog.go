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
)

// AllRoles lists every role in a stable order.
var AllRoles = []RoleName{
	RoleAdmin, RoleDesigner, RoleProjectLead, RolePerformanceMarketer, RoleOperator,
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

	ShopifyPublish = PermissionSpec{"shopify", "publish", "Publish an approved SKU to Shopify"}
	ProductionRead = PermissionSpec{"production", "read", "View production jobs and the print queue"}

	UserRead   = PermissionSpec{"user", "read", "View users and their roles"}
	UserManage = PermissionSpec{"user", "manage", "Create users and assign roles"}

	ProjectRead   = PermissionSpec{"project", "read", "View projects"}
	ProjectManage = PermissionSpec{"project", "manage", "Create, edit and archive projects"}

	BrandRead   = PermissionSpec{"brand", "read", "View brands and their pricing policy"}
	BrandManage = PermissionSpec{"brand", "manage", "Edit brand identity and pricing ladders"}

	AuditRead = PermissionSpec{"audit", "read", "Read the audit trail"}
)

// AllPermissions is the full catalog in seed order (21 permissions).
var AllPermissions = []PermissionSpec{
	DesignCreate, DesignRead, DesignUpdate, DesignDelete, DesignSubmit, DesignApprove, DesignReject,
	PricingRead, PricingGenerate, PricingOverride,
	ConfigRead, ConfigManage,
	ShopifyPublish, ProductionRead,
	UserRead, UserManage,
	ProjectRead, ProjectManage,
	BrandRead, BrandManage,
	AuditRead,
}

// roleGrants is the exact role -> permissions matrix. ADMIN is every permission
// by construction. The operational roles are deliberately narrow: OPERATOR never
// sees costs (no config:read / pricing:read); project:* and brand:* are
// ADMIN-only.
var roleGrants = map[RoleName][]PermissionSpec{
	RoleAdmin:    AllPermissions,
	RoleDesigner: {DesignCreate, DesignRead, DesignUpdate, DesignSubmit},
	RoleProjectLead: {
		DesignRead, DesignApprove, DesignReject,
		PricingRead, PricingGenerate, PricingOverride,
		ShopifyPublish, ConfigRead, AuditRead,
	},
	RolePerformanceMarketer: {DesignRead, PricingRead},
	RoleOperator:            {DesignRead, ProductionRead},
}

// RoleDescriptions is the one-line description of each role.
var RoleDescriptions = map[RoleName]string{
	RoleAdmin:               "Full access, including user management and cost configuration",
	RoleDesigner:            "Uploads and revises designs; cannot approve or price",
	RoleProjectLead:         "Approves designs, generates prices, publishes to Shopify",
	RolePerformanceMarketer: "Reads pricing and margins to plan ad spend",
	RoleOperator:            "Runs production jobs; cannot see cost assumptions",
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
