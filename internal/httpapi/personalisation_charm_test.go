package httpapi

// Whether the customer's page promises a picture.

import (
	"encoding/json"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func jobWithProps(t *testing.T, pairs ...string) gen.ProductionJob {
	t.Helper()
	props := make([]production.LineProp, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		props = append(props, production.LineProp{Name: pairs[i], Value: pairs[i+1]})
	}
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return gen.ProductionJob{PersonalisationProperties: raw}
}

func TestACharmPlankIsNotPromisedAPicture(t *testing.T) {
	// The charm is the part the customer chose by name and the file never
	// draws it, so a preview would be a picture of the product without it.
	job := jobWithProps(t,
		"STEP 4-First Name-", "LOKESH", "STEP 5-Second Name", "SMITHA",
		"Charm", "Warm Embrace")
	if jobHasCharm(job) != true {
		t.Error("a charm order was not recognised as one")
	}
}

func TestAPlainPlankStillGetsItsPicture(t *testing.T) {
	// The regression that matters: every plank without a charm must keep the
	// preview it has today.
	job := jobWithProps(t,
		"STEP 4-First Name-", "LOKESH", "STEP 5-Second Name", "SMITHA",
		"Charm", "No charm")
	if jobHasCharm(job) {
		t.Error("a plain plank was treated as a charm order and lost its preview")
	}
}

func TestAJobWithNoPropertiesAtAllIsNotACharm(t *testing.T) {
	// A job created before the column existed. Reading it as a charm would
	// silently withdraw the preview from older orders.
	if jobHasCharm(gen.ProductionJob{}) {
		t.Error("a job with no stored properties was treated as a charm order")
	}
}

func TestUnreadablePropertiesDoNotWithdrawThePreview(t *testing.T) {
	// Fails to the behaviour every plank has today rather than to "no
	// picture", which is the direction that cannot make a page worse.
	job := gen.ProductionJob{PersonalisationProperties: []byte("{not json")}
	if jobHasCharm(job) {
		t.Error("unreadable properties withdrew the preview")
	}
}
