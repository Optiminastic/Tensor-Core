package production

// The machine choice, tested against the shop's own numbers.
//
// The colours here are the real hexes off the live fleet - D3B7A7 is the Latte
// Brown the shop prints GOLD in, 2850E0 is its blue - and the eight-digit forms
// are exactly what BambuBuddy reports.

import (
	"testing"
	"time"
)

func machine(id int, name string, freeIn time.Duration, colours ...string) MachineState {
	return MachineState{
		PrinterID: id, Name: name, Model: "A2L", Online: true,
		RemainingOnCurrent: freeIn, LoadedColours: colours,
	}
}

// The formula, stated as the shop states it.
func TestFreeInIsCurrentJobPlusEverythingQueuedBehindIt(t *testing.T) {
	m := MachineState{
		RemainingOnCurrent: 40 * time.Minute,
		QueuedAhead:        2*time.Hour + 15*time.Minute,
	}
	if got, want := m.FreeIn(), 2*time.Hour+55*time.Minute; got != want {
		t.Errorf("FreeIn = %v, want %v", got, want)
	}
	// An idle printer with nothing queued is free now, not "unknown".
	if got := (MachineState{}).FreeIn(); got != 0 {
		t.Errorf("an idle printer is free in %v, want 0", got)
	}
}

// BambuBuddy's eight-digit tray colour is the same spool as Tensor's #RRGGBB.
//
// The alpha channel says nothing about which filament is loaded, and comparing
// the two raw is how a printer holding exactly the right colour reads as
// holding the wrong one.
func TestNormaliseHex(t *testing.T) {
	for raw, want := range map[string]string{
		"D3B7A7FF":  "D3B7A7", // as BambuBuddy reports a tray
		"#D3B7A7":   "D3B7A7", // as Tensor stores a colour
		"d3b7a7":    "D3B7A7",
		" #2850E0 ": "2850E0",
		"":          "",
		"#FFF":      "", // too short to be a colour
		"NOTAHEX":   "",
		"#GGGGGG":   "",
	} {
		if got := NormaliseHex(raw); got != want {
			t.Errorf("NormaliseHex(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Soonest free wins, among those that can actually print it.
func TestPickEarliestFreeChoosesTheSoonestEligiblePrinter(t *testing.T) {
	machines := []MachineState{
		machine(1, "A1", 3*time.Hour, "D3B7A7FF"),
		machine(2, "A2", 20*time.Minute, "D3B7A7FF"),
		machine(3, "A3", 1*time.Hour, "D3B7A7FF"),
	}
	got := PickEarliestFree(machines, []string{"#D3B7A7"})
	if got.Machine == nil {
		t.Fatalf("no machine chosen: %s", got.Reason)
	}
	if got.Machine.Name != "A2" {
		t.Errorf("chose %s, want A2 - it frees up first", got.Machine.Name)
	}
	if got.FreeIn != 20*time.Minute {
		t.Errorf("FreeIn = %v, want 20m", got.FreeIn)
	}
}

// A printer that is free NOW but holds the wrong filament is not a candidate.
//
// The rule this pins: colour filters, it does not merely score. Printing in the
// wrong colour is scrap; waiting an hour is an hour.
func TestPickEarliestFreeNeverPrefersAnIdlePrinterWithTheWrongColour(t *testing.T) {
	machines := []MachineState{
		machine(1, "IDLE-WRONG", 0, "2850E0FF"),           // free now, blue
		machine(2, "BUSY-RIGHT", 4*time.Hour, "D3B7A7FF"), // busy, the gold we want
	}
	got := PickEarliestFree(machines, []string{"#D3B7A7"})
	if got.Machine == nil {
		t.Fatalf("no machine chosen: %s", got.Reason)
	}
	if got.Machine.Name != "BUSY-RIGHT" {
		t.Errorf("chose %s; an idle printer with the wrong spool is not a candidate at all",
			got.Machine.Name)
	}
}

// Every colour on the plate has to be loaded, not just one of them.
func TestCanPrintNeedsEveryColourOnThePlate(t *testing.T) {
	twoColour := []string{"#D3B7A7", "#FFFFFF"}

	both := machine(1, "both", 0, "D3B7A7FF", "FFFFFFFF")
	if !both.CanPrint(twoColour) {
		t.Error("a printer holding both colours cannot print a two-colour plate")
	}

	half := machine(2, "half", 0, "D3B7A7FF")
	if half.CanPrint(twoColour) {
		t.Error("a printer holding one of two colours accepted a two-colour plate; " +
			"it would stop at the first tool change")
	}
	if missing := half.MissingColours(twoColour); len(missing) != 1 || missing[0] != "FFFFFF" {
		t.Errorf("MissingColours = %v, want [FFFFFF]", missing)
	}
}

// An offline printer is not "free in zero minutes".
func TestPickEarliestFreeSkipsOfflinePrinters(t *testing.T) {
	off := machine(1, "OFFLINE", 0, "D3B7A7FF")
	off.Online = false
	machines := []MachineState{off, machine(2, "ONLINE", 2*time.Hour, "D3B7A7FF")}

	got := PickEarliestFree(machines, []string{"#D3B7A7"})
	if got.Machine == nil || got.Machine.Name != "ONLINE" {
		t.Errorf("chose %v, want ONLINE - an unreachable printer is not a candidate", got.Machine)
	}
}

// When nothing can take it, say what to load.
//
// "no printer has #D3B7A7 loaded" is a job for the floor; "could not queue the
// plate" is a mystery.
func TestPickEarliestFreeExplainsWhyNothingCanTakeIt(t *testing.T) {
	machines := []MachineState{
		machine(1, "A1", 0, "2850E0FF"),
		machine(2, "A2", 0, "FFFFFFFF"),
	}
	got := PickEarliestFree(machines, []string{"#D3B7A7"})
	if got.Machine != nil {
		t.Fatal("a plate printed on a machine with none of its colours")
	}
	if got.Reason != "no printer has #D3B7A7 loaded" {
		t.Errorf("reason = %q, want it to name the missing colour", got.Reason)
	}

	// Every colour exists, but never together on one machine - a different
	// problem with a different fix, so it gets different words.
	split := PickEarliestFree(machines, []string{"#2850E0", "#FFFFFF"})
	if split.Machine != nil {
		t.Fatal("a two-colour plate was placed on a machine holding one of them")
	}
	if split.Reason != "no single printer has every colour this plate needs loaded at once" {
		t.Errorf("reason = %q, want the both-colours wording", split.Reason)
	}
}

// The same snapshot always gives the same answer.
func TestPickEarliestFreeBreaksTiesPredictably(t *testing.T) {
	machines := []MachineState{
		machine(9, "nine", time.Hour, "D3B7A7FF"),
		machine(2, "two", time.Hour, "D3B7A7FF"),
		machine(5, "five", time.Hour, "D3B7A7FF"),
	}
	for i := 0; i < 5; i++ {
		got := PickEarliestFree(machines, []string{"#D3B7A7"})
		if got.Machine == nil || got.Machine.PrinterID != 2 {
			t.Fatalf("tie broke to %v, want printer 2 every time", got.Machine)
		}
	}
}

// A plate with no colour recorded is not blocked on colour.
//
// It has its own problem - the planner refuses to bed it at all - and answering
// "no printer has  loaded" here would be a second, misleading complaint.
func TestPickEarliestFreeWithNoColourRequirement(t *testing.T) {
	machines := []MachineState{machine(1, "A1", time.Hour), machine(2, "A2", 0)}
	got := PickEarliestFree(machines, nil)
	if got.Machine == nil || got.Machine.Name != "A2" {
		t.Errorf("chose %v, want the soonest-free printer", got.Machine)
	}
}
