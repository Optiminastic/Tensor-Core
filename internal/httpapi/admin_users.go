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
// A third route, PUT /users/:id/brands, briefly existed here with a table
// behind it. It is gone: store-level access was removed at the shop's
// instruction - every role reaches every brand - so there was nothing for it to
// save (migration 0087).

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// memberResponse is one person on the team.
//
// user_id is a Better Auth id and this service holds no profile for it - no
// name, no email. The frontend joins those on from its own tables, which is why
// this is deliberately three fields and not a user object.
type memberResponse struct {
	UserID string `json:"user_id"`
	// Roles is never null, always a list: `null` makes every caller write the
	// same defensive check for a member whose roles have all been removed.
	Roles []string `json:"roles"`
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
		out = append(out, memberResponse{UserID: r.UserID, Roles: orEmpty(r.Roles)})
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

	if err := s.store.Q.DeleteUserRoles(ctx, userID); err != nil {
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

// orEmpty turns a nil slice into an empty one, so the JSON is [] and not null.
func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
