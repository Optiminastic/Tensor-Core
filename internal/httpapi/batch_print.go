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
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
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
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}
	ctx := c.Request.Context()
	log := obs.FromContext(ctx)

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}

	// Locked only. A Draft bed is still being planned - the next planner pass
	// can dissolve it and rebuild it from different jobs - so sending one to a
	// printer would commit filament to a layout that no longer exists by the
	// time it prints. Printing and Completed are already past this point.
	if batch.Status != production.BatchOpen {
		detail(c, http.StatusConflict, fmt.Sprintf(
			"Only a locked batch can be sent to a printer; this one is %s.", batch.Status))
		return
	}

	// The plate must have been sliced. While plate_sliced_at is NULL the batch
	// is running on batchTimeFromJobs' estimate and no .gcode.3mf exists, so
	// there is simply nothing to send. Say which of the two it is rather than
	// reporting a bare missing file.
	if !batch.PlateSlicedAt.Valid {
		if batch.PlateSliceError != nil && strings.TrimSpace(*batch.PlateSliceError) != "" {
			detail(c, http.StatusConflict, fmt.Sprintf(
				"This batch's plate could not be sliced: %s", *batch.PlateSliceError))
			return
		}
		detail(c, http.StatusConflict,
			"This batch's plate has not been sliced yet, so there is no print file to send.")
		return
	}

	key := slicing.PlateGcodeKey(batch.ID)
	obj, err := s.storage.Get(ctx, key)
	if err != nil {
		// Sliced, but the file is not in storage: a batch measured before the
		// slice worker started keeping the artifact. Recoverable by re-slicing
		// the plate, so say so instead of failing blankly.
		log.Warn("sliced plate is missing from storage", "batch", batch.ID, "key", key, "error", err)
		detail(c, http.StatusConflict,
			"This batch was measured before print files were kept. Re-slice the plate to produce one.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	// Named for the batch so an operator scanning BambuBuddy's library or queue
	// can tell at a glance which bed an item is, without cross-referencing ids.
	filename := fmt.Sprintf("%s.gcode.3mf", batch.BatchNumber)

	uploaded, err := s.bambu.UploadFile(ctx, filename, obj.Body)
	if err != nil {
		log.Error("could not send the plate to BambuBuddy", "batch", batch.ID, "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusUnprocessableEntity, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not send the print file to BambuBuddy.")
		return
	}

	// BambuBuddy dedupes its library by content, so a plate it already holds
	// comes back as a duplicate. That is the guard against a double-press
	// queueing the same bed twice - a batch is one physical print, and a second
	// queue item for it would print the whole bed again in plastic.
	if uploaded.DuplicateOf != nil {
		c.JSON(http.StatusOK, printBatchResponse{
			Filename: uploaded.Filename, FileID: uploaded.ID, Queued: false, AlreadySent: true,
			Note: "this plate is already in BambuBuddy's library, so it was not queued again",
		})
		return
	}

	item, err := s.bambu.QueueForPrinting(ctx, uploaded.ID, bambubuddy.QueueOptions{
		TargetModel:            s.batchPrinterModel(ctx, batch),
		InsertAtTop:            true,
		RequirePreviousSuccess: true,
		AutoOffAfter:           false,
	})
	if err != nil {
		// The file reached the library either way. Reporting this as a failed
		// send would have the operator send it again, which the duplicate check
		// would then refuse - a confusing dead end.
		log.Warn("plate uploaded but not queued", "batch", batch.ID, "error", err)
		note := "it could not be added to the print queue"
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			note = reason.Reason
		}
		c.JSON(http.StatusOK, printBatchResponse{
			Filename: uploaded.Filename, FileID: uploaded.ID, Queued: false, Note: note,
		})
		return
	}

	log.Info("batch sent to BambuBuddy", "batch", batch.ID, "file", uploaded.Filename, "queue_item", item.ID)
	c.JSON(http.StatusOK, printBatchResponse{
		Filename: uploaded.Filename, FileID: uploaded.ID, Queued: true, Note: item.WaitingReason,
	})
}

// batchPrinterModel is the printer model this bed should run on, e.g. "H2C",
// taken from the slicing profile the batch was planned against.
//
// Empty when the batch has no profile, which QueueForPrinting treats as "any
// printer" rather than guessing a model - a plate sent to the wrong model is a
// failed print. batches.machine_id references machine_profiles, not the fleet
// units table, so this reads the profile directly.
func (s *Server) batchPrinterModel(ctx context.Context, batch gen.Batch) string {
	if batch.MachineID == nil {
		return ""
	}
	profile, err := s.store.Q.GetMachineProfileFull(ctx, *batch.MachineID)
	if err != nil {
		return ""
	}
	return profile.Family
}

func (s *Server) registerBatchPrint(g *gin.RouterGroup) {
	// machine:manage, matching the fleet upload route: this puts work on a
	// physical printer's queue, which is an operational act on a machine rather
	// than an edit to the batch record.
	g.POST("/:id/print", s.guards.RequirePermission(auth.MachineManage.Key()), s.printBatch)
}
