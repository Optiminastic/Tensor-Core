package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// canAccessBrand reports whether a user may see and work in a brand. Holders of
// brand:manage (admins) reach every brand; everyone else only the brands an admin
// assigned them via user_brands. It fails closed: a database error denies access.
func (s *Server) canAccessBrand(ctx context.Context, user auth.AuthenticatedUser, slug string) bool {
	if user.Has(auth.BrandManage.Key()) {
		return true
	}
	ok, err := s.store.Q.UserHasBrand(ctx, gen.UserHasBrandParams{UserID: user.ID, BrandSlug: slug})
	if err != nil {
		return false
	}
	return ok
}

// requireBrandAccess enforces canAccessBrand for the current request, writing a
// 403 and returning false when the caller may not touch the brand. Callers must
// already have passed authentication (the router's RequireUser guard).
func (s *Server) requireBrandAccess(c *gin.Context, slug string) bool {
	user, ok := auth.UserFrom(c)
	if !ok || !s.canAccessBrand(c.Request.Context(), user, slug) {
		detail(c, http.StatusForbidden, "You do not have access to this brand.")
		return false
	}
	return true
}
