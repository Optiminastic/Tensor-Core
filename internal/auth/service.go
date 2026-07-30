package auth

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// ResolveUserAuthz reads a user's roles and permissions from the database and
// their permissions version. It is the runtime source of truth the frontend
// calls when minting a token. An unknown user returns empty roles/permissions
// and version 0 -- a user with no role yet is a normal state, not an error.
//
// Roles are sorted by value and permissions lexicographically so the token
// payload is stable.
func ResolveUserAuthz(ctx context.Context, q *gen.Queries, userID string) (UserAuthzResponse, error) {
	grants, err := q.GetUserRoleGrants(ctx, userID)
	if err != nil {
		return UserAuthzResponse{}, err
	}

	roleSet := make(map[RoleName]struct{})
	permSet := make(map[string]struct{})
	for _, g := range grants {
		roleSet[RoleName(g.RoleName)] = struct{}{}
		if g.Resource != nil && g.Action != nil {
			permSet[*g.Resource+":"+*g.Action] = struct{}{}
		}
	}

	version, err := q.GetPermissionsVersion(ctx, userID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return UserAuthzResponse{}, err
		}
		version = 0
	}

	return UserAuthzResponse{
		Roles:              sortedRoleNames(roleSet),
		Permissions:        SortedKeys(permSet),
		PermissionsVersion: int(version),
	}, nil
}

// BumpPermissionsVersion invalidates a user's cached token authz. The first bump
// creates the row at version 1; later bumps increment.
func BumpPermissionsVersion(ctx context.Context, q *gen.Queries, userID string) (int, error) {
	v, err := q.BumpPermissionsVersion(ctx, userID)
	return int(v), err
}

// CurrentPermissionsVersion reads a user's current permissions version, mapping a
// missing row to 0 (a user who has never had a grant change). It backs the guard
// layer's freshness check.
func CurrentPermissionsVersion(ctx context.Context, q *gen.Queries, userID string) (int, error) {
	v, err := q.GetPermissionsVersion(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return int(v), nil
}

func sortedRoleNames(set map[RoleName]struct{}) []RoleName {
	out := make([]RoleName, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
