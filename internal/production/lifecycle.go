// Package production holds the pure rules of the print-queue lifecycle, ported
// from print-queue-be. It has no database or HTTP dependencies: it defines the
// job state machine's values, the role-gated PATCH field set, the Shopify line
// item shape, and the personalisation auto-validation. The httpapi handlers own
// the I/O and call into here for the decisions.
package production

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Primary job status: the print lifecycle. A job is created queued, moved to
// in_production, then completed - or failed (only through the dedicated /fail
// action, never a direct PATCH).
const (
	StatusQueued       = "queued"
	StatusInProduction = "in_production"
	StatusCompleted    = "completed"
	StatusFailed       = "failed"
)

// Assembly sub-status.
const (
	AssemblyPending     = "pending"
	AssemblyCompleted   = "completed"
	AssemblyNotRequired = "not_required"
)

// QC sub-status.
const (
	QcPending = "pending"
	QcPassed  = "passed"
	QcFailed  = "failed"
)

// Packaging sub-status.
const (
	PackagingPending  = "pending"
	PackagingPackaged = "packaged"
)

// Personalisation sub-status. Only validated or not_required jobs are batchable.
const (
	PersonalisationPending     = "pending"
	PersonalisationValidated   = "validated"
	PersonalisationNotRequired = "not_required"
)

// Failure stages and reasons recorded by /fail (and later QC failures).
const (
	FailureStagePrint = "print"
	FailureStageQc    = "qc"
)

// Batch status: pending_approval (auto-created) -> open (approved) -> in_progress
// -> completed. A PATCH may set open/in_progress/completed but not pending_approval.
const (
	BatchPendingApproval = "pending_approval"
	BatchOpen            = "open"
	BatchInProgress      = "in_progress"
	BatchCompleted       = "completed"
)

// Operational machine status. Only online machines are batch-eligible.
const (
	MachineOnline      = "online"
	MachineBusy        = "busy"
	MachineOffline     = "offline"
	MachineMaintenance = "maintenance"
)

// Issue reasons (production_jobs.issue_reason): the Validation stage's fixed
// taxonomy. A job with any of these set is excluded from batching until fixed,
// but is never dropped - it stays visible with a reason. This is a separate
// axis from `status` (the print lifecycle) and `personalisation_status`; the
// three job-list display states (issue / pending / ready-for-batching) are
// derived by the API layer, not stored as a third status column.
const (
	IssueSKUMissing         = "sku_missing"
	IssueNoApprovedDesign   = "no_approved_design"
	IssueSTLMissing         = "stl_missing"
	IssueColourMissing      = "colour_missing"
	IssueMaterialMissing    = "material_missing"
	IssueProfileMissing     = "profile_missing"
	IssueFilamentOutOfStock = "filament_out_of_stock"
)

var batchStatusTargets = set(BatchOpen, BatchInProgress, BatchCompleted)
var machineStatuses = set(MachineOnline, MachineBusy, MachineOffline, MachineMaintenance)

// TargetBedUtilisationPercent (planner.go) is the same "batch is full" cutoff
// reused here for manually adding a job to an existing Draft batch - see
// CompatibilityKey's doc comment for the narrower compatibility definition
// this pairs with.

// CompatibilityKey is a job's physical/slicing-profile signature - the "same
// machine configuration" check used when manually adding a job to an
// existing batch. Deliberately a narrower subset of planner.go's groupKey
// (which also folds in supportUsed/infillBucket/priorityTier for batching
// efficiency): those three are packing/scheduling heuristics, not machine
// configuration, so a job is offered as compatible here even if it differs
// on them. A plain comparable struct - build one from a job's already-loaded
// fields and compare with ==.
type CompatibilityKey struct {
	Material      string
	NozzleLeft    string
	NozzleRight   string
	QualityMM     string
	MachineFamily string
}

// ValidBatchStatusTarget reports whether s is a PATCH-settable batch status.
func ValidBatchStatusTarget(s string) bool { return batchStatusTargets[s] }

// ValidMachineStatus validates an operational machine status.
func ValidMachineStatus(s string) bool { return machineStatuses[s] }

// statusPatchTargets is the set of primary statuses a PATCH may set. "failed" is
// deliberately absent: a job can only fail through /fail, which records the
// reason and queues a reprint.
var statusPatchTargets = set(StatusQueued, StatusInProduction, StatusCompleted)

var assemblyStatuses = set(AssemblyPending, AssemblyCompleted, AssemblyNotRequired)
var qcStatuses = set(QcPending, QcPassed, QcFailed)
var packagingStatuses = set(PackagingPending, PackagingPackaged)

var failureStages = set(FailureStagePrint, FailureStageQc)
var failureReasons = set(
	"bed_adhesion", "warping", "layer_shift", "filament_issue", "colour_issue",
	"support_failure", "power_issue", "machine_error", "wrong_personalisation",
	"design_issue", "operator_error", "other",
)

// ValidStatusTarget reports whether s is a PATCH-settable primary status.
func ValidStatusTarget(s string) bool { return statusPatchTargets[s] }

// ValidAssemblyStatus / ValidQcStatus / ValidPackagingStatus validate a PATCH
// sub-status value.
func ValidAssemblyStatus(s string) bool  { return assemblyStatuses[s] }
func ValidQcStatus(s string) bool        { return qcStatuses[s] }
func ValidPackagingStatus(s string) bool { return packagingStatuses[s] }

// ValidFailureStage / ValidFailureReason validate a /fail request.
func ValidFailureStage(s string) bool  { return failureStages[s] }
func ValidFailureReason(s string) bool { return failureReasons[s] }

// Role names as they appear in the JWT `roles` claim. Kept as strings so this
// package stays free of the auth package.
const (
	roleAdmin       = "ADMIN"
	roleProjectLead = "PROJECT_LEAD"
	roleOperator    = "OPERATOR"
	rolePackagingQc = "PACKAGING_QC"
)

// allPatchFields is what a lead/admin may change on a job. An operator may only
// move the primary status; the QC/packaging role uses the dedicated endpoints
// and gets no PATCH fields at all.
var allPatchFields = set(
	"status", "assembly_status", "qc_status", "packaging_status", "batch_id", "priority", "held",
)

var patchFieldsByRole = map[string]map[string]bool{
	roleAdmin:       allPatchFields,
	roleProjectLead: allPatchFields,
	roleOperator:    set("status"),
	rolePackagingQc: {},
}

// AllowedPatchFields returns the union of PATCH-able job fields across the
// caller's roles. A field absent from the result must be rejected with 403.
func AllowedPatchFields(roles []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range roles {
		for f := range patchFieldsByRole[r] {
			out[f] = true
		}
	}
	return out
}

// LineItem is one element of an order's line_items jsonb: the per-line snapshot a
// production job is built from. Optional fields are pointers so "absent" is
// distinguishable from an empty value.
type LineItem struct {
	ProductID                    string     `json:"product_id"`
	SKU                          *string    `json:"sku"`
	ProductName                  string     `json:"product_name"`
	Category                     *string    `json:"category"`
	Quantity                     int        `json:"quantity"`
	Unit                         string     `json:"unit"`
	Material                     *string    `json:"material"`
	Colour                       *string    `json:"colour"`
	NozzleProfile                *string    `json:"nozzle_profile"`
	FilamentGrams                *float64   `json:"filament_grams"`
	ImageURL                     *string    `json:"image_url"`
	ModelFileURL                 *string    `json:"model_file_url"`
	EstimatedPrintTimeMinutes    *int       `json:"estimated_print_time_minutes"`
	DueDate                      *time.Time `json:"due_date"`
	Priority                     int        `json:"priority"`
	PersonalisationRequired      bool       `json:"personalisation_required"`
	PersonalisationName          *string    `json:"personalisation_name"`
	PersonalisationFont          *string    `json:"personalisation_font"`
	PersonalisationColour        *string    `json:"personalisation_colour"`
	PersonalisationVariant       *string    `json:"personalisation_variant"`
	PersonalisationPhotoRequired bool       `json:"personalisation_photo_required"`
	PersonalisationPhotoURL      *string    `json:"personalisation_photo_url"`
	CustomerApprovalRequired     bool       `json:"customer_approval_required"`
	CustomerApprovalReceived     bool       `json:"customer_approval_received"`
}

// Confirms is the set of personalisation confirmation booleans. A job is
// validated only when every one is true (matching the source's six check fields).
type Confirms struct {
	Name     bool
	Photo    bool
	Font     bool
	Colour   bool
	Variant  bool
	Approval bool
}

// AllTrue reports whether every confirmation is satisfied.
func (c Confirms) AllTrue() bool {
	return c.Name && c.Photo && c.Font && c.Colour && c.Variant && c.Approval
}

// StatusFor maps a set of confirmations to the personalisation status.
func StatusFor(c Confirms) string {
	if c.AllTrue() {
		return PersonalisationValidated
	}
	return PersonalisationPending
}

// AutoValidatePersonalisation derives the initial personalisation status and
// confirmations for a line item at job-creation time. A line with no
// personalisation is not_required; otherwise each value field is confirmed when
// present, the photo when not required or supplied, and approval when not
// required or already received. All-true means validated, else pending.
func AutoValidatePersonalisation(li LineItem) (string, Confirms) {
	if !li.PersonalisationRequired {
		return PersonalisationNotRequired, Confirms{}
	}
	c := Confirms{
		Name:     nonEmpty(li.PersonalisationName),
		Font:     nonEmpty(li.PersonalisationFont),
		Colour:   nonEmpty(li.PersonalisationColour),
		Variant:  nonEmpty(li.PersonalisationVariant),
		Photo:    !li.PersonalisationPhotoRequired || nonEmpty(li.PersonalisationPhotoURL),
		Approval: !li.CustomerApprovalRequired || li.CustomerApprovalReceived,
	}
	return StatusFor(c), c
}

// PersonalisationLog explains what's still pending for a personalisation-
// required job, one line per unmet confirmation - the order/job detail
// pages render this as the "what's ready / what's pending and why" log. Nil
// when personalisation isn't required for this job at all.
func PersonalisationLog(c Confirms, required bool) []string {
	if !required {
		return nil
	}
	var out []string
	if !c.Name {
		out = append(out, "Name not confirmed")
	}
	if !c.Font {
		out = append(out, "Font not confirmed")
	}
	if !c.Colour {
		out = append(out, "Colour not confirmed")
	}
	if !c.Variant {
		out = append(out, "Variant not confirmed")
	}
	if !c.Photo {
		out = append(out, "Photo not uploaded")
	}
	if !c.Approval {
		out = append(out, "Customer approval not received")
	}
	return out
}

// Pipeline stage: a coarse, computed-on-read display/filter label derived
// from the 5 stored status columns plus held/issue_reason/batch state -
// never stored (see PipelineStage). Ten values, not the "NEW..COMPLETED"
// eleven originally proposed: COMPLETED is dropped since dispatch_orders
// only tracks pending->dispatched with nothing further, so it and DISPATCHED
// would be the same condition. FAILED is added - a job whose print or QC
// failed needs a real bucket rather than silently misrepresenting it.
const (
	StageNew          = "NEW"
	StageValidating   = "VALIDATING"
	StageReady        = "READY"
	StageWaitingBatch = "WAITING_BATCH"
	StageReserved     = "RESERVED"
	StageBatched      = "BATCHED"
	StagePrinting     = "PRINTING"
	StageQC           = "QC"
	StagePacked       = "PACKED"
	StageDispatched   = "DISPATCHED"
	StageFailed       = "FAILED"
)

// PipelineStageInput is everything PipelineStage needs about one job and
// (when batched) its batch - resolved by the caller (httpapi), since this
// package stays DB-free by design.
type PipelineStageInput struct {
	Status, AssemblyStatus, QcStatus, PackagingStatus, PersonalisationStatus string
	Held                                                                     bool
	IssueReason                                                              *string
	// BatchStatus is nil when the job is unbatched, else one of
	// BatchPendingApproval/BatchOpen/BatchInProgress/BatchCompleted.
	BatchStatus *string
	// Dispatched is true when the job's order has a dispatched (not merely
	// created) dispatch_orders row - best-effort, resolved by the caller.
	Dispatched bool
}

// PipelineStage derives a single display/filter label for a job's position
// in the pipeline, first-match-wins:
//
//  1. Failed: status == failed (the print itself failed - a separate
//     reprint job, not this row, continues the pipeline) or
//     qc_status == failed (same: a reprint was already queued).
//  2. Batched (BatchStatus != nil and not yet completed): pending_approval
//     -> RESERVED (a bed slot is tentatively held, not yet approved),
//     open -> BATCHED (approved, filament reserved, queued),
//     in_progress -> PRINTING. A completed batch falls through to the
//     post-print rules below, as if unbatched - by the time a batch reaches
//     'completed', Phase A guarantees every one of its jobs' own status is
//     already 'completed' too.
//  3. status == completed - NOTE this means the print finished, not that
//     the whole job is done; assembly/QC/packaging/dispatch all happen
//     after this value, gated on the sub-status fields below:
//     qc_status != passed, or packaging_status != packaged -> QC
//     (packaging isn't a distinct bucket in this 10-value list - a job
//     that passed QC but isn't packaged yet still reads as QC);
//     packaging_status == packaged -> PACKED, or DISPATCHED if the order
//     has actually shipped.
//  4. Unbatched, not yet printed: issue_reason set -> VALIDATING (blocked
//     on a design/SKU/file problem), personalisation_status == pending ->
//     NEW (blocked on a human), held -> READY (cleared, but paused),
//     otherwise -> WAITING_BATCH (eligible, just hasn't been picked up
//     yet).
func PipelineStage(in PipelineStageInput) string {
	if in.Status == StatusFailed || in.QcStatus == QcFailed {
		return StageFailed
	}
	if in.BatchStatus != nil && *in.BatchStatus != BatchCompleted {
		switch *in.BatchStatus {
		case BatchPendingApproval:
			return StageReserved
		case BatchOpen:
			return StageBatched
		case BatchInProgress:
			return StagePrinting
		}
	}
	if in.Status == StatusCompleted {
		if in.QcStatus != QcPassed || in.PackagingStatus != PackagingPackaged {
			return StageQC
		}
		if in.Dispatched {
			return StageDispatched
		}
		return StagePacked
	}
	if in.IssueReason != nil {
		return StageValidating
	}
	if in.PersonalisationStatus == PersonalisationPending {
		return StageNew
	}
	if in.Held {
		return StageReady
	}
	return StageWaitingBatch
}

// numberDigits is how many random digits follow a generated identifier's
// prefix (5, e.g. JOB-12345 / BATCH-12345).
const numberDigits = 5

// numberBound is the exclusive upper bound for a numberDigits-digit number
// (10^numberDigits) - rand.Int draws from [0, numberBound).
var numberBound = new(big.Int).Exp(big.NewInt(10), big.NewInt(numberDigits), nil)

// NewJobNumber returns a job identifier of the form JOB-12345 (a random
// 5-digit number, zero-padded).
func NewJobNumber() (string, error) { return newNumber("JOB-") }

// NewBatchNumber returns a batch identifier of the form BATCH-12345 (a
// random 5-digit number, zero-padded) - same shape as NewJobNumber.
func NewBatchNumber() (string, error) { return newNumber("BATCH-") }

func newNumber(prefix string) (string, error) {
	n, err := rand.Int(rand.Reader, numberBound)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%0*d", prefix, numberDigits, n.Int64()), nil
}

func nonEmpty(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// Validate is a tiny helper the handlers use to turn an invalid enum into a
// consistent message.
func InvalidValue(field, value string) error {
	return fmt.Errorf("%s %q is not a valid value", field, value)
}
