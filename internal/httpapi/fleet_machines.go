package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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

func (s *Server) fleetMachineDTO(ctx context.Context, m gen.Machine) fleetMachineResponse {
	var currentBatchID *string
	if m.CurrentBatchID != nil {
		id := m.CurrentBatchID.String()
		currentBatchID = &id
	}
	startedAt := db.TimePtr(m.PrintStartedAt)
	return fleetMachineResponse{
		ID: m.ID.String(), MachineID: m.MachineID, Name: m.Name, ImageURL: m.ImageUrl,
		Status: m.Status, Filaments: s.computeLiveFilaments(ctx, m),
		CurrentBatchID: currentBatchID, CurrentLayer: m.CurrentLayer, TotalLayers: m.TotalLayers,
		BatchTotalTimeMinutes: m.BatchTotalTimeMinutes, PrintStartedAt: startedAt,
		RemainingSeconds: remainingSeconds(m.BatchTotalTimeMinutes, startedAt),
		TotalWasteGrams:  db.NumFloat(m.TotalWasteGrams),
		CreatedAt:        db.Time(m.CreatedAt), UpdatedAt: db.Time(m.UpdatedAt),
	}
}

// fleetFilamentResponse is one colour/material a machine is currently using,
// paired with that bucket's live level in the shared filament pool.
type fleetFilamentResponse struct {
	Colour         string  `json:"colour"`
	Type           string  `json:"type"`
	RemainingGrams float64 `json:"remaining_grams"`
}

// computeLiveFilaments derives what a machine is currently consuming - the
// distinct (material, colour) pairs needed by whatever batch is actively
// printing on it, or the next one queued if it's idle - each paired with
// that colour's current level in the single shared filament_inventory pool
// (see filament_colour.go; there is one warehouse pool, not a separate
// stock per machine). machines.filaments is a schema column nothing ever
// writes to (dead groundwork); this computes a live view in its place
// rather than trusting that always-empty JSONB blob.
func (s *Server) computeLiveFilaments(ctx context.Context, m gen.Machine) json.RawMessage {
	empty := json.RawMessage("[]")

	batchID := m.CurrentBatchID
	if batchID == nil {
		queued, err := s.store.Q.ListQueuedBatchesForFleetMachine(ctx, m.ID)
		if err != nil || len(queued) == 0 {
			return empty
		}
		batchID = &queued[0].ID
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, batchID)
	if err != nil || len(jobs) == 0 {
		return empty
	}

	split := filamentSplitForJobs(jobs)
	out := make([]fleetFilamentResponse, 0, len(split))
	for key := range split {
		var grams float64
		if row, err := s.store.Q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{
			Material: key.Material, Colour: colourPtr(key.Colour),
		}); err == nil {
			grams = db.NumFloat(row.GramsAvailable)
		}
		out = append(out, fleetFilamentResponse{Colour: key.Colour, Type: key.Material, RemainingGrams: grams})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Colour < out[j].Colour
	})

	data, err := json.Marshal(out)
	if err != nil {
		return empty
	}
	return data
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
	g.GET("/:id/queue", s.guards.RequirePermission(auth.MachineRead.Key()), s.getFleetMachineQueue)
	s.registerFleetMachineWrites(g)
}

func (s *Server) listFleetMachines(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.store.Q.ListFleetMachines(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list machines.")
		return
	}
	out := make([]fleetMachineResponse, 0, len(rows))
	for _, m := range rows {
		out = append(out, s.fleetMachineDTO(ctx, m))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getFleetMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	m, err := s.store.Q.GetFleetMachine(ctx, id)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	c.JSON(http.StatusOK, s.fleetMachineDTO(ctx, m))
}

// getFleetMachineQueue lists every batch already queued (open or in_progress)
// on this physical unit's linked profile, oldest first - what Machine
// Management shows as "queued on this machine", not just its current print.
func (s *Server) getFleetMachineQueue(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetFleetMachine(ctx, id); err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
		return
	}
	// Every status, not just open/in_progress: this feeds the machine's Kanban
	// board, whose Draft and Completed columns could otherwise never contain
	// anything. The narrower ListQueuedBatchesForFleetMachine still backs the
	// scheduler's load calculation, where "outstanding work" is the point.
	rows, err := s.store.Q.ListAllBatchesForFleetMachine(ctx, gen.ListAllBatchesForFleetMachineParams{
		FleetMachineID: id, RowLimit: machineQueueLimit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list the machine's queue.")
		return
	}
	out := make([]batchResponse, 0, len(rows))
	for _, b := range rows {
		out = append(out, batchDTO(b))
	}
	c.JSON(http.StatusOK, out)
}

// machineQueueLimit caps a machine's board history. Generous enough to cover
// any plausible working period, bounded so a long-lived machine does not
// return every plate it has ever printed.
const machineQueueLimit = 200
