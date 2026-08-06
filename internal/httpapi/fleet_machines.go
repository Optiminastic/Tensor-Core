package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// fleetMachineResponse is the physical printer fleet (table: machines) - live
// print-state, distinct from machineOpsResponse (machine_profiles, the printer
// model/slicing profile). remaining_seconds is computed on read from
// batch_total_time_minutes and print_started_at rather than stored, so it is
// always accurate without anything having to tick it down.
type fleetMachineResponse struct {
	ID                    string          `json:"id"`
	MachineID             string          `json:"machine_id"`
	Name                  string          `json:"name"`
	ImageURL              *string         `json:"image_url"`
	Status                string          `json:"status"`
	Filaments             json.RawMessage `json:"filaments"`
	CurrentBatchID        *string         `json:"current_batch_id"`
	CurrentLayer          *int32          `json:"current_layer"`
	TotalLayers           *int32          `json:"total_layers"`
	BatchTotalTimeMinutes *int32          `json:"batch_total_time_minutes"`
	PrintStartedAt        *time.Time      `json:"print_started_at"`
	RemainingSeconds      *int64          `json:"remaining_seconds"`
	TotalWasteGrams       float64         `json:"total_waste_grams"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

func fleetMachineDTO(m gen.Machine) fleetMachineResponse {
	var currentBatchID *string
	if m.CurrentBatchID != nil {
		id := m.CurrentBatchID.String()
		currentBatchID = &id
	}
	startedAt := db.TimePtr(m.PrintStartedAt)
	return fleetMachineResponse{
		ID: m.ID.String(), MachineID: m.MachineID, Name: m.Name, ImageURL: m.ImageUrl,
		Status: m.Status, Filaments: json.RawMessage(m.Filaments),
		CurrentBatchID: currentBatchID, CurrentLayer: m.CurrentLayer, TotalLayers: m.TotalLayers,
		BatchTotalTimeMinutes: m.BatchTotalTimeMinutes, PrintStartedAt: startedAt,
		RemainingSeconds: remainingSeconds(m.BatchTotalTimeMinutes, startedAt),
		TotalWasteGrams:  db.NumFloat(m.TotalWasteGrams),
		CreatedAt:        db.Time(m.CreatedAt), UpdatedAt: db.Time(m.UpdatedAt),
	}
}

// remainingSeconds computes a live countdown from a total duration and a start
// time, floored at zero. Returns nil when either input is missing (nothing is
// printing) rather than a stale stored number.
func remainingSeconds(totalMinutes *int32, startedAt *time.Time) *int64 {
	if totalMinutes == nil || startedAt == nil {
		return nil
	}
	total := time.Duration(*totalMinutes) * time.Minute
	remaining := int64((total - time.Since(*startedAt)).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

func (s *Server) registerFleetMachines(r *gin.Engine) {
	g := r.Group("/machine-fleet")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.MachineRead.Key()), s.listFleetMachines)
	g.GET("/:id", s.guards.RequirePermission(auth.MachineRead.Key()), s.getFleetMachine)
}

func (s *Server) listFleetMachines(c *gin.Context) {
	rows, err := s.store.Q.ListFleetMachines(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list machines.")
		return
	}
	out := make([]fleetMachineResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, fleetMachineDTO(m))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getFleetMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	m, err := s.store.Q.GetFleetMachine(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	c.JSON(http.StatusOK, fleetMachineDTO(m))
}
