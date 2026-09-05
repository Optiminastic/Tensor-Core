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
}

func (s *Server) printBatch(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}

	resp, err := s.SendBatchToPrinter(ctx, batch)
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
