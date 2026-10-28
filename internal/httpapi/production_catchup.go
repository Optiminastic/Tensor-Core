package httpapi

// Catching the board up with goods that already shipped.
//
// WHAT THIS IS FOR. The four station tabs - Assembly, Finishing, Quality
// Check, Packaging - show work waiting to be done. When an order is packed and
// posted without anybody clicking through those stations, the job sits on the
// board for ever and the floor reads a queue of work that was finished weeks
// ago. This moves those jobs to where reality already is.
//
// IT DOES NOT PRETEND ANYBODY INSPECTED ANYTHING. A real station click writes
// a check row - assembly_checks, qc_checks - carrying what the inspector
// actually saw: parts combined, fit check, notes. This writes none of those.
// Inventing them would be a fabricated inspection record on goods that have
// already reached customers, which is a different and much worse thing than a
// stale board.
//
// Every job gets a job event naming the person who ran it and saying plainly
// that it was a catch-up, so the trail distinguishes this from somebody doing
// the work.
//
// ADMIN ONLY, and deliberately not given to the station roles. Packaging-QC
// hold qc:submit so they can record a check they performed; the ability to
// declare a whole board finished without performing anything is a different
// power, and it belongs with whoever answers for the shop.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// catchUpReasonMax bounds the note. It is written to every job's event, so it
// is a sentence explaining the sweep, not a report.
const catchUpReasonMax = 300

func (s *Server) registerProductionCatchUp(r *gin.Engine) {
	g := r.Group("/brands/:slug/production/catch-up")
	g.Use(s.guards.RequireUser(), s.guards.RequirePermission(auth.ProductionCatchUp.Key()))
	// GET first, so the screen can say HOW MANY before anybody commits to it.
	// A button that reports "done" over an unknown number is how a sweep gets
	// run twice.
	g.GET("", s.previewProductionCatchUp)
	g.POST("", s.runProductionCatchUp)
}

type catchUpJobResponse struct {
	JobNumber string `json:"job_number"`
	// Waiting names the station the job is currently stuck at, so the preview
	// reads as the board does rather than as a list of ids.
	Waiting string `json:"waiting"`
}

type catchUpPreviewResponse struct {
	Total int                  `json:"total"`
	Jobs  []catchUpJobResponse `json:"jobs"`
}

func (s *Server) previewProductionCatchUp(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	rows, err := s.store.Q.ListJobsAwaitingStations(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the station queues.")
		return
	}

	out := catchUpPreviewResponse{Total: len(rows), Jobs: make([]catchUpJobResponse, 0, len(rows))}
	for _, r := range rows {
		out.Jobs = append(out.Jobs, catchUpJobResponse{
			JobNumber: r.JobNumber, Waiting: waitingStation(r),
		})
	}
	c.JSON(http.StatusOK, out)
}

type catchUpRequest struct {
	// Reason is required. A sweep that declares a board finished should say
	// why in the record it leaves, and "shipped before Tensor tracked it" is
	// a different thing from "clearing test data".
	Reason string `json:"reason" binding:"required"`
	// Confirm must be the exact number the preview reported.
	//
	// NOT A BOOLEAN. A yes/no cannot tell the difference between agreeing to
	// sweep four jobs and agreeing to sweep four hundred, and the number on
	// screen is the only thing that makes the decision informed.
	Confirm int `json:"confirm"`
}

type catchUpResultResponse struct {
	Advanced int `json:"advanced"`
	Failed   int `json:"failed"`
	// Dispatched is how many ORDERS were recorded as shipped. Lower than
	// Advanced, because an order usually has several jobs on it.
	Dispatched int      `json:"dispatched"`
	Jobs       []string `json:"jobs"`
}

func (s *Server) runProductionCatchUp(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	var req catchUpRequest
	if !bindJSON(c, &req) {
		return
	}
	reason := trimTo(req.Reason, catchUpReasonMax)
	if reason == "" {
		detail(c, http.StatusUnprocessableEntity,
			"Say why these are being marked done. It goes on every job's record.")
		return
	}

	ctx := c.Request.Context()
	log := obs.FromContext(ctx)

	rows, err := s.store.Q.ListJobsAwaitingStations(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the station queues.")
		return
	}
	if len(rows) == 0 {
		c.JSON(http.StatusOK, catchUpResultResponse{Jobs: []string{}})
		return
	}
	// RE-READ AND RE-CHECKED. The count came from a separate request, and the
	// floor keeps working between the two - so a sweep that was agreed for
	// four jobs must not quietly take six.
	if req.Confirm != len(rows) {
		detail(c, http.StatusConflict,
			"The queues changed while you were looking: there are now "+
				itoaSafe(len(rows))+" jobs waiting, not "+itoaSafe(req.Confirm)+
				". Check the list again before running this.")
		return
	}

	actor := currentUserID(c)
	out := catchUpResultResponse{Jobs: make([]string, 0, len(rows))}
	// One dispatch record per ORDER, not per job: an order of three planks
	// went out in one parcel, and three shipment records would overstate what
	// the shop posted.
	dispatched := map[uuid.UUID]bool{}

	for _, row := range rows {
		// ONE JOB PER TRANSACTION, not one for the whole sweep. A failure
		// halfway through a single transaction would roll back jobs that were
		// already correct; here it costs one job and the rest still land.
		comment := "Stations marked done without inspection (bulk catch-up): " + reason
		err := s.store.InTx(ctx, func(q *gen.Queries) error {
			if _, err := q.CatchUpJobStations(ctx, row.ID); err != nil {
				return err
			}
			return recordJobEvent(ctx, q, jobEvent{
				JobID:     row.ID,
				EventType: production.EventPackagingPacked,
				Stage:     production.StagePackaging,
				// SAYS WHAT IT WAS. Anybody reading this job's history later
				// must be able to tell a bulk catch-up from somebody doing
				// the work, or the record is worse than useless.
				Comment: &comment,
				ActorID: actor,
				BatchID: row.BatchID,
				Metadata: map[string]any{
					"catch_up":      true,
					"inspected":     false,
					"was_assembly":  row.AssemblyStatus,
					"was_finishing": row.FinishingStatus,
					"was_qc":        row.QcStatus,
					"was_packaging": row.PackagingStatus,
				},
			})
		})
		if err != nil {
			out.Failed++
			log.Error("could not catch up a job", "job", row.JobNumber, "error", err)
			continue
		}
		out.Advanced++
		out.Jobs = append(out.Jobs, row.JobNumber)

		// THE LAST STEP, and the one that takes the job off the board: a job
		// that is merely packaged still sits on the Dispatch tab waiting to
		// be sent. These were sent weeks ago.
		if row.OrderID == nil || dispatched[*row.OrderID] {
			continue
		}
		dispatched[*row.OrderID] = true
		n, dErr := s.store.Q.InsertCatchUpDispatch(ctx, gen.InsertCatchUpDispatchParams{
			ID: uuid.New(), OrderID: *row.OrderID,
		})
		if dErr != nil {
			// The stations moved; only the shipment record did not. Worth a
			// line, not worth failing the job that already advanced.
			log.Error("could not record the catch-up dispatch",
				"job", row.JobNumber, "error", dErr)
			continue
		}
		if n > 0 {
			out.Dispatched++
		}
	}

	// LOUD, because this is the one action that declares work finished that
	// nobody performed, and the only other trace is one event per job.
	log.Warn("production stations marked done in bulk without inspection",
		"brand", brand, "advanced", out.Advanced, "dispatched", out.Dispatched,
		"failed", out.Failed, "by", actor, "reason", reason)

	c.JSON(http.StatusOK, out)
}

// trimTo bounds free text that is written to every job's record.
func trimTo(v string, max int) string {
	v = strings.TrimSpace(v)
	if len(v) > max {
		return v[:max]
	}
	return v
}

// itoaSafe renders a count for a sentence shown to an operator.
func itoaSafe(v int) string { return strconv.Itoa(v) }

// waitingStation names the first station a job is stuck at, in the order the
// board runs them.
func waitingStation(r gen.ListJobsAwaitingStationsRow) string {
	switch {
	case r.AssemblyStatus == production.AssemblyPending:
		return "Assembly"
	case r.FinishingStatus == production.FinishingPending:
		return "Finishing"
	case r.QcStatus == production.QcPending:
		return "Quality Check"
	case r.PackagingStatus == production.PackagingPending:
		return "Packaging"
	}
	return "Dispatch"
}
