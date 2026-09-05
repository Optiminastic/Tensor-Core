package httpapi

// What BambuBuddy's printer states mean for Tensor's fleet board.
//
// The one that matters is FAILED. BambuBuddy reports it as a fact about the
// LAST PRINT - the machine is connected, at 0%, and its own UI shows it as
// ready - and Tensor used to turn that into an error status. That painted five
// of thirteen machines red and took them out of scheduling entirely, because
// machineCanPrint and plannableMachine both accept only idle or running. Five
// free printers were being withheld from batching over a previous job.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func activePrinter() bambubuddy.Printer { return bambubuddy.Printer{IsActive: true} }

func TestFleetStatusFromPrinterState(t *testing.T) {
	for state, want := range map[string]string{
		"RUNNING": production.FleetMachineRunning,
		"IDLE":    production.FleetMachineIdle,
		"FINISH":  production.FleetMachineIdle,
		"PAUSE":   production.FleetMachineIdle,
		// The point of this file.
		"FAILED": production.FleetMachineIdle,
	} {
		got := fleetStatus(activePrinter(), bambubuddy.Status{Connected: true, State: state}, true)
		if got != want {
			t.Errorf("state %q -> %q, want %q", state, got, want)
		}
	}
}

// Unreachable or disconnected is off, whatever the last state said.
func TestFleetStatusUnreachableIsOff(t *testing.T) {
	running := bambubuddy.Status{Connected: true, State: "RUNNING"}
	if got := fleetStatus(activePrinter(), running, false); got != production.FleetMachineOff {
		t.Errorf("unreachable printer = %q, want off", got)
	}
	if got := fleetStatus(activePrinter(), bambubuddy.Status{State: "RUNNING"}, true); got != production.FleetMachineOff {
		t.Errorf("disconnected printer = %q, want off", got)
	}
	if got := fleetStatus(bambubuddy.Printer{}, running, true); got != production.FleetMachineOff {
		t.Errorf("inactive printer = %q, want off", got)
	}
}

// The advice survives the status change: the machine is schedulable, and
// somebody still has to clear the plate.
func TestStatusReasonStillReportsAFailedPrint(t *testing.T) {
	reason := statusReason(bambubuddy.Status{Connected: true, State: "FAILED"})
	if reason == nil || *reason == "" {
		t.Fatal("a failed last print must still be reported to the floor")
	}
	if statusReason(bambubuddy.Status{Connected: true, State: "IDLE"}) != nil {
		t.Error("an idle printer that has not failed needs no note")
	}
}
