package httpapi

// Writing a physical machine's live print state.
//
// Until now the machines table was write-once: seeded, then never updated.
// UpdateFleetMachineState had no callers at all, so every fleet machine sat
// permanently at status 'idle' with current_batch_id, current_layer,
// total_layers, batch_total_time_minutes and print_started_at all NULL - while
// the UI (fleet-machine-row.tsx) already rendered every one of those fields
// and a live countdown derived from them.
//
// This is the missing write side. It is a real product gap rather than
// scaffolding for the print simulator: a Bambu bridge reporting real printer
// telemetry needs exactly this route, and the simulator is simply its first
// caller.

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

type patchFleetMachineRequest struct {
	Status *string `json:"status"`
	// CurrentBatchID and the four progress fields are always applied together
	// with the status, and always as a complete set - see SetFleetMachineState
	// for why there is no partial update.
	CurrentBatchID        *string `json:"current_batch_id"`
	CurrentLayer          *int32  `json:"current_layer"`
	TotalLayers           *int32  `json:"total_layers"`
	BatchTotalTimeMinutes *int32  `json:"batch_total_time_minutes"`
}

// FleetMachineState is one machine's complete live state. Every field is
// replaced on every write, deliberately: a partial update is how a machine
// ends up 'idle' while still advertising a current batch and a counting-down
// timer for a print that finished hours ago.
type FleetMachineState struct {
	Status                string
	CurrentBatchID        *uuid.UUID
	CurrentLayer          *int32
	TotalLayers           *int32
	BatchTotalTimeMinutes *int32
	// StartedAt is when the current print began; remaining_seconds is derived
	// from it and BatchTotalTimeMinutes on every read, so nothing has to tick
	// a stored countdown down. Nil for a machine that is not printing.
	StartedAt *time.Time
}

// IdleFleetMachineState is the state a machine returns to when its print ends:
// everything cleared, so no stale batch or countdown survives.
func IdleFleetMachineState() FleetMachineState {
	return FleetMachineState{Status: production.FleetMachineIdle}
}

// SetFleetMachineState replaces a machine's live print state wholesale.
func (s *Server) SetFleetMachineState(ctx context.Context, id uuid.UUID, st FleetMachineState) (gen.Machine, error) {
	if !production.ValidFleetMachineState(st.Status) {
		return gen.Machine{}, statusErr(http.StatusUnprocessableEntity, "That machine status is not valid.")
	}
	m, err := s.store.Q.UpdateFleetMachineState(ctx, gen.UpdateFleetMachineStateParams{
		ID: id, Status: st.Status, CurrentBatchID: st.CurrentBatchID,
		CurrentLayer: st.CurrentLayer, TotalLayers: st.TotalLayers,
		BatchTotalTimeMinutes: st.BatchTotalTimeMinutes,
		PrintStartedAt:        db.Timestamptz(st.StartedAt),
	})
	if err != nil {
		if isNoRows(err) {
			return gen.Machine{}, statusErrf(http.StatusNotFound, "That machine does not exist.", err)
		}
		return gen.Machine{}, statusErrf(http.StatusInternalServerError, "Could not update the machine.", err)
	}
	return m, nil
}

func (s *Server) patchFleetMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req patchFleetMachineRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Status == nil {
		detail(c, http.StatusUnprocessableEntity, "A status is required.")
		return
	}
	st := FleetMachineState{
		Status: *req.Status, CurrentLayer: req.CurrentLayer, TotalLayers: req.TotalLayers,
		BatchTotalTimeMinutes: req.BatchTotalTimeMinutes,
	}
	if req.CurrentBatchID != nil {
		batchID, err := uuid.Parse(*req.CurrentBatchID)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "The current_batch_id is not a valid identifier.")
			return
		}
		if _, err := s.store.Q.GetBatchByID(c.Request.Context(), batchID); err != nil {
			dbError(c, err, "That batch does not exist.", "Could not load the batch.")
			return
		}
		st.CurrentBatchID = &batchID
	}
	// The caller says a print is underway; the clock starts now. Deliberately
	// not client-supplied: remaining_seconds is derived from it, so a caller
	// able to backdate it could make a print appear already finished.
	if st.Status == production.FleetMachineRunning {
		now := time.Now()
		st.StartedAt = &now
	}

	m, err := s.SetFleetMachineState(c.Request.Context(), id, st)
	if err != nil {
		writeStatusError(c, err, "Could not update the machine.")
		return
	}
	c.JSON(http.StatusOK, s.fleetMachineDTO(c.Request.Context(), m))
}

// registerFleetMachineWrites adds the write half of /machine-fleet. Guarded by
// machine:manage - machine:read is deliberately not enough to move a printer.
func (s *Server) registerFleetMachineWrites(g *gin.RouterGroup) {
	g.PATCH("/:id", s.guards.RequirePermission(auth.MachineManage.Key()), s.patchFleetMachine)
}
