package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// productionJobResponse is the API shape of a production job. machine_* are resolved via the
// job's batch and stay null until the batching phase.
type productionJobResponse struct {
	ID                         string     `json:"id"`
	JobNumber                  string     `json:"job_number"`
	OrderID                    *string    `json:"order_id"`
	BatchID                    *string    `json:"batch_id"`
	Description                string     `json:"description"`
	Quantity                   int32      `json:"quantity"`
	Status                     string     `json:"status"`
	AssemblyStatus             string     `json:"assembly_status"`
	QcStatus                   string     `json:"qc_status"`
	PackagingStatus            string     `json:"packaging_status"`
	ShopifyOrderID             *int64     `json:"shopify_order_id"`
	Sku                        *string    `json:"sku"`
	ProductName                *string    `json:"product_name"`
	Material                   *string    `json:"material"`
	Colour                     *string    `json:"colour"`
	NozzleProfile              *string    `json:"nozzle_profile"`
	FilamentGramsRequired      *float64   `json:"filament_grams_required"`
	PrintFileID                *string    `json:"print_file_id"`
	EstimatedPrintTimeMinutes  *int32     `json:"estimated_print_time_minutes"`
	DueDate                    *time.Time `json:"due_date"`
	Priority                   int32      `json:"priority"`
	PersonalisationName        *string    `json:"personalisation_name"`
	PersonalisationFont        *string    `json:"personalisation_font"`
	PersonalisationColour      *string    `json:"personalisation_colour"`
	PersonalisationVariant     *string    `json:"personalisation_variant"`
	PersonalisationStatus      string     `json:"personalisation_status"`
	NameConfirmed              bool       `json:"name_confirmed"`
	PhotoConfirmed             bool       `json:"photo_confirmed"`
	FontConfirmed              bool       `json:"font_confirmed"`
	ColourConfirmed            bool       `json:"colour_confirmed"`
	VariantConfirmed           bool       `json:"variant_confirmed"`
	CustomerApprovalReceived   bool       `json:"customer_approval_received"`
	PersonalisationNotes       *string    `json:"personalisation_notes"`
	PersonalisationPhotoFileID *string    `json:"personalisation_photo_file_id"`
	PersonalisationValidatedBy *string    `json:"personalisation_validated_by"`
	PersonalisationValidatedAt *time.Time `json:"personalisation_validated_at"`
	ReprintOfJobID             *string    `json:"reprint_of_job_id"`
	Held                       bool       `json:"held"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

func productionJobDTO(j gen.ProductionJob) productionJobResponse {
	return productionJobResponse{
		ID: j.ID.String(), JobNumber: j.JobNumber, OrderID: uuidPtrStr(j.OrderID),
		BatchID: uuidPtrStr(j.BatchID), Description: j.Description, Quantity: j.Quantity,
		Status: j.Status, AssemblyStatus: j.AssemblyStatus, QcStatus: j.QcStatus,
		PackagingStatus: j.PackagingStatus, ShopifyOrderID: j.ShopifyOrderID, Sku: j.Sku,
		ProductName: j.ProductName, Material: j.Material, Colour: j.Colour, NozzleProfile: j.NozzleProfile,
		FilamentGramsRequired: db.NumFloatPtr(j.FilamentGramsRequired), PrintFileID: uuidPtrStr(j.PrintFileID),
		EstimatedPrintTimeMinutes: j.EstimatedPrintTimeMinutes, DueDate: db.TimePtr(j.DueDate),
		Priority: j.Priority, PersonalisationName: j.PersonalisationName, PersonalisationFont: j.PersonalisationFont,
		PersonalisationColour: j.PersonalisationColour, PersonalisationVariant: j.PersonalisationVariant,
		PersonalisationStatus: j.PersonalisationStatus, NameConfirmed: j.NameConfirmed,
		PhotoConfirmed: j.PhotoConfirmed, FontConfirmed: j.FontConfirmed, ColourConfirmed: j.ColourConfirmed,
		VariantConfirmed: j.VariantConfirmed, CustomerApprovalReceived: j.CustomerApprovalReceived,
		PersonalisationNotes: j.PersonalisationNotes, PersonalisationPhotoFileID: uuidPtrStr(j.PersonalisationPhotoFileID),
		PersonalisationValidatedBy: j.PersonalisationValidatedBy, PersonalisationValidatedAt: db.TimePtr(j.PersonalisationValidatedAt),
		ReprintOfJobID: uuidPtrStr(j.ReprintOfJobID), Held: j.Held,
		CreatedAt: db.Time(j.CreatedAt), UpdatedAt: db.Time(j.UpdatedAt),
	}
}

func (s *Server) registerProductionJobs(r *gin.Engine) {
	g := r.Group("/production-jobs")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.ProductionRead.Key()), s.listProductionJobs)
	g.GET("/:id", s.guards.RequirePermission(auth.ProductionRead.Key()), s.getProductionJob)
	g.POST("", s.guards.RequirePermission(auth.ProductionCreate.Key()), s.createProductionJob)
	g.PATCH("/:id", s.guards.RequirePermission(auth.ProductionUpdate.Key()), s.patchProductionJob)
	g.POST("/from-order/:order_id", s.guards.RequirePermission(auth.ProductionCreate.Key()), s.createJobsFromOrder)
	g.POST("/:id/personalisation", s.guards.RequirePermission(auth.ProductionUpdate.Key()), s.validatePersonalisation)
	g.POST("/:id/print-file", s.guards.RequirePermission(auth.ProductionUpdate.Key()), s.setPrintFile)
	g.POST("/:id/fail", s.guards.RequirePermission(auth.ProductionFail.Key()), s.failProductionJob)
	g.POST("/:id/assembly", s.guards.RequirePermission(auth.AssemblySubmit.Key()), s.submitAssembly)
	g.POST("/:id/assembly/skip", s.guards.RequirePermission(auth.AssemblySubmit.Key()), s.skipAssembly)
	g.POST("/:id/qc", s.guards.RequirePermission(auth.QcSubmit.Key()), s.submitQc)
	g.POST("/:id/packaging", s.guards.RequirePermission(auth.PackagingSubmit.Key()), s.submitPackaging)
}

// --- list / get ---------------------------------------------------------------

func (s *Server) listProductionJobs(c *gin.Context) {
	page, ok := parsePageParams(c)
	if !ok {
		return
	}
	statusFilter := nilIfEmpty(c.Query("status"))
	qcFilter := nilIfEmpty(c.Query("qc_status"))
	ctx := c.Request.Context()

	if !page.paginate {
		rows, err := s.store.Q.ListProductionJobs(ctx, gen.ListProductionJobsParams{
			Status: statusFilter, QcStatus: qcFilter,
		})
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list production jobs.")
			return
		}
		c.JSON(http.StatusOK, productionJobsDTO(rows))
		return
	}

	rows, err := s.store.Q.ListProductionJobsPage(ctx, gen.ListProductionJobsPageParams{
		Status: statusFilter, QcStatus: qcFilter,
		CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list production jobs.")
		return
	}
	if n := len(rows); n > 0 {
		setNextCursor(c, n, page.limit, db.Time(rows[n-1].CreatedAt), rows[n-1].ID)
	}
	c.JSON(http.StatusOK, productionJobsDTO(rows))
}

func productionJobsDTO(rows []gen.ProductionJob) []productionJobResponse {
	out := make([]productionJobResponse, 0, len(rows))
	for _, j := range rows {
		out = append(out, productionJobDTO(j))
	}
	return out
}

func (s *Server) getProductionJob(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	j, err := s.store.Q.GetProductionJobByID(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	c.JSON(http.StatusOK, productionJobDTO(j))
}

// --- create -------------------------------------------------------------------

type createJobRequest struct {
	OrderID     *string `json:"order_id"`
	Description string  `json:"description" binding:"required,min=1,max=255"`
	Quantity    int32   `json:"quantity"`
}

func (s *Server) createProductionJob(c *gin.Context) {
	var req createJobRequest
	if !bindJSON(c, &req) {
		return
	}
	quantity := req.Quantity
	if quantity < 1 {
		quantity = 1
	}
	ctx := c.Request.Context()

	var orderID *uuid.UUID
	var shopifyOrderID *int64
	if req.OrderID != nil && *req.OrderID != "" {
		oid, err := uuid.Parse(*req.OrderID)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "The order_id is not a valid identifier.")
			return
		}
		order, err := s.store.Q.GetOrderByID(ctx, oid)
		if err != nil {
			dbError(c, err, "That order does not exist.", "Could not load the order.")
			return
		}
		orderID = &oid
		v := order.ShopifyOrderID
		shopifyOrderID = &v
	}

	jobNumber, err := production.NewJobNumber()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not generate a job number.")
		return
	}
	job, err := s.store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber, OrderID: orderID, ShopifyOrderID: shopifyOrderID,
		Description: req.Description, Quantity: quantity,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationPending,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the production job.")
		return
	}
	c.JSON(http.StatusCreated, productionJobDTO(job))
}

// --- from-order ---------------------------------------------------------------

func (s *Server) createJobsFromOrder(c *gin.Context) {
	orderID, ok := parseUUIDParam(c, "order_id")
	if !ok {
		return
	}
	created, err := s.CreateJobsForOrder(c.Request.Context(), orderID)
	if err != nil {
		respondErr(c, err, "Could not create the production jobs.")
		return
	}
	c.JSON(http.StatusCreated, created)
}

// errJobsAlreadyCreated marks the idempotent no-op: this order already has jobs.
// The HTTP route renders it as a 409, while the job-creation worker treats it as
// success - a webhook replay and an operator clicking Create Job must both land
// on the same outcome rather than a retry loop.
var errJobsAlreadyCreated = errors.New("production jobs already created for this order")

// CreateJobsForOrder turns one order's line items into production jobs (and, for
// multi-part products, the assembly groups and part slots). It is the shared body
// of the HTTP route and the River job-creation worker, so an order automated from
// a webhook and one created by hand produce identical rows.
//
// Every error carries its own status and client-facing message via *httpErr, so
// extracting this out of the handler did not change a single response string.
// Callers that do not speak HTTP match errJobsAlreadyCreated with errors.Is.
func (s *Server) CreateJobsForOrder(ctx context.Context, orderID uuid.UUID) ([]productionJobResponse, error) {
	order, err := s.store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		if isNoRows(err) {
			return nil, &httpErr{status: http.StatusNotFound, msg: "That order does not exist.", cause: err}
		}
		obs.FromContext(ctx).Error("database error", "error", err, "order", orderID)
		return nil, &httpErr{status: http.StatusInternalServerError, msg: "Could not load the order.", cause: err}
	}
	existing, err := s.store.Q.CountJobsForOrder(ctx, &orderID)
	if err != nil {
		return nil, &httpErr{status: http.StatusInternalServerError, msg: "Could not check the order's jobs.", cause: err}
	}
	if existing > 0 {
		return nil, &httpErr{
			status: http.StatusConflict,
			msg:    "Production jobs have already been created for this order.",
			cause:  errJobsAlreadyCreated,
		}
	}

	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return nil, &httpErr{status: http.StatusInternalServerError, msg: "The order's line items are corrupt.", cause: err}
	}

	// Plan the jobs (and, for multi-part products, the assembly groups and part
	// slots). A single-part line stays one job, exactly as before; a line whose SKU
	// resolves to a design with named parts fans out into one unit per ordered
	// quantity and one job per part instance.
	groups, jobs, err := s.planJobsForOrder(ctx, order, items)
	if err != nil {
		return nil, &httpErr{status: http.StatusInternalServerError, msg: "Could not prepare the production jobs.", cause: err}
	}

	created := make([]productionJobResponse, 0, len(jobs))
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		for _, g := range groups {
			if _, err := q.InsertAssemblyGroup(ctx, g); err != nil {
				return err
			}
		}
		for _, pj := range jobs {
			job, err := q.InsertProductionJob(ctx, pj.params)
			if err != nil {
				return err
			}
			if pj.part != nil {
				jobID := job.ID
				if _, err := q.InsertAssemblyGroupPart(ctx, gen.InsertAssemblyGroupPartParams{
					ID: uuid.New(), AssemblyGroupID: pj.part.groupID, JobID: &jobID,
					PartRole: pj.part.role, PartInstance: pj.part.instance, PartUid: pj.part.partUID,
				}); err != nil {
					return err
				}
			}
			created = append(created, productionJobDTO(job))
		}
		return nil
	})
	if err != nil {
		return nil, &httpErr{status: http.StatusInternalServerError, msg: "Could not create the production jobs.", cause: err}
	}

	// New jobs are new batchable work. Scheduled after the commit and
	// best-effort: the jobs are already durable, and a replan is idempotent.
	s.triggerBatchPlan(ctx)
	return created, nil
}

// plannedJob is one job to insert, optionally linked to a product-unit part slot.
type plannedJob struct {
	params gen.InsertProductionJobParams
	part   *plannedPart // nil for a single-part product's job
}

// plannedPart is the slot a fanned-out job fills within its product unit.
type plannedPart struct {
	groupID  uuid.UUID
	role     string
	instance int32
	partUID  string
}

// planJobsForOrder turns an order's line items into the jobs to insert, plus the
// assembly groups and part slots for any multi-part products. Resolution per line:
//   - SKU is empty or not catalogued -> one job with the line's own values (legacy).
//   - SKU resolves to a design with no parts -> one job, catalog model + material.
//   - SKU resolves to a design with named parts -> one assembly group per ordered
//     unit, and one job per (part, instance), each independently reprintable.
func (s *Server) planJobsForOrder(
	ctx context.Context, order gen.Order, items []production.LineItem,
) ([]gen.InsertAssemblyGroupParams, []plannedJob, error) {
	var groups []gen.InsertAssemblyGroupParams
	var jobs []plannedJob
	// Cache per design so two lines sharing a SKU create one template file, not two.
	templateByDesign := make(map[uuid.UUID]uuid.UUID)

	for _, li := range items {
		base, err := jobParamsForLine(order, li)
		if err != nil {
			return nil, nil, err
		}

		sku := strings.TrimSpace(li.SKU)
		if sku == "" {
			jobs = append(jobs, plannedJob{params: base})
			continue
		}
		design, err := s.store.Q.GetDesignBySku(ctx, &sku)
		if err != nil {
			if isNoRows(err) {
				jobs = append(jobs, plannedJob{params: base}) // not catalogued; keep the fallback
				continue
			}
			return nil, nil, err
		}

		// Attach the design's model + material/colour and re-validate personalisation
		// against the product's rules, exactly as the single-part catalog path did.
		templateFile, ok := templateByDesign[design.ID]
		if !ok {
			templateFile, err = s.ensureTemplateFile(ctx, design)
			if err != nil {
				return nil, nil, err
			}
			templateByDesign[design.ID] = templateFile
		}
		base.PrintFileID = &templateFile
		material := design.Material
		base.Material = &material
		if design.Colour != nil {
			base.Colour = design.Colour
		}
		if len(design.PersonalisationRules) > 0 {
			applyPersonalisationRules(&base, design.PersonalisationRules, li)
		}

		parts, err := s.store.Q.ListDesignPartsByDesign(ctx, design.ID)
		if err != nil {
			return nil, nil, err
		}
		if len(parts) == 0 {
			jobs = append(jobs, plannedJob{params: base}) // single-part product
			continue
		}

		// Multi-part product: one assembly group per ordered unit, one job per part
		// instance within it.
		units := int(base.Quantity)
		if units < 1 {
			units = 1
		}
		for u := 1; u <= units; u++ {
			groupID := uuid.New()
			orderID := order.ID
			skuCopy := sku
			groups = append(groups, gen.InsertAssemblyGroupParams{
				ID: groupID, OrderID: &orderID, DesignSku: &skuCopy,
				UnitIndex: int32(u), Status: production.GroupPrinting,
			})
			for _, p := range parts {
				count := int(p.Quantity)
				if count < 1 {
					count = 1
				}
				for inst := 1; inst <= count; inst++ {
					pj, err := partJobFromBase(base, p, templateFile, inst)
					if err != nil {
						return nil, nil, err
					}
					partUID, err := production.NewPartUID()
					if err != nil {
						return nil, nil, err
					}
					jobs = append(jobs, plannedJob{
						params: pj,
						part: &plannedPart{
							groupID: groupID, role: p.Role, instance: int32(inst), partUID: partUID,
						},
					})
				}
			}
		}
	}
	return groups, jobs, nil
}

// jobParamsForLine builds the insert params for one order line's job: the print
// facts snapshot plus auto-validated personalisation. Catalog resolution (print
// file, material/colour, personalisation rules) is layered on by the caller.
func jobParamsForLine(order gen.Order, li production.LineItem) (gen.InsertProductionJobParams, error) {
	jobNumber, err := production.NewJobNumber()
	if err != nil {
		return gen.InsertProductionJobParams{}, err
	}
	quantity := int32(li.Quantity)
	if quantity < 1 {
		quantity = 1
	}
	status, confirms := production.AutoValidatePersonalisation(li)
	shopifyID := order.ShopifyOrderID

	return gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber,
		OrderID: ptr(order.ID), ShopifyOrderID: &shopifyID,
		Description: descriptionOf(li), Quantity: quantity,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		Sku: nonEmptyPtr(firstNonEmpty(li.SKU, li.ProductID)), ProductName: nonEmptyPtr(li.ProductName),
		Material: li.Material, Colour: li.Colour, NozzleProfile: li.NozzleProfile,
		FilamentGramsRequired:     filamentForQty(li.FilamentGrams, quantity),
		EstimatedPrintTimeMinutes: intPtrToInt32(li.EstimatedPrintTimeMinutes),
		DueDate:                   db.Timestamptz(li.DueDate),
		Priority:                  int32(li.Priority),
		PersonalisationName:       li.PersonalisationName, PersonalisationFont: li.PersonalisationFont,
		PersonalisationColour: li.PersonalisationColour, PersonalisationVariant: li.PersonalisationVariant,
		PersonalisationStatus: status,
		NameConfirmed:         confirms.Name, PhotoConfirmed: confirms.Photo, FontConfirmed: confirms.Font,
		ColourConfirmed: confirms.Colour, VariantConfirmed: confirms.Variant,
		CustomerApprovalReceived: confirms.Approval,
	}, nil
}

// partJobFromBase builds one part-job from a product's base job: a fresh id and job
// number, quantity 1, the part's own print file and material/colour overrides (each
// falling back to the product's), and a description naming the part (and its
// instance when a product needs several identical ones). Per-part filament is left
// unset because slicing does not yet meter parts individually.
func partJobFromBase(
	base gen.InsertProductionJobParams, p gen.DesignPart, templateFile uuid.UUID, instance int,
) (gen.InsertProductionJobParams, error) {
	jobNumber, err := production.NewJobNumber()
	if err != nil {
		return gen.InsertProductionJobParams{}, err
	}
	pj := base
	pj.ID = uuid.New()
	pj.JobNumber = jobNumber
	pj.Quantity = 1
	pj.FilamentGramsRequired = nil
	pj.Description = partDescription(base.Description, p, instance)
	if p.PrintFileID != nil {
		pj.PrintFileID = p.PrintFileID
	} else {
		pj.PrintFileID = &templateFile
	}
	if p.Material != nil {
		pj.Material = p.Material
	}
	if p.Colour != nil {
		pj.Colour = p.Colour
	}
	if p.NozzleProfile != nil {
		pj.NozzleProfile = p.NozzleProfile
	}
	return pj, nil
}

// partDescription labels a part-job by its product and role, adding an instance
// number only when the product needs more than one of that identical part.
func partDescription(base string, p gen.DesignPart, instance int) string {
	if p.Quantity > 1 {
		return fmt.Sprintf("%s - %s #%d", base, p.Role, instance)
	}
	return base + " - " + p.Role
}

// --- PATCH --------------------------------------------------------------------

func (s *Server) patchProductionJob(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	body, ok := readBody(c)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		detail(c, http.StatusUnprocessableEntity, "The request body is invalid.")
		return
	}

	user, _ := auth.UserFrom(c)
	allowed := production.AllowedPatchFields(roleStrings(user.Roles))
	for field := range raw {
		if !allowed[field] {
			detail(c, http.StatusForbidden, "You cannot change '"+field+"'.")
			return
		}
	}

	params := gen.UpdateProductionJobFieldsParams{ID: id}
	if !applyJobPatch(c, raw, &params) {
		return
	}
	if !s.applyBatchAssignment(c, raw, &params) {
		return
	}

	// 404 if the job does not exist (the update would otherwise report no rows).
	if _, err := s.store.Q.GetProductionJobByID(c.Request.Context(), id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	job, err := s.store.Q.UpdateProductionJobFields(c.Request.Context(), params)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the production job.")
		return
	}
	c.JSON(http.StatusOK, productionJobDTO(job))
}

// applyJobPatch validates and copies the provided PATCH fields into params.
func applyJobPatch(c *gin.Context, raw map[string]json.RawMessage, params *gen.UpdateProductionJobFieldsParams) bool {
	if v, ok := raw["status"]; ok {
		var status string
		if !decodeField(c, v, &status) {
			return false
		}
		if status == production.StatusFailed {
			detail(c, http.StatusUnprocessableEntity, "A job can only be failed through the fail action.")
			return false
		}
		if !production.ValidStatusTarget(status) {
			detail(c, http.StatusUnprocessableEntity, "That status is not valid.")
			return false
		}
		params.Status = &status
	}
	if !patchEnum(c, raw, "assembly_status", production.ValidAssemblyStatus, func(v string) { params.AssemblyStatus = &v }) {
		return false
	}
	if !patchEnum(c, raw, "qc_status", production.ValidQcStatus, func(v string) { params.QcStatus = &v }) {
		return false
	}
	if !patchEnum(c, raw, "packaging_status", production.ValidPackagingStatus, func(v string) { params.PackagingStatus = &v }) {
		return false
	}
	if v, ok := raw["priority"]; ok {
		var p int32
		if !decodeField(c, v, &p) {
			return false
		}
		params.Priority = &p
	}
	if v, ok := raw["held"]; ok {
		var h bool
		if !decodeField(c, v, &h) {
			return false
		}
		params.Held = &h
	}
	// batch_id is handled by the caller (patchProductionJob), which has store access
	// to validate the target batch exists.
	return true
}

// applyBatchAssignment validates and applies a batch_id change: a null clears the
// assignment, a non-null id must refer to an existing batch.
func (s *Server) applyBatchAssignment(c *gin.Context, raw map[string]json.RawMessage, params *gen.UpdateProductionJobFieldsParams) bool {
	v, ok := raw["batch_id"]
	if !ok {
		return true
	}
	var idStr *string
	if !decodeField(c, v, &idStr) {
		return false
	}
	params.SetBatchID = true
	if idStr == nil || *idStr == "" {
		params.BatchID = nil
		return true
	}
	batchID, err := uuid.Parse(*idStr)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The batch_id is not a valid identifier.")
		return false
	}
	if _, err := s.store.Q.GetBatchByID(c.Request.Context(), batchID); err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return false
	}
	params.BatchID = &batchID
	return true
}

// patchEnum decodes an optional string field, validates it, and applies it.
func patchEnum(c *gin.Context, raw map[string]json.RawMessage, field string, valid func(string) bool, set func(string)) bool {
	v, ok := raw[field]
	if !ok {
		return true
	}
	var value string
	if !decodeField(c, v, &value) {
		return false
	}
	if !valid(value) {
		detail(c, http.StatusUnprocessableEntity, "That "+field+" is not valid.")
		return false
	}
	set(value)
	return true
}

// --- personalisation ----------------------------------------------------------

type personalisationRequest struct {
	NameConfirmed            bool    `json:"name_confirmed"`
	PhotoConfirmed           bool    `json:"photo_confirmed"`
	FontConfirmed            bool    `json:"font_confirmed"`
	ColourConfirmed          bool    `json:"colour_confirmed"`
	VariantConfirmed         bool    `json:"variant_confirmed"`
	CustomerApprovalReceived bool    `json:"customer_approval_received"`
	Notes                    *string `json:"notes"`
	PhotoFileID              *string `json:"photo_file_id"`
}

func (s *Server) validatePersonalisation(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req personalisationRequest
	if !bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()

	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	photoFileID, ok := s.resolveOptionalFile(c, req.PhotoFileID)
	if !ok {
		return
	}

	confirms := production.Confirms{
		Name: req.NameConfirmed, Photo: req.PhotoConfirmed, Font: req.FontConfirmed,
		Colour: req.ColourConfirmed, Variant: req.VariantConfirmed, Approval: req.CustomerApprovalReceived,
	}
	status := production.StatusFor(confirms)
	var validatedAt pgtype.Timestamptz
	var validatedBy *string
	if status == production.PersonalisationValidated {
		validatedAt = db.Timestamptz(nowPtr())
		uid := currentUserID(c)
		validatedBy = &uid
	}

	job, err := s.store.Q.ValidateProductionJobPersonalisation(ctx, gen.ValidateProductionJobPersonalisationParams{
		ID: id, NameConfirmed: req.NameConfirmed, PhotoConfirmed: req.PhotoConfirmed,
		FontConfirmed: req.FontConfirmed, ColourConfirmed: req.ColourConfirmed,
		VariantConfirmed: req.VariantConfirmed, CustomerApprovalReceived: req.CustomerApprovalReceived,
		PersonalisationNotes: req.Notes, PersonalisationPhotoFileID: photoFileID,
		PersonalisationStatus: status, PersonalisationValidatedBy: validatedBy,
		PersonalisationValidatedAt: validatedAt,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the personalisation.")
		return
	}
	c.JSON(http.StatusOK, productionJobDTO(job))
}

// --- print file ---------------------------------------------------------------

type printFileRequest struct {
	FileID string `json:"file_id" binding:"required"`
}

func (s *Server) setPrintFile(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req printFileRequest
	if !bindJSON(c, &req) {
		return
	}
	fileID, err := uuid.Parse(req.FileID)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The file_id is not a valid identifier.")
		return
	}
	ctx := c.Request.Context()

	if _, err := s.store.Q.GetProductionJobByID(ctx, id); err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if _, err := s.store.Q.GetFileAsset(ctx, fileID); err != nil {
		dbError(c, err, "That file does not exist.", "Could not load the file.")
		return
	}
	job, err := s.store.Q.SetProductionJobPrintFile(ctx, gen.SetProductionJobPrintFileParams{
		ID: id, PrintFileID: &fileID,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not set the print file.")
		return
	}
	c.JSON(http.StatusOK, productionJobDTO(job))
}

// --- fail ---------------------------------------------------------------------

type failJobRequest struct {
	Reason              string   `json:"reason" binding:"required"`
	Notes               *string  `json:"notes"`
	FilamentWastedGrams *float64 `json:"filament_wasted_grams"`
	TimeWastedMinutes   *int32   `json:"time_wasted_minutes"`
}

type failJobResponse struct {
	FailedJob  productionJobResponse `json:"failed_job"`
	ReprintJob productionJobResponse `json:"reprint_job"`
}

func (s *Server) failProductionJob(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req failJobRequest
	if !bindJSON(c, &req) {
		return
	}
	if !production.ValidFailureReason(req.Reason) {
		detail(c, http.StatusUnprocessableEntity, "That failure reason is not valid.")
		return
	}
	if req.FilamentWastedGrams != nil && *req.FilamentWastedGrams < 0 {
		detail(c, http.StatusUnprocessableEntity, "Filament wasted cannot be negative.")
		return
	}
	if req.TimeWastedMinutes != nil && *req.TimeWastedMinutes < 0 {
		detail(c, http.StatusUnprocessableEntity, "Time wasted cannot be negative.")
		return
	}
	ctx := c.Request.Context()

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	if job.Status != production.StatusInProduction {
		detail(c, http.StatusConflict, "Only a job that is in production can be failed.")
		return
	}

	reprintParams := cloneForReprint(job)
	var failed, reprint gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		failed, err = q.SetProductionJobStatus(ctx, gen.SetProductionJobStatusParams{
			ID: id, Status: production.StatusFailed,
		})
		if err != nil {
			return err
		}
		if _, err := q.InsertProductionJobFailure(ctx, gen.InsertProductionJobFailureParams{
			ID: uuid.New(), JobID: id, Stage: production.FailureStagePrint, Reason: req.Reason,
			Notes: req.Notes, FilamentWastedGrams: req.FilamentWastedGrams,
			TimeWastedMinutes: req.TimeWastedMinutes, CreatedBy: currentUserID(c),
		}); err != nil {
			return err
		}
		// Decrement the wasted filament from stock (best-effort, no-op if untracked).
		if req.FilamentWastedGrams != nil {
			if err := s.decrementFilament(ctx, q, job.Material, *req.FilamentWastedGrams); err != nil {
				return err
			}
		}
		reprint, err = q.InsertProductionJob(ctx, reprintParams)
		if err != nil {
			return err
		}
		return s.repointAssemblyPart(ctx, q, id, reprint.ID)
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not fail the production job.")
		return
	}
	c.JSON(http.StatusOK, failJobResponse{FailedJob: productionJobDTO(failed), ReprintJob: productionJobDTO(reprint)})
}

// repointAssemblyPart moves a failed part's slot onto its reprint job, so the part
// (its stable part_uid, role, and instance) stays trackable across attempts. It is
// a no-op for a single-part product's job, which fills no slot.
func (s *Server) repointAssemblyPart(ctx context.Context, q *gen.Queries, oldJobID, newJobID uuid.UUID) error {
	slot, err := q.GetAssemblyPartByJob(ctx, &oldJobID)
	if err != nil {
		if isNoRows(err) {
			return nil
		}
		return err
	}
	next := newJobID
	return q.RepointAssemblyPartJob(ctx, gen.RepointAssemblyPartJobParams{ID: slot.ID, JobID: &next})
}

// cloneForReprint builds the insert params for a fresh queued reprint of a failed
// job. It carries the print facts and personalisation, resets the lifecycle
// sub-states to their defaults, leaves the job unbatched, and links back to the
// source via reprint_of_job_id.
func cloneForReprint(src gen.ProductionJob) gen.InsertProductionJobParams {
	jobNumber, _ := production.NewJobNumber()
	return gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber,
		OrderID: src.OrderID, ShopifyOrderID: src.ShopifyOrderID,
		Description: src.Description, Quantity: src.Quantity,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		Sku: src.Sku, ProductName: src.ProductName, Material: src.Material, Colour: src.Colour,
		NozzleProfile: src.NozzleProfile, FilamentGramsRequired: db.NumFloatPtr(src.FilamentGramsRequired),
		PrintFileID: src.PrintFileID, EstimatedPrintTimeMinutes: src.EstimatedPrintTimeMinutes,
		DueDate: src.DueDate, Priority: src.Priority,
		PersonalisationName: src.PersonalisationName, PersonalisationFont: src.PersonalisationFont,
		PersonalisationColour: src.PersonalisationColour, PersonalisationVariant: src.PersonalisationVariant,
		PersonalisationStatus: src.PersonalisationStatus,
		NameConfirmed:         src.NameConfirmed, PhotoConfirmed: src.PhotoConfirmed, FontConfirmed: src.FontConfirmed,
		ColourConfirmed: src.ColourConfirmed, VariantConfirmed: src.VariantConfirmed,
		CustomerApprovalReceived:   src.CustomerApprovalReceived,
		PersonalisationPhotoFileID: src.PersonalisationPhotoFileID,
		ReprintOfJobID:             ptr(src.ID),
	}
}

// resolveOptionalFile validates that an optional file id refers to an existing
// file asset, returning the parsed id (or nil when absent).
func (s *Server) resolveOptionalFile(c *gin.Context, raw *string) (*uuid.UUID, bool) {
	if raw == nil || *raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "The photo_file_id is not a valid identifier.")
		return nil, false
	}
	if _, err := s.store.Q.GetFileAsset(c.Request.Context(), id); err != nil {
		dbError(c, err, "That file does not exist.", "Could not load the file.")
		return nil, false
	}
	return &id, true
}
