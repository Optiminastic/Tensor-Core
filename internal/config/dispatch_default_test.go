package config

import "testing"

// The one default that, if it flips back, stops the whole floor.
//
// Tensor used to reach a printer two ways: the dispatcher, and a Queue button
// on every bed. The button is gone - sending a bed is the dispatcher's job and
// having two paths meant two senders racing for the same plate - so this flag
// is now the only road from a locked bed to a printer.
//
// Unset, it used to be false. That combination plans beds, locks them, reserves
// their filament and prints NOTHING, with no error anywhere: DispatchReadyBatches
// returns an empty outcome on the first line and logs nothing, so the symptom is
// a quiet floor and a growing Locked column. Nobody would look here.
//
// Opting out is still one line in the environment. Opting in must not be.
func TestAutoDispatchIsOnUnlessSomebodyTurnsItOff(t *testing.T) {
	t.Setenv("BATCH_AUTO_DISPATCH", "")
	if !Load().BatchAutoDispatch {
		t.Fatal("BATCH_AUTO_DISPATCH defaulted to off; with no Queue button, no bed can ever reach a printer")
	}

	// Still a switch, not a behaviour. A shop that wants a person in the loop
	// before filament is committed has to be able to say so.
	t.Setenv("BATCH_AUTO_DISPATCH", "false")
	if Load().BatchAutoDispatch {
		t.Error("BATCH_AUTO_DISPATCH=false did not turn automatic dispatch off")
	}
}

// The other default whose absence is invisible.
//
// ResolveUserAuthz reads role_permissions and never the Go catalog, so a deploy
// that ships a new permission without projecting it grants that permission to
// nobody - including ADMIN, whose "everything" is itself only rows the seed
// writes. Nothing errors. A page is simply missing from the nav, for everyone,
// and the natural reading is that the feature did not deploy.
//
// It was a manual step until the image went distroless and took the shell with
// it, at which point the step could not be performed at all. On by default is
// what makes the deploy self-sufficient; RUN_SEED=false is still there for a
// deployment where a separate step owns the catalog.
func TestTheSeedRunsUnlessSomebodyTurnsItOff(t *testing.T) {
	t.Setenv("RUN_SEED", "")
	if !Load().RunSeed {
		t.Fatal("RUN_SEED defaulted to off; a new permission would reach nobody, ADMIN included, " +
			"and the only symptom is a missing page")
	}

	t.Setenv("RUN_SEED", "false")
	if Load().RunSeed {
		t.Error("RUN_SEED=false did not opt out")
	}
}
