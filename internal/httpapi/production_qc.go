package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The station handlers (assembly, QC, packaging) move a completed job through the
// post-print stages. Each records an audit row and advances the matching
// sub-status, and each gates on the prior stage so the order cannot be skipped.

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

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if job.Status != production.StatusCompleted {
		detail(c, http.StatusConflict, "The job must be completed before assembly.")
		return
	}
	photoID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	var updated gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.InsertAssemblyCheck(ctx, gen.InsertAssemblyCheckParams{
			ID: uuid.New(), JobID: id, PartsCombined: req.PartsCombined,
			HardwareAttached: req.HardwareAttached, AddonsAttached: req.AddonsAttached,
			FitCheckOk: req.FitCheckOk, PhotoFileID: photoID, Notes: req.Notes,
			AssembledBy: currentUserID(c),
		}); err != nil {
			return err
		}
		updated, err = q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
			ID: id, AssemblyStatus: ptr(production.AssemblyCompleted),
		})
		return err
	})
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
	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if job.Status != production.StatusCompleted {
		detail(c, http.StatusConflict, "The job must be completed before assembly can be skipped.")
		return
	}
	updated, err := s.store.Q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
		ID: id, AssemblyStatus: ptr(production.AssemblyNotRequired),
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not skip assembly.")
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

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if job.Status != production.StatusCompleted {
		detail(c, http.StatusConflict, "The job must be completed before QC.")
		return
	}
	if job.AssemblyStatus == production.AssemblyPending {
		detail(c, http.StatusConflict, "Assembly must be completed or skipped before QC.")
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
		if _, err := q.InsertQcCheck(ctx, gen.InsertQcCheckParams{
			ID: uuid.New(), JobID: id, CorrectPersonalisation: req.CorrectPersonalisation,
			CorrectColour: req.CorrectColour, SurfaceFinishOk: req.SurfaceFinishOk, NoCracks: req.NoCracks,
			NoLayerDefects: req.NoLayerDefects, DimensionsOk: req.DimensionsOk, AssemblyFitOk: req.AssemblyFitOk,
			AddonsWorking: req.AddonsWorking, PackagingSafe: req.PackagingSafe, PhotoFileID: photoID,
			Decision: req.Decision, Notes: req.Notes, InspectedBy: currentUserID(c),
		}); err != nil {
			return err
		}
		var err error
		updated, err = q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
			ID: id, QcStatus: &qcStatus,
		})
		if err != nil {
			return err
		}
		if req.Decision == "fail" {
			r, err := q.InsertProductionJob(ctx, cloneForReprint(job))
			if err != nil {
				return err
			}
			reprint = &r
		}
		return nil
	})
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

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if job.QcStatus != production.QcPassed {
		detail(c, http.StatusConflict, "QC must pass before packaging.")
		return
	}
	photoID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	var updated gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.UpsertPackagingDetail(ctx, gen.UpsertPackagingDetailParams{
			ID: uuid.New(), JobID: id, PackagingType: req.PackagingType, Addons: req.Addons,
			GiftMessage: req.GiftMessage, Fragile: req.Fragile, CourierPartner: req.CourierPartner,
			InvoiceReference: req.InvoiceReference, PhotoFileID: photoID, PackedBy: currentUserID(c),
		}); err != nil {
			return err
		}
		updated, err = q.UpdateProductionJobFields(ctx, gen.UpdateProductionJobFieldsParams{
			ID: id, PackagingStatus: ptr(production.PackagingPackaged),
		})
		return err
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the packaging.")
		return
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}
