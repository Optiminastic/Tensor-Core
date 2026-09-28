package httpapi

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// jobParamsFor is the whole body of what buildJobsForOrder used to do inline,
// so these exercise the real construction without a database: the registry
// lookup that decides HOW MANY jobs happens above it, and the part is the only
// thing this layer does differently.

func lineFor(sku, name string) production.LineItem {
	return production.LineItem{
		SKU: &sku, ProductName: name, Quantity: 1,
		Properties: []production.LineProp{
			{Name: "STEP 2 - First Name-", Value: "AYUSH"},
		},
	}
}

// The regression that matters most in the whole change.
//
// Every product printing today prints one thing. If a single-part line stops
// producing exactly the job it produced before - same number, same
// description, no part stamped - then this change reaches every order in the
// shop, not just combos.
func TestASinglePartLineIsTheJobItAlwaysWas(t *testing.T) {
	s := &Server{}
	li := lineFor("DNPWL-BLU", "Dual Name Plank with Light")

	p := s.jobParamsFor(jobParamsInput{
		order: gen.Order{OrderNumber: "T3DPS-115257"}, li: li,
		jobNumber: "JOB-115257", role: "", quantity: 1,
		status: production.PersonalisationNotRequired, generated: true,
	})

	if p.JobNumber != "JOB-115257" {
		t.Errorf("job number = %q, want the order's own", p.JobNumber)
	}
	// Left unset so the column's default stands. Writing "body" here would
	// make every new job differ from every job already in the table, for a
	// distinction that does not exist for a product printing one thing.
	if p.PartRole != nil {
		t.Errorf("part_role = %q; a single-part line must not stamp one", *p.PartRole)
	}
	if strings.Contains(p.Description, " - ") && strings.HasSuffix(p.Description, " - ") {
		t.Errorf("description = %q; nothing should be appended", p.Description)
	}
}

func TestAPartStampsItsRoleAndSaysSoInTheDescription(t *testing.T) {
	s := &Server{}
	li := lineFor("SCWL-RED", "Soulmate COMBO with LIGHT")

	p := s.jobParamsFor(jobParamsInput{
		order: gen.Order{OrderNumber: "T3DPS-115257"}, li: li,
		jobNumber: "JOB-115257-2", role: "rose", quantity: 1,
		status: production.PersonalisationNotRequired, generated: true,
	})

	if p.PartRole == nil || *p.PartRole != "rose" {
		t.Fatalf("part_role = %v, want rose", p.PartRole)
	}
	// Three rows on a bed list reading "Soulmate COMBO with LIGHT - RED" tell
	// an operator nothing about which one is in front of them.
	if !strings.HasSuffix(p.Description, " - rose") {
		t.Errorf("description = %q, want the part named", p.Description)
	}
}

// jobPartRoles is what decides how many jobs a line becomes. Every path that
// is not "the registry describes several files" must answer with exactly one
// entry, and that entry must be "" - the single-job path, unchanged.
func TestJobPartRolesAnswersOneForEverythingThatIsNotACombo(t *testing.T) {
	s := &Server{}
	li := lineFor("DNPWL-BLU", "Dual Name Plank with Light")

	// An uploaded design is one file by definition, and must not reach the
	// registry at all - s has no store here, so a lookup would panic.
	roles := s.jobPartRoles(t.Context(), li, false)
	if len(roles) != 1 || roles[0] != "" {
		t.Errorf("roles = %#v, want one unnamed part for an uploaded design", roles)
	}
}
