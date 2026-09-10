package httpapi

// POST /batches/:id/print - send a locked batch's sliced plate to BambuBuddy
// and queue it for printing.
//
// This is the last hop of the pipeline. Tensor decided which jobs share a bed,
// packed them (bedpack), merged them into one plate (meshio) and sliced that
// plate (BatchSliceWorker). All that is left is to hand the resulting
// .gcode.3mf to the machine host.
//
// The file is NOT sliced here. It was sliced once, when the batch was measured,
// and that same artifact is what gets sent - so the time and filament figures
// shown against the batch describe the exact file that prints. Re-slicing at
// send time would produce a second file that nobody costed.

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type printBatchResponse struct {
	Filename string `json:"filename"`
	FileID   int    `json:"file_id"`
	Queued   bool   `json:"queued"`
	// AlreadySent is true when BambuBuddy recognised this exact plate as
	// already in its library. Reported instead of queueing a second copy - see
	// the duplicate check below.
	AlreadySent bool `json:"already_sent"`
	// Note carries BambuBuddy's own words about why an item is waiting, or why
	// it declined to queue. Empty when it queued and started.
	Note string `json:"note"`
	// Locked is true when THIS call locked a Draft on the way to sending it.
	// The half an operator cannot undo by pressing the button again, so it is
	// reported separately from Queued rather than folded into the note.
	Locked bool `json:"locked"`
}

func (s *Server) printBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	// Locking a Draft is a batch edit, and this route is guarded on
	// machine:manage. Letting that permission alone freeze a bed and reserve
	// its filament would quietly widen a role, which internal/auth's catalog
	// tests treat as spec - so the extra half needs the extra permission.
	// Checked here rather than in middleware because it depends on the batch's
	// status, which the router does not know.
	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	if batch.Status == production.BatchPendingApproval &&
		!s.guards.HasPermission(c, auth.BatchManage.Key()) {
		detail(c, http.StatusForbidden,
			"Locking this draft needs the batch:manage permission.")
		return
	}
	// Required because this path can now build a merged plate, which needs
	// object storage - the same reason approveBatch calls it.
	if !s.filesReady(c) {
		return
	}

	resp, err := s.QueueBatchForPrinting(ctx, id, currentUserID(c))
	if err != nil {
		writeStatusError(c, err, "Could not send the batch to a printer.")
		return
	}
	c.JSON(http.StatusOK, resp)
}

// recordPrintError stores why a batch could not be sent, best-effort.
//
// Best-effort on purpose: the operator is already being told what went wrong by
// the response, and failing THIS write must not turn a clear "BambuBuddy
// rejected the file" into an opaque 500. The record exists for everyone who
// looks at the batch later.
func (s *Server) recordPrintError(ctx context.Context, batchID uuid.UUID, reason string) {
	if err := s.store.Q.SetBatchPrintError(ctx, gen.SetBatchPrintErrorParams{
		ID: batchID, PrintError: &reason,
	}); err != nil {
		obs.FromContext(ctx).Warn("could not record the batch's print error",
			"batch", batchID, "error", err)
	}
}

// int32Ptr adapts BambuBuddy's int queue id to the nullable int32 column.
func int32Ptr(v int) *int32 {
	n := int32(v)
	return &n
}

func (s *Server) registerBatchPrint(g *gin.RouterGroup) {
	// machine:manage, matching the fleet upload route: this puts work on a
	// physical printer's queue, which is an operational act on a machine rather
	// than an edit to the batch record.
	g.POST("/:id/print", s.guards.RequirePermission(auth.MachineManage.Key()), s.printBatch)
}
