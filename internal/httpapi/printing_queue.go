package httpapi

// GET /printing/queue - BambuBuddy's print queue, as it stands right now.
//
// Read through rather than mirrored into a table. Once a plate is sent,
// BambuBuddy owns what happens to it: an operator can reorder, cancel or
// re-target an item in BambuBuddy's own UI, and a copy in Tensor would be stale
// from that moment on without anything on screen admitting it.
//
// The cost is that this page is only as available as the tunnel to BambuBuddy.
// That is the right trade for a live queue - a queue board that is confidently
// wrong is worse than one that says it cannot reach the printer host.

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// queueItemResponse is one plate in the queue, in the shape the board renders.
type queueItemResponse struct {
	ID          int    `json:"id"`
	Position    int    `json:"position"`
	Status      string `json:"status"`
	Name        string `json:"name"`
	PrinterID   *int   `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	// ThumbnailURL is served by Tensor, not BambuBuddy: the browser cannot
	// reach the tailnet host, and BambuBuddy's own paths need the API key.
	ThumbnailURL      *string  `json:"thumbnail_url"`
	PrintTimeSeconds  int      `json:"print_time_seconds"`
	FilamentUsedGrams float64  `json:"filament_used_grams"`
	FilamentType      string   `json:"filament_type"`
	FilamentColours   []string `json:"filament_colours"`
	EstimatedCost     *float64 `json:"estimated_cost"`
	NozzleDiameter    *float64 `json:"nozzle_diameter"`
	LayerHeight       *float64 `json:"layer_height"`
	BedType           string   `json:"bed_type"`
	SlicedForModel    string   `json:"sliced_for_model"`
	BatchName         string   `json:"batch_name"`
	CreatedBy         string   `json:"created_by"`
	CreatedAt         string   `json:"created_at"`
	StartedAt         string   `json:"started_at"`
	CompletedAt       string   `json:"completed_at"`
	// Why this item is not moving, and why it failed. Both are BambuBuddy's
	// own wording - Tensor has no better explanation to offer.
	WaitingReason string `json:"waiting_reason"`
	ErrorMessage  string `json:"error_message"`
}

func (s *Server) listPrintingQueue(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}

	items, err := s.bambu.ListQueue(ctx)
	if err != nil {
		obs.FromContext(ctx).Error("could not read the BambuBuddy queue", "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusBadGateway, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not reach BambuBuddy to read the print queue.")
		return
	}

	out := make([]queueItemResponse, 0, len(items))
	for _, it := range items {
		out = append(out, queueItemDTO(it))
	}

	// Waiting work first, then by queue position: an operator opens this to see
	// what is about to run, and BambuBuddy's own ordering puts every printer's
	// position 1 together regardless of whether the item is still live.
	sort.SliceStable(out, func(i, j int) bool {
		iw, jw := isWaitingStatus(out[i].Status), isWaitingStatus(out[j].Status)
		if iw != jw {
			return iw
		}
		if out[i].PrinterName != out[j].PrinterName {
			return out[i].PrinterName < out[j].PrinterName
		}
		return out[i].Position < out[j].Position
	})
	c.JSON(http.StatusOK, out)
}

func isWaitingStatus(status string) bool {
	return status == bambubuddy.QueuePending || status == bambubuddy.QueuePrinting
}

func queueItemDTO(it bambubuddy.QueueItem) queueItemResponse {
	return queueItemResponse{
		ID: it.ID, Position: it.Position, Status: it.Status, Name: it.Name(),
		PrinterID: it.PrinterID, PrinterName: it.PrinterName,
		ThumbnailURL:      queueThumbnailURL(it),
		PrintTimeSeconds:  it.PrintTimeSeconds,
		FilamentUsedGrams: it.FilamentUsedGrams,
		FilamentType:      it.FilamentType,
		FilamentColours:   it.Colours(),
		EstimatedCost:     it.EstimatedCost,
		NozzleDiameter:    it.NozzleDiameter,
		LayerHeight:       it.LayerHeight,
		BedType:           it.BedType,
		SlicedForModel:    firstNonEmpty(it.SlicedForModel, it.TargetModel),
		BatchName:         it.BatchName,
		CreatedBy:         it.CreatedByUsername,
		CreatedAt:         it.CreatedAt,
		StartedAt:         it.StartedAt,
		CompletedAt:       it.CompletedAt,
		WaitingReason:     it.WaitingReason,
		ErrorMessage:      it.ErrorMessage,
	}
}

// queueThumbnailURL points at Tensor's own proxy, which is the only route the
// browser can actually load: BambuBuddy sits on a tailnet the user's machine
// need not be on, and its thumbnail routes want the API key.
func queueThumbnailURL(it bambubuddy.QueueItem) *string {
	switch {
	case it.LibraryFileID != nil && it.LibraryFileThumbnail != "":
		u := "/printing/queue/thumbnail/library/" + strconv.Itoa(*it.LibraryFileID)
		return &u
	case it.ArchiveID != nil && it.ArchiveThumbnail != "":
		u := "/printing/queue/thumbnail/archive/" + strconv.Itoa(*it.ArchiveID)
		return &u
	}
	return nil
}

// GET /printing/queue/thumbnail/:kind/:id - proxy a plate preview.
//
// A proxy rather than a redirect: BambuBuddy is on a tailnet the browser need
// not be able to reach, and its thumbnail routes want the API key, which must
// never leave this service.
func (s *Server) printingQueueThumbnail(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}

	kind := c.Param("kind")
	if kind != "library" && kind != "archive" {
		detail(c, http.StatusBadRequest, "Unknown thumbnail source.")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		detail(c, http.StatusBadRequest, "That is not a valid id.")
		return
	}

	body, contentType, err := s.bambu.Thumbnail(ctx, kind, id)
	if err != nil {
		// Logged at Debug, not Error: a missing preview is cosmetic, and a
		// queue of plates without thumbnails would otherwise fill the log with
		// one entry per tile per page load.
		obs.FromContext(ctx).Debug("could not fetch a queue thumbnail",
			"kind", kind, "id", id, "error", err)
		c.Status(http.StatusNotFound)
		return
	}
	if contentType == "" {
		contentType = "image/png"
	}
	// Immutable: a plate's preview is generated once from its sliced file, so
	// re-fetching it on every poll of the queue board is pure waste.
	c.Header("Cache-Control", "private, max-age=86400, immutable")
	c.Data(http.StatusOK, contentType, body)
}

func (s *Server) registerPrintingQueue(r *gin.Engine) {
	g := r.Group("/printing/queue")
	g.Use(s.guards.RequireUser())
	// machine:read, not batch:read - this is the state of the printers, and
	// whoever may look at the fleet may look at what is queued on it.
	g.GET("", s.guards.RequirePermission(auth.MachineRead.Key()), s.listPrintingQueue)
	g.GET("/thumbnail/:kind/:id",
		s.guards.RequirePermission(auth.MachineRead.Key()), s.printingQueueThumbnail)
}
