package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// /admin is guarded at the router level by user:manage.
func (s *Server) registerAdmin(r *gin.Engine) {
	g := r.Group("/admin")
	g.Use(s.guards.RequireUser(), s.guards.RequirePermission(auth.UserManage.Key()))
	g.POST("/invites", s.createInvite)
	g.GET("/invites", s.listInvites)
	g.POST("/invites/:invite_id/revoke", s.revokeInvite)
}

type inviteCreateRequest struct {
	Email string `json:"email" binding:"required,email"`
	Role  string `json:"role" binding:"required,oneof=ADMIN DESIGNER PROJECT_LEAD PERFORMANCE_MARKETER OPERATOR"`
}

type inviteCreatedResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	Token     string    `json:"token"`
}

func (s *Server) createInvite(c *gin.Context) {
	var req inviteCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	user, _ := auth.UserFrom(c)

	var invite gen.UserInvite
	var token string
	err := s.store.InTx(c.Request.Context(), func(q *gen.Queries) error {
		inv, tok, err := auth.IssueInvite(c.Request.Context(), q, req.Email, auth.RoleName(req.Role), user.ID, auth.DefaultInviteTTL)
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
		ID: invite.ID.String(), Email: invite.Email, Role: req.Role,
		ExpiresAt: db.Time(invite.ExpiresAt), Token: token,
	})
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
	rows, err := s.store.Q.ListInvitesWithRole(c.Request.Context())
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
