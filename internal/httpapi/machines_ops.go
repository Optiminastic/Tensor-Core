package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// machineOpsResponse is the operational view of a machine (status, not cost).
type machineOpsResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	IsActive bool   `json:"is_active"`
}

func (s *Server) registerMachineOps(r *gin.Engine) {
	g := r.Group("/machines")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.MachineRead.Key()), s.listMachineOps)
	g.GET("/suggested", s.guards.RequirePermission(auth.MachineRead.Key()), s.suggestedMachine)
	g.GET("/:id", s.guards.RequirePermission(auth.MachineRead.Key()), s.getMachineOps)
	g.PATCH("/:id", s.guards.RequirePermission(auth.MachineManage.Key()), s.updateMachineStatus)
}

func (s *Server) listMachineOps(c *gin.Context) {
	rows, err := s.store.Q.ListMachineOps(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list machines.")
		return
	}
	out := make([]machineOpsResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, machineOpsResponse{ID: m.ID.String(), Name: m.Name, Status: m.Status, IsActive: m.IsActive})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getMachineOps(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	m, err := s.store.Q.GetMachineOps(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	c.JSON(http.StatusOK, machineOpsResponse{ID: m.ID.String(), Name: m.Name, Status: m.Status, IsActive: m.IsActive})
}

type machineStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

func (s *Server) updateMachineStatus(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req machineStatusRequest
	if !bindJSON(c, &req) {
		return
	}
	if !production.ValidMachineStatus(req.Status) {
		detail(c, http.StatusUnprocessableEntity, "Status must be one of online, busy, offline, maintenance.")
		return
	}
	if _, err := s.store.Q.GetMachineOps(c.Request.Context(), id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	m, err := s.store.Q.UpdateMachineStatus(c.Request.Context(), gen.UpdateMachineStatusParams{ID: id, Status: req.Status})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the machine.")
		return
	}
	c.JSON(http.StatusOK, machineOpsResponse{ID: m.ID.String(), Name: m.Name, Status: m.Status, IsActive: m.IsActive})
}

// suggestedMachine returns the least-loaded online machine, or null when none is
// online.
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
