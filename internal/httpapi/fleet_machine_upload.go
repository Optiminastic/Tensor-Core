package httpapi

// POST /machine-fleet/:id/upload - send a model file to BambuBuddy's library
// and add it to its print queue.
//
// Tensor does not store the file. It streams straight through to BambuBuddy,
// which already owns file storage, thumbnailing and the queue an operator
// releases from. Keeping a second copy here would mean two libraries that drift
// apart and two answers to "what is queued".
//
// It queues, it does not print. Releasing a queued file to the bed stays a
// deliberate act in BambuBuddy, so nothing on this path can start a print.

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxPrinterUploadBytes bounds one printer upload.
//
// Much larger than designs_create.go's own ceiling, deliberately: that one caps
// a single part's STL, while this carries a whole print plate - the sample
// library's squid plate alone is 80MB. Generous, but not unbounded: without a
// ceiling one request can exhaust the process.
const maxPrinterUploadBytes = 512 << 20 // 512 MiB

// uploadableExt is what BambuBuddy can actually do something with. Rejecting
// anything else here gives the operator an immediate, specific error instead of
// a confusing failure several seconds later, after the whole file has crossed
// the network.
// .gcode is deliberately absent: BambuBuddy rejects raw gcode outright ("they
// need a .gcode.3mf zip container"), so accepting it here would only send the
// operator a large file across the network to be refused at the far end.
var uploadableExt = map[string]bool{
	".3mf": true, ".stl": true, ".gcode.3mf": true, ".obj": true, ".step": true,
}

type uploadToPrinterResponse struct {
	FileID   int    `json:"file_id"`
	Filename string `json:"filename"`
	Queued   bool   `json:"queued"`
	// Duplicate is true when BambuBuddy recognised identical content already in
	// its library and reused it. Not an error - but the operator should know
	// why the library did not grow.
	Duplicate bool `json:"duplicate"`
	// QueueNote is BambuBuddy's own reason when it declined to queue the file,
	// passed through unchanged. Empty when the file queued.
	QueueNote string `json:"queue_note"`
}

func (s *Server) uploadToPrinter(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}
	ctx := c.Request.Context()

	// The machine must exist even though the upload is fleet-wide: the route is
	// addressed per machine, and accepting a file for a machine that does not
	// exist would silently succeed.
	machine, err := s.store.Q.GetFleetMachine(ctx, id)
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "That machine does not exist.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not load the machine.")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPrinterUploadBytes)
	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach a model file in the 'file' field.")
		return
	}
	if header.Size <= 0 {
		detail(c, http.StatusUnprocessableEntity, "That file is empty.")
		return
	}
	if !uploadableFilename(header.Filename) {
		detail(c, http.StatusUnprocessableEntity,
			"That file type cannot be sent to a printer. Upload a .3mf, .gcode.3mf, .stl, .obj or .step file.")
		return
	}

	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return
	}
	defer func() { _ = src.Close() }()

	uploaded, err := s.bambu.UploadFile(ctx, header.Filename, src)
	if err != nil {
		obs.FromContext(ctx).Error("bambubuddy upload failed", "file", header.Filename, "error", err)
		// A rejection BambuBuddy explained is shown verbatim - it tells the
		// operator what to do differently. An internal fault is not: its text
		// means nothing to them, and a raw Go decode error reached this UI once
		// already.
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			detail(c, http.StatusUnprocessableEntity, reason.Reason)
			return
		}
		detail(c, http.StatusBadGateway, "Could not send the file to BambuBuddy.")
		return
	}

	// Queued with the settings an operator would otherwise set by hand in
	// BambuBuddy's "Edit Queue Item" dialog:
	//
	//   Any <model>  - any idle printer of this model takes it, rather than
	//                  pinning the plate to one machine that may be busy for
	//                  hours while an identical one sits free. Same reasoning
	//                  as Tensor's own earliest-completion machine assignment.
	//   ASAP         - top of the queue, starting as soon as one is idle.
	//   Only start if previous print succeeded - a failed first layer or a
	//                  jammed nozzle must not quietly consume the rest of the
	//                  queue behind it.
	//   Power off when done - OFF. Powering down a shared shop printer because
	//                  one plate finished is not a decision this path should
	//                  make.
	//
	// BambuBuddy's dispatcher then decides when it actually runs; see
	// QueueForPrinting for why that decision is not made here.
	//
	// A failure to queue is NOT reported as a failed upload: the file is in the
	// library either way, and saying otherwise would have the operator send it
	// again.
	queued := true
	queueNote := ""
	opts := bambubuddy.QueueOptions{
		TargetModel:            s.printerModelFor(ctx, machine),
		InsertAtTop:            true,
		RequirePreviousSuccess: true,
		AutoOffAfter:           false,
	}
	if opts.TargetModel == "" {
		// No profile family recorded, so "any of this model" has no meaning.
		// Fall back to pinning this specific printer rather than queueing an
		// untargeted item, which would never dispatch at all.
		printerID, err := s.bambuPrinterIDFor(ctx, machine.MachineID)
		if err != nil || printerID < 0 {
			queued, queueNote = false, "this printer could not be resolved in BambuBuddy"
		} else {
			opts.PrinterID = printerID
		}
	}

	if queued {
		item, qErr := s.bambu.QueueForPrinting(ctx, uploaded.ID, opts)
		if qErr != nil {
			obs.FromContext(ctx).Warn("bambubuddy did not queue the file",
				"file", uploaded.Filename, "reason", qErr)
			queued = false
			var reason bambubuddy.ReasonError
			if errors.As(qErr, &reason) {
				queueNote = reason.Reason
			} else {
				queueNote = "it could not be added to the print queue"
			}
		} else {
			// The dispatcher's own words when it is holding the item, so the
			// operator sees "waiting for the current print" rather than a bare
			// "queued" that hides why nothing is happening.
			queueNote = item.WaitingReason
		}
	}

	c.JSON(http.StatusOK, uploadToPrinterResponse{
		FileID: uploaded.ID, Filename: uploaded.Filename, Queued: queued,
		Duplicate: uploaded.DuplicateOf != nil, QueueNote: queueNote,
	})
}

// uploadableFilename checks the extension, handling the two-part ".gcode.3mf"
// that filepath.Ext alone would read as just ".3mf".
func uploadableFilename(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for ext := range uploadableExt {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return uploadableExt[filepath.Ext(lower)]
}

func (s *Server) registerFleetMachineUpload(g *gin.RouterGroup) {
	// machine:manage, not machine:read - this puts work on a physical printer's
	// queue, which is an operational change however read-only the file itself is.
	g.POST("/:id/upload", s.guards.RequirePermission(auth.MachineManage.Key()), s.uploadToPrinter)
}

// printerModelFor is the printer model a machine's plates should target, e.g.
// "H2C" - taken from the slicing profile the fleet unit is linked to.
//
// Empty when the unit has no profile, which the caller treats as "cannot route
// by model" rather than guessing one: a plate sent to the wrong model is a
// failed print at best.
func (s *Server) printerModelFor(ctx context.Context, machine gen.Machine) string {
	if machine.MachineProfileID == nil {
		return ""
	}
	profile, err := s.store.Q.GetMachineProfileFull(ctx, *machine.MachineProfileID)
	if err != nil {
		return ""
	}
	return profile.Family
}
