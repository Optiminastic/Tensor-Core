package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
)

// What the form shows when a brand has configured nothing and the deployment
// was started with credentials.
//
// This is the "pre-filled" behaviour, and it is worth a test because the
// failure is invisible: a missing key here renders as an empty box beside a
// Connected badge, which reads as something nobody filled in rather than as a
// bug.
func TestEnvironmentSettingsWhatsApp(t *testing.T) {
	s := &Server{cfg: config.Settings{
		WhatsAppAccessToken:      "SYSTEM-USER-TOKEN",
		WhatsAppPhoneNumberID:    "3761254503933348",
		WhatsAppWABAID:           "905281876901226",
		WhatsAppAppID:            "1086052380908872",
		WhatsAppAPIVersion:       "v25.0",
		WhatsAppTemplateName:     "cart_recovery_checkout",
		WhatsAppTemplateLanguage: "en",
	}}

	values, secrets := s.environmentSettings(providerWhatsApp)

	for key, want := range map[string]string{
		"phone_number_id":   "3761254503933348",
		"waba_id":           "905281876901226",
		"app_id":            "1086052380908872",
		"api_version":       "v25.0",
		"template_name":     "cart_recovery_checkout",
		"template_language": "en",
	} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}

	// The token is returned - see the note on settingField.Secret for why -
	// and is also NAMED as a secret, which is what makes the form mask it.
	if values["access_token"] != "SYSTEM-USER-TOKEN" {
		t.Errorf("access_token = %q, want it pre-filled", values["access_token"])
	}
	if len(secrets) != 1 || secrets[0] != "access_token" {
		t.Errorf("secrets = %v, want [access_token] - an unnamed secret renders unmasked", secrets)
	}
	// Not configured, so not offered. An empty app_secret must not be reported
	// as a stored one, or the form says "leave blank to keep" about nothing.
	if _, ok := values["app_secret"]; ok {
		t.Error("app_secret should be absent when unset")
	}
}

// A deployment with no WhatsApp credentials must report nothing, so the row
// reads "Not connected" rather than showing a version string and a template
// name and claiming to be configured.
func TestEnvironmentSettingsWhatsAppUnconfigured(t *testing.T) {
	// The version and template carry defaults, so they are always present -
	// which is exactly the case that could look configured by accident.
	s := &Server{cfg: config.Settings{
		WhatsAppAPIVersion:       "v25.0",
		WhatsAppTemplateName:     "cart_recovery_checkout",
		WhatsAppTemplateLanguage: "en",
	}}
	values, secrets := s.environmentSettings(providerWhatsApp)
	if values != nil || secrets != nil {
		t.Errorf("got %v / %v, want nothing - defaults alone are not a connection", values, secrets)
	}
}

func TestEnvironmentSettingsDelhivery(t *testing.T) {
	s := &Server{cfg: config.Settings{DelhiveryAPIKey: "KEY", DelhiveryBaseURL: "https://host"}}
	values, secrets := s.environmentSettings(providerDelhivery)
	if values["base_url"] != "https://host" {
		t.Errorf("base_url = %q", values["base_url"])
	}
	if values["api_key"] != "KEY" {
		t.Errorf("api_key = %q, want it pre-filled", values["api_key"])
	}
	if len(secrets) != 1 || secrets[0] != "api_key" {
		t.Errorf("secrets = %v, want [api_key]", secrets)
	}

	none, noneSecrets := (&Server{}).environmentSettings(providerDelhivery)
	if none != nil || noneSecrets != nil {
		t.Errorf("got %v / %v, want nothing", none, noneSecrets)
	}
}

// Every field an admin can fill must be answerable from somewhere. A required
// field with no environment fallback and no default is one an admin has to
// find in somebody else's console, which is fine - but a field that is
// required AND secret AND has a default would be a credential with a guessable
// value, which is not.
func TestIntegrationSpecsAreCoherent(t *testing.T) {
	for _, spec := range integrationSpecs {
		if !spec.Available {
			continue
		}
		if len(spec.Fields) == 0 {
			t.Errorf("%s is available with no fields to fill", spec.Provider)
		}
		seen := map[string]bool{}
		for _, field := range spec.Fields {
			if seen[field.Key] {
				t.Errorf("%s has two %q fields", spec.Provider, field.Key)
			}
			seen[field.Key] = true

			if field.Label == "" || field.Help == "" {
				t.Errorf("%s/%s needs a label and a help line - these are opaque ids "+
					"from another console", spec.Provider, field.Key)
			}
			if field.Secret && field.Default != "" {
				t.Errorf("%s/%s is a secret with a default, which is a credential "+
					"with a guessable value", spec.Provider, field.Key)
			}
		}
	}
}
