package httpapi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func option(serial string, eligible bool, freeIn time.Duration, items int) machineOption {
	return machineOption{
		Machine:      gen.Machine{MachineID: serial, Name: serial},
		Eligible:     eligible,
		FreeAt:       time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC).Add(freeIn),
		PendingItems: items,
	}
}

func TestChooseMachinePicksTheSoonestFree(t *testing.T) {
	options := []machineOption{
		option("P2S-1", true, 3*time.Hour, 0),
		option("P2S-2", true, 20*time.Minute, 0),
		option("P2S-3", true, 90*time.Minute, 0),
	}
	if got := chooseMachine(options); got != 1 {
		t.Fatalf("chose %d (%s), want P2S-2 at 20 minutes",
			got, options[got].Machine.MachineID)
	}
}

// A printer that cannot print the bed must never win, however free it is. This
// is the whole reason eligibility is a gate and not a penalty: as a penalty,
// three free hours would buy a wrong-coloured print.
func TestChooseMachineNeverPicksAnIneligiblePrinterHoweverFree(t *testing.T) {
	options := []machineOption{
		option("A2L-1", false, 0, 0),
		option("A2L-2", true, 4*time.Hour, 0),
	}
	if got := chooseMachine(options); got != 1 {
		t.Fatalf("chose %d, want the eligible printer even at four hours", got)
	}
}

func TestChooseMachineReportsNoneWhenNothingIsEligible(t *testing.T) {
	options := []machineOption{option("A2L-1", false, 0, 0), option("P2S-1", false, 0, 0)}
	if got := chooseMachine(options); got != -1 {
		t.Fatalf("chose %d, want -1 - no machine is a refusal, not a fallback", got)
	}
}

// Five idle P2S units all project "free now". Without a tie-break every bed
// goes to whichever sorts first, and one printer does the work of five.
func TestChooseMachineBreaksAnExactTieOnTheEmptierQueue(t *testing.T) {
	options := []machineOption{
		option("P2S-1", true, 0, 2),
		option("P2S-2", true, 0, 0),
		option("P2S-3", true, 0, 1),
	}
	if got := chooseMachine(options); got != 1 {
		t.Fatalf("chose %d (%s), want the printer with an empty queue",
			got, options[got].Machine.MachineID)
	}
}

// Same fleet, same state, same answer - twice running. An operator being told
// "P2S-3, because it is free soonest" has to be able to trust that pressing
// again would not silently pick another.
func TestChooseMachineIsStableWhenEverythingTies(t *testing.T) {
	options := []machineOption{
		option("P2S-9", true, 0, 0),
		option("P2S-2", true, 0, 0),
		option("P2S-5", true, 0, 0),
	}
	first := chooseMachine(options)
	if second := chooseMachine(options); first != second {
		t.Fatalf("chose %d then %d for an unchanged fleet", first, second)
	}
	if options[first].Machine.MachineID != "P2S-2" {
		t.Errorf("chose %s, want the stable lowest serial", options[first].Machine.MachineID)
	}
}

func TestSortOptionsPutsEligiblePrintersFirstSoonestFirst(t *testing.T) {
	options := []machineOption{
		option("A2L-1", false, 0, 0),
		option("P2S-1", true, 2*time.Hour, 0),
		option("A2L-2", false, 0, 0),
		option("P2S-2", true, 10*time.Minute, 0),
	}
	sortOptions(options)

	want := []string{"P2S-2", "P2S-1", "A2L-1", "A2L-2"}
	for i, w := range want {
		if options[i].Machine.MachineID != w {
			t.Fatalf("position %d = %s, want %s", i, options[i].Machine.MachineID, w)
		}
	}
}

func TestChosenReasonSaysWhenAndHowMuchChoiceThereWas(t *testing.T) {
	sole := option("P2S-1", true, 0, 0)
	if got := chosenReason(sole, []machineOption{sole, option("A2L-1", false, 0, 0)}); got !=
		"free now and the only printer holding this bed's colours" {
		t.Errorf("reason = %q", got)
	}

	many := []machineOption{
		option("P2S-1", true, 0, 0), option("P2S-2", true, 0, 0), option("P2S-3", true, 0, 0),
	}
	got := chosenReason(many[0], many)
	if want := "free now, and holds this bed's colours - 2 other printers could also take it"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
}

func TestHumanMinutesReadsLikeAPersonWouldSayIt(t *testing.T) {
	cases := map[time.Duration]string{
		1 * time.Minute:   "1 minute",
		40 * time.Minute:  "40 minutes",
		60 * time.Minute:  "1 hour",
		95 * time.Minute:  "1h 35m",
		180 * time.Minute: "3 hours",
	}
	for d, want := range cases {
		if got := humanMinutes(d); got != want {
			t.Errorf("humanMinutes(%s) = %q, want %q", d, got, want)
		}
	}
}

// --- the fleet as it actually stands, 2026-09 ---
//
// These pin the behaviour an operator will meet on the real floor, and the
// reason the colour map is not optional. The hexes are what the fourteen
// printers report; the plate hexes are what resolveColourHex writes when the
// map is empty and the filament shelf has nothing, i.e. fallbackColours.

func liveFleetTrays() map[string][]loadedTray {
	white := func(ams, t int) loadedTray { return trayAt(ams, t, "#FFFFFF", "PLA") }
	return map[string][]loadedTray{
		// Five P2S units, white plus the blue every one of them reports.
		"P2S-1": {white(0, 0), trayAt(0, 1, "#2850E0", "PLA")},
		"P2S-2": {white(0, 0), trayAt(0, 1, "#2850E0", "PLA")},
		// An A2L holding the spool BambuBuddy calls Latte Brown - the one the
		// shop uses for GOLD.
		"A2L-1": {white(0, 0), trayAt(0, 1, "#D3B7A7", "PLA")},
		// A printer with nothing but white.
		"H2C-1": {white(0, 0), white(0, 1)},
	}
}

func eligibleCount(slots []meshio.Slot, identities []colourIdentity) int {
	var n int
	for _, trays := range liveFleetTrays() {
		if _, err := bindPlateToTrays(slots, trays, identities, bedColours{}); err == nil {
			n++
		}
	}
	return n
}

// A white-on-white bed needs no colour map at all.
func TestLiveFleetCanAlwaysPrintAWhiteBed(t *testing.T) {
	slots := []meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#FFFFFF", "PLA")}
	if got := eligibleCount(slots, nil); got != 1 {
		t.Errorf("%d printers can print a two-slot white bed, want 1 (only one holds two whites)", got)
	}
}

// THE case that decides whether this feature does anything on day one. Tensor
// writes #1560BD into a BLUE plate; every printer reports #2850E0. With no
// colour map the two never meet, so a blue bed - six of the twelve locked right
// now - can go nowhere, and the refusal has to say so in those terms.
func TestLiveFleetCannotPlaceABlueBedUntilBlueIsMapped(t *testing.T) {
	slots := []meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#1560BD", "PLA")}

	if got := eligibleCount(slots, nil); got != 0 {
		t.Fatalf("%d printers accepted a blue bed with an empty colour map, want 0", got)
	}

	_, err := bindPlateToTrays(slots, liveFleetTrays()["P2S-1"], nil, bedColours{})
	var unserved slotUnservedError
	if !errors.As(err, &unserved) || unserved.Reason != slotUnmapped {
		t.Fatalf("refusal = %v, want the unmapped one - the fix is a colour-map row, "+
			"not a different printer, and the message has to send the operator there", err)
	}

	// One confirmed row, and the same fleet takes it.
	mapped := []colourIdentity{{Name: "BLUE", Hexes: []string{"#1560BD", "#2850E0"}}}
	if got := eligibleCount(slots, mapped); got != 2 {
		t.Errorf("%d printers can print a blue bed once BLUE is mapped, want 2", got)
	}
}

// Gold is the case that proves distance could never have worked: the catalogue
// hex and the spool are 12609 apart because the catalogue is simply wrong.
func TestLiveFleetPlacesAGoldBedOnlyThroughTheMap(t *testing.T) {
	slots := []meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#D4AF37", "PLA")}

	if got := eligibleCount(slots, nil); got != 0 {
		t.Fatalf("%d printers accepted a gold bed unmapped, want 0", got)
	}
	mapped := []colourIdentity{{Name: "GOLD", Hexes: []string{"#D4AF37", "#D3B7A7"}}}
	if got := eligibleCount(slots, mapped); got != 1 {
		t.Errorf("%d printers can print a gold bed once GOLD is mapped, want 1", got)
	}
}

// The bug that sent one gold plate to the same printer five times.
//
// A printer whose last print FAILED is recorded as idle - correct for batching,
// because withholding it there took five of thirteen machines out of planning
// over something that had already happened. But idle is the BEST score this
// picker can give, so a machine failing every plate ranked as the most
// available on the floor. A5 could not home its Z axis and kept being chosen.
func TestAPrinterWhoseLastPrintFailedIsNotChosen(t *testing.T) {
	reason := "The last print failed - check the plate is clear before the next one."
	row := gen.ListFleetMachinesWithFamilyRow{
		ID: uuid.New(), MachineID: "A5", Name: "A5",
		Status:       production.FleetMachineIdle,
		StatusReason: &reason,
	}
	profile := uuid.New()
	row.MachineProfileID = &profile

	s := &Server{}
	got := s.weighMachine(weighInputs{
		Row: row, Now: time.Now(),
		Sliceable: func(string) string { return "" },
	})

	if got.Eligible {
		t.Fatal("a printer that just failed a print was offered as available")
	}
	if !strings.Contains(got.Refusal, "last print failed") {
		t.Errorf("refusal = %q, want it to say the last print failed", got.Refusal)
	}
}

// And it comes back on its own: the sync clears status_reason the moment the
// printer leaves FAILED, so clearing the plate is all it takes.
func TestAPrinterIsOfferedAgainOnceItsFailureClears(t *testing.T) {
	profile := uuid.New()
	row := gen.ListFleetMachinesWithFamilyRow{
		ID: uuid.New(), MachineID: "A5", Name: "A5",
		Status: production.FleetMachineIdle, MachineProfileID: &profile,
		Filaments: []byte(`[{"colour":"#FFFFFF","type":"PLA","ams_id":0,"tray_id":0}]`),
	}

	s := &Server{}
	got := s.weighMachine(weighInputs{
		Row: row, Now: time.Now(),
		Slots:     []meshio.Slot{{Colour: "#FFFFFF", Material: "PLA"}},
		Sliceable: func(string) string { return "" },
	})

	if !got.Eligible {
		t.Fatalf("a healthy idle printer was refused: %q", got.Refusal)
	}
}

// blockedOption is a printer that could have printed the bed and was not
// allowed to - the A5 case: gold in its trays, locked out after a failed print.
func blockedOption(serial, refusal string) machineOption {
	o := option(serial, false, 0, 0)
	o.HoldsColours = true
	o.Refusal = refusal
	return o
}

func TestChosenReasonNamesAPrinterThatHoldsTheColoursButIsBlocked(t *testing.T) {
	// The exact situation that made two gold beds land on one printer look
	// like a scheduling bug: A2 was genuinely the only machine allowed to take
	// them, because the only other one holding gold had failed its last print.
	won := option("A2", true, 8*time.Minute, 0)
	got := chosenReason(won, []machineOption{
		won,
		blockedOption("A5", "the last print failed - check the plate is clear before the next one"),
		option("H1", false, 0, 0), // no gold: must not be mentioned at all
	})

	if !strings.Contains(got, "the only printer holding this bed's colours") {
		t.Errorf("should still say it was the only eligible printer: %q", got)
	}
	if !strings.Contains(got, "A5") || !strings.Contains(got, "the last print failed") {
		t.Errorf("should name A5 and why it was unavailable: %q", got)
	}
	if strings.Contains(got, "H1") {
		t.Errorf("H1 does not hold the colours and must not be offered as a near miss: %q", got)
	}
}

func TestChosenReasonSaysNothingAboutPrintersThatCouldNotPrintTheBedAnyway(t *testing.T) {
	won := option("A2", true, 0, 0)
	got := chosenReason(won, []machineOption{won, option("H1", false, 0, 0)})
	if want := "free now and the only printer holding this bed's colours"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
}

func TestChosenReasonPluralisesSeveralBlockedPrinters(t *testing.T) {
	won := option("A2", true, 0, 0)
	got := chosenReason(won, []machineOption{
		won,
		blockedOption("A5", "this printer is off"),
		blockedOption("A4", "this printer is in maintenance"),
	})
	if !strings.Contains(got, "Other printers also hold this bed's colours but they are unavailable") {
		t.Errorf("plural phrasing wrong: %q", got)
	}
	// Sorted, so the same fleet state always reads the same way.
	if strings.Index(got, "A4") > strings.Index(got, "A5") {
		t.Errorf("blocked printers should be named in a stable order: %q", got)
	}
}
