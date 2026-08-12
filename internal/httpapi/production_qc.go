package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The station handlers (assembly, finishing, QC, packaging) move a completed job
// through the post-print stages. Each records an audit row and advances the
// matching sub-status.
//
// The prior-stage gate lives in the UPDATE's WHERE clause (AdvanceJobX in
// stations.sql), not in a pre-read: check-and-act is one statement inside the
// transaction, so a double-click or two operators racing produce exactly one
// check row and one transition - the loser matches no row and gets a 409. The
// pre-read that remains exists only to 404 a missing job and to resolve an
// optional photo before the transaction opens.

// stationConflict maps a guarded transition that matched no row onto the 409 an
// operator should see. The job exists (the handler pre-read it) - it has simply
// already left this station, or never reached it.
func stationConflict(c *gin.Context, message string) {
	detail(c, http.StatusConflict, message)
}

type assemblyRequest struct {
	PartsCombined    bool    `json:"parts_combined"`
	HardwareAttached bool    `json:"hardware_attached"`
	AddonsAttached   bool    `json:"addons_attached"`
	FitCheckOk       bool    `json:"fit_check_ok"`
	PhotoFileID      *string `json:"photo_file_id"`
	Notes            *string `json:"notes"`
}

func (s *Server) submitAssembly(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req assemblyRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	photoID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	var updated gen.ProductionJob
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.AdvanceJobAssembly(ctx, gen.AdvanceJobAssemblyParams{
			ID: id, AssemblyStatus: production.AssemblyCompleted,
		})
		if err != nil {
			return err
		}
		check, err := q.InsertAssemblyCheck(ctx, gen.InsertAssemblyCheckParams{
			ID: uuid.New(), JobID: id, PartsCombined: req.PartsCombined,
			HardwareAttached: req.HardwareAttached, AddonsAttached: req.AddonsAttached,
			FitCheckOk: req.FitCheckOk, PhotoFileID: photoID, Notes: req.Notes,
			AssembledBy: currentUserID(c),
		})
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventAssemblyCompleted,
			Stage: production.StageAssembly, Comment: req.Notes, ActorID: currentUserID(c),
			BatchID: updated.BatchID, Metadata: map[string]any{"check_id": check.ID},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stationConflict(c, "This job is not waiting on assembly - it has already been assembled or skipped.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the assembly.")
		return
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}

func (s *Server) skipAssembly(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	var updated gen.ProductionJob
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.AdvanceJobAssembly(ctx, gen.AdvanceJobAssemblyParams{
			ID: id, AssemblyStatus: production.AssemblyNotRequired,
		})
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventAssemblySkipped,
			Stage: production.StageAssembly, ActorID: currentUserID(c), BatchID: updated.BatchID,
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stationConflict(c, "This job is not waiting on assembly - it has already been assembled or skipped.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not skip assembly.")
		return
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}

type finishingRequest struct {
	SupportsRemoved bool    `json:"supports_removed"`
	Sanded          bool    `json:"sanded"`
	SeamsCleaned    bool    `json:"seams_cleaned"`
	SurfaceFinishOk bool    `json:"surface_finish_ok"`
	PhotoFileID     *string `json:"photo_file_id"`
	Notes           *string `json:"notes"`
}

// submitFinishing records the finishing pass. It gates on assembly the same way
// QC gates on finishing: a job whose parts aren't together yet has nothing to
// sand.
func (s *Server) submitFinishing(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req finishingRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	photoID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	var updated gen.ProductionJob
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.AdvanceJobFinishing(ctx, gen.AdvanceJobFinishingParams{
			ID: id, FinishingStatus: production.FinishingCompleted,
		})
		if err != nil {
			return err
		}
		check, err := q.InsertFinishingCheck(ctx, gen.InsertFinishingCheckParams{
			ID: uuid.New(), JobID: id, SupportsRemoved: req.SupportsRemoved, Sanded: req.Sanded,
			SeamsCleaned: req.SeamsCleaned, SurfaceFinishOk: req.SurfaceFinishOk,
			PhotoFileID: photoID, Notes: req.Notes, FinishedBy: currentUserID(c),
		})
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventFinishingCompleted,
			Stage: production.StageFinishing, Comment: req.Notes, ActorID: currentUserID(c),
			BatchID: updated.BatchID, Metadata: map[string]any{"check_id": check.ID},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stationConflict(c, "This job is not waiting on finishing - assembly is still pending, or finishing is already done.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the finishing.")
		return
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}

// skipFinishing marks finishing as not required for a part that needs no
// finishing - no form, just the decision, mirroring skipAssembly.
func (s *Server) skipFinishing(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	var updated gen.ProductionJob
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.AdvanceJobFinishing(ctx, gen.AdvanceJobFinishingParams{
			ID: id, FinishingStatus: production.FinishingNotRequired,
		})
		if err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventFinishingSkipped,
			Stage: production.StageFinishing, ActorID: currentUserID(c), BatchID: updated.BatchID,
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stationConflict(c, "This job is not waiting on finishing - assembly is still pending, or finishing is already done.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not skip finishing.")
		return
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}

type qcRequest struct {
	CorrectPersonalisation bool    `json:"correct_personalisation"`
	CorrectColour          bool    `json:"correct_colour"`
	SurfaceFinishOk        bool    `json:"surface_finish_ok"`
	NoCracks               bool    `json:"no_cracks"`
	NoLayerDefects         bool    `json:"no_layer_defects"`
	DimensionsOk           bool    `json:"dimensions_ok"`
	AssemblyFitOk          bool    `json:"assembly_fit_ok"`
	AddonsWorking          bool    `json:"addons_working"`
	PackagingSafe          bool    `json:"packaging_safe"`
	PhotoFileID            *string `json:"photo_file_id"`
	Decision               string  `json:"decision" binding:"required"`
	Notes                  *string `json:"notes"`
}

// qcFailureReason is the reason recorded on a production_job_failures row for a
// QC failure. The QC form is a checklist, not a reason picker, so there is no
// operator-chosen code to store - the specific defect goes in the notes, and
// the ISSUE flow (POST /production-jobs/:id/issues) is where a structured
// reason is captured.
const qcFailureReason = "other"

type qcResponse struct {
	Job        productionJobResponse  `json:"job"`
	ReprintJob *productionJobResponse `json:"reprint_job"`
}

func (s *Server) submitQc(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req qcRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Decision != "pass" && req.Decision != "fail" {
		detail(c, http.StatusUnprocessableEntity, "Decision must be 'pass' or 'fail'.")
		return
	}
	ctx := c.Request.Context()

	// A QC fail clones a reprint, which needs the source job's full slicing and
	// geometry snapshot - hence the loaded row rather than a bare existence check.
	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	photoID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	qcStatus := production.QcPassed
	if req.Decision == "fail" {
		qcStatus = production.QcFailed
	}

	var updated gen.ProductionJob
	var reprint *gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.AdvanceJobQc(ctx, gen.AdvanceJobQcParams{ID: id, QcStatus: qcStatus})
		if err != nil {
			return err
		}
		if _, err := q.InsertQcCheck(ctx, gen.InsertQcCheckParams{
			ID: uuid.New(), JobID: id, CorrectPersonalisation: req.CorrectPersonalisation,
			CorrectColour: req.CorrectColour, SurfaceFinishOk: req.SurfaceFinishOk, NoCracks: req.NoCracks,
			NoLayerDefects: req.NoLayerDefects, DimensionsOk: req.DimensionsOk, AssemblyFitOk: req.AssemblyFitOk,
			AddonsWorking: req.AddonsWorking, PackagingSafe: req.PackagingSafe, PhotoFileID: photoID,
			Decision: req.Decision, Notes: req.Notes, InspectedBy: currentUserID(c),
		}); err != nil {
			return err
		}
		event := production.EventQcPassed
		if req.Decision == "fail" {
			event = production.EventQcFailed
		}
		if err := recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: event, Stage: production.StageQc,
			Comment: req.Notes, ActorID: currentUserID(c), BatchID: updated.BatchID,
		}); err != nil {
			return err
		}
		if req.Decision == "fail" {
			// A QC failure is a real failure of this unit, but until now it
			// wrote no production_job_failures row at all - only /fail did - so
			// "why did QC fail" had no queryable answer, just free-text notes.
			if _, err := q.InsertProductionJobFailure(ctx, gen.InsertProductionJobFailureParams{
				ID: uuid.New(), JobID: id, Stage: production.FailureStageQc,
				Reason: qcFailureReason, Notes: req.Notes, CreatedBy: currentUserID(c),
			}); err != nil {
				return err
			}
			reprintNumber, nerr := q.NextJobNumber(ctx)
			if nerr != nil {
				return nerr
			}
			r, rerr := q.InsertProductionJob(ctx, reprintParamsFor(job, job.Quantity, reprintNumber))
			if rerr != nil {
				return rerr
			}
			reprint = &r
			if err := recordJobEvent(ctx, q, jobEvent{
				JobID: id, EventType: production.EventReprintCreated, Stage: production.StageQc,
				ActorID: currentUserID(c), RelatedJobID: &r.ID,
				Metadata: map[string]any{"quantity": r.Quantity, "job_number": r.JobNumber},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stationConflict(c, "This job is not waiting on QC - assembly or finishing is still pending, or QC is already decided.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the QC check.")
		return
	}

	resp := qcResponse{Job: s.singleJobDTO(ctx, updated)}
	if reprint != nil {
		dto := s.singleJobDTO(ctx, *reprint)
		resp.ReprintJob = &dto
	}
	c.JSON(http.StatusOK, resp)
}

type packagingRequest struct {
	PackagingType    string  `json:"packaging_type" binding:"required,min=1,max=128"`
	Addons           *string `json:"addons"`
	GiftMessage      *string `json:"gift_message"`
	Fragile          bool    `json:"fragile"`
	CourierPartner   *string `json:"courier_partner"`
	InvoiceReference *string `json:"invoice_reference"`
	PhotoFileID      *string `json:"photo_file_id"`
}

func (s *Server) submitPackaging(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req packagingRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	photoID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	var updated gen.ProductionJob
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		updated, err = q.AdvanceJobPackaging(ctx, gen.AdvanceJobPackagingParams{
			ID: id, PackagingStatus: production.PackagingPackaged,
		})
		if err != nil {
			return err
		}
		if _, err := q.UpsertPackagingDetail(ctx, gen.UpsertPackagingDetailParams{
			ID: uuid.New(), JobID: id, PackagingType: req.PackagingType, Addons: req.Addons,
			GiftMessage: req.GiftMessage, Fragile: req.Fragile, CourierPartner: req.CourierPartner,
			InvoiceReference: req.InvoiceReference, PhotoFileID: photoID, PackedBy: currentUserID(c),
		}); err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventPackagingPacked,
			Stage: production.StagePackaging, ActorID: currentUserID(c), BatchID: updated.BatchID,
			Metadata: map[string]any{"packaging_type": req.PackagingType},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stationConflict(c, "This job is not waiting on packaging - QC has not passed, or it is already packed.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the packaging.")
		return
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}
