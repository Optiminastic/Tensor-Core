package production

// Which printer should run this plate: the one that frees up soonest and has
// the right filament in it.
//
// Pure, and deliberately so. Everything here is arithmetic over a snapshot the
// caller gathered - live printer state and BambuBuddy's queue - so the rule can
// be tested without a printer, a database or a network. The caller does the
// I/O; this decides.
//
// The rule the shop asked for, exactly:
//
//	free at = time left on the job it is printing now
//	        + the estimated print time of everything already queued on it
//
// and among the printers that CAN print the plate - meaning their AMS holds
// every colour the plate needs - the one that frees up soonest wins.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MachineState is one printer, as it stands right now.
//
// A snapshot rather than a handle: the picker must not be able to go and ask
// another question halfway through deciding, because then the answer depends on
// when it asked.
type MachineState struct {
	PrinterID int
	Name      string
	// Model is the printer class - "A2L", "H2C", "P2S".
	Model string
	// Online is false for a printer BambuBuddy cannot reach. An unreachable
	// printer is not "free in 0 minutes", it is not a candidate at all.
	Online bool
	// RemainingOnCurrent is what is left of the plate it is printing now.
	// Zero for an idle printer.
	RemainingOnCurrent time.Duration
	// QueuedAhead is the estimated print time of everything already waiting on
	// this printer.
	QueuedAhead time.Duration
	// LoadedColours are the AMS tray colours it can print without anyone
	// touching it, as BambuBuddy reports them.
	LoadedColours []string
}

// FreeIn is how long until this printer can start something new.
//
// The shop's formula, and nothing more: what is on it now, plus what is behind
// that. Deliberately NOT weighted by how good a match the printer is or how
// idle it has been - those were the fleet-scoring heuristics that made
// scheduling unpredictable, and "soonest free" is a promise anyone on the floor
// can check with a stopwatch.
func (m MachineState) FreeIn() time.Duration {
	return m.RemainingOnCurrent + m.QueuedAhead
}

// CanPrint reports whether this printer holds every colour the plate needs.
//
// Every colour, not any: a bed of four planks in two colours needs both loaded,
// because the plate's AMS mapping expects one slot per colour and a missing one
// stops the print at the first tool change rather than at the start.
func (m MachineState) CanPrint(required []string) bool {
	return len(m.MissingColours(required)) == 0
}

// MissingColours are the plate's colours this printer does not have, so an
// operator can be told what to load rather than merely that it will not print.
func (m MachineState) MissingColours(required []string) []string {
	loaded := map[string]bool{}
	for _, c := range m.LoadedColours {
		if key := NormaliseHex(c); key != "" {
			loaded[key] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, want := range required {
		key := NormaliseHex(want)
		if key == "" || seen[key] || loaded[key] {
			continue
		}
		seen[key] = true
		missing = append(missing, key)
	}
	return missing
}

// NormaliseHex reduces a colour to the six hex digits everything compares on.
//
// BambuBuddy reports a tray as "D3B7A7FF" - eight digits, the last two an alpha
// channel that has nothing to do with which spool is in the slot - while Tensor
// stores "#D3B7A7". Comparing those raw is how a printer holding exactly the
// right filament reads as holding the wrong one.
func NormaliseHex(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "#")
	if len(s) == 8 {
		s = s[:6] // drop the alpha channel
	}
	if len(s) != 6 {
		return ""
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return ""
		}
	}
	return s
}

// MachineChoice is the picked printer and why, or why none was picked.
type MachineChoice struct {
	// Machine is nil when nothing can take the plate.
	Machine *MachineState
	// FreeIn is the chosen printer's wait. Meaningless when Machine is nil.
	FreeIn time.Duration
	// Reason explains a refusal in words an operator can act on - which colour
	// is missing and which printers would need it - rather than restating that
	// the plate did not go anywhere.
	Reason string
}

// PickEarliestFree chooses the printer that can print this plate and will be
// free soonest.
//
// Candidates are filtered before they are ranked, never scored together: a
// printer that is idle this second but holds the wrong filament is not a
// slightly worse choice than one that frees up in an hour with the right
// filament, it is not a choice at all. Printing in the wrong colour is scrap,
// and waiting is not.
//
// Ties break on the printer's own id so the same snapshot always yields the
// same answer - a scheduler that shuffles under a stable input is one nobody
// can debug.
func PickEarliestFree(machines []MachineState, required []string) MachineChoice {
	var eligible []MachineState
	offline, wrongColour := 0, 0

	for _, m := range machines {
		switch {
		case !m.Online:
			offline++
		case !m.CanPrint(required):
			wrongColour++
		default:
			eligible = append(eligible, m)
		}
	}

	if len(eligible) == 0 {
		return MachineChoice{Reason: noMachineReason(machines, required, offline, wrongColour)}
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		if a, b := eligible[i].FreeIn(), eligible[j].FreeIn(); a != b {
			return a < b
		}
		return eligible[i].PrinterID < eligible[j].PrinterID
	})

	best := eligible[0]
	return MachineChoice{Machine: &best, FreeIn: best.FreeIn()}
}

// noMachineReason says what would have to change for this plate to print.
func noMachineReason(machines []MachineState, required []string, offline, wrongColour int) string {
	if len(machines) == 0 {
		return "no printers are known to BambuBuddy"
	}
	if wrongColour == 0 && offline > 0 {
		return fmt.Sprintf("all %d printers are offline", offline)
	}

	// Name the colours nobody has. That is the one fact that turns "it did not
	// print" into something a person can fix in a minute.
	nobodyHas := map[string]bool{}
	for _, want := range required {
		key := NormaliseHex(want)
		if key == "" {
			continue
		}
		nobodyHas[key] = true
	}
	for _, m := range machines {
		if !m.Online {
			continue
		}
		for _, c := range m.LoadedColours {
			delete(nobodyHas, NormaliseHex(c))
		}
	}
	if len(nobodyHas) > 0 {
		missing := make([]string, 0, len(nobodyHas))
		for c := range nobodyHas {
			missing = append(missing, "#"+c)
		}
		sort.Strings(missing)
		return "no printer has " + strings.Join(missing, " or ") + " loaded"
	}
	// Every colour exists somewhere, just never all on one machine.
	return "no single printer has every colour this plate needs loaded at once"
}
