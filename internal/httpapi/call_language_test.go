package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestACallOpensInTheCustomersStateLanguage(t *testing.T) {
	cases := map[string]string{
		"Maharashtra":    "Marathi",
		"MH":             "Marathi",
		"Gujarat":        "Gujarati",
		"West Bengal":    "Bengali",
		"Tripura":        "Bengali",
		"Karnataka":      "Kannada",
		"Kerala":         "Malayalam",
		"Tamil Nadu":     "Tamil",
		"  tamil  nadu ": "Tamil",
		"Pondicherry":    "Tamil",
		"Andhra Pradesh": "Telugu",
		"Telangana":      "Telugu",
		"Punjab":         "Punjabi",
		"Orissa":         "Odia",
		"Odisha":         "Odia",
		// Every other state, and no state at all, opens in Hindi.
		"Uttar Pradesh": "Hindi",
		"Delhi":         "Hindi",
		"Assam":         "Hindi",
		"Goa":           "Hindi",
		"":              "Hindi",
	}
	for state, want := range cases {
		if got := callLanguageForState(state); got != want {
			t.Errorf("callLanguageForState(%q) = %q, want %q", state, got, want)
		}
	}
}

// The language reaches the agent only once its variable is named - Sarvam
// refuses a whole call over a variable the agent does not define - and then on
// every call, Hindi when there is no state. The state itself is never sent.
func TestTheCallCarriesTheLanguageOnlyWhenConfigured(t *testing.T) {
	req := placeVoiceCallRequest{CustomerName: "Riya", CustomerNumber: "9799931864", CustomerState: "Maharashtra"}

	off := (&Server{cfg: config.Settings{}}).agentVariables(req)
	if _, ok := off["preferred_language"]; ok {
		t.Errorf("language sent with no variable configured: %v", off)
	}

	on := &Server{cfg: config.Settings{SarvamLanguageVariable: "preferred_language"}}
	got := on.agentVariables(req)
	if got["preferred_language"] != "Marathi" {
		t.Errorf("preferred_language = %q, want Marathi", got["preferred_language"])
	}
	for key := range got {
		if key == "customer_state" {
			t.Error("the state was sent; only the language should be")
		}
	}
	req.CustomerState = ""
	if lang := on.agentVariables(req)["preferred_language"]; lang != "Hindi" {
		t.Errorf("no state: preferred_language = %q, want Hindi", lang)
	}
}
