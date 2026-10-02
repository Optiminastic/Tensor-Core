package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// SeedResult reports how much the catalog projection touched.
type SeedResult struct {
	Permissions int
	Roles       int
	Grants      int
}

// SyncAll projects the permission catalog into the database, idempotently. It
// upserts every permission and role, then reconciles each role's grants:
// inserting the ones the catalog says it should have and DELETING any stale ones
// (revocation matters). Grants is the count of wanted grants (89 for the current
// catalog: 39 admin + 5 + 27 + 2 + 11 + 5).
//
// IT MUST BE RUN AFTER ANY CHANGE TO catalog.go. The catalog is the source of
// the intent and this table is the source of truth at runtime - ResolveUserAuthz
// reads role_permissions, never the Go slice - so a deploy that ships a changed
// matrix without running cmd/seed changes nothing at all. A new permission does
// not exist, a withdrawn one is still granted.
//
// It does NOT bump permissions versions, deliberately: that is per user and this
// is per role, and walking every user here would make a catalog sync a write
// against the whole table. Tokens carry the old set until they are reminted,
// which ResolveUserAuthz does on its own schedule (about every 15 minutes), so a
// grant change reaches people within that window rather than instantly.
func SyncAll(ctx context.Context, store *db.Store) (SeedResult, error) {
	var result SeedResult
	err := store.InTx(ctx, func(q *gen.Queries) error {
		permIDs := make(map[string]uuid.UUID, len(AllPermissions))
		for _, p := range AllPermissions {
			id, err := q.UpsertPermission(ctx, gen.UpsertPermissionParams{
				ID: uuid.New(), Resource: p.Resource, Action: p.Action, Description: p.Description,
			})
			if err != nil {
				return err
			}
			permIDs[p.Key()] = id
		}

		roleIDs := make(map[RoleName]uuid.UUID, len(AllRoles))
		for _, r := range AllRoles {
			id, err := q.UpsertRole(ctx, gen.UpsertRoleParams{
				ID: uuid.New(), Name: string(r), Description: RoleDescriptions[r],
			})
			if err != nil {
				return err
			}
			roleIDs[r] = id
		}

		grantCount := 0
		for _, r := range AllRoles {
			roleID := roleIDs[r]

			want := make(map[uuid.UUID]struct{})
			for _, p := range GrantsFor(r) {
				want[permIDs[p.Key()]] = struct{}{}
			}
			grantCount += len(want)

			current, err := q.ListRolePermissionIDs(ctx, roleID)
			if err != nil {
				return err
			}
			currentSet := make(map[uuid.UUID]struct{}, len(current))
			for _, pid := range current {
				currentSet[pid] = struct{}{}
			}

			for pid := range want {
				if _, ok := currentSet[pid]; !ok {
					if err := q.InsertRolePermission(ctx, gen.InsertRolePermissionParams{
						RoleID: roleID, PermissionID: pid,
					}); err != nil {
						return err
					}
				}
			}
			for pid := range currentSet {
				if _, ok := want[pid]; !ok {
					if err := q.DeleteRolePermission(ctx, gen.DeleteRolePermissionParams{
						RoleID: roleID, PermissionID: pid,
					}); err != nil {
						return err
					}
				}
			}
		}

		result = SeedResult{Permissions: len(AllPermissions), Roles: len(AllRoles), Grants: grantCount}
		return nil
	})
	return result, err
}
