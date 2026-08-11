package production

import (
	"regexp"
	"testing"
)

func strptr(s string) *string { return &s }

var jobNumberPattern = regexp.MustCompile(`^JOB-\d{5}$`)
var batchNumberPattern = regexp.MustCompile(`^BATCH-\d{5}$`)

func TestNewJobNumberFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		n, err := NewJobNumber()
		if err != nil {
			t.Fatalf("NewJobNumber: %v", err)
		}
		if !jobNumberPattern.MatchString(n) {
			t.Fatalf("job number %q does not match JOB-##### (5 digits)", n)
		}
	}
}

func TestNewBatchNumberFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		n, err := NewBatchNumber()
		if err != nil {
			t.Fatalf("NewBatchNumber: %v", err)
		}
		if !batchNumberPattern.MatchString(n) {
			t.Fatalf("batch number %q does not match BATCH-##### (5 digits)", n)
		}
	}
}

func TestAutoValidateNotRequired(t *testing.T) {
	status, c := AutoValidatePersonalisation(LineItem{PersonalisationRequired: false})
	if status != PersonalisationNotRequired {
		t.Errorf("status = %q, want not_required", status)
	}
	if c.AllTrue() {
		t.Error("no confirmations should be set when personalisation is not required")
	}
}

func TestAutoValidateAllPresentValidates(t *testing.T) {
	li := LineItem{
		PersonalisationRequired: true,
		PersonalisationName:     strptr("Ada"),
		PersonalisationFont:     strptr("Serif"),
		PersonalisationColour:   strptr("Gold"),
		PersonalisationVariant:  strptr("Large"),
		// photo not required, approval not required -> both satisfied.
	}
	status, c := AutoValidatePersonalisation(li)
	if status != PersonalisationValidated || !c.AllTrue() {
		t.Fatalf("status = %q, confirms = %+v, want validated/all-true", status, c)
	}
}

func TestAutoValidateMissingFieldPends(t *testing.T) {
	li := LineItem{
		PersonalisationRequired: true,
		PersonalisationName:     strptr("Ada"),
		// font/colour/variant missing -> pending.
	}
	status, _ := AutoValidatePersonalisation(li)
	if status != PersonalisationPending {
		t.Errorf("status = %q, want pending", status)
	}
}

func TestAutoValidatePhotoAndApprovalGates(t *testing.T) {
	base := LineItem{
		PersonalisationRequired: true,
		PersonalisationName:     strptr("Ada"), PersonalisationFont: strptr("Serif"),
		PersonalisationColour: strptr("Gold"), PersonalisationVariant: strptr("Large"),
	}

	// Photo required but not supplied -> pending.
	li := base
	li.PersonalisationPhotoRequired = true
	if status, _ := AutoValidatePersonalisation(li); status != PersonalisationPending {
		t.Errorf("photo required + absent: status = %q, want pending", status)
	}
	// Photo required and supplied -> validated.
	li.PersonalisationPhotoURL = strptr("https://cdn/x.png")
	if status, _ := AutoValidatePersonalisation(li); status != PersonalisationValidated {
		t.Errorf("photo required + supplied: status = %q, want validated", status)
	}

	// Approval required but not received -> pending.
	li = base
	li.CustomerApprovalRequired = true
	if status, _ := AutoValidatePersonalisation(li); status != PersonalisationPending {
		t.Errorf("approval required + not received: status = %q, want pending", status)
	}
	li.CustomerApprovalReceived = true
	if status, _ := AutoValidatePersonalisation(li); status != PersonalisationValidated {
		t.Errorf("approval required + received: status = %q, want validated", status)
	}
}

func TestAllowedPatchFieldsByRole(t *testing.T) {
	if f := AllowedPatchFields([]string{roleOperator}); !f["status"] || f["qc_status"] || len(f) != 1 {
		t.Errorf("operator fields = %v, want only status", f)
	}
	if f := AllowedPatchFields([]string{rolePackagingQc}); len(f) != 0 {
		t.Errorf("packaging_qc fields = %v, want none", f)
	}
	admin := AllowedPatchFields([]string{roleAdmin})
	for _, want := range []string{"status", "assembly_status", "qc_status", "packaging_status", "batch_id", "priority", "held"} {
		if !admin[want] {
			t.Errorf("admin missing patch field %q", want)
		}
	}
	// Union across roles.
	union := AllowedPatchFields([]string{roleOperator, roleAdmin})
	if len(union) != len(allPatchFields) {
		t.Errorf("union size = %d, want %d", len(union), len(allPatchFields))
	}
}

func TestCompatibilityKeyEquality(t *testing.T) {
	base := CompatibilityKey{
		Material: "PLA", NozzleLeft: "0.4", NozzleRight: "", QualityMM: "0.2", MachineFamily: "H2C",
	}
	same := base
	if same != base {
		t.Error("identical keys should be equal")
	}

	tests := []struct {
		name  string
		other CompatibilityKey
	}{
		{"different material", CompatibilityKey{Material: "PETG", NozzleLeft: "0.4", QualityMM: "0.2", MachineFamily: "H2C"}},
		{"different left nozzle", CompatibilityKey{Material: "PLA", NozzleLeft: "0.6", QualityMM: "0.2", MachineFamily: "H2C"}},
		{"different quality", CompatibilityKey{Material: "PLA", NozzleLeft: "0.4", QualityMM: "0.12", MachineFamily: "H2C"}},
		{"different machine family", CompatibilityKey{Material: "PLA", NozzleLeft: "0.4", QualityMM: "0.2", MachineFamily: "H2S"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if base == tc.other {
				t.Errorf("keys should differ: %+v vs %+v", base, tc.other)
			}
		})
	}
}

func TestStatusPatchTargetExcludesFailed(t *testing.T) {
	if ValidStatusTarget(StatusFailed) {
		t.Error("PATCH must not be able to set status=failed")
	}
	for _, s := range []string{StatusQueued, StatusInProduction, StatusCompleted} {
		if !ValidStatusTarget(s) {
			t.Errorf("status %q should be PATCH-settable", s)
		}
	}
}

func TestFailureReasonValidation(t *testing.T) {
	if !ValidFailureReason("bed_adhesion") || !ValidFailureReason("other") {
		t.Error("known failure reasons should validate")
	}
	if ValidFailureReason("exploded") {
		t.Error("unknown failure reason should not validate")
	}
	if !ValidFailureStage(FailureStagePrint) || ValidFailureStage("shipping") {
		t.Error("failure stage validation is wrong")
	}
}

func TestPipelineStage(t *testing.T) {
	pendingApproval, open, inProgress, completed := BatchPendingApproval, BatchOpen, BatchInProgress, BatchCompleted

	tests := []struct {
		name string
		in   PipelineStageInput
		want string
	}{
		{
			"unbatched, personalisation pending -> NEW",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationPending},
			StageNew,
		},
		{
			"unbatched, issue reason set -> VALIDATING",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationNotRequired, IssueReason: strptr(IssueSTLMissing)},
			StageValidating,
		},
		{
			"unbatched, held -> READY",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationNotRequired, Held: true},
			StageReady,
		},
		{
			"unbatched, clean and unheld -> WAITING_BATCH",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationNotRequired},
			StageWaitingBatch,
		},
		{
			"batch pending_approval -> RESERVED",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationNotRequired, BatchStatus: &pendingApproval},
			StageReserved,
		},
		{
			"batch open -> BATCHED",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationNotRequired, BatchStatus: &open},
			StageBatched,
		},
		{
			"batch in_progress -> PRINTING",
			PipelineStageInput{Status: StatusQueued, PersonalisationStatus: PersonalisationNotRequired, BatchStatus: &inProgress},
			StagePrinting,
		},
		{
			"batch completed, job still qc pending -> falls through to QC, not RESERVED/BATCHED",
			PipelineStageInput{
				Status: StatusCompleted, QcStatus: QcPending, PackagingStatus: PackagingPending,
				PersonalisationStatus: PersonalisationNotRequired, BatchStatus: &completed,
			},
			StageQC,
		},
		{
			"printed, qc pending -> QC",
			PipelineStageInput{Status: StatusCompleted, QcStatus: QcPending, PackagingStatus: PackagingPending},
			StageQC,
		},
		{
			"printed, qc passed but not yet packaged -> still QC (no distinct awaiting-packaging bucket)",
			PipelineStageInput{Status: StatusCompleted, QcStatus: QcPassed, PackagingStatus: PackagingPending},
			StageQC,
		},
		{
			"printed, qc passed, packaged, not dispatched -> PACKED",
			PipelineStageInput{Status: StatusCompleted, QcStatus: QcPassed, PackagingStatus: PackagingPackaged},
			StagePacked,
		},
		{
			"printed, qc passed, packaged, dispatched -> DISPATCHED",
			PipelineStageInput{Status: StatusCompleted, QcStatus: QcPassed, PackagingStatus: PackagingPackaged, Dispatched: true},
			StageDispatched,
		},
		{
			"print failed -> FAILED, regardless of batch state",
			PipelineStageInput{Status: StatusFailed, BatchStatus: &open},
			StageFailed,
		},
		{
			"qc failed -> FAILED, even though status is completed",
			PipelineStageInput{Status: StatusCompleted, QcStatus: QcFailed, PackagingStatus: PackagingPending},
			StageFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PipelineStage(tc.in); got != tc.want {
				t.Errorf("PipelineStage() = %q, want %q", got, tc.want)
			}
		})
	}
}
