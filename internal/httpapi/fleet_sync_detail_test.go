package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The fleet spans three printer models (A2L, H2C, P2S) across thirteen units.
// Storing only a name and a status made that list unreadable and, worse, left
// the scheduler with no way to tell which plates can run where - a plate sliced
// for one model cannot run on another.
//
// Driven through a stub BambuBuddy rather than the real one so the assertion is
// about Tensor's mapping, not about whichever printers happen to be plugged in.
func bambuStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/printers/":
			_, _ = w.Write([]byte(`[
				{"id":1,"name":"A1","model":"A2L","serial_number":"SER-A1",
				 "ip_address":"192.168.1.11","location":"Rack 1","is_active":true,"nozzle_count":1},
				{"id":4,"name":"H1","model":"H2C","serial_number":"SER-H1",
				 "ip_address":"192.168.1.31","location":"Rack 2","is_active":true,"nozzle_count":2}
			]`))
		case "/api/v1/printers/1/status":
			_, _ = w.Write([]byte(`{"connected":true,"state":"RUNNING","layer_num":5,"total_layers":143}`))
		case "/api/v1/printers/4/status":
			// A printer whose last print failed: connected and idle, so every
			// other check would call it healthy.
			_, _ = w.Write([]byte(`{"connected":true,"state":"FAILED","layer_num":0,"total_layers":143}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestFleetSyncStoresHardwareDetail(t *testing.T) {
	stub := bambuStub(t)
	defer stub.Close()

	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	cfg := config.Settings{
		Environment: "development", AuthAudience: "tensor-core",
		BambuBuddyURL: stub.URL, BambuBuddyAPIKey: "test-key",
	}
	s := NewServer(cfg, store, auth.NewGuards(minter.verifier, ""), nil)

	if _, err := s.SyncFleetFromBambuBuddy(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	machines, err := store.Q.ListFleetMachines(context.Background())
	if err != nil {
		t.Fatalf("list machines: %v", err)
	}
	byName := map[string]gen0Machine{}
	for _, m := range machines {
		byName[m.Name] = gen0Machine{
			Model: m.Model, Location: m.Location, IP: m.IpAddress,
			Nozzles: m.NozzleCount, Status: m.Status, Reason: m.StatusReason,
		}
	}

	a1, ok := byName["A1"]
	if !ok {
		t.Fatalf("A1 was not synced; got %v", keys(byName))
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for _, c := range []struct{ what, got, want string }{
		{"A1 model", str(a1.Model), "A2L"},
		{"A1 location", str(a1.Location), "Rack 1"},
		{"A1 ip", str(a1.IP), "192.168.1.11"},
		{"A1 status", a1.Status, production.FleetMachineRunning},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}
	if a1.Nozzles == nil || *a1.Nozzles != 1 {
		t.Errorf("A1 nozzle_count = %v, want 1", a1.Nozzles)
	}

	// FAILED is a fact about the LAST PRINT, not the printer: such a machine is
	// connected, at 0%, and BambuBuddy's own UI shows it as ready. Tensor used to
	// call it an error, which painted five of thirteen machines red and - the
	// costly half - took them out of scheduling entirely, since machineCanPrint
	// and plannableMachine accept only idle or running.
	//
	// The advice survives as status_reason: somebody still has to clear the
	// plate before the next plate lands on the last one.
	h1, ok := byName["H1"]
	if !ok {
		t.Fatalf("H1 was not synced; got %v", keys(byName))
	}
	if h1.Status != production.FleetMachineIdle {
		t.Errorf("a printer whose last print FAILED = %q, want %q - it is available",
			h1.Status, production.FleetMachineIdle)
	}
	if h1.Reason == nil || *h1.Reason == "" {
		t.Error("a printer whose last print failed must still say the plate needs clearing")
	}
	if str(h1.Model) != "H2C" {
		t.Errorf("H1 model = %q, want H2C", str(h1.Model))
	}
}

// gen0Machine is the handful of columns this test cares about, so the
// assertions do not depend on the generated row type's full shape.
type gen0Machine struct {
	Model, Location, IP *string
	Nozzles             *int32
	Status              string
	Reason              *string
}

func keys(m map[string]gen0Machine) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var _ = json.Marshal
var _ = db.Time
