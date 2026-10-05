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
