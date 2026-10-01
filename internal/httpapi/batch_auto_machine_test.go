package httpapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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

// healthyRow is a printer with nothing wrong with it, holding one red spool:
// idle, on, profiled, no failed print behind it, and able to print redBed.
//
// Loaded on purpose, so a test can be about ONE thing. Every refusal in
// weighMachine returns early and the colour gate runs FIRST, so a machine with
// no spools is refused for that - and a test meaning to prove something else
// would pass for the wrong reason.
func healthyRow(serial string) gen.ListFleetMachinesWithFamilyRow {
	profileID := uuid.New()
	ready := production.MachineOnline
	ams, tray := 0, 0
	trays, err := json.Marshal([]loadedTray{{
		Colour: "#FF0000", Type: "PLA", RemainingGrams: 900,
		AmsID: &ams, TrayID: &tray,
	}})
	if err != nil {
		panic(err)
	}
	// An A2L, because that is the middle class and the one most beds route to.
	family := "A2L"
	return gen.ListFleetMachinesWithFamilyRow{
		ID: uuid.New(), MachineID: serial, Name: serial,
		Status:           production.FleetMachineIdle,
		MachineProfileID: &profileID, ProfileStatus: &ready,
		ProfileFamily: &family,
		Filaments:     trays,
	}
}

// redBed is a one-slot plate the printer above can take.
var redBed = []meshio.Slot{{Colour: "#FF0000", Material: "PLA"}}

// One bed per printer at a time.
//
// The shop sends a bed to a machine and the next only once that one is
// actually laying plastic. Stacking a queue per printer commits a bed hours
// before it runs, which is exactly when the reasons for choosing that machine
// stop being true: the spools get swapped, a job goes on hold, a faster
// printer frees up.
func TestAPrinterWithABedAlreadyWaitingIsNotOfferedAnother(t *testing.T) {
	s := &Server{}
	row := healthyRow("H2C-1")

	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
		Sliceable: func(string) string { return "" },
		// One plate pending on this printer in BambuBuddy.
		PrinterIDs: map[string]int{"H2C-1": 11},
		Load:       map[int]queueLoad{11: {Items: 1, Minutes: 120}},
	})

	if opt.Eligible {
		t.Error("a printer with a bed already waiting was offered another")
	}
	if !strings.Contains(opt.Refusal, "already has a bed waiting") {
		t.Errorf("refusal = %q, want it to say the printer is already holding one", opt.Refusal)
	}
}

// The case that makes the rule useful rather than merely strict.
//
// A printer PRINTING is not a printer with a backlog - it is the machine
// working. queueMinutesByPrinter counts only QueuePending, so the next bed
// goes the moment the last one starts, which is the whole point.
func TestAPrinterThatIsPrintingWithNothingQueuedStillTakesTheNextBed(t *testing.T) {
	s := &Server{}
	row := healthyRow("H2C-2")
	remaining := int32(45)
	row.RemainingMinutes = &remaining
	row.RemainingObservedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
		Sliceable:  func(string) string { return "" },
		PrinterIDs: map[string]int{"H2C-2": 12},
		// Nothing PENDING: the plate on the bed is carried by remaining
		// minutes, not by the queue.
		Load: map[int]queueLoad{},
	})

	if !opt.Eligible {
		t.Errorf("a printing machine with an empty queue was refused: %q", opt.Refusal)
	}
}

// A bed Tensor has sent but BambuBuddy's queue cannot see yet counts too, or
// five beds sent in the minutes before the first is sliced all pick the same
// printer - the exact failure freeAtFor's in-flight term was added for.
func TestABedInFlightCountsAgainstItsPrinter(t *testing.T) {
	s := &Server{}
	row := healthyRow("A2L-9")

	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
		Sliceable: func(string) string { return "" },
		InFlight:  map[uuid.UUID]int{row.ID: 1},
	})

	if opt.Eligible {
		t.Error("a printer with a bed in flight to it was offered another")
	}
	if !strings.Contains(opt.Refusal, "already has a bed waiting") {
		t.Errorf("refusal = %q; this must be the waiting rule, not the colour gate", opt.Refusal)
	}
}

// The cap is a setting, not a law: zero means "do not apply it", so the rule
// can be turned off without deleting the code that implements it.
func TestAZeroWaitingCapDisablesTheRule(t *testing.T) {
	s := &Server{}
	row := healthyRow("P2S-4")

	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: 0,
		Sliceable:  func(string) string { return "" },
		PrinterIDs: map[string]int{"P2S-4": 4},
		Load:       map[int]queueLoad{4: {Items: 3, Minutes: 300}},
	})

	if !opt.Eligible {
		t.Errorf("the rule applied with the cap off: %q", opt.Refusal)
	}
}

// A plate may never go to a machine with a SMALLER bed.
//
// A fit, not a preference. The plate's offsets are fixed and the slicer is
// told not to rearrange them, so four planks packed on an A2L's 330x320 come
// out 270x270 and simply cannot print on a P2S's 256x256. A bed reached P4
// that way and BambuBuddy answered "G-code conflicts detected after slicing".
//
// The rule is one-directional: see TestAPlateIsOfferedALargerBedAsAFallback
// for the other half, which was refused for a long time and did not need to be.
func TestAPlateIsRefusedByAMachineWithASmallerBed(t *testing.T) {
	s := &Server{}
	row := healthyRow("P2S-7") // healthyRow is an A2L
	p2s := "P2S"
	row.ProfileFamily = &p2s

	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
		Sliceable: func(string) string { return "" },
		BedFamily: "A2L", // the plate was laid out for an A2L
	})

	if opt.Eligible {
		t.Error("an A2L plate was offered to a P2S; it physically cannot print there")
	}
	if !strings.Contains(opt.Refusal, "laid out for a A2L") {
		t.Errorf("refusal = %q, want it to name the class the plate needs", opt.Refusal)
	}
}

func TestAPlateIsAcceptedByItsOwnClass(t *testing.T) {
	s := &Server{}
	opt := s.weighMachine(weighInputs{
		Row: healthyRow("A2L-3"), Slots: redBed, Now: time.Now(),
		WaitingCap: maxBedsWaitingPerMachine,
		Sliceable:  func(string) string { return "" },
		BedFamily:  "A2L",
	})
	if !opt.Eligible {
		t.Errorf("an A2L plate was refused by an A2L: %q", opt.Refusal)
	}
}

// A bed planned before classes existed, or built by the optimiser, carries no
// class. Those must still be offered to the fleet rather than stranded.
func TestABedWithNoClassIsOfferedToAnyMachine(t *testing.T) {
	s := &Server{}
	opt := s.weighMachine(weighInputs{
		Row: healthyRow("A2L-4"), Slots: redBed, Now: time.Now(),
		WaitingCap: maxBedsWaitingPerMachine,
		Sliceable:  func(string) string { return "" },
		BedFamily:  "",
	})
	if !opt.Eligible {
		t.Errorf("a bed with no recorded class was stranded: %q", opt.Refusal)
	}
}

// oversizedOption is an eligible printer whose bed is bigger than the plate.
func oversizedOption(serial string, freeIn time.Duration, items int) machineOption {
	o := option(serial, true, freeIn, items)
	o.Oversized = true
	return o
}

// The half of the rule that was missing: a plate sits inside a larger bed
// unchanged, so a bigger machine can print it. Refusing it there kept three
// H2Cs idle, because a bed is only ever laid out for an H2C at five units and
// the planner's cap makes five impossible.
//
// The larger bed here is the A2L, not the H2C. An H2C's two nozzles both reach
// only X 25..325, so it takes plates laid out for ITSELF and nothing else -
// see TestAnH2CRefusesAPlateLaidOutForAnotherClass.
func TestAPlateIsOfferedALargerBedAsAFallback(t *testing.T) {
	s := &Server{}
	row := healthyRow("A2L-1") // healthyRow is an A2L

	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
		Sliceable: func(string) string { return "" },
		BedFamily: "P2S", // laid out for the smallest bed
	})

	if !opt.Eligible {
		t.Fatalf("a P2S plate was refused an A2L: %q", opt.Refusal)
	}
	if !opt.Oversized {
		t.Error("the A2L was not marked oversized, so the picker would treat it as an equal")
	}
}

// An H2C takes a SMALLER bed's plate and refuses a WIDER one.
//
// A 256mm P2S plate has room inside the 300mm both its nozzles reach, so it is
// offered. That the plate is currently laid out from X=0 while the H2C starts
// at X=25 is fixed by re-plating when it is SENT, not by hiding the printer
// here - refusing it here was briefly worse than the bug it guarded against:
// every H2C greyed out for every small bed, so no machine could be selected and
// the send path that re-plates was unreachable.
//
// An A2L plate is 330 wide and does not fit 300 however it is laid out.
func TestAnH2CTakesASmallerPlateAndRefusesAWiderOne(t *testing.T) {
	s := &Server{}
	row := healthyRow("H2C-1")
	h2c := "H2C"
	row.ProfileFamily = &h2c

	weigh := func(family string) machineOption {
		return s.weighMachine(weighInputs{
			Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
			Sliceable: func(string) string { return "" },
			BedFamily: family,
		})
	}
	if opt := weigh("P2S"); !opt.Eligible {
		t.Errorf("an H2C refused a P2S plate it has room for: %q", opt.Refusal)
	}
	if opt := weigh("A2L"); opt.Eligible {
		t.Error("an H2C accepted a 330mm A2L plate; both nozzles only reach 300mm")
	}
}

func TestAnExactClassPrinterIsNeverMarkedOversized(t *testing.T) {
	s := &Server{}
	row := healthyRow("A2L-1")
	opt := s.weighMachine(weighInputs{
		Row: row, Slots: redBed, Now: time.Now(), WaitingCap: maxBedsWaitingPerMachine,
		Sliceable: func(string) string { return "" },
		BedFamily: "A2L",
	})
	if !opt.Eligible || opt.Oversized {
		t.Errorf("eligible=%v oversized=%v, want an exact match ranked in the first tier",
			opt.Eligible, opt.Oversized)
	}
}

// The tier is the whole point. "Max bed utilisation" means a three-plank plate
// does not occupy an H2C while a P2S could have it - even a P2S that is busy.
func TestAnExactClassPrinterBeatsALargerOneEvenWhenItIsBusier(t *testing.T) {
	options := []machineOption{
		oversizedOption("H2C-1", 0, 0),        // free right now
		option("P2S-1", true, 4*time.Hour, 0), // its own class, hours away
	}
	if got := chooseMachine(options); got != 1 {
		t.Errorf("chose %d, want the P2S - a larger bed is a fallback, not a peer", got)
	}
}

func TestALargerBedWinsWhenNoPrinterOfTheBedsOwnClassCanTakeIt(t *testing.T) {
	options := []machineOption{
		option("P2S-1", false, 0, 0), // own class, refused
		oversizedOption("H2C-1", 30*time.Minute, 0),
	}
	if got := chooseMachine(options); got != 1 {
		t.Errorf("chose %d, want the H2C - nothing of the bed's own class was available", got)
	}
}

// Inside the fallback tier the ordinary rules still decide, unchanged.
func TestAmongLargerBedsTheSoonestFreeStillWins(t *testing.T) {
	t.Run("free time", func(t *testing.T) {
		options := []machineOption{
			oversizedOption("H2C-1", 3*time.Hour, 0),
			oversizedOption("H2C-2", 20*time.Minute, 0),
		}
		if got := chooseMachine(options); got != 1 {
			t.Errorf("chose %d, want the sooner one", got)
		}
	})
	t.Run("queue depth breaks a tie", func(t *testing.T) {
		options := []machineOption{
			oversizedOption("H2C-1", time.Hour, 2),
			oversizedOption("H2C-2", time.Hour, 0),
		}
		if got := chooseMachine(options); got != 1 {
			t.Errorf("chose %d, want the emptier queue", got)
		}
	})
	t.Run("then a stable name", func(t *testing.T) {
		options := []machineOption{
			oversizedOption("H2C-2", time.Hour, 0),
			oversizedOption("H2C-1", time.Hour, 0),
		}
		if got := chooseMachine(options); got != 1 {
			t.Errorf("chose %d, want the same answer every time", got)
		}
	})
}

// A bed planned before classes existed is offered to anything, and must not be
// mistaken for a plate that was downgraded to a bigger machine.
func TestABedWithNoClassIsNeverMarkedOversized(t *testing.T) {
	s := &Server{}
	opt := s.weighMachine(weighInputs{
		Row: healthyRow("A2L-1"), Slots: redBed, Now: time.Now(),
		WaitingCap: maxBedsWaitingPerMachine,
		Sliceable:  func(string) string { return "" },
		BedFamily:  "",
	})
	if !opt.Eligible || opt.Oversized {
		t.Errorf("eligible=%v oversized=%v, want it offered as an ordinary choice",
			opt.Eligible, opt.Oversized)
	}
}

func TestChoiceReasonSaysWhyABedWentToALargerMachine(t *testing.T) {
	if note := oversizedNote(oversizedOption("H2C-1", 0, 0), "P2S"); !strings.Contains(note, "P2S") {
		t.Errorf("note = %q, want it to name the class the plate was laid out for", note)
	}
	if note := oversizedNote(option("P2S-1", true, 0, 0), "P2S"); note != "" {
		t.Errorf("note = %q, want nothing for an ordinary choice", note)
	}
	// No class on the bed, so there is no smaller class to name.
	if note := oversizedNote(oversizedOption("H2C-1", 0, 0), ""); note != "" {
		t.Errorf("note = %q, want nothing when the bed states no class", note)
	}
}

func TestSortOptionsRanksALargerBedBelowAnExactMatch(t *testing.T) {
	options := []machineOption{
		oversizedOption("H2C-1", 0, 0),
		option("P2S-1", true, 2*time.Hour, 0),
	}
	sortOptions(options)
	if options[0].Machine.MachineID != "P2S-1" {
		t.Errorf("first = %s, want the exact class first so the list reads as the picker chooses",
			options[0].Machine.MachineID)
	}
}

// The nozzle map, which Bambu Studio otherwise decides for itself.
//
// Left alone it slices in "Auto For Flush" mode, and on this fleet that put
// every filament on one nozzle - filament_map ["1","1","1"] on a machine with
// two extruders and a spool loaded on each. The second nozzle was never used.
func TestTheNozzleMapPinsTheFixedSpoolToItsOwnNozzle(t *testing.T) {
	idx := int32(1) // the external spool feeds PHYSICAL extruder 1
	machine := gen.Machine{FixedNozzleIndex: &idx}
	// Slot 1 is the plank body, bound to the external spool; slot 2 is the
	// lettering, bound to an AMS tray.
	assignments := []slotAssignment{
		{SlotIndex: 0, AmsIndex: amsExternalSpool, TrayHex: "#FFFFFF"},
		{SlotIndex: 1, AmsIndex: 6, TrayHex: "#D3C5A3"},
	}

	// The H2C's own map, as every file Bambu Studio produced for these
	// printers carries it: logical 1 is physical 1, logical 2 is physical 0.
	got := nozzleMapOverrides(machine, assignments, []int{1, 0})
	if got == nil {
		t.Fatal("no overrides sent, so the slicer would map the nozzles itself")
	}
	if got["filament_map_mode"] != "Manual" {
		t.Errorf("mode = %v, want Manual - Auto For Flush is what ignored the second nozzle",
			got["filament_map_mode"])
	}
	// "1", not "2". physical_extruder_map is [1,0] on an H2C, so the physical
	// index the external spool feeds is LOGICAL 1. Adding one to the physical
	// index, which this used to do, named the AMS nozzle - and Bambu Studio's
	// own slice of this plate puts the external colour on 1 and the AMS colour
	// on 2.
	want := []string{"1", "2"}
	mapping, _ := got["filament_map"].([]string)
	if len(mapping) != len(want) || mapping[0] != want[0] || mapping[1] != want[1] {
		t.Errorf("filament_map = %v, want %v - the body on the fixed nozzle, the colour on the other",
			mapping, want)
	}
}

// The swap is the whole point: an identity map and the H2C's map must not
// produce the same answer, or the conversion is not happening.
func TestTheNozzleMapFollowsThePresetsExtruderOrder(t *testing.T) {
	idx := int32(1)
	machine := gen.Machine{FixedNozzleIndex: &idx}
	assignments := []slotAssignment{
		{SlotIndex: 0, AmsIndex: amsExternalSpool, TrayHex: "#FFFFFF"},
		{SlotIndex: 1, AmsIndex: 6, TrayHex: "#D3C5A3"},
	}

	swapped, _ := nozzleMapOverrides(machine, assignments, []int{1, 0})["filament_map"].([]string)
	identity, _ := nozzleMapOverrides(machine, assignments, []int{0, 1})["filament_map"].([]string)
	if swapped[0] == identity[0] {
		t.Fatalf("both maps gave %q for the fixed nozzle; physical_extruder_map is being ignored",
			swapped[0])
	}
	if swapped[0] != "1" || identity[0] != "2" {
		t.Errorf("swapped = %v, identity = %v; want the fixed nozzle at 1 and 2 respectively",
			swapped, identity)
	}
}

// A physical index the preset does not place must not be guessed at: pinning
// the wrong nozzle prints every colour from the wrong spool.
func TestNoNozzleMapWhenThePresetDoesNotPlaceThatNozzle(t *testing.T) {
	idx := int32(3)
	got := nozzleMapOverrides(gen.Machine{FixedNozzleIndex: &idx}, []slotAssignment{
		{AmsIndex: amsExternalSpool, TrayHex: "#FFFFFF"}, {AmsIndex: 6, TrayHex: "#D3C5A3"},
	}, []int{1, 0})
	if got != nil {
		t.Errorf("overrides = %v, want none when the preset does not place that extruder", got)
	}
}

// The prime tower is where a nozzle purges the colour before it. BambuBuddy's
// H2C pipeline ships it off, and its G-code for a two-colour plate carries no
// prime tower at all where Bambu Studio's does.
func TestThePrimeTowerIsOnForATwoColourPlate(t *testing.T) {
	idx := int32(1)
	got := nozzleMapOverrides(gen.Machine{FixedNozzleIndex: &idx}, []slotAssignment{
		{AmsIndex: amsExternalSpool, TrayHex: "#FFFFFF"}, {AmsIndex: 6, TrayHex: "#D3C5A3"},
	}, []int{1, 0})
	if got["enable_prime_tower"] != "1" {
		t.Errorf("enable_prime_tower = %v, want \"1\" - without it each colour change bleeds",
			got["enable_prime_tower"])
	}
}

// One colour never purges, so a tower would be plastic and minutes for nothing.
func TestNoPrimeTowerForASingleColourPlate(t *testing.T) {
	idx := int32(1)
	got := nozzleMapOverrides(gen.Machine{FixedNozzleIndex: &idx}, []slotAssignment{
		{AmsIndex: amsExternalSpool, TrayHex: "#FFFFFF"}, {AmsIndex: 6, TrayHex: "#FFFFFF"},
	}, []int{1, 0})
	if _, ok := got["enable_prime_tower"]; ok {
		t.Errorf("overrides = %v, want no prime tower when the plate never changes colour", got)
	}
}

// A printer with one extruder has nothing to map, and describing a second
// nozzle to it would describe a machine that does not exist.
func TestNoNozzleMapForASingleNozzleMachine(t *testing.T) {
	got := nozzleMapOverrides(gen.Machine{}, []slotAssignment{{AmsIndex: 6}}, nil)
	if got != nil {
		t.Errorf("overrides = %v, want none for a one-nozzle printer", got)
	}
}

// Every colour off the AMS: there is nothing on the fixed spool to pin, so the
// slicer is left to arrange them rather than told to stack them on one nozzle.
func TestNoNozzleMapWhenNothingUsesTheFixedSpool(t *testing.T) {
	idx := int32(1)
	got := nozzleMapOverrides(gen.Machine{FixedNozzleIndex: &idx}, []slotAssignment{
		{AmsIndex: 6}, {AmsIndex: 7},
	}, []int{1, 0})
	if got != nil {
		t.Errorf("overrides = %v, want none when no slot comes off the fixed spool", got)
	}
}

// extruder_ams_count: BambuBuddy's H2C preset carries no AMS keys, so its
// slices tell the printer there is no AMS on either nozzle. This reproduces
// what Bambu Studio writes for the same machine, indexed by LOGICAL extruder.
func TestTheAMSTopologyMatchesBambuStudiosForAnH2C(t *testing.T) {
	idx := int32(1)
	got := nozzleMapOverrides(gen.Machine{FixedNozzleIndex: &idx}, []slotAssignment{
		{AmsIndex: amsExternalSpool, TrayHex: "#FFFFFF"}, {AmsIndex: 6, TrayHex: "#D3C5A3"},
	}, []int{1, 0})

	topology, _ := got["extruder_ams_count"].([]string)
	want := []string{"1#1|4#0", "1#0|4#1"}
	if len(topology) != 2 || topology[0] != want[0] || topology[1] != want[1] {
		t.Fatalf("extruder_ams_count = %v, want %v - Bambu Studio's own value for this printer",
			topology, want)
	}
}

// A one-nozzle machine states a one-entry map. Describing a second extruder
// would describe a machine that does not exist.
func TestNoAMSTopologyForASingleNozzleMachine(t *testing.T) {
	if got, ok := amsTopologyFor(1, []int{0}); ok {
		t.Errorf("topology = %v, want none for a one-extruder preset", got)
	}
}
