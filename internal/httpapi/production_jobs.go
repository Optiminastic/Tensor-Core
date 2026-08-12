package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
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
	FinishingStatus            string     `json:"finishing_status"`
	QcStatus                   string     `json:"qc_status"`
	PackagingStatus            string     `json:"packaging_status"`
	ShopifyOrderID             *int64     `json:"shopify_order_id"`
	ShopifyCustomerID          *int64     `json:"shopify_customer_id"`
	CustomerName               *string    `json:"customer_name"`
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
	SplitOfJobID               *string    `json:"split_of_job_id"`
	Held                       bool       `json:"held"`
	Colours                    []string   `json:"colours"`
	SupportUsed                *bool      `json:"support_used"`
	InfillPct                  *float64   `json:"infill_pct"`
	LeftNozzleMm               *float64   `json:"left_nozzle_mm"`
	RightNozzleMm              *float64   `json:"right_nozzle_mm"`
	FlowPct                    *float64   `json:"flow_pct"`
	QualityMm                  *float64   `json:"quality_mm"`
	MachineFamily              *string    `json:"machine_family"`
	IssueReason                *string    `json:"issue_reason"`
	// Geometry/slice snapshot taken from the matched design at creation time
	// (see 0030_job_geometry_snapshot.sql). BboxXMm/YMm/ZMm and ColourCount
	// are per-unit facts; SupportWeightG/PurgeWeightG are already scaled to
	// this job's quantity, matching FilamentGramsRequired's convention.
	BboxXMm        *float64 `json:"bbox_x_mm"`
	BboxYMm        *float64 `json:"bbox_y_mm"`
	BboxZMm        *float64 `json:"bbox_z_mm"`
	SupportWeightG *float64 `json:"support_weight_g"`
	PurgeWeightG   *float64 `json:"purge_weight_g"`
	ColourCount    *int32   `json:"colour_count"`
	// PipelineStage is a coarse, computed (never stored) label for where this
	// job sits end to end - see production.PipelineStage's doc comment for
	// exactly how it's derived and its two documented simplifications.
	PipelineStage string `json:"pipeline_stage"`
	// PersonalisationLog explains what's ready/still pending and why - a
	// checklist derived from the six confirm booleans above, nil when
	// personalisation isn't required for this job at all.
	PersonalisationLog []string  `json:"personalisation_log"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	// SplitProgress is "150 total / 72 printed / 78 remaining" for a job
	// whose quantity was split across multiple batches (see
	// AutoCreateBatches/splitJobIDsFor) - only populated by the single-job
	// GET, and only when this job is actually part of a split group larger
	// than itself, to keep list endpoints free of the extra lookup.
	SplitProgress *splitProgressResponse `json:"split_progress,omitempty"`
}

// splitProgressResponse is production.GetSplitJobProgressRow's totals in
// API terms.
type splitProgressResponse struct {
	TotalQuantity     int64 `json:"total_quantity"`
	CompletedQuantity int64 `json:"completed_quantity"`
	RemainingQuantity int64 `json:"remaining_quantity"`
}

// productionJobDTO builds the API shape for one job. batchStatus/dispatched
// are the two bits of cross-table state PipelineStage needs beyond the job
// row itself - resolved by the caller (singleJobDTO for one job,
// productionJobsDTO's bulk lookups for a list), never re-queried here.
func productionJobDTO(j gen.ProductionJob, batchStatus *string, dispatched bool) productionJobResponse {
	confirms := production.Confirms{
		Name: j.NameConfirmed, Photo: j.PhotoConfirmed, Font: j.FontConfirmed,
		Colour: j.ColourConfirmed, Variant: j.VariantConfirmed, Approval: j.CustomerApprovalReceived,
	}
	required := j.PersonalisationStatus != production.PersonalisationNotRequired
	stage := production.PipelineStage(production.PipelineStageInput{
		Status: j.Status, AssemblyStatus: j.AssemblyStatus, FinishingStatus: j.FinishingStatus,
		QcStatus:        j.QcStatus,
		PackagingStatus: j.PackagingStatus, PersonalisationStatus: j.PersonalisationStatus,
		Held: j.Held, IssueReason: j.IssueReason, BatchStatus: batchStatus, Dispatched: dispatched,
	})
	return productionJobResponse{
		ID: j.ID.String(), JobNumber: j.JobNumber, OrderID: uuidPtrStr(j.OrderID),
		BatchID: uuidPtrStr(j.BatchID), Description: j.Description, Quantity: j.Quantity,
		Status: j.Status, AssemblyStatus: j.AssemblyStatus, FinishingStatus: j.FinishingStatus,
		QcStatus:        j.QcStatus,
		PackagingStatus: j.PackagingStatus, ShopifyOrderID: j.ShopifyOrderID,
		ShopifyCustomerID: j.ShopifyCustomerID, CustomerName: j.CustomerName, Sku: j.Sku,
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
		ReprintOfJobID: uuidPtrStr(j.ReprintOfJobID), SplitOfJobID: uuidPtrStr(j.SplitOfJobID), Held: j.Held,
		Colours: decodeColours(j.Colours), SupportUsed: j.SupportUsed, InfillPct: db.NumFloatPtr(j.InfillPct),
		LeftNozzleMm: db.NumFloatPtr(j.LeftNozzleMm), RightNozzleMm: db.NumFloatPtr(j.RightNozzleMm),
		FlowPct: db.NumFloatPtr(j.FlowPct), QualityMm: db.NumFloatPtr(j.QualityMm),
		MachineFamily: j.MachineFamily, IssueReason: j.IssueReason,
		BboxXMm: db.NumFloatPtr(j.BboxXMm), BboxYMm: db.NumFloatPtr(j.BboxYMm), BboxZMm: db.NumFloatPtr(j.BboxZMm),
		SupportWeightG: db.NumFloatPtr(j.SupportWeightG), PurgeWeightG: db.NumFloatPtr(j.PurgeWeightG),
		ColourCount:        j.ColourCount,
		PipelineStage:      stage,
		PersonalisationLog: production.PersonalisationLog(confirms, required),
		CreatedAt:          db.Time(j.CreatedAt), UpdatedAt: db.Time(j.UpdatedAt),
	}
}

// singleJobDTO is productionJobDTO for the single-job endpoints: it resolves
// the job's batch status and order dispatch state itself, best-effort - a
// lookup failure degrades to the unbatched/not-dispatched branch rather than
// failing the response, matching attachPlateBbox's convention elsewhere in
// this package.
func (s *Server) singleJobDTO(ctx context.Context, j gen.ProductionJob) productionJobResponse {
	var batchStatus *string
	if j.BatchID != nil {
		if b, err := s.store.Q.GetBatchByID(ctx, *j.BatchID); err == nil {
			batchStatus = &b.Status
		}
	}
	var dispatched bool
	if j.OrderID != nil {
		if ids, err := s.store.Q.ListDispatchedOrderIDs(ctx, []uuid.UUID{*j.OrderID}); err == nil && len(ids) > 0 {
			dispatched = true
		}
	}
	return productionJobDTO(j, batchStatus, dispatched)
}

// productionJobsDTO is productionJobDTO for a list of jobs: two bulk lookups
// (not one per row) resolve every job's batch status and order dispatch
// state at once, keyed by the distinct batch/order ids actually present in
// rows - a lookup failure degrades the same way as singleJobDTO, per job.
func (s *Server) productionJobsDTO(ctx context.Context, rows []gen.ProductionJob) []productionJobResponse {
	batchIDs := dedupeIDs(rows, func(j gen.ProductionJob) *uuid.UUID { return j.BatchID })
	orderIDs := dedupeIDs(rows, func(j gen.ProductionJob) *uuid.UUID { return j.OrderID })

	batchStatusByID := map[uuid.UUID]string{}
	if len(batchIDs) > 0 {
		if statuses, err := s.store.Q.ListBatchStatusesForIDs(ctx, batchIDs); err == nil {
			for _, b := range statuses {
				batchStatusByID[b.ID] = b.Status
			}
		}
	}
	dispatchedOrderIDs := map[uuid.UUID]bool{}
	if len(orderIDs) > 0 {
		if ids, err := s.store.Q.ListDispatchedOrderIDs(ctx, orderIDs); err == nil {
			for _, id := range ids {
				dispatchedOrderIDs[id] = true
			}
		}
	}

	out := make([]productionJobResponse, 0, len(rows))
	for _, j := range rows {
		var batchStatus *string
		if j.BatchID != nil {
			if status, ok := batchStatusByID[*j.BatchID]; ok {
				batchStatus = &status
			}
		}
		dispatched := j.OrderID != nil && dispatchedOrderIDs[*j.OrderID]
		out = append(out, productionJobDTO(j, batchStatus, dispatched))
	}
	return out
}

// dedupeIDs collects the distinct non-nil ids get extracts from rows - turns
// a job list's scattered batch_id/order_id pointers into the small
// deduplicated slices ListBatchStatusesForIDs/ListDispatchedOrderIDs need.
func dedupeIDs(rows []gen.ProductionJob, get func(gen.ProductionJob) *uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, j := range rows {
		id := get(j)
		if id == nil || seen[*id] {
			continue
		}
		seen[*id] = true
		out = append(out, *id)
	}
	return out
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
	g.POST("/:id/finishing", s.guards.RequirePermission(auth.FinishingSubmit.Key()), s.submitFinishing)
	g.POST("/:id/finishing/skip", s.guards.RequirePermission(auth.FinishingSubmit.Key()), s.skipFinishing)
	g.POST("/:id/qc", s.guards.RequirePermission(auth.QcSubmit.Key()), s.submitQc)
	g.POST("/:id/packaging", s.guards.RequirePermission(auth.PackagingSubmit.Key()), s.submitPackaging)
}

// --- list / get ---------------------------------------------------------------

// filterByPipelineStage keeps only the responses matching stage, in place -
// a Go-side filter (not a SQL WHERE) since pipeline_stage is computed, not a
// column: a SQL CASE would have to duplicate PipelineStage's branching logic
// across two languages, which drifts. On the paginated endpoint this means a
// page can come back short (even empty) with more matching rows past the
// cursor, since the DB's LIMIT is applied before this filter runs - callers
// polling this filter must keep following next_cursor, not stop on a short page.
func filterByPipelineStage(jobs []productionJobResponse, stage string) []productionJobResponse {
	if stage == "" {
		return jobs
	}
	out := make([]productionJobResponse, 0, len(jobs))
	for _, j := range jobs {
		if j.PipelineStage == stage {
			out = append(out, j)
		}
	}
	return out
}

func (s *Server) listProductionJobs(c *gin.Context) {
	page, ok := parsePageParams(c)
	if !ok {
		return
	}
	statusFilter := nilIfEmpty(c.Query("status"))
	assemblyFilter := nilIfEmpty(c.Query("assembly_status"))
	finishingFilter := nilIfEmpty(c.Query("finishing_status"))
	qcFilter := nilIfEmpty(c.Query("qc_status"))
	packagingFilter := nilIfEmpty(c.Query("packaging_status"))
	stageFilter := c.Query("pipeline_stage")
	ctx := c.Request.Context()

	orderFilter, ok := queryUUID(c, "order_id")
	if !ok {
		return
	}
	batchFilter, ok := queryUUID(c, "batch_id")
	if !ok {
		return
	}

	if !page.paginate {
		rows, err := s.store.Q.ListProductionJobs(ctx, gen.ListProductionJobsParams{
			Status: statusFilter, AssemblyStatus: assemblyFilter, FinishingStatus: finishingFilter,
			QcStatus:        qcFilter,
			PackagingStatus: packagingFilter, OrderID: orderFilter, BatchID: batchFilter,
		})
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not list production jobs.")
			return
		}
		c.JSON(http.StatusOK, filterByPipelineStage(s.productionJobsDTO(ctx, rows), stageFilter))
		return
	}

	rows, err := s.store.Q.ListProductionJobsPage(ctx, gen.ListProductionJobsPageParams{
		Status: statusFilter, AssemblyStatus: assemblyFilter, FinishingStatus: finishingFilter,
		QcStatus:        qcFilter,
		PackagingStatus: packagingFilter, OrderID: orderFilter, BatchID: batchFilter,
		CursorCreatedAt: page.cursorTS, CursorID: page.cursorID, PageLimit: page.limit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list production jobs.")
		return
	}
	if n := len(rows); n > 0 {
		setNextCursor(c, n, page.limit, db.Time(rows[n-1].CreatedAt), rows[n-1].ID)
	}
	c.JSON(http.StatusOK, filterByPipelineStage(s.productionJobsDTO(ctx, rows), stageFilter))
}

func (s *Server) getProductionJob(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	j, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}
	dto := s.singleJobDTO(ctx, j)
	// One cheap indexed lookup on a single-resource detail page (not a list
	// endpoint, so no N+1 risk) - only surfaced when the group is actually
	// bigger than this job alone, which is a no-op for the overwhelming
	// majority of jobs that were never split.
	if progress, err := s.store.Q.GetSplitJobProgress(ctx, id); err == nil && progress.TotalQuantity > int64(j.Quantity) {
		dto.SplitProgress = &splitProgressResponse{
			TotalQuantity: progress.TotalQuantity, CompletedQuantity: progress.CompletedQuantity,
			RemainingQuantity: progress.TotalQuantity - progress.CompletedQuantity,
		}
	}
	c.JSON(http.StatusOK, dto)
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
	var shopifyOrderID, shopifyCustomerID *int64
	var customerName *string
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
		shopifyCustomerID = order.ShopifyCustomerID
		customerName = order.CustomerName
	}

	jobNumber, err := s.store.Q.NextJobNumber(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not generate a job number.")
		return
	}
	job, err := s.store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber, OrderID: orderID, ShopifyOrderID: shopifyOrderID,
		ShopifyCustomerID: shopifyCustomerID, CustomerName: customerName,
		Description: req.Description, Quantity: quantity,
		Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
		QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationPending,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the production job.")
		return
	}
	c.JSON(http.StatusCreated, s.singleJobDTO(ctx, job))
}

// --- from-order ---------------------------------------------------------------

// errJobsAlreadyCreated is CreateJobsForOrder's idempotency-guard outcome -
// distinct from a real failure so the HTTP handler can answer 409 and the
// River worker (job_creation_worker.go) can ack without retrying.
var errJobsAlreadyCreated = errors.New("production jobs already exist for this order")

func (s *Server) createJobsFromOrder(c *gin.Context) {
	orderID, ok := parseUUIDParam(c, "order_id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	jobs, err := s.CreateJobsForOrder(ctx, orderID)
	if err != nil {
		switch {
		case errors.Is(err, errJobsAlreadyCreated):
			detail(c, http.StatusConflict, "Production jobs have already been created for this order.")
		case isNoRows(err):
			detail(c, http.StatusNotFound, "That order does not exist.")
		default:
			detail(c, http.StatusInternalServerError, "Could not create the production jobs.")
		}
		return
	}
	c.JSON(http.StatusCreated, s.productionJobsDTO(ctx, jobs))
}

// CreateJobsForOrder is the Job Creation Worker's core (Stage 2 + Stage 3
// combined: SKU-match and validate in the same pass). Shared by the kept
// manual backfill endpoint above and the River worker
// (job_creation_worker.go) - not a second, divergent implementation of
// either. Idempotent: a second call for an order that already has jobs
// returns errJobsAlreadyCreated rather than duplicating them.
func (s *Server) CreateJobsForOrder(ctx context.Context, orderID uuid.UUID) ([]gen.ProductionJob, error) {
	order, err := s.store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("load order: %w", err)
	}
	// A fast path only: it skips buildJobsForOrder's per-line design lookups on
	// a replay. The authoritative check is the locked one inside the
	// transaction below - this one races by construction.
	existing, err := s.store.Q.CountJobsForOrder(ctx, &orderID)
	if err != nil {
		return nil, fmt.Errorf("check existing jobs: %w", err)
	}
	if existing > 0 {
		return nil, errJobsAlreadyCreated
	}

	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return nil, fmt.Errorf("order line items are corrupt: %w", err)
	}
	params, err := s.buildJobsForOrder(ctx, order, items)
	if err != nil {
		return nil, fmt.Errorf("prepare jobs: %w", err)
	}

	var created []gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		// Lock, then re-count. Both are needed: the lock serialises concurrent
		// runs for this order (a re-sync enqueues a second job, and the manual
		// backfill endpoint can race either), and the re-count is what the
		// loser of that race sees. Under READ COMMITTED an unlocked count
		// cannot see the winner's uncommitted inserts.
		if err := q.LockOrderForJobCreation(ctx, orderID.String()); err != nil {
			return err
		}
		locked, err := q.CountJobsForOrder(ctx, &orderID)
		if err != nil {
			return err
		}
		if locked > 0 {
			return errJobsAlreadyCreated
		}
		for _, p := range params {
			job, err := q.InsertProductionJob(ctx, p)
			if err != nil {
				return err
			}
			if err := recordJobEvent(ctx, q, jobEvent{
				JobID: job.ID, EventType: production.EventJobCreated, ActorID: systemActor,
				Metadata: map[string]any{"order_id": orderID.String()},
			}); err != nil {
				return err
			}
			created = append(created, job)
		}
		// Clearing here is what makes POST /production-jobs/from-order/:id the
		// retry mechanism for a discarded import - no separate retry route.
		// A no-op on an order that never failed.
		return q.ClearOrderJobCreationFailure(ctx, orderID)
	})
	// Returned unwrapped: the 409 mapping in createJobsFromOrder and the
	// worker's success-ack both match on this sentinel, and it is not an
	// insert failure.
	if errors.Is(err, errJobsAlreadyCreated) {
		return nil, errJobsAlreadyCreated
	}
	if err != nil {
		return nil, fmt.Errorf("insert jobs: %w", err)
	}
	return created, nil
}

// buildJobsForOrder decomposes an order's line items into job insert params,
// one per line: the Job Creation Worker (Stage 2) and Validation (Stage 3)
// combined into one pass. Each line's SKU is matched against the design
// catalog (matchDesignForSKU); a match's print facts override the Shopify-
// property-sourced fallback (which is a best guess, per mapShopifyLineItems'
// own comment). No match still creates the job - flagged with issue_reason,
// never dropped, matching the "never allow bad jobs into batching" rule (a
// flagged job is simply excluded from ListBatchableJobs until fixed).
func (s *Server) buildJobsForOrder(
	ctx context.Context, order gen.Order, items []production.LineItem,
) ([]gen.InsertProductionJobParams, error) {
	shopifyID := order.ShopifyOrderID
	out := make([]gen.InsertProductionJobParams, 0, len(items))
	for _, li := range items {
		jobNumber, err := s.store.Q.NextJobNumber(ctx)
		if err != nil {
			return nil, err
		}
		quantity := int32(li.Quantity)
		if quantity < 1 {
			quantity = 1
		}
		status, confirms := production.AutoValidatePersonalisation(li)
		match := s.matchDesignForSKU(ctx, li.SKU)

		p := gen.InsertProductionJobParams{
			ID: uuid.New(), JobNumber: jobNumber,
			OrderID: ptr(order.ID), ShopifyOrderID: &shopifyID,
			ShopifyCustomerID: order.ShopifyCustomerID, CustomerName: order.CustomerName,
			Description: descriptionOf(li), Quantity: quantity,
			Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
			QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
			Sku: li.SKU, ProductName: nonEmptyPtr(li.ProductName),
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
			Colours:                  []byte("[]"),
		}
		applyMatch(&p, match, quantity)
		out = append(out, p)
	}
	return out, nil
}

// applyMatch overlays a matched design's print facts onto a job's insert
// params, or records why nothing was matched.
func applyMatch(p *gen.InsertProductionJobParams, match production.MatchResult, quantity int32) {
	if !match.Matched() {
		p.IssueReason = &match.Reason
		return
	}
	d := match.Design
	p.Material = nonEmptyPtr(d.Material)
	if len(d.Colours) > 0 {
		if raw, err := json.Marshal(d.Colours); err == nil {
			p.Colours = raw
		}
	}
	p.PrintFileID = d.PrintFileID
	p.LeftNozzleMm, p.RightNozzleMm, p.FlowPct, p.QualityMm = d.LeftNozzleMM, d.RightNozzleMM, d.FlowPct, d.QualityMM
	if d.MachineFamily != "" {
		p.MachineFamily = &d.MachineFamily
	}
	if d.PrintTimeMin != nil {
		v := int32(*d.PrintTimeMin)
		p.EstimatedPrintTimeMinutes = &v
	}
	if d.FilamentGrams != nil {
		v := *d.FilamentGrams * float64(quantity)
		p.FilamentGramsRequired = &v
	}
	// Geometry/slice snapshot (see 0030_job_geometry_snapshot.sql). Bbox and
	// colour count are per-unit facts, not scaled. Support/purge weight scale
	// by quantity, matching FilamentGramsRequired's whole-job-total
	// convention above - same source metric, same reasoning.
	p.BboxXMm, p.BboxYMm, p.BboxZMm = d.BboxXMM, d.BboxYMM, d.BboxZMM
	p.ColourCount = intPtrToInt32(d.ColourCount)
	if d.SupportWeightG != nil {
		v := *d.SupportWeightG * float64(quantity)
		p.SupportWeightG = &v
	}
	if d.PurgeWeightG != nil {
		v := *d.PurgeWeightG * float64(quantity)
		p.PurgeWeightG = &v
	}
	// Fix: these two columns already existed and are already read by the
	// planner's compatibility grouping (groupKey), but were never populated
	// here - every job had the same false/0 value on both axes.
	p.SupportUsed, p.InfillPct = d.SupportUsed, d.InfillPct
	if p.PrintFileID == nil {
		reason := production.IssueSTLMissing
		p.IssueReason = &reason
	}
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
	c.JSON(http.StatusOK, s.singleJobDTO(c.Request.Context(), job))
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
	if !patchEnum(c, raw, "finishing_status", production.ValidFinishingStatus, func(v string) { params.FinishingStatus = &v }) {
		return false
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
	// issue_reason takes patchField[*string] rather than patchEnum because the
	// point of exposing it is clearing it: only a *string decodes JSON null,
	// and patchEnum is string-only. Empty string clears too, matching
	// UpdateDesignSku's existing frontend contract.
	if !patchField(c, raw, "issue_reason",
		func(v *string) string {
			if v == nil || *v == "" || production.ValidIssueReason(*v) {
				return ""
			}
			return "That issue_reason is not valid."
		},
		func(v *string) {
			params.SetIssueReason = true
			if v != nil && *v == "" {
				v = nil
			}
			params.IssueReason = v
		},
	) {
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
	if status == production.PersonalisationValidated {
		s.triggerBatchPlanIfThresholdMet(ctx)
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, job))
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
	// The upload may have cleared an stl_missing flag, which is precisely the
	// event that makes a job batchable - without this it would wait for the
	// periodic tick.
	if job.IssueReason == nil {
		s.triggerBatchPlanIfThresholdMet(ctx)
	}
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, job))
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
	// A print can be failed while it is still on the bed (in_production) or
	// after it comes off (completed) - a part is very often only found to be
	// bad at assembly, once someone picks it up. Restricting this to
	// in_production made the whole endpoint unreachable in practice, because
	// batch completion sets every job on the bed to 'completed'.
	if job.Status != production.StatusInProduction && job.Status != production.StatusCompleted {
		detail(c, http.StatusConflict, "Only a job that is printing or printed can be failed.")
		return
	}

	var failed, reprint gen.ProductionJob
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		var err error
		reprintNumber, err := q.NextJobNumber(ctx)
		if err != nil {
			return err
		}
		reprintParams := reprintParamsFor(job, job.Quantity, reprintNumber)
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
		// Decrement the wasted filament from stock, split across the job's
		// colours (best-effort per bucket, no-op if untracked) - the same fix
		// as the colour-blind shortage check this batch approval used to have.
		if req.FilamentWastedGrams != nil {
			waste := make(map[filamentKey]float64)
			filamentSplit(waste, job.Material, decodeColours(job.Colours), *req.FilamentWastedGrams)
			if err := s.adjustFilamentByColour(ctx, q, waste, -1); err != nil {
				return err
			}
		}
		reprint, err = q.InsertProductionJob(ctx, reprintParams)
		if err != nil {
			return err
		}
		if err := recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventPrintFailed, Stage: production.FailureStagePrint,
			Reason: req.Reason, Comment: req.Notes, ActorID: currentUserID(c), BatchID: job.BatchID,
		}); err != nil {
			return err
		}
		return recordJobEvent(ctx, q, jobEvent{
			JobID: id, EventType: production.EventReprintCreated, Stage: production.FailureStagePrint,
			Reason: req.Reason, ActorID: currentUserID(c), RelatedJobID: &reprint.ID,
			Metadata: map[string]any{"quantity": reprint.Quantity, "job_number": reprint.JobNumber},
		})
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not fail the production job.")
		return
	}
	// A fresh urgent reprint just entered the queue - worth a replan
	// regardless of how much else is currently queued, unlike the
	// threshold-gated per-job triggers (see triggerBatchPlan's doc comment).
	s.triggerBatchPlan(ctx)
	c.JSON(http.StatusOK, failJobResponse{
		FailedJob: s.singleJobDTO(ctx, failed), ReprintJob: s.singleJobDTO(ctx, reprint),
	})
}

// splitProductionJob builds a fragment of src carrying `quantity` units. The
// job number is passed in rather than minted here: it now comes from a database
// sequence (NextJobNumber), so it needs the caller's queries handle - and the
// caller is already inside the transaction the fragment is inserted in.
func splitProductionJob(src gen.ProductionJob, quantity int32, jobNumber string) gen.InsertProductionJobParams {
	return gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: jobNumber,
		OrderID: src.OrderID, ShopifyOrderID: src.ShopifyOrderID,
		ShopifyCustomerID: src.ShopifyCustomerID, CustomerName: src.CustomerName,
		Description: src.Description, Quantity: quantity,
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
		SplitOfJobID:               ptr(src.ID),
		Colours:                    src.Colours,
		SupportUsed:                src.SupportUsed,
		InfillPct:                  db.NumFloatPtr(src.InfillPct),
		LeftNozzleMm:               db.NumFloatPtr(src.LeftNozzleMm),
		RightNozzleMm:              db.NumFloatPtr(src.RightNozzleMm),
		FlowPct:                    db.NumFloatPtr(src.FlowPct),
		QualityMm:                  db.NumFloatPtr(src.QualityMm),
		MachineFamily:              src.MachineFamily,
		// A fragment is the same physical product, just fewer units - copied
		// as-is, same convention as FilamentGramsRequired above (not
		// re-scaled to the fragment's smaller quantity).
		BboxXMm:        db.NumFloatPtr(src.BboxXMm),
		BboxYMm:        db.NumFloatPtr(src.BboxYMm),
		BboxZMm:        db.NumFloatPtr(src.BboxZMm),
		SupportWeightG: db.NumFloatPtr(src.SupportWeightG),
		PurgeWeightG:   db.NumFloatPtr(src.PurgeWeightG),
		ColourCount:    src.ColourCount,
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
