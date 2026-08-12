// Package production holds the pure rules of the print-queue lifecycle, ported
// from print-queue-be. It has no database or HTTP dependencies: it defines the
// job state machine's values, the role-gated PATCH field set, the Shopify line
// item shape, and the personalisation auto-validation. The httpapi handlers own
// the I/O and call into here for the decisions.
package production

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// Assembly-group status: the lifecycle of one physical unit of a multi-part
// product. It stays printing until every part slot is built, then can be
// assembled, QC'd as a whole, and packaged. needs_design_review parks a unit whose
// part keeps failing beyond the reprint cap.
const (
	GroupPrinting        = "printing"
	GroupAwaitingParts   = "awaiting_parts"
	GroupReadyToAssemble = "ready_to_assemble"
	GroupAssembled       = "assembled"
	GroupQc              = "qc"
	GroupPackaged        = "packaged"
	GroupNeedsReview     = "needs_design_review"
)

var batchStatusTargets = set(BatchOpen, BatchInProgress, BatchCompleted)
var machineStatuses = set(MachineOnline, MachineBusy, MachineOffline, MachineMaintenance)

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
	SKU                          string     `json:"sku"`
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

// NewJobNumber returns a job identifier of the form JOB-XXXXXX (6 uppercase hex).
func NewJobNumber() (string, error) { return newNumber("JOB-") }

// NewBatchNumber returns a batch identifier of the form BATCH-XXXXXX.
func NewBatchNumber() (string, error) { return newNumber("BATCH-") }

// NewPartUID returns a stable, system-minted identifier for one product part slot,
// of the form PART-XXXXXX. It is minted once when the slot is created and carried
// across reprints, so a part stays trackable through every reprint attempt.
func NewPartUID() (string, error) { return newNumber("PART-") }

func newNumber(prefix string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + strings.ToUpper(hex.EncodeToString(b)), nil
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
