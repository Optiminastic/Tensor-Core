package httpapi

// The backstop that frees a job nothing else can reach.
//
// WHAT THESE PIN. The expiry sweep walks LINKS, and only those with status
// 'sent' or 'opened'. Once a link is 'confirmed' or 'expired' the sweep never
// looks at it again - so a job still at awaiting_customer behind a closed link
// used to be reachable by nothing at all, because this backstop stepped aside
// whenever a link existed.
//
// It is not a theoretical hole. The sweep releases a job and then closes its
// link, and that release is fire-and-forget: its error is logged and the link
// is marked 'expired' regardless. One failed release stranded a job for good.
// On production, 2026-10-27, that was 31 queued jobs excluded from every
// planning run and no new batch for hours.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// strandedAfter is deliberately tiny here; the age bound's own behaviour is
// exercised by TestAFreshHoldIsLeftAlone.
var strandedAfterTest = pgtype.Interval{Microseconds: 1, Valid: true}

// seedHeldJob inserts an order, a job held for its customer, and optionally a
// link in the given status. It backdates the job so the age bound passes.
func seedHeldJob(t *testing.T, store *db.Store, name, linkStatus string) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	orderID := uuid.New()
	_, err := store.Pool.Exec(ctx, `
		INSERT INTO orders (id, shopify_order_id, order_number, financial_status,
		                    total_price, currency, brand_slug)
		VALUES ($1, $2, $3, 'paid', 100, 'INR', 'test-brand')`,
		orderID, int64(uuid.New().ID()), name)
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}

	job, err := store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: name, OrderID: &orderID,
		Description: "Held job", Quantity: 1,
		Status:         production.StatusQueued,
		AssemblyStatus: production.AssemblyPending,
		QcStatus:       production.QcPending, PackagingStatus: production.PackagingPending,
		PersonalisationStatus: production.PersonalisationAwaitingCustomer,
		Colours:               []byte("[]"),
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	// Older than the age bound, which otherwise protects a job whose send is
	// still queued behind it.
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET updated_at = now() - interval '1 hour' WHERE id = $1`,
		job.ID); err != nil {
		t.Fatalf("backdate job: %v", err)
	}

	if linkStatus != "" {
		if _, err := store.Pool.Exec(ctx, `
			INSERT INTO personalisation_links
			       (id, brand_slug, order_id, token_hash, status, expires_at)
			VALUES ($1, 'test-brand', $2, $3, $4, now() - interval '1 hour')`,
			uuid.New(), orderID, uuid.NewString(), linkStatus); err != nil {
			t.Fatalf("seed link (%s): %v", linkStatus, err)
		}
	}
	return job.ID
}

func personalisationStatusOf(t *testing.T, store *db.Store, jobID uuid.UUID) string {
	t.Helper()
	var status string
	if err := store.Pool.QueryRow(context.Background(),
		`SELECT personalisation_status FROM production_jobs WHERE id = $1`,
		jobID).Scan(&status); err != nil {
		t.Fatalf("read job: %v", err)
	}
	return status
}

func TestAClosedLinkNoLongerStrandsItsJob(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// THE REGRESSION. Both of these links are closed, so the expiry sweep
	// will never walk them again; before the fix the backstop saw a link and
	// left the job held for ever.
	expired := seedHeldJob(t, store, "STRANDED-EXPIRED", "expired")
	confirmed := seedHeldJob(t, store, "STRANDED-CONFIRMED", "confirmed")
	// The original case: no link was ever written at all.
	noLink := seedHeldJob(t, store, "STRANDED-NOLINK", "")

	freed, err := store.Q.ReleaseStrandedPersonalisationHolds(ctx, strandedAfterTest)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if freed != 3 {
		t.Errorf("freed %d jobs, want 3", freed)
	}
	for name, id := range map[string]uuid.UUID{
		"expired link": expired, "confirmed link": confirmed, "no link": noLink,
	} {
		if got := personalisationStatusOf(t, store, id); got != production.PersonalisationValidated {
			t.Errorf("%s: job is %q, want validated - nothing else will ever free it", name, got)
		}
	}
}

func TestAnOpenLinkStillHoldsItsJob(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// These three are the expiry sweep's business, and releasing them here
	// would print a customer's order while they are still editing it - the
	// single worst outcome this whole mechanism exists to prevent.
	//
	// 'pending' is open too: the link is claimed but not yet delivered, and
	// ReleaseStalePersonalisationClaims owns that case.
	sent := seedHeldJob(t, store, "OPEN-SENT", "sent")
	opened := seedHeldJob(t, store, "OPEN-OPENED", "opened")
	pending := seedHeldJob(t, store, "OPEN-PENDING", "pending")

	freed, err := store.Q.ReleaseStrandedPersonalisationHolds(ctx, strandedAfterTest)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if freed != 0 {
		t.Errorf("freed %d jobs; an open window must not be cut short", freed)
	}
	for name, id := range map[string]uuid.UUID{
		"sent": sent, "opened": opened, "pending": pending,
	} {
		if got := personalisationStatusOf(t, store, id); got != production.PersonalisationAwaitingCustomer {
			t.Errorf("%s link: job is %q, want it still awaiting the customer", name, got)
		}
	}
}

func TestAFreshHoldIsLeftAlone(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	// The age bound keeps this off the sender's heels: a model lands, the job
	// is held, and the send is queued behind it. Without the bound this would
	// release the job between those two moments.
	id := seedHeldJob(t, store, "FRESH-HOLD", "")
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET updated_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("touch job: %v", err)
	}

	freed, err := store.Q.ReleaseStrandedPersonalisationHolds(ctx,
		pgtype.Interval{Microseconds: 15 * 60 * 1_000_000, Valid: true})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if freed != 0 {
		t.Errorf("freed %d jobs; a hold younger than the bound must stand", freed)
	}
}
