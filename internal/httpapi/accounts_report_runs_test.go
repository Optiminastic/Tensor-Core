package httpapi

// The register's history: what went, what failed, and the file either way.
//
// Before this, accounts_report_settings kept only the LAST send - one row per
// brand, overwritten daily. "Send me Tuesday's spreadsheet" and "why did
// Thursday not arrive" were both unanswerable, and the only copy of the file
// was in whoever's inbox received it.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func TestIntegrationAFailedRegisterIsRecordedToo(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	var slug string
	if err := store.Pool.QueryRow(ctx, `SELECT slug FROM brands LIMIT 1`).Scan(&slug); err != nil {
		t.Fatalf("brand: %v", err)
	}

	// One sent, one failed, in two different windows.
	noon := time.Date(2026, 10, 29, 6, 30, 0, 0, time.UTC)
	for i, tc := range []struct{ status, detail string }{
		{reportRunSent, ""},
		{reportRunFailed, "smtp: 535 authentication failed"},
	} {
		var det *string
		if tc.detail != "" {
			d := tc.detail
			det = &d
		}
		if _, err := store.Q.InsertAccountsReportRun(ctx, gen.InsertAccountsReportRunParams{
			ID:          uuid.New(),
			BrandSlug:   slug,
			WindowStart: timestamptzAt(noon.AddDate(0, 0, i)),
			WindowEnd:   timestamptzAt(noon.AddDate(0, 0, i+1)),
			Status:      tc.status,
			Detail:      det,
			RowCount:    int32(31 + i),
			OrderCount:  int32(12 + i),
			ToAddresses: []string{"insha@example.com"},
			CcAddresses: []string{}, BccAddresses: []string{},
			Source: reportRunScheduled,
		}); err != nil {
			t.Fatalf("insert run %d: %v", i, err)
		}
	}

	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.AccountsManage.Key()})

	rr := doJSON(router, http.MethodGet, "/brands/"+slug+"/accounts-report/runs", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET runs = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []struct {
			Status  string   `json:"status"`
			Detail  string   `json:"detail"`
			Rows    int      `json:"rows"`
			To      []string `json:"to"`
			HasFile bool     `json:"has_file"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("got %d runs, want 2", len(body.Items))
	}
	// NEWEST FIRST, which is the only order the page reads them in.
	if body.Items[0].Status != reportRunFailed {
		t.Errorf("first row is %q, want the newest run (the failure)", body.Items[0].Status)
	}
	if body.Items[0].Detail == "" {
		t.Error("a failed run carries no reason; that is the question this exists to answer")
	}
	if body.Items[0].HasFile {
		t.Error("a failed run reports a spreadsheet; nothing was ever sent")
	}
	// Recipients are stored AS SENT, so editing the list does not rewrite
	// what last week's mail says it went to.
	if len(body.Items[1].To) != 1 {
		t.Errorf("to = %v, want the addresses the register actually went to", body.Items[1].To)
	}
}

func TestIntegrationARunWithNoStoredFileIsNotDownloadable(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	var slug string
	if err := store.Pool.QueryRow(ctx, `SELECT slug FROM brands LIMIT 1`).Scan(&slug); err != nil {
		t.Fatalf("brand: %v", err)
	}
	id := uuid.New()
	if _, err := store.Q.InsertAccountsReportRun(ctx, gen.InsertAccountsReportRunParams{
		ID: id, BrandSlug: slug,
		WindowStart: timestamptzAt(time.Now().Add(-24 * time.Hour)),
		WindowEnd:   timestamptzAt(time.Now()),
		Status:      reportRunSent, RowCount: 3, OrderCount: 2,
		ToAddresses: []string{"a@example.com"}, CcAddresses: []string{},
		BccAddresses: []string{}, Source: reportRunManual,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.AccountsManage.Key()})

	// 404 and a sentence, rather than a 500 or an empty file: a register sent
	// before the history existed has no spreadsheet, and that is not a fault.
	rr := doJSON(router, http.MethodGet,
		"/brands/"+slug+"/accounts-report/runs/"+id.String()+"/file", token, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("download = %d, want 404 for a run with no file", rr.Code)
	}
}

func TestIntegrationTheHistoryNeedsAccountsManage(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	var slug string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT slug FROM brands LIMIT 1`).Scan(&slug); err != nil {
		t.Fatalf("brand: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	// A register is the shop's whole sales day, down to each customer's name.
	token := minter.mint(t, []string{auth.ProductionRead.Key()})

	rr := doJSON(router, http.MethodGet, "/brands/"+slug+"/accounts-report/runs", token, nil)
	if rr.Code != http.StatusForbidden {
		t.Errorf("GET runs without accounts:manage = %d, want 403", rr.Code)
	}
}
