"""Sync the permission catalog into the database.

The catalog in `catalog.py` is the source of truth; these tables are its
projection. This is idempotent and safe to re-run on every deploy: it adds what
is new, updates descriptions, and removes grants the catalog no longer makes.

Run with:  uv run python -m app.auth.seed
"""

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.auth.catalog import ALL_PERMISSIONS, ROLE_DESCRIPTIONS, ROLE_PERMISSIONS
from app.db.models.authz import Permission, Role, RolePermission


def sync_permissions(db: Session) -> int:
    """Insert or update every permission in the catalog."""
    existing = {(p.resource, p.action): p for p in db.scalars(select(Permission))}

    for spec in ALL_PERMISSIONS:
        found = existing.get((spec.resource, spec.action))
        if found is None:
            db.add(
                Permission(resource=spec.resource, action=spec.action, description=spec.description)
            )
        else:
            found.description = spec.description

    db.flush()
    return len(ALL_PERMISSIONS)


def sync_roles(db: Session) -> int:
    """Insert or update every role in the catalog."""
    existing = {r.name: r for r in db.scalars(select(Role))}

    for name, description in ROLE_DESCRIPTIONS.items():
        found = existing.get(name.value)
        if found is None:
            db.add(Role(name=name.value, description=description))
        else:
            found.description = description

    db.flush()
    return len(ROLE_DESCRIPTIONS)


def sync_role_permissions(db: Session) -> int:
    """Make each role's grants match the catalog exactly.

    Revocations matter as much as grants: if a permission is taken away from a
    role in the catalog, leaving the row behind would silently keep granting it.
    """
    permissions = {(p.resource, p.action): p for p in db.scalars(select(Permission))}
    roles = {r.name: r for r in db.scalars(select(Role))}
    current = {(rp.role_id, rp.permission_id): rp for rp in db.scalars(select(RolePermission))}

    wanted: set[tuple] = set()
    for role_name, specs in ROLE_PERMISSIONS.items():
        role = roles[role_name.value]
        for spec in specs:
            permission = permissions[(spec.resource, spec.action)]
            wanted.add((role.id, permission.id))

    for key in wanted - current.keys():
        db.add(RolePermission(role_id=key[0], permission_id=key[1]))

    for key in current.keys() - wanted:
        db.delete(current[key])

    db.flush()
    return len(wanted)


def sync_all(db: Session) -> dict[str, int]:
    """Sync the whole catalog. Order matters: grants need both sides to exist."""
    permissions = sync_permissions(db)
    roles = sync_roles(db)
    grants = sync_role_permissions(db)
    return {"permissions": permissions, "roles": roles, "grants": grants}


def main() -> None:
    # Imported here so `python -m app.auth.seed` is the only path that needs a
    # live DATABASE_URL — importing this module for its sync_* helpers does not.
    from app.db.session import _ensure_initialised

    with _ensure_initialised()() as db:
        result = sync_all(db)
        db.commit()
    print(
        f"Synced {result['permissions']} permissions, "
        f"{result['roles']} roles, {result['grants']} grants."
    )


if __name__ == "__main__":
    main()
