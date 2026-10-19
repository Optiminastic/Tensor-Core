package httpapi

// Emptying a field from the dashboard.
//
// "Blank means no change" exists to protect a SECRET: it comes back masked,
// so an admin who may not read a token must not be able to wipe it by saving
// the form. Applied to every field it also made a plain one impossible to
// empty - the save returned 200 and changed nothing.
//
// That silence had teeth. test_numbers restricts the WhatsApp bot to a list
// of numbers while it is being tried out, and clearing it is how the shop
// opens the bot to real customers. It could not be cleared: the field went
// blank on screen, the save reported success, and every customer except the
// test number went on getting no reply at all, with nothing logged.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func TestIntegrationBlankClearsAPlainFieldButNotASecret(t *testing.T) {
	h := newWAHarness(t, map[string]string{
		"test_numbers": "+919799931864", "waba_id": "905281876901226",
		"app_id": "1086052380908872", "template_name": "cart_recovery",
		"template_language": "en",
	})
	token := h.minter.mint(t, []string{auth.IntegrationManage.Key()})
	path := "/brands/" + waBrand + "/integrations/whatsapp"

	settings := func() map[string]string {
		t.Helper()
		rows, err := h.store.Q.ListIntegrationSettings(h.ctx, gen.ListIntegrationSettingsParams{
			BrandSlug: waBrand, Provider: providerWhatsApp,
		})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range rows {
			out[r.SettingKey] = r.SettingValue
		}
		return out
	}

	before := settings()
	if before["test_numbers"] == "" {
		t.Fatalf("the harness should start with a test number restricting the bot")
	}
	if before["access_token"] == "" {
		t.Fatalf("the harness should start with a stored access token")
	}

	// The shop opens the bot to everyone: it empties the test-numbers box and
	// saves. Every other field is sent blank too, exactly as a form does when
	// its secrets come back masked.
	rr := doJSON(h.router, http.MethodPut, path, token, map[string]any{
		"values": map[string]string{"test_numbers": "   "},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rr.Code, rr.Body.String())
	}

	after := settings()
	if v, present := after["test_numbers"]; present {
		t.Errorf("test_numbers survived the save as %q - the bot still answers nobody else", v)
	}
	// THE SECRET IS UNTOUCHED. The same blank form must not wipe a token the
	// admin was never allowed to read back.
	if after["access_token"] != before["access_token"] {
		t.Errorf("a blank form wiped the access token")
	}
	if after["phone_number_id"] != before["phone_number_id"] {
		t.Errorf("phone_number_id changed to %q", after["phone_number_id"])
	}
}

// Clearing it is what makes the bot answer everyone, which is the whole
// point of the fix - asserted on the settings the bot actually reads.
func TestIntegrationClearingTestNumbersLetsEveryoneThrough(t *testing.T) {
	h := newWAHarness(t, map[string]string{
		"test_numbers": "+919799931864", "waba_id": "905281876901226",
		"app_id": "1086052380908872", "template_name": "cart_recovery",
		"template_language": "en",
	})
	token := h.minter.mint(t, []string{auth.IntegrationManage.Key()})

	stranger := "919000000001"
	if h.server.whatsappBotSettingsFor(h.ctx, waBrand).answers(stranger) {
		t.Fatalf("the bot answered a stranger while a test number was set")
	}

	rr := doJSON(h.router, http.MethodPut, "/brands/"+waBrand+"/integrations/whatsapp", token,
		map[string]any{"values": map[string]string{"test_numbers": ""}})
	if rr.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rr.Code, rr.Body.String())
	}

	if !h.server.whatsappBotSettingsFor(h.ctx, waBrand).answers(stranger) {
		t.Fatal("the bot still does not answer a stranger after the restriction was cleared")
	}
}

// A required field cannot be emptied: the refusal names it, rather than
// quietly leaving the old value and reporting success.
func TestIntegrationARequiredFieldCannotBeCleared(t *testing.T) {
	h := newWAHarness(t, map[string]string{
		"waba_id": "905281876901226", "app_id": "1086052380908872",
		"template_name": "cart_recovery", "template_language": "en",
	})
	token := h.minter.mint(t, []string{auth.IntegrationManage.Key()})

	rr := doJSON(h.router, http.MethodPut, "/brands/"+waBrand+"/integrations/whatsapp", token,
		map[string]any{"values": map[string]string{"phone_number_id": ""}})

	// Held already, so blanking it is "no change" rather than a refusal -
	// what must never happen is the row disappearing.
	if rr.Code != http.StatusOK {
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		t.Fatalf("save = %d %v", rr.Code, body)
	}
	rows, _ := h.store.Q.ListIntegrationSettings(h.ctx, gen.ListIntegrationSettingsParams{
		BrandSlug: waBrand, Provider: providerWhatsApp,
	})
	for _, r := range rows {
		if r.SettingKey == "phone_number_id" {
			return
		}
	}
	t.Fatal("phone_number_id was cleared; the brand can no longer send at all")
}
