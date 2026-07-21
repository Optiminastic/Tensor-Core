package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// /internal is service-to-service: guarded by the shared secret, never exposed
// to a browser.
func (s *Server) registerInternal(r *gin.Engine) {
	g := r.Group("/internal")
	g.Use(s.guards.RequireInternalSecret())
	g.GET("/users/:user_id/authz", s.internalUserAuthz)
	g.GET("/bootstrap-status", s.internalBootstrapStatus)
	g.POST("/bootstrap-admin", s.internalBootstrapAdmin)
	g.GET("/invites/:token", s.internalCheckInvite)
	g.POST("/invites/accept", s.internalAcceptInvite)
}

func (s *Server) internalUserAuthz(c *gin.Context) {
	userID := c.Param("user_id")
	if len(userID) < 1 || len(userID) > 64 {
		detail(c, http.StatusUnprocessableEntity, "Invalid user id.")
		return
	}
	resp, err := auth.ResolveUserAuthz(c.Request.Context(), s.store.Q, userID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not resolve authorization.")
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) internalBootstrapStatus(c *gin.Context) {
	exists, err := s.store.Q.AdminExists(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read bootstrap status.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"adminExists": exists})
}

type bootstrapAdminRequest struct {
	UserID string `json:"user_id" binding:"required,min=1,max=64"`
}

func (s *Server) internalBootstrapAdmin(c *gin.Context) {
	var req bootstrapAdminRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	exists, err := s.store.Q.AdminExists(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not check for an existing admin.")
		return
	}
	if exists {
		detail(c, http.StatusConflict, "An admin already exists. Use an invite instead.")
		return
	}

	adminRoleID, err := s.store.Q.GetRoleIDByName(ctx, string(auth.RoleAdmin))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusServiceUnavailable, "Roles are not seeded. Run: go run ./cmd/seed")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the admin role.")
		return
	}

	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.InsertUserRole(ctx, gen.InsertUserRoleParams{
			UserID: req.UserID, RoleID: adminRoleID, AssignedBy: nil,
		}); err != nil {
			return err
		}
		_, err := q.BumpPermissionsVersion(ctx, req.UserID)
		return err
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not grant admin.")
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) internalCheckInvite(c *gin.Context) {
	token := c.Param("token")
	if len(token) < 1 || len(token) > 128 {
		detail(c, http.StatusUnprocessableEntity, "Invalid token.")
		return
	}
	ctx := c.Request.Context()

	invite, err := auth.ValidateInvite(ctx, s.store.Q, token)
	if err != nil {
		var ie auth.InviteError
		if errors.As(err, &ie) {
			detail(c, http.StatusBadRequest, ie.Msg)
			return
		}
		detail(c, http.StatusInternalServerError, "Could not validate the invite.")
		return
	}
	roleName, err := s.store.Q.GetRoleNameByID(ctx, invite.RoleID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "The invited role no longer exists")
		return
	}
	c.JSON(http.StatusOK, gin.H{"email": invite.Email, "role": roleName})
}

type acceptInviteRequest struct {
	Token  string `json:"token" binding:"required,min=1"`
	UserID string `json:"user_id" binding:"required,min=1,max=64"`
}

func (s *Server) internalAcceptInvite(c *gin.Context) {
	var req acceptInviteRequest
	if !bindJSON(c, &req) {
		return
	}
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		_, err := auth.AcceptInvite(c.Request.Context(), q, req.Token, req.UserID)
		return err
	})
	if err != nil {
		var ie auth.InviteError
		if errors.As(err, &ie) {
			detail(c, http.StatusBadRequest, ie.Msg)
			return
		}
		detail(c, http.StatusInternalServerError, "Could not accept the invite.")
		return
	}
	c.Status(http.StatusNoContent)
}
