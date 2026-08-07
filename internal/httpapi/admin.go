package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// /admin: inviting and assigning brands stay ADMIN-only (user:manage). Viewing the
// roster and removing members open to user:read (ADMIN + PROJECT_LEAD); the remove
// handler then enforces the hierarchy - a non-admin can only remove junior members.
func (s *Server) registerAdmin(r *gin.Engine) {
	g := r.Group("/admin")
	g.Use(s.guards.RequireUser())
	g.POST("/invites", s.guards.RequirePermission(auth.UserManage.Key()), s.createInvite)
	g.GET("/invites", s.guards.RequirePermission(auth.UserManage.Key()), s.listInvites)
	g.POST("/invites/:invite_id/revoke", s.guards.RequirePermission(auth.UserManage.Key()), s.revokeInvite)
	g.GET("/users", s.guards.RequirePermission(auth.UserRead.Key()), s.listMembers)
	g.PUT("/users/:user_id/brands", s.guards.RequirePermission(auth.UserManage.Key()), s.setMemberBrands)
	g.DELETE("/users/:user_id", s.guards.RequirePermission(auth.UserRead.Key()), s.removeMember)
}

type inviteCreateRequest struct {
	Email      string   `json:"email" binding:"required,email"`
	Role       string   `json:"role" binding:"required,oneof=ADMIN DESIGNER PROJECT_LEAD PERFORMANCE_MARKETER OPERATOR PACKAGING_QC"`
	BrandSlugs []string `json:"brand_slugs" binding:"omitempty,dive,required"`
}

type inviteCreatedResponse struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	BrandSlugs []string  `json:"brand_slugs"`
	ExpiresAt  time.Time `json:"expires_at"`
	Token      string    `json:"token"`
}

func (s *Server) createInvite(c *gin.Context) {
	var req inviteCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	user, _ := auth.UserFrom(c)

	brands, ok := s.validateBrandSlugs(c, req.BrandSlugs)
	if !ok {
		return
	}

	var invite gen.UserInvite
	var token string
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		inv, tok, err := auth.IssueInvite(c.Request.Context(), q, auth.InviteRequest{
			Email: req.Email, Role: auth.RoleName(req.Role), BrandSlugs: brands,
			CreatedBy: user.ID, TTL: auth.DefaultInviteTTL,
		})
		if err != nil {
			return err
		}
		invite, token = inv, tok
		return nil
	})
	if err != nil {
		var ie auth.InviteError
		if errors.As(err, &ie) {
			detail(c, http.StatusBadRequest, ie.Msg)
			return
		}
		detail(c, http.StatusInternalServerError, "Could not create the invite.")
		return
	}
	c.JSON(http.StatusCreated, inviteCreatedResponse{
		ID: invite.ID.String(), Email: invite.Email, Role: req.Role, BrandSlugs: brands,
		ExpiresAt: db.Time(invite.ExpiresAt), Token: token,
	})
}

// validateBrandSlugs de-duplicates the requested slugs and verifies each brand
// exists, returning a clean list (never nil). It writes a 4xx and returns false on
// an unknown or malformed slug.
func (s *Server) validateBrandSlugs(c *gin.Context, slugs []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(slugs))
	out := make([]string, 0, len(slugs))
	for _, raw := range slugs {
		slug := strings.ToLower(strings.TrimSpace(raw))
		if !slugPattern.MatchString(slug) {
			detail(c, http.StatusUnprocessableEntity, "A brand slug is not valid.")
			return nil, false
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		exists, err := s.store.Q.BrandExists(c.Request.Context(), slug)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not verify a brand.")
			return nil, false
		}
		if !exists {
			detail(c, http.StatusNotFound, fmt.Sprintf("Brand '%s' does not exist.", slug))
			return nil, false
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	return out, true
}

type inviteReadResponse struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedBy  *string    `json:"created_by"`
}

func (s *Server) listInvites(c *gin.Context) {
	ctx := c.Request.Context()
	page, ok := parsePageParams(c)
	if !ok {
		return
	}

	// Default (no ?limit): the full list, unchanged.
	if !page.paginate {
		rows, err := s.store.Q.ListInvitesWithRole(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list invites.")
			return
		}
		out := make([]inviteReadResponse, 0, len(rows))
		for _, r := range rows {
			out = append(out, inviteReadResponse{
				ID: r.ID.String(), Email: r.Email, Role: r.RoleName, ExpiresAt: db.Time(r.ExpiresAt),
				AcceptedAt: db.TimePtr(r.AcceptedAt), RevokedAt: db.TimePtr(r.RevokedAt), CreatedBy: r.CreatedBy,
			})
		}
		c.JSON(http.StatusOK, out)
		return
	}

	// Paginated: one keyset page, same body shape, next cursor in the header.
	rows, err := s.store.Q.ListInvitesWithRolePage(ctx, gen.ListInvitesWithRolePageParams{
		CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list invites.")
		return
	}
	out := make([]inviteReadResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, inviteReadResponse{
			ID: r.ID.String(), Email: r.Email, Role: r.RoleName, ExpiresAt: db.Time(r.ExpiresAt),
			AcceptedAt: db.TimePtr(r.AcceptedAt), RevokedAt: db.TimePtr(r.RevokedAt), CreatedBy: r.CreatedBy,
		})
	}
	if n := len(rows); n > 0 {
		last := rows[n-1]
		setNextCursor(c, n, page.limit, last.CreatedAt.Time, last.ID)
	}
	c.JSON(http.StatusOK, out)
}

type memberResponse struct {
	UserID     string   `json:"user_id"`
	Roles      []string `json:"roles"`
	BrandSlugs []string `json:"brand_slugs"`
}

// listMembers returns every user that has a role, with their roles and assigned
// brands. Emails are resolved on the frontend (Better Auth owns the user table).
func (s *Server) listMembers(c *gin.Context) {
	ctx := c.Request.Context()
	members, err := s.store.Q.ListMembers(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list members.")
		return
	}
	assignments, err := s.store.Q.ListAllUserBrands(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list brand assignments.")
		return
	}
	brandsByUser := make(map[string][]string, len(members))
	for _, a := range assignments {
		brandsByUser[a.UserID] = append(brandsByUser[a.UserID], a.BrandSlug)
	}
	out := make([]memberResponse, 0, len(members))
	for _, m := range members {
		brands := brandsByUser[m.UserID]
		if brands == nil {
			brands = []string{}
		}
		out = append(out, memberResponse{UserID: m.UserID, Roles: m.Roles, BrandSlugs: brands})
	}
	c.JSON(http.StatusOK, out)
}

type setBrandsRequest struct {
	BrandSlugs []string `json:"brand_slugs" binding:"omitempty,dive,required"`
}

// setMemberBrands replaces a member's brand access with the given set. Access is
// read per request from user_brands, so no permissions-version bump is needed -
// the change takes effect on the member's next request.
func (s *Server) setMemberBrands(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("user_id"))
	if userID == "" || len(userID) > 64 {
		detail(c, http.StatusUnprocessableEntity, "A valid user id is required.")
		return
	}
	var req setBrandsRequest
	if !bindJSON(c, &req) {
		return
	}
	brands, ok := s.validateBrandSlugs(c, req.BrandSlugs)
	if !ok {
		return
	}
	actor, _ := auth.UserFrom(c)

	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		if err := q.DeleteUserBrands(c.Request.Context(), userID); err != nil {
			return err
		}
		for _, slug := range brands {
			if err := q.InsertUserBrand(c.Request.Context(), gen.InsertUserBrandParams{
				UserID: userID, BrandSlug: slug, AssignedBy: actor.ID,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update brand access.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user_id": userID, "brand_slugs": brands})
}

// removeMember de-provisions a team member: it deletes all their roles and brand
// assignments and bumps their permissions version, so their next token carries no
// access (fail closed). Their Better Auth account is untouched. Guarded by
// user:manage. Refuses to remove the caller themselves or the last admin.
func (s *Server) removeMember(c *gin.Context) {
	userID := strings.TrimSpace(c.Param("user_id"))
	if userID == "" || len(userID) > 64 {
		detail(c, http.StatusUnprocessableEntity, "A valid user id is required.")
		return
	}
	actor, _ := auth.UserFrom(c)
	if userID == actor.ID {
		detail(c, http.StatusBadRequest, "You cannot remove yourself.")
		return
	}
	ctx := c.Request.Context()

	grants, err := s.store.Q.GetUserRoleGrants(ctx, userID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the member.")
		return
	}
	if len(grants) == 0 {
		detail(c, http.StatusNotFound, "That member does not exist.")
		return
	}
	targetIsAdmin, targetIsLead := false, false
	for _, g := range grants {
		switch g.RoleName {
		case string(auth.RoleAdmin):
			targetIsAdmin = true
		case string(auth.RoleProjectLead):
			targetIsLead = true
		}
	}

	// Hierarchy: only an admin (user:manage) may remove an admin or a project lead.
	// A non-admin (a project lead with user:read) can remove junior members only.
	actorIsAdmin := actor.Has(auth.UserManage.Key())
	if !actorIsAdmin && (targetIsAdmin || targetIsLead) {
		detail(c, http.StatusForbidden, "Only an admin can remove an admin or a project lead.")
		return
	}
	if targetIsAdmin {
		others, err := s.store.Q.CountAdminsExcluding(ctx, userID)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not verify admins.")
			return
		}
		if others == 0 {
			detail(c, http.StatusConflict, "You cannot remove the last admin.")
			return
		}
	}

	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.DeleteUserRoles(ctx, userID); err != nil {
			return err
		}
		if err := q.DeleteUserBrands(ctx, userID); err != nil {
			return err
		}
		_, err := q.BumpPermissionsVersion(ctx, userID)
		return err
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the member.")
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) revokeInvite(c *gin.Context) {
	id, ok := parseUUIDParam(c, "invite_id")
	if !ok {
		return
	}
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		return auth.RevokeInvite(c.Request.Context(), q, id)
	})
	if err != nil {
		var ie auth.InviteError
		if errors.As(err, &ie) {
			detail(c, http.StatusNotFound, ie.Msg)
			return
		}
		detail(c, http.StatusInternalServerError, "Could not revoke the invite.")
		return
	}
	c.Status(http.StatusNoContent)
}
