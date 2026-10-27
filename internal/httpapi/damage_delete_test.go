package httpapi

// Deleting a damage report.
//
// What this mostly guards is the REFUSAL. Deleting a row is easy to get
// right; deleting the only written record of a van that is already booked
// against the shop's account is the mistake worth a test, because nothing
// downstream would notice - the charge turns up on an invoice weeks later
// with nothing to match it to.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func TestIntegrationDeletingADamageReportTakesItsTimelineToo(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)
	admin := h.minter.mint(t, []string{
		auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key(),
		auth.DamageResolve.Key(), auth.DamageDelete.Key(),
	})

	// Give it a timeline worth losing.
	if rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/resolution", support,
		map[string]any{"kind": "closed", "note": "Sorted on the phone."},
	); rr.Code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.Code, rr.Body.String())
	}
	if got := damageStagesFor(t, h, id, support)["closed"]; got != "done" {
		t.Fatalf("expected a timeline before deleting, got %q", got)
	}

	rr := doJSON(h.router, http.MethodDelete,
		"/brands/"+waBrand+"/damage-reports/"+id, admin, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rr.Code, rr.Body.String())
	}

	// Gone from the board...
	if rr := doJSON(h.router, http.MethodGet,
		"/brands/"+waBrand+"/damage-reports/"+id, admin, nil); rr.Code != http.StatusNotFound {
		t.Errorf("the report is still readable: %d", rr.Code)
	}
	// ...and its stages went with it, by the cascade rather than by hand.
	var left int
	if err := h.store.Pool.QueryRow(h.ctx,
		`SELECT count(*) FROM damage_stages WHERE report_id = $1`, id).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d timeline rows outlived the report", left)
	}
}

func TestIntegrationABookedCourierIsNotDeletedAway(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)
	admin := h.minter.mint(t, []string{
		auth.WhatsAppRead.Key(), auth.WhatsAppReply.Key(),
		auth.DamageResolve.Key(), auth.DamageDelete.Key(),
	})
	decideReverse(t, h, id, support)
	if rr := doJSON(h.router, http.MethodPost,
		"/brands/"+waBrand+"/damage-reports/"+id+"/payment", support, nil); rr.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rr.Code, rr.Body.String())
	}

	// A van is booked. Delhivery is not configured in this harness, so the
	// waybill is written the way the booking would write it.
	by := "tester"
	if _, err := h.store.Q.BookDamagePickup(h.ctx, gen.BookDamagePickupParams{
		ID: uuidFromString(t, id), BrandSlug: waBrand,
		PickupWaybill: "69187910002321", BookedBy: &by,
	}); err != nil {
		t.Fatal(err)
	}

	rr := doJSON(h.router, http.MethodDelete,
		"/brands/"+waBrand+"/damage-reports/"+id, admin, nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("delete with a courier booked = %d %s, want 409", rr.Code, rr.Body.String())
	}
	// THE WAYBILL HAS TO BE IN THE REFUSAL. "Cannot delete" leaves somebody
	// with a booking they cannot find; the number is what they cancel.
	if !strings.Contains(rr.Body.String(), "69187910002321") {
		t.Errorf("the refusal does not name the waybill: %s", rr.Body.String())
	}
	// And it really is still there.
	if rr := doJSON(h.router, http.MethodGet,
		"/brands/"+waBrand+"/damage-reports/"+id, admin, nil); rr.Code != http.StatusOK {
		t.Errorf("the report was deleted anyway: %d", rr.Code)
	}
}

func TestIntegrationSupportMayDecideButNotDelete(t *testing.T) {
	h := newWAHarness(t, nil)
	id, support := filedReport(t, h)

	// SEPARATION OF DUTIES, and the reason for a second permission. Support
	// holds damage:resolve - deciding what the shop does about a complaint is
	// their job. Erasing the record that a customer complained is not.
	rr := doJSON(h.router, http.MethodDelete,
		"/brands/"+waBrand+"/damage-reports/"+id, support, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("support deleted a report (%d); damage:delete is admin-only", rr.Code)
	}
}

// uuidFromString parses an id the API handed back as a string.
func uuidFromString(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("not a uuid: %q", s)
	}
	return id
}
