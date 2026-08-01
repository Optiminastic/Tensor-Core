package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// The /machines router owns machine SLICING + operational config (not cost).
// Reads need machine:read; writes need machine:manage (Admin, Project Lead,
// Operator). Cost lives on /config/machines (config:manage) - see config.go.
func (s *Server) registerMachineOps(r *gin.Engine) {
	g := r.Group("/machines")
	g.Use(s.guards.RequireUser())
	read := s.guards.RequirePermission(auth.MachineRead.Key())
	manage := s.guards.RequirePermission(auth.MachineManage.Key())

	g.GET("", read, s.listMachinesConfig)
	g.GET("/suggested", read, s.suggestedMachine)
	g.GET("/profile-options", read, s.profileOptions)
	g.GET("/:id", read, s.getMachineConfig)
	g.POST("", manage, s.createMachineConfig)
	g.PATCH("/:id", manage, s.updateMachineConfig)
	g.DELETE("/:id", manage, s.deleteMachine)
}

// machineOpsResponse is the light operational view (status, not cost) returned by
// the batch-planner's machine suggestion.
type machineOpsResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	IsActive bool   `json:"is_active"`
}

// profileOptions returns the filaments and layer heights the bundled Bambu build
// can slice for a given family + nozzle, so the settings form only offers valid
// config. Backed by the curated catalog (the API host has no profile files).
func (s *Server) profileOptions(c *gin.Context) {
	family := c.Query("family")
	if family == "" {
		family = "H2S"
	}
	nozzle, err := strconv.ParseFloat(c.Query("nozzle"), 64)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "A numeric 'nozzle' query parameter is required.")
		return
	}
	c.JSON(http.StatusOK, slicing.ProfileOptions(family, nozzle))
}

// suggestedMachine returns the least-loaded online machine, or null when none is
// online. Used by the batch planner to pick where a batch runs.
func (s *Server) suggestedMachine(c *gin.Context) {
	m, err := s.store.Q.SuggestMachine(c.Request.Context())
	if err != nil {
		if isNoRows(err) {
			c.JSON(http.StatusOK, nil)
			return
		}
		detail(c, http.StatusInternalServerError, "Could not suggest a machine.")
		return
	}
	c.JSON(http.StatusOK, machineOpsResponse{ID: m.ID.String(), Name: m.Name, Status: m.Status, IsActive: true})
}
