package httpapi

// The agent's variable names, pinned.
//
// This is the one mistake in the voice path that does not announce itself:
// Sarvam ignores an unknown variable silently, so a misspelled name produces a
// call that connects, sounds fine, and tells the customer about their
// "{{product_name}}". It cost one round of real calls to find, which is why the
// names are now asserted rather than trusted.
//
// The list comes from the agent's own Build > Variables page:
//
//	cart_value, customer_name, item_count, cart_item_name
//
// The product's name is CONFIGURABLE because it has already moved once - it
// shipped as "Product_name", capital P, then became cart_item_name. Renaming
// it refuses every call, because Sarvam matches names exactly. The other three
// have never moved and stay pinned here.

import (
	"reflect"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestAgentVariablesUseTheAgentsOwnNames(t *testing.T) {
	server := &Server{cfg: config.Settings{SarvamProductVariable: "cart_item_name"}}
	got := server.agentVariables(placeVoiceCallRequest{
		CustomerName:   "  Priya  ",
		CustomerNumber: "+919876543210",
		ProductName:    "Dual Name Plank - Blue",
		CartValue:      "2,499",
		ItemCount:      2,
	})

	want := map[string]string{
		"customer_name":  "Priya",
		"cart_item_name": "Dual Name Plank - Blue",
		"cart_value":     "2,499",
		"item_count":     "2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent variables = %#v, want %#v", got, want)
	}
}

func TestEmptyVariablesAreOmittedSoTheAgentUsesItsDefault(t *testing.T) {
	server := &Server{cfg: config.Settings{SarvamProductVariable: "cart_item_name"}}
	got := server.agentVariables(placeVoiceCallRequest{
		CustomerName:   "Arjun",
		CustomerNumber: "+919876543210",
		ProductName:    "   ",
		CartValue:      "",
		ItemCount:      0,
	})

	// Blank beats the agent's default and reads as a gap in the sentence, so
	// nothing empty is sent at all. item_count is the sharp case: zero is a
	// number the agent would happily say out loud.
	want := map[string]string{"customer_name": "Arjun"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent variables = %#v, want %#v", got, want)
	}
}
