package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

type projectResponse struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Brand       string    `json:"brand"`
	Description *string   `json:"description"`
	Status      string    `json:"status"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func projectDTO(
	id uuid.UUID, name, brand string, description *string, status, createdBy string,
	created, updated pgtype.Timestamptz,
) projectResponse {
	return projectResponse{
		ID: id.String(), Name: name, Brand: brand, Description: description, Status: status,
		CreatedBy: createdBy, CreatedAt: db.Time(created), UpdatedAt: db.Time(updated),
	}
}

func (s *Server) registerProjects(r *gin.Engine) {
	g := r.Group("/projects")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.ProjectRead.Key()), s.listProjects)
	g.POST("", s.guards.RequirePermission(auth.ProjectManage.Key()), s.createProject)
	g.GET("/:id", s.guards.RequirePermission(auth.ProjectRead.Key()), s.getProject)
	g.PATCH("/:id", s.guards.RequirePermission(auth.ProjectManage.Key()), s.updateProject)
}

func (s *Server) listProjects(c *gin.Context) {
	rows, err := s.store.Q.ListProjects(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list projects.")
		return
	}
	out := make([]projectResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectDTO(r.ID, r.Name, r.Brand, r.Description, r.Status, r.CreatedBy, r.CreatedAt, r.UpdatedAt))
	}
	c.JSON(http.StatusOK, out)
}

type projectCreateRequest struct {
	Name        string  `json:"name" binding:"required,min=1,max=120"`
	Brand       string  `json:"brand" binding:"required,min=1,max=120"`
	Description *string `json:"description" binding:"omitempty,max=500"`
}

// brandMustExist confirms a project's brand references a real brand slug. It
// writes a 422 and returns false when the brand is not configured, so the FK
// constraint never surfaces as a raw 500.
func (s *Server) brandMustExist(c *gin.Context, slug string) bool {
	exists, err := s.store.Q.BrandExists(c.Request.Context(), slug)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not verify the brand.")
		return false
	}
	if !exists {
		detail(c, http.StatusUnprocessableEntity, notConfigured(pricing.Brand(slug)))
		return false
	}
	return true
}

func (s *Server) createProject(c *gin.Context) {
	var req projectCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	if !s.brandMustExist(c, req.Brand) {
		return
	}
	user, _ := auth.UserFrom(c) // created_by comes from the token, never the body.

	r, err := s.store.Q.InsertProject(c.Request.Context(), gen.InsertProjectParams{
		ID: uuid.New(), Name: req.Name, Brand: req.Brand,
		Description: req.Description, Status: "active", CreatedBy: user.ID,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the project.")
		return
	}
	c.JSON(http.StatusCreated, projectDTO(r.ID, r.Name, r.Brand, r.Description, r.Status, r.CreatedBy, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) getProject(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	r, err := s.store.Q.GetProject(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, "Project not found")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the project.")
		return
	}
	c.JSON(http.StatusOK, projectDTO(r.ID, r.Name, r.Brand, r.Description, r.Status, r.CreatedBy, r.CreatedAt, r.UpdatedAt))
}

func (s *Server) updateProject(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	body, ok := readBody(c)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return
	}

	params := gen.UpdateProjectParams{ID: id}
	if v, ok := raw["name"]; ok {
		var name string
		if !decodeField(c, v, &name) {
			return
		}
		if len(name) < 1 || len(name) > 120 {
			detail(c, http.StatusUnprocessableEntity, "Name must be 1 to 120 characters.")
			return
		}
		params.Name = &name
	}
	if v, ok := raw["brand"]; ok {
		var brand string
		if !decodeField(c, v, &brand) {
			return
		}
		if len(brand) < 1 || len(brand) > 120 {
			detail(c, http.StatusUnprocessableEntity, "Brand must be 1 to 120 characters.")
			return
		}
		if !s.brandMustExist(c, brand) {
			return
		}
		params.Brand = &brand
	}
	if v, ok := raw["description"]; ok {
		var desc *string
		if !decodeField(c, v, &desc) {
			return
		}
		if desc != nil && len(*desc) > 500 {
			detail(c, http.StatusUnprocessableEntity, "Description must be at most 500 characters.")
			return
		}
		params.SetDescription = true
		params.Description = desc
	}
	if v, ok := raw["status"]; ok {
		var status string
		if !decodeField(c, v, &status) {
			return
		}
		if status != "active" && status != "archived" {
			detail(c, http.StatusUnprocessableEntity, "Status must be 'active' or 'archived'.")
			return
		}
		params.Status = status
	}

	r, err := s.store.Q.UpdateProject(c.Request.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			detail(c, http.StatusNotFound, "Project not found")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not update the project.")
		return
	}
	c.JSON(http.StatusOK, projectDTO(r.ID, r.Name, r.Brand, r.Description, r.Status, r.CreatedBy, r.CreatedAt, r.UpdatedAt))
}
