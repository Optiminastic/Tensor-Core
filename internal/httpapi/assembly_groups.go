package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// An assembly group is one physical unit of a multi-part product. Its parts print
// as independent jobs; the unit can only be assembled once every part is built and
// QC-passed, so a good part waits (held) for its failed siblings rather than being
// packaged or scrapped. These handlers expose a unit and gate its assembly.

type assemblyPartResponse struct {
	PartUID  string  `json:"part_uid"`
	Role     string  `json:"part_role"`
	Instance int32   `json:"part_instance"`
	JobID    *string `json:"job_id"`
}

type assemblyGroupResponse struct {
	ID         string                 `json:"id"`
	OrderID    *string                `json:"order_id"`
	DesignSku  *string                `json:"design_sku"`
	UnitIndex  int32                  `json:"unit_index"`
	Status     string                 `json:"status"`
	TotalParts int32                  `json:"total_parts"`
	ReadyParts int32                  `json:"ready_parts"`
	Parts      []assemblyPartResponse `json:"parts"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

func (s *Server) registerAssemblyGroups(r *gin.Engine) {
	g := r.Group("/assembly-groups")
	g.Use(s.guards.RequireUser())
	g.GET("/:id", s.guards.RequirePermission(auth.ProductionRead.Key()), s.getAssemblyGroup)
	g.POST("/:id/assemble", s.guards.RequirePermission(auth.AssemblySubmit.Key()), s.assembleGroup)
}

func (s *Server) getAssemblyGroup(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	group, err := s.store.Q.GetAssemblyGroup(ctx, id)
	if err != nil {
		dbError(c, err, "That assembly group does not exist.", "Could not load the assembly group.")
		return
	}
	dto, err := s.assemblyGroupDTO(ctx, group)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the assembly group.")
		return
	}
	c.JSON(http.StatusOK, dto)
}

// assembleGroup marks a product unit assembled. It fails closed: every part slot
// must have a current job that is completed and QC-passed, so a unit missing a
// still-printing or reprinting part cannot be assembled or shipped.
func (s *Server) assembleGroup(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	group, err := s.store.Q.GetAssemblyGroup(ctx, id)
	if err != nil {
		dbError(c, err, "That assembly group does not exist.", "Could not load the assembly group.")
		return
	}
	if group.Status == production.GroupAssembled || group.Status == production.GroupPackaged {
		detail(c, http.StatusConflict, "This unit has already been assembled.")
		return
	}
	readiness, err := s.store.Q.GetAssemblyGroupReadiness(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not check the unit's parts.")
		return
	}
	if readiness.TotalParts == 0 || readiness.ReadyParts < readiness.TotalParts {
		detail(c, http.StatusConflict, "Every part must be completed and QC-passed before assembly.")
		return
	}

	updated, err := s.store.Q.SetAssemblyGroupStatus(ctx, gen.SetAssemblyGroupStatusParams{
		ID: id, Status: production.GroupAssembled,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not assemble the unit.")
		return
	}
	dto, err := s.assemblyGroupDTO(ctx, updated)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the assembly group.")
		return
	}
	c.JSON(http.StatusOK, dto)
}

// reconcileAssemblyGroupAfterQcPass runs after a part-job passes QC. If the job is
// not part of a multi-part unit it is a no-op. Otherwise: when every part of the
// unit is now built, the unit becomes ready_to_assemble and its held good parts are
// released; until then this good part is held (parked as WIP) so it is never
// packaged while a sibling is still being reprinted.
func (s *Server) reconcileAssemblyGroupAfterQcPass(ctx context.Context, q *gen.Queries, jobID uuid.UUID) error {
	slot, err := q.GetAssemblyPartByJob(ctx, &jobID)
	if err != nil {
		if isNoRows(err) {
			return nil // a single-part product's job fills no slot
		}
		return err
	}
	readiness, err := q.GetAssemblyGroupReadiness(ctx, slot.AssemblyGroupID)
	if err != nil {
		return err
	}
	if readiness.TotalParts > 0 && readiness.ReadyParts >= readiness.TotalParts {
		if _, err := q.SetAssemblyGroupStatus(ctx, gen.SetAssemblyGroupStatusParams{
			ID: slot.AssemblyGroupID, Status: production.GroupReadyToAssemble,
		}); err != nil {
			return err
		}
		return s.releaseHeldParts(ctx, q, slot.AssemblyGroupID)
	}

	// Siblings not ready: hold this good part and mark the unit as waiting on parts.
	if _, err := q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
		ID: jobID, Held: ptr(true),
	}); err != nil {
		return err
	}
	_, err = q.SetAssemblyGroupStatus(ctx, gen.SetAssemblyGroupStatusParams{
		ID: slot.AssemblyGroupID, Status: production.GroupAwaitingParts,
	})
	return err
}

// releaseHeldParts clears the hold on every current part-job of a unit, once all
// its parts are built and it can move to assembly.
func (s *Server) releaseHeldParts(ctx context.Context, q *gen.Queries, groupID uuid.UUID) error {
	parts, err := q.ListAssemblyGroupParts(ctx, groupID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if p.JobID == nil {
			continue
		}
		if _, err := q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
			ID: *p.JobID, Held: ptr(false),
		}); err != nil {
			return err
		}
	}
	return nil
}

// assemblyGroupDTO shapes a unit and its part slots for the API, including the
// built/total part counts used to gate assembly.
func (s *Server) assemblyGroupDTO(ctx context.Context, g gen.AssemblyGroup) (assemblyGroupResponse, error) {
	parts, err := s.store.Q.ListAssemblyGroupParts(ctx, g.ID)
	if err != nil {
		return assemblyGroupResponse{}, err
	}
	readiness, err := s.store.Q.GetAssemblyGroupReadiness(ctx, g.ID)
	if err != nil {
		return assemblyGroupResponse{}, err
	}
	out := assemblyGroupResponse{
		ID: g.ID.String(), OrderID: uuidPtrStr(g.OrderID), DesignSku: g.DesignSku,
		UnitIndex: g.UnitIndex, Status: g.Status,
		TotalParts: readiness.TotalParts, ReadyParts: readiness.ReadyParts,
		CreatedAt: db.Time(g.CreatedAt), UpdatedAt: db.Time(g.UpdatedAt),
		Parts: make([]assemblyPartResponse, 0, len(parts)),
	}
	for _, p := range parts {
		out.Parts = append(out.Parts, assemblyPartResponse{
			PartUID: p.PartUid, Role: p.PartRole, Instance: p.PartInstance, JobID: uuidPtrStr(p.JobID),
		})
	}
	return out, nil
}
