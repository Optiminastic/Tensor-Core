package httpapi

// The team roster: who is a member, what they may do, and which brands they
// may do it in.
//
// These three routes did not exist. The People page has called them since it
// was built - GET /admin/users for the roster, DELETE for removal, PUT
// .../brands for brand access - and every one answered 404. Because that page
// loads all of its data in one Promise.all, the single missing roster route
// blanked the whole screen: no members, no invites, and "No brands exist yet"
// on a workspace that had a brand. The visible symptom was the brand list; the
// cause was this file not existing.
//
// Brand access needed a table as well as a route (migration 0086,
// user_brand_access), because nothing anywhere had ever stored the answer.

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// memberResponse is one person on the team.
//
// user_id is a Better Auth id and this service holds no profile for it - no
// name, no email. The frontend joins those on from its own tables, which is why
// this is deliberately three fields and not a user object.
type memberResponse struct {
	UserID string `json:"user_id"`
	// Roles and BrandSlugs are never null, always a list. A member with no
	// brand grants is the ordinary state of somebody just invited, and `null`
	// there makes every caller write the same defensive check.
	Roles      []string `json:"roles"`
	BrandSlugs []string `json:"brand_slugs"`
}

// setMemberBrandsRequest replaces a member's brand access wholesale.
//
// A SET rather than add/remove calls: the UI is a multi-select, so "these are
// the brands" is what the admin actually decided. Two endpoints would make the
// screen send a diff it would have to compute, and a dropped request would
// leave access half-applied with nothing saying so.
type setMemberBrandsRequest struct {
	// No `required`: an empty list is a legitimate instruction - it means this
	// member sees no brand - and binding:"required" rejects an empty slice,
	// which would make revoking the last brand impossible.
	BrandSlugs []string `json:"brand_slugs"`
}

// registerAdminUsers adds the roster routes to the /admin group.
//
// Called from registerAdmin, so it inherits that group's guard: RequireUser
// plus user:manage. Managing the team is an admin act, and the guard sits on
// the group rather than per-route so a new route here cannot be added
// unguarded by accident.
func (s *Server) registerAdminUsers(g *gin.RouterGroup) {
	g.GET("/users", s.listMembers)
	g.DELETE("/users/:user_id", s.removeMember)
	g.PUT("/users/:user_id/brands", s.setMemberBrands)
}

// listMembers is the roster: everyone holding a role, with their brands.
func (s *Server) listMembers(c *gin.Context) {
	rows, err := s.store.Q.ListMembers(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list the team.")
		return
	}
	out := make([]memberResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, memberResponse{
			UserID:     r.UserID,
			Roles:      orEmpty(r.Roles),
			BrandSlugs: orEmpty(r.BrandSlugs),
		})
	}
	c.JSON(http.StatusOK, out)
}

// removeMember strips a person's roles and brand access.
//
// Their Better Auth account survives, deliberately: this service does not own
// it, and "no longer on this team" is not "delete this person". With no roles
// they hold no permissions, so every guard rejects them - which is the whole
// of what removal means here.
func (s *Server) removeMember(c *gin.Context) {
	userID := c.Param("user_id")
	if userID == "" {
		detail(c, http.StatusBadRequest, "A user id is required.")
		return
	}
	ctx := c.Request.Context()

	actor, _ := auth.UserFrom(c)
	if actor.ID == userID {
		// Removing yourself takes away user:manage, and if you were the last
		// admin nobody can grant it back - the workspace would need a database
		// edit to recover. The invite flow is how an admin hands over.
		detail(c, http.StatusConflict, "You cannot remove yourself from the team.")
		return
	}

	member, err := s.store.Q.UserHasAnyRole(ctx, userID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the team.")
		return
	}
	if !member {
		detail(c, http.StatusNotFound, "That person is not on the team.")
		return
	}

	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.DeleteUserRoles(ctx, userID); err != nil {
			return err
		}
		return q.DeleteUserBrandAccess(ctx, userID)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove that person.")
		return
	}

	// Their token still carries the old permissions until it is reminted, so
	// bump the version that invalidates it. Without this a removed member keeps
	// working for the lifetime of their access token.
	if _, err := s.store.Q.BumpPermissionsVersion(ctx, userID); err != nil {
		obs.FromContext(ctx).Warn(
			"could not bump the permissions version after removing a member",
			"user", userID, "error", err)
	}
	c.Status(http.StatusNoContent)
}

// setMemberBrands replaces which brands a member may work in.
func (s *Server) setMemberBrands(c *gin.Context) {
	userID := c.Param("user_id")
	if userID == "" {
		detail(c, http.StatusBadRequest, "A user id is required.")
		return
	}
	var req setMemberBrandsRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	member, err := s.store.Q.UserHasAnyRole(ctx, userID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the team.")
		return
	}
	if !member {
		detail(c, http.StatusNotFound, "That person is not on the team.")
		return
	}

	// De-duplicated before the write. The insert is ON CONFLICT DO NOTHING so a
	// repeat is harmless, but a caller sending the same slug twice should not
	// depend on that to behave.
	slugs := dedupeStrings(req.BrandSlugs)

	actor, _ := auth.UserFrom(c)
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		// Replace, not merge: the request is the whole set, so what is not in
		// it is revoked. Inside one transaction, or a failure halfway leaves a
		// member with no access at all rather than their previous access.
		if err := q.DeleteUserBrandAccess(ctx, userID); err != nil {
			return err
		}
		for _, slug := range slugs {
			if err := q.InsertUserBrandAccess(ctx, gen.InsertUserBrandAccessParams{
				UserID: userID, BrandSlug: slug, GrantedBy: &actor.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// A slug that is not a brand violates the foreign key, and that is a
		// bad request rather than a server fault - the caller named something
		// that does not exist.
		detail(c, http.StatusBadRequest,
			"Could not save that brand access. Check every brand still exists.")
		return
	}

	if _, err := s.store.Q.BumpPermissionsVersion(ctx, userID); err != nil {
		obs.FromContext(ctx).Warn(
			"could not bump the permissions version after changing brand access",
			"user", userID, "error", err)
	}
	c.Status(http.StatusNoContent)
}

// orEmpty turns a nil slice into an empty one, so the JSON is [] and not null.
func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// dedupeStrings keeps the first occurrence of each value, in order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
