package httpapi

// GET /printing/history - what actually came off the beds, from BambuBuddy.
//
// The sibling of printing_queue.go, and read the same way: through, never
// mirrored. The two together are the whole board - what is waiting, and what
// happened.
//
// Machine Management's History tab used to list Tensor's own batches with
// status 'completed'. That is a record of Tensor's intentions, not of the
// floor: a plate that FAILED on the printer never appeared in it, and neither
// did a plate somebody ran straight from BambuBuddy. Both are exactly what
// somebody opens a history for. BambuBuddy is the only system that watched the
// print, so it is the only one that can say how long it really took, how much
// filament it really used, and why it stopped.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// archiveResponse is one finished print, in the shape the history board renders.
type archiveResponse struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// PrinterID is the machine that ran it; PrinterName is resolved here from
	// the fleet, because BambuBuddy's archive carries only the id and a board
	// showing "printer 4" helps nobody.
	PrinterID   *int   `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	// ThumbnailURL is served by Tensor's own proxy - the browser cannot reach
	// the tailnet host, and BambuBuddy's paths need the API key.
	ThumbnailURL *string `json:"thumbnail_url"`

	// Estimated against actual, both sent. The board shows the actual and can
	// still say "ran 40 minutes long" because it has the estimate beside it -
	// which is most of the reason to keep a history at all.
	PrintTimeSeconds    int     `json:"print_time_seconds"`
	ActualTimeSeconds   int     `json:"actual_time_seconds"`
	DurationSeconds     int     `json:"duration_seconds"`
	FilamentUsedGrams   float64 `json:"filament_used_grams"`
	FilamentActualGrams float64 `json:"filament_actual_grams"`

	FilamentType    string   `json:"filament_type"`
	FilamentColours []string `json:"filament_colours"`
	Cost            *float64 `json:"cost"`
	EnergyCost      *float64 `json:"energy_cost"`
	EnergyKwh       *float64 `json:"energy_kwh"`

	Quantity       int      `json:"quantity"`
	LayerHeight    *float64 `json:"layer_height"`
	NozzleDiameter *float64 `json:"nozzle_diameter"`
	BedType        string   `json:"bed_type"`
	SlicedForModel string   `json:"sliced_for_model"`

	RunCount           int `json:"run_count"`
	SuccessfulRunCount int `json:"successful_run_count"`
	FailedRunCount     int `json:"failed_run_count"`
	// FailureReason is BambuBuddy's own wording for why it stopped.
	FailureReason string `json:"failure_reason"`

	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

// listPrintingHistory answers with BambuBuddy's archive, newest first.
func (s *Server) listPrintingHistory(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}

	limit := bambubuddy.DefaultArchiveLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			detail(c, http.StatusBadRequest, "limit must be a positive whole number.")
			return
		}
		limit = n
	}

	items, err := s.bambu.ListArchives(ctx, limit)
	if err != nil {
		obs.FromContext(ctx).Error("could not read the BambuBuddy archive", "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusBadGateway, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not reach BambuBuddy to read the print history.")
		return
	}

	// Printer names in one read rather than one per row. The fleet is thirteen
	// machines, so this is a single small call whose failure is not worth
	// failing the page for - an unnamed printer still shows its id.
	names := s.printerNames(ctx)

	out := make([]archiveResponse, 0, len(items))
	for _, a := range items {
		out = append(out, archiveDTO(a, names))
	}

	// Newest first, by whichever timestamp the print actually reached. A print
	// still running has no completed_at and belongs at the top, since it is the
	// most recent thing that happened - sorting on completed_at alone would
	// bury it under prints that finished days ago.
	sort.SliceStable(out, func(i, j int) bool {
		return historyStamp(out[i]) > historyStamp(out[j])
	})
	c.JSON(http.StatusOK, out)
}

// historyStamp is the moment a row is ordered by: when it finished, else when
// it started, else when it was created. ISO-8601 sorts correctly as a string,
// which is why this compares text rather than parsing thirteen timestamps.
func historyStamp(a archiveResponse) string {
	return firstNonEmpty(a.CompletedAt, a.StartedAt, a.CreatedAt)
}

// printerNames maps BambuBuddy printer ids to their names.
//
// Best-effort: BambuBuddy has already answered once by the time this runs, so a
// failure here is a fleet read that went wrong on its own, and a history board
// missing its machine names is far better than no history board.
func (s *Server) printerNames(ctx context.Context) map[int]string {
	printers, err := s.bambu.ListPrinters(ctx)
	if err != nil {
		obs.FromContext(ctx).Debug("could not name printers for the history board", "error", err)
		return nil
	}
	names := make(map[int]string, len(printers))
	for _, p := range printers {
		names[p.ID] = p.Name
	}
	return names
}

func archiveDTO(a bambubuddy.Archive, names map[int]string) archiveResponse {
	name := ""
	if a.PrinterID != nil {
		name = names[*a.PrinterID]
	}
	return archiveResponse{
		ID: a.ID, Name: a.Name(), Status: a.Status,
		PrinterID: a.PrinterID, PrinterName: name,
		ThumbnailURL: archiveThumbnailURL(a),

		PrintTimeSeconds:    a.PrintTimeSeconds,
		ActualTimeSeconds:   a.ActualTimeSeconds,
		DurationSeconds:     a.DurationSeconds(),
		FilamentUsedGrams:   a.FilamentUsedGrams,
		FilamentActualGrams: a.TotalFilamentActualGrams,

		FilamentType:    a.FilamentType,
		FilamentColours: a.Colours(),
		Cost:            a.Cost,
		EnergyCost:      a.EnergyCost,
		EnergyKwh:       a.EnergyKwh,

		Quantity:       a.Quantity,
		LayerHeight:    a.LayerHeight,
		NozzleDiameter: a.NozzleDiameter,
		BedType:        a.BedType,
		SlicedForModel: a.SlicedForModel,

		RunCount:           a.RunCount,
		SuccessfulRunCount: a.SuccessfulRunCount,
		FailedRunCount:     a.FailedRunCount,
		FailureReason:      a.FailureReason,

		CreatedBy:   a.CreatedByUsername,
		CreatedAt:   a.CreatedAt,
		StartedAt:   a.StartedAt,
		CompletedAt: a.CompletedAt,
	}
}

// archiveThumbnailURL reuses the queue board's own proxy - an archive preview
// is fetched by the same route with kind "archive", so there is one proxy and
// one cache rather than two that drift.
func archiveThumbnailURL(a bambubuddy.Archive) *string {
	u := "/printing/queue/thumbnail/archive/" + strconv.Itoa(a.ID)
	return &u
}

func (s *Server) registerPrintingHistory(r *gin.Engine) {
	g := r.Group("/printing/history")
	g.Use(s.guards.RequireUser())
	// machine:read, matching the queue: this is the state of the printers, and
	// whoever may look at the fleet may look at what it has produced.
	g.GET("", s.guards.RequirePermission(auth.MachineRead.Key()), s.listPrintingHistory)
}
