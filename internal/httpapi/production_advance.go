package httpapi

// Moving a job - or a selection of them - on by one station.
//
// WHAT THIS IS AND IS NOT. The station dialogs record an INSPECTION: parts
// combined, fit checked, what the packer put in the box. This records only
// that the step is done. It exists because most of this shop's work does not
// need a checklist per job - somebody at the bench finishes a tray of planks
// and wants to move the tray on - and forcing a four-field dialog on each one
// means the dialogs stop being filled in honestly, which is worse than not
// having them.
//
// So both paths stay. The dialog is there when there is something worth
// recording; this is there when there is not. The job event says which was
// used, so a job's history never implies a check that nobody performed.
//
// ONE STATION AT A TIME, in order. The arrow moves Assembly to Finishing, not
// Assembly to Dispatch: the underlying queries each refuse unless the station
// before them is done, so a job cannot be skipped past a stage by pressing
// harder.

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxAdvanceBatch bounds one request.
//
// Generous, because "select all" on a tray of work is the point of this - but
// bounded, because an unbounded list is one request that holds a connection
// for minutes and reports nothing until it finishes.
const maxAdvanceBatch = 500

func (s *Server) registerProductionAdvance(r *gin.Engine) {
	g := r.Group("/brands/:slug/production/advance")
	g.Use(s.guards.RequireUser())
	// NO PERMISSION ON THE GROUP. Each station has its own - assembly:submit,
	// finishing:submit, qc:submit, packaging:submit - and which one applies
	// depends on the body, so the check is in the handler where the station
	// is known.
	g.POST("", s.advanceJobs)
}

type advanceRequest struct {
	// Station is the step being completed: assembly, finishing, qc or
	// packaging. Dispatch is not here - that is a shipment, not a station.
	Station string      `json:"station" binding:"required"`
	JobIDs  []uuid.UUID `json:"job_ids" binding:"required"`
}

type advanceResultResponse struct {
	Moved int `json:"moved"`
	// Skipped are jobs that were not waiting at this station any more -
	// somebody else moved them while this selection was open. Not an error:
	// the outcome the presser wanted is already true.
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// Detail is the first real failure's own words, so the screen can say
	// what went wrong rather than only how many.
	Detail string `json:"detail,omitempty"`
}

// advanceStation describes one step: who may do it, what it writes, and what
// the event is called.
type advanceStation struct {
	Permission auth.PermissionSpec
	Event      string
	Stage      string
	Apply      func(ctx *gin.Context, q *gen.Queries, id uuid.UUID) (gen.ProductionJob, error)
}

func advanceStations() map[string]advanceStation {
	return map[string]advanceStation{
		"assembly": {
			Permission: auth.AssemblySubmit,
			Event:      production.EventAssemblyCompleted,
			Stage:      production.StageAssembly,
			Apply: func(c *gin.Context, q *gen.Queries, id uuid.UUID) (gen.ProductionJob, error) {
				return q.AdvanceJobAssembly(c.Request.Context(), gen.AdvanceJobAssemblyParams{
					ID: id, AssemblyStatus: production.AssemblyCompleted,
				})
			},
		},
		"finishing": {
			Permission: auth.FinishingSubmit,
			Event:      production.EventFinishingCompleted,
			Stage:      production.StageFinishing,
			Apply: func(c *gin.Context, q *gen.Queries, id uuid.UUID) (gen.ProductionJob, error) {
				return q.AdvanceJobFinishing(c.Request.Context(), gen.AdvanceJobFinishingParams{
					ID: id, FinishingStatus: production.FinishingCompleted,
				})
			},
		},
		"qc": {
			Permission: auth.QcSubmit,
			Event:      production.EventQcPassed,
			Stage:      production.StageQc,
			Apply: func(c *gin.Context, q *gen.Queries, id uuid.UUID) (gen.ProductionJob, error) {
				return q.AdvanceJobQc(c.Request.Context(), gen.AdvanceJobQcParams{
					ID: id, QcStatus: production.QcPassed,
				})
			},
		},
		"packaging": {
			Permission: auth.PackagingSubmit,
			Event:      production.EventPackagingPacked,
			Stage:      production.StagePackaging,
			Apply: func(c *gin.Context, q *gen.Queries, id uuid.UUID) (gen.ProductionJob, error) {
				return q.AdvanceJobPackaging(c.Request.Context(), gen.AdvanceJobPackagingParams{
					ID: id, PackagingStatus: production.PackagingPackaged,
				})
			},
		},
	}
}

func (s *Server) advanceJobs(c *gin.Context) {
	brand := c.Param("slug")
	if !s.brandMustExist(c, brand) {
		return
	}
	var req advanceRequest
	if !bindJSON(c, &req) {
		return
	}

	station, known := advanceStations()[req.Station]
	if !known {
		// Dispatch is named rather than lumped in with a typo: somebody will
		// try it, and "that is not a station" is unhelpful when the tab
		// exists on the same screen.
		if req.Station == "dispatch" {
			detail(c, http.StatusUnprocessableEntity,
				"Dispatch is a shipment, not a station. Send the order from the Dispatch tab.")
			return
		}
		detail(c, http.StatusUnprocessableEntity, "That is not a station.")
		return
	}
	if !s.guards.HasPermission(c, station.Permission.Key()) {
		detail(c, http.StatusForbidden,
			"You do not have permission to complete the "+req.Station+" step.")
		return
	}

	if len(req.JobIDs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "Pick at least one job.")
		return
	}
	if len(req.JobIDs) > maxAdvanceBatch {
		detail(c, http.StatusUnprocessableEntity,
			"That is more than this can move at once. Narrow the selection and go again.")
		return
	}

	ctx := c.Request.Context()
	log := obs.FromContext(ctx)
	actor := currentUserID(c)
	var out advanceResultResponse

	for _, id := range req.JobIDs {
		// ONE TRANSACTION PER JOB. A failure partway through a single
		// transaction would roll back jobs that had already moved, and the
		// operator would be looking at a tray they know they finished.
		err := s.store.InTx(ctx, func(q *gen.Queries) error {
			updated, err := station.Apply(c, q, id)
			if err != nil {
				return err
			}
			return recordJobEvent(ctx, q, jobEvent{
				JobID: id, EventType: station.Event, Stage: station.Stage,
				ActorID: actor, BatchID: updated.BatchID,
				// SAYS NO CHECK WAS RECORDED. The dialog path writes an
				// assembly_checks or qc_checks row; this one does not, and a
				// history that did not distinguish them would imply an
				// inspection nobody performed.
				Metadata: map[string]any{"via": "station_arrow", "checklist": false},
			})
		})
		switch {
		case err == nil:
			out.Moved++
		case errors.Is(err, pgx.ErrNoRows):
			// The query's own guard refused: this job is not waiting at this
			// station. Somebody else moved it, or it never belonged in the
			// selection. Counted, not failed.
			out.Skipped++
		default:
			out.Failed++
			if out.Detail == "" {
				out.Detail = err.Error()
			}
			log.Error("could not advance a job", "job", id, "station", req.Station, "error", err)
		}
	}

	log.Info("jobs advanced a station",
		"brand", brand, "station", req.Station, "moved", out.Moved,
		"skipped", out.Skipped, "failed", out.Failed, "by", actor)

	c.JSON(http.StatusOK, out)
}
