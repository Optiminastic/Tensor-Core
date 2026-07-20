"""The permission catalog and the role -> permission grants.

This is the single source of truth for what Tensor's roles may do. The frontend
does not duplicate it: Better Auth asks this service for a user's resolved
permissions when it mints a token, and every guard names a permission string.

Roles are bundles. Endpoints check permissions, never roles, so granting a new
role access to an endpoint is a data change here rather than a code change at
the call site.
"""

from typing import Final, NamedTuple

from app.db.models.authz import RoleName


class PermissionSpec(NamedTuple):
    resource: str
    action: str
    description: str

    @property
    def key(self) -> str:
        return f"{self.resource}:{self.action}"


def _p(resource: str, action: str, description: str) -> PermissionSpec:
    return PermissionSpec(resource, action, description)


# --- The catalog -------------------------------------------------------------
# Every permission Tensor recognises. Seeded into `permissions` by the seed
# command; guards reference these keys.

DESIGN_CREATE: Final = _p("design", "create", "Upload a new design")
DESIGN_READ: Final = _p("design", "read", "View a design and its pre-check report")
DESIGN_UPDATE: Final = _p("design", "update", "Edit a design that is not yet approved")
DESIGN_DELETE: Final = _p("design", "delete", "Delete a design")
DESIGN_SUBMIT: Final = _p("design", "submit", "Submit a design for review")
DESIGN_APPROVE: Final = _p("design", "approve", "Approve a design and its selling price")
DESIGN_REJECT: Final = _p("design", "reject", "Send a design back to the designer")

PRICING_READ: Final = _p("pricing", "read", "See Design CP, selling price and margins")
PRICING_GENERATE: Final = _p("pricing", "generate", "Generate a selling price from the ladder")
PRICING_OVERRIDE: Final = _p("pricing", "override", "Override a generated selling price")

CONFIG_READ: Final = _p("config", "read", "View cost assumptions, materials and machines")
CONFIG_MANAGE: Final = _p("config", "manage", "Edit cost assumptions, materials and machines")

SHOPIFY_PUBLISH: Final = _p("shopify", "publish", "Publish an approved SKU to Shopify")

PRODUCTION_READ: Final = _p("production", "read", "View production jobs and the print queue")

USER_READ: Final = _p("user", "read", "View users and their roles")
USER_MANAGE: Final = _p("user", "manage", "Create users and assign roles")

PROJECT_READ: Final = _p("project", "read", "View projects")
PROJECT_MANAGE: Final = _p("project", "manage", "Create, edit and archive projects")

BRAND_READ: Final = _p("brand", "read", "View brands and their pricing policy")
BRAND_MANAGE: Final = _p("brand", "manage", "Edit brand identity and pricing ladders")

AUDIT_READ: Final = _p("audit", "read", "Read the audit trail")

ALL_PERMISSIONS: Final[tuple[PermissionSpec, ...]] = (
    DESIGN_CREATE,
    DESIGN_READ,
    DESIGN_UPDATE,
    DESIGN_DELETE,
    DESIGN_SUBMIT,
    DESIGN_APPROVE,
    DESIGN_REJECT,
    PRICING_READ,
    PRICING_GENERATE,
    PRICING_OVERRIDE,
    CONFIG_READ,
    CONFIG_MANAGE,
    SHOPIFY_PUBLISH,
    PRODUCTION_READ,
    USER_READ,
    USER_MANAGE,
    PROJECT_READ,
    PROJECT_MANAGE,
    BRAND_READ,
    BRAND_MANAGE,
    AUDIT_READ,
)


# --- Role grants -------------------------------------------------------------
# Derived from the product's separation of duties:
#   Designer   uploads, cannot approve or price.
#   Lead       approves, rejects, prices; cannot manage users.
#   Operator   runs the floor; must not see cost assumptions.
#   Marketer   reads pricing to plan spend; changes nothing.

ROLE_DESCRIPTIONS: Final[dict[RoleName, str]] = {
    RoleName.ADMIN: "Full access, including user management and cost configuration",
    RoleName.DESIGNER: "Uploads and revises designs; cannot approve or price",
    RoleName.PROJECT_LEAD: "Approves designs, generates prices, publishes to Shopify",
    RoleName.PERFORMANCE_MARKETER: "Reads pricing and margins to plan ad spend",
    RoleName.OPERATOR: "Runs production jobs; cannot see cost assumptions",
}

ROLE_PERMISSIONS: Final[dict[RoleName, tuple[PermissionSpec, ...]]] = {
    # Admin holds every permission by construction, so a new permission is never
    # accidentally withheld from the role that is meant to have all of them.
    RoleName.ADMIN: ALL_PERMISSIONS,
    RoleName.DESIGNER: (
        DESIGN_CREATE,
        DESIGN_READ,
        DESIGN_UPDATE,
        DESIGN_SUBMIT,
    ),
    RoleName.PROJECT_LEAD: (
        DESIGN_READ,
        DESIGN_APPROVE,
        DESIGN_REJECT,
        PRICING_READ,
        PRICING_GENERATE,
        PRICING_OVERRIDE,
        SHOPIFY_PUBLISH,
        CONFIG_READ,
        AUDIT_READ,
    ),
    RoleName.PERFORMANCE_MARKETER: (
        DESIGN_READ,
        PRICING_READ,
    ),
    # Deliberately no CONFIG_READ or PRICING_READ: the floor must not see costs.
    RoleName.OPERATOR: (
        DESIGN_READ,
        PRODUCTION_READ,
    ),
}


def permissions_for(role: RoleName) -> frozenset[str]:
    """The permission keys a single role grants."""
    return frozenset(p.key for p in ROLE_PERMISSIONS[role])


def permissions_for_roles(roles: list[RoleName]) -> frozenset[str]:
    """The union of permissions across roles. A user may hold several."""
    return frozenset().union(*(permissions_for(r) for r in roles)) if roles else frozenset()
