package production

import "testing"

func strptr(s string) *string { return &s }

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
