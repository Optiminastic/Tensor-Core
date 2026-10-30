package httpapi

// What the queue says about a product the REGISTRY describes.
//
// modelStatusOf used to answer "does Tensor build this?" from the hardcoded
// generatedSKUSegments list, which has no database and so cannot know about a
// product configured in the registry. Everything downstream inherited that:
//
//	JOB-116176  PWDH-R  Design file required - upload the 3MF for this job.
//	                    [ Upload model ]
//
// against a job whose model Tensor was in the middle of rendering. The job
// itself was created correctly - buildJobsForOrder has always asked
// rendersProduct - so this was the queue describing a healthy job as broken,
// and offering an operator an upload that would have replaced a model that
// was on its way.
//
// The answer is now resolved once by the caller, from the registry, and passed
// in. These tests pin both halves: a registry product reads as generating, and
// a product nobody renders still asks for its file.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// awaitingRender is a job as job creation leaves one for a generated product:
// no model yet, and stl_missing recorded because it genuinely has no file -
// it is waiting on its own render, not on a designer.
func awaitingRender(sku, name string) gen.ProductionJob {
	issue := production.IssueSTLMissing
	return gen.ProductionJob{
		Status:                production.StatusQueued,
		Sku:                   &sku,
		ProductName:           &name,
		PersonalisationStatus: production.PersonalisationNotRequired,
		IssueReason:           &issue,
	}
}

func TestARegistryProductReadsAsGeneratingNotAsMissingAFile(t *testing.T) {
	job := awaitingRender("PWDH-R", "Personalized Wall Diya Holder - Lotus")

	// The hardcoded list does not know this product - that is the whole
	// premise of the registry render path, and why the answer has to come
	// from elsewhere.
	if IsGeneratedProduct("PWDH-R", "Personalized Wall Diya Holder - Lotus") {
		t.Fatal("PWDH is in the hardcoded list, so this test no longer covers the bug")
	}

	if got := modelStatusOf(job, true); got != ModelGenerating {
		t.Errorf("modelStatusOf = %q, want %q - the queue said a rendering job "+
			"needed a file uploading", got, ModelGenerating)
	}
	// And therefore nothing is "blocking" it: a render in progress is
	// progress, not a fault to put a red row against.
	if got := batchingBlockedReason(job, true, true); got != nil {
		t.Errorf("batchingBlockedReason = %q, want nil while the model is rendering", *got)
	}
}

func TestAProductNobodyRendersStillAsksForItsFile(t *testing.T) {
	// The other half, and the reason the wording exists at all. Nothing
	// renders this, so the operator's move really is to upload a model.
	job := awaitingRender("THO-PLA-FFF-0022", "Sunrise Lamp")

	if got := modelStatusOf(job, false); got != ModelApprovalRequired {
		t.Errorf("modelStatusOf = %q, want %q", got, ModelApprovalRequired)
	}
	got := batchingBlockedReason(job, true, false)
	if got == nil || *got != UploadDesignFileWording {
		t.Errorf("batchingBlockedReason = %v, want %q", got, UploadDesignFileWording)
	}
}

func TestARenderAlreadyInFlightReadsAsGeneratingEitherWay(t *testing.T) {
	// model_render_queued_at is checked BEFORE the generated question, and
	// three behaviours ride on that order - the queue pill during a rebuild,
	// batchingBlockedReason's early return, and the sibling guard that decides
	// which render re-plates a bed. This pins the order rather than the
	// answer.
	job := awaitingRender("PWDH-R", "Personalized Wall Diya Holder - Lotus")
	job.ModelRenderQueuedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	for _, generated := range []bool{true, false} {
		if got := modelStatusOf(job, generated); got != ModelGenerating {
			t.Errorf("modelStatusOf(generated=%v) = %q, want %q - a queued render is "+
				"in flight whatever the registry says", generated, got, ModelGenerating)
		}
	}
}

func TestAnAttachedModelOutranksTheGeneratedQuestion(t *testing.T) {
	id := uuid.New()
	job := awaitingRender("PWDH-R", "Personalized Wall Diya Holder - Lotus")
	job.PrintFileID = &id

	for _, generated := range []bool{true, false} {
		if got := modelStatusOf(job, generated); got != ModelReady {
			t.Errorf("modelStatusOf(generated=%v) = %q, want %q", generated, got, ModelReady)
		}
	}
}

// generatedByProduct must ask once per DISTINCT product, not once per row -
// rendersProduct reads the registry and this list runs to hundreds of jobs.
func TestIntegrationGeneratedIsResolvedOncePerProduct(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s := registryServer(t, store)

	rows := make([]gen.ProductionJob, 0, 100)
	for i := 0; i < 100; i++ {
		switch i % 2 {
		case 0:
			rows = append(rows, awaitingRender("PWDH-R", "Lotus"))
		default:
			rows = append(rows, awaitingRender("PDHR-P", "Royal"))
		}
	}

	got := s.generatedByProduct(ctx, rows)
	if len(got) != 2 {
		t.Errorf("resolved %d distinct products from 100 jobs over 2, want 2", len(got))
	}
}

func TestIntegrationReadinessReportsWhatIsRenderingAndWhatStopped(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products, production_jobs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key()})

	_, variantID := seedProductWithVariant(t, store, "PWDH", "PWDH-P")
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO variant_designs (id, variant_id, role, template_key)
		 VALUES ($1, $2, 'body', 'dnp_two_heart')`,
		uuid.New(), variantID); err != nil {
		t.Fatalf("design: %v", err)
	}

	// One job mid-render, and one that stopped with the renderer's own words.
	// The SKU is stored in a different case to the one the registry carries,
	// because that is how orders arrive.
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO production_jobs (id, job_number, description, quantity, sku, status,
		    model_render_queued_at)
		 VALUES ($1, 'JOB-1', 'Lotus', 1, 'pwdh-p', 'queued', now())`,
		uuid.New()); err != nil {
		t.Fatalf("rendering job: %v", err)
	}
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO production_jobs (id, job_number, description, quantity, sku, status,
		    model_error)
		 VALUES ($1, 'JOB-2', 'Lotus', 1, 'PWDH-P', 'queued', 'openscad: exit 1')`,
		uuid.New()); err != nil {
		t.Fatalf("failed job: %v", err)
	}

	rr := doJSON(router, http.MethodGet, "/registry/products/PWDH/readiness", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("readiness = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []struct {
			SKU             string   `json:"sku"`
			Ready           bool     `json:"ready"`
			Rendering       int64    `json:"rendering"`
			RenderFailed    int64    `json:"render_failed"`
			LastRenderError string   `json:"last_render_error"`
			Problems        []string `json:"problems"`
			Notes           []string `json:"notes"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("got %d SKUs, want 1", len(body.Items))
	}
	got := body.Items[0]

	if got.Rendering != 1 {
		t.Errorf("rendering = %d, want 1", got.Rendering)
	}
	if got.RenderFailed != 1 {
		t.Errorf("render_failed = %d, want 1 - both jobs carry the same SKU in "+
			"different cases, and an order's SKU arrives however the storefront wrote it",
			got.RenderFailed)
	}
	if got.LastRenderError != "openscad: exit 1" {
		t.Errorf("last_render_error = %q, want the renderer's own words", got.LastRenderError)
	}
	// A stopped render is work somebody has to do, so it is a Problem and the
	// SKU is not ready. One in flight is progress, so it is a Note.
	if got.Ready {
		t.Error("a SKU with a stopped render reports ready")
	}
	if !anyContains(got.Problems, "stopped while building") {
		t.Errorf("problems = %v, want the stopped render named", got.Problems)
	}
	if !anyContains(got.Notes, "building its model now") {
		t.Errorf("notes = %v, want the render in flight named", got.Notes)
	}
}

func anyContains(items []string, want string) bool {
	for _, s := range items {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestAJobHeldForItsCustomerDoesNotClaimToBeBuilding(t *testing.T) {
	job := awaitingRender("PWDH-P", "Personalized Wall Diya Holder - Lotus")
	job.PersonalisationStatus = production.PersonalisationAwaitingCustomer

	// It read as "Creating" for the whole thirty-minute window, which is a
	// claim that work is happening. An operator watching a job say it was
	// being built for half an hour reasonably concluded something had jammed -
	// and the Personalisation column beside it said awaiting_customer, so the
	// screen contradicted itself.
	if got := modelStatusOf(job, true); got != ModelAwaitingCustomer {
		t.Errorf("modelStatusOf = %q, want %q", got, ModelAwaitingCustomer)
	}
	// And it is not "blocked" either: waiting for a customer is progress.
	if got := batchingBlockedReason(job, true, true); got == nil {
		t.Error("batchingBlockedReason = nil; a held job should still explain itself")
	}
}

func TestARebuildInFlightOutranksWaitingForTheCustomer(t *testing.T) {
	// The branch order matters: a job being re-rendered IS being worked on,
	// whatever its personalisation status says. Three behaviours ride on
	// ModelRenderQueuedAt being checked first - see modelStatusOf.
	job := awaitingRender("PWDH-P", "Personalized Wall Diya Holder - Lotus")
	job.PersonalisationStatus = production.PersonalisationAwaitingCustomer
	job.ModelRenderQueuedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	if got := modelStatusOf(job, true); got != ModelGenerating {
		t.Errorf("modelStatusOf = %q, want %q - a render in flight outranks the hold",
			got, ModelGenerating)
	}
}

func TestAProductNobodyRendersIsUnaffectedByTheHold(t *testing.T) {
	// The new branch is gated on `generated`, so a bought-in product still
	// asks for its file rather than claiming to wait on a customer.
	job := awaitingRender("THO-PLA-FFF-0022", "Sunrise Lamp")
	job.PersonalisationStatus = production.PersonalisationAwaitingCustomer

	if got := modelStatusOf(job, false); got != ModelApprovalRequired {
		t.Errorf("modelStatusOf = %q, want %q", got, ModelApprovalRequired)
	}
}
