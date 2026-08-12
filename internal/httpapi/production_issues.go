package httpapi

// Operator-reported issues at assembly, finishing and QC.
//
// An issue is an EVENT, not a status. Three things follow from that, and all
// three are deliberate:
//
//   - The job's sub-status is untouched, so the row stays in its station queue
//     and stays visible. The spec is explicit: "do not simply remove or hide the
//     product". Moving it to an "issue" status would drop it out of the queue,
//     which is exactly hiding it.
//   - Raising an issue does not block progress. An operator can flag a small
//     defect, fix it, and still press DONE - which is what actually happens on a
//     bench.
//   - It is not production_jobs.issue_reason. That column is the pre-print
//     validation taxonomy, and its presence excludes a job from batching
//     (ListBatchableJobs carries AND issue_reason IS NULL), so reusing it would
//     silently strand any reprint.

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type stationIssueRequest struct {
	Stage   string  `json:"stage" binding:"required"`
	Reason  string  `json:"reason" binding:"required"`
	Comment *string `json:"comment"`
	// RequestReprint queues a reprint in the same transaction. Offered at QC,
	// where a defect usually means the part has to be made again.
	RequestReprint bool `json:"request_reprint"`
	// Quantity for that reprint; defaults to 1. One damaged part out of five is
	// a reprint of one, not five.
	Quantity *int32 `json:"quantity"`
}

type stationIssueResponse struct {
	Job        productionJobResponse  `json:"job"`
	ReprintJob *productionJobResponse `json:"reprint_job"`
}

func (s *Server) registerJobIssues(r *gin.Engine) {
	g := r.Group("/production-jobs")
	g.Use(s.guards.RequireUser())
	// Guarded per stage inside the handler rather than by the router: the
	// permission depends on the stage in the body, and whoever may pass a
	// station may flag it.
	g.POST("/:id/issues", s.reportStationIssue)
}

// issuePermission is the permission required to raise an issue at a stage -
// the same one required to complete that station.
func issuePermission(stage string) (string, bool) {
	switch stage {
	case production.StageAssembly:
		return auth.AssemblySubmit.Key(), true
	case production.StageFinishing:
		return auth.FinishingSubmit.Key(), true
	case production.StageQc:
		return auth.QcSubmit.Key(), true
	default:
		return "", false
	}
}

func (s *Server) reportStationIssue(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req stationIssueRequest
	if !bindJSON(c, &req) {
		return
	}
	if !production.ValidIssueStage(req.Stage) {
		detail(c, http.StatusUnprocessableEntity,
			"An issue can only be raised at assembly, finishing or QC.")
		return
	}
	if !production.ValidStationIssueReason(req.Reason) {
		detail(c, http.StatusUnprocessableEntity, "That issue reason is not valid.")
		return
	}
	permission, ok := issuePermission(req.Stage)
	if !ok {
		detail(c, http.StatusUnprocessableEntity, "That stage does not accept issues.")
		return
	}
	if !s.guards.HasPermission(c, permission) {
		return
	}

	ctx := c.Request.Context()
	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}

	quantity := int32(1)
	if req.Quantity != nil {
		quantity = *req.Quantity
	}
	if req.RequestReprint && (quantity < 1 || quantity > job.Quantity) {
		detail(c, http.StatusUnprocessableEntity,
			"The reprint quantity must be between 1 and the job's quantity.")
		return
	}

	actor := currentUserID(c)
	var reprint *gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if err := recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.IssueEventType(req.Stage), Stage: req.Stage,
			Reason: req.Reason, Comment: req.Comment, ActorID: actor, BatchID: job.BatchID,
		}); err != nil {
			return err
		}
		if !req.RequestReprint {
			return nil
		}
		if err := recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventReprintRequested, Stage: req.Stage,
			Reason: req.Reason, Comment: req.Comment, ActorID: actor,
			Metadata: map[string]any{"quantity": quantity},
		}); err != nil {
			return err
		}
		number, err := q.NextJobNumber(ctx)
		if err != nil {
			return err
		}
		r, err := q.InsertProductionJob(ctx, reprintParamsFor(job, quantity, number))
		if err != nil {
			return err
		}
		reprint = &r
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventReprintCreated, Stage: req.Stage,
			Reason: req.Reason, ActorID: actor, RelatedJobID: &r.ID,
			Metadata: map[string]any{"quantity": quantity, "job_number": r.JobNumber},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		detail(c, http.StatusConflict, "That production job does not exist.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the issue.")
		return
	}

	// A reprint is a new queued job, so the planner should see it promptly
	// rather than waiting for the periodic tick.
	if reprint != nil {
		s.triggerBatchPlan(ctx)
	}

	updated, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not reload the production job.")
		return
	}
	resp := stationIssueResponse{Job: s.singleJobDTO(ctx, updated)}
	if reprint != nil {
		dto := s.singleJobDTO(ctx, *reprint)
		resp.ReprintJob = &dto
	}
	c.JSON(http.StatusCreated, resp)
}
