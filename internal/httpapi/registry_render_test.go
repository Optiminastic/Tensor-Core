package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// plankPlan must produce exactly what the DNP path always produced.
//
// The regression that matters most in this change: a plank rendered after it
// must be byte-identical to one rendered before. If plankPlan drops or renames
// an argument, every plank on the floor changes shape and the tests that cover
// the renderer would not notice, because they test the renderer.
func TestPlankPlanCarriesWhatTheDNPPathAlwaysDid(t *testing.T) {
	params, err := personalise.ParamsFromProperties([]production.LineProp{
		{Name: "STEP 3-", Value: "2 RED HEART"},
		{Name: "STEP 4-First Name-", Value: "AMENA"},
		{Name: "STEP 5-Second Name", Value: "SALMAN"},
	})
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	plan := plankPlan(params)

	if plan.Template != params.Template {
		t.Errorf("template = %q, want %q", plan.Template, params.Template)
	}
	// The arguments are Params.Args() verbatim. Not "equivalent to" - the same
	// call, so the two cannot drift.
	want := params.Args()
	if len(plan.Args) != len(want) {
		t.Fatalf("args = %v, want %v", plan.Args, want)
	}
	for k, v := range want {
		if plan.Args[k] != v {
			t.Errorf("args[%s] = %q, want %q", k, plan.Args[k], v)
		}
	}
	// PART is the caller's, because the two coloured passes differ only by it.
	if _, ok := plan.Args["PART"]; ok {
		t.Error("PART is in Args; it belongs to argsForPart, which is called twice")
	}

	// The record beside the file keeps its old shape, or batch_rebuild's
	// "does this model still match the order" comparison silently changes
	// meaning for every plank already on disk.
	if plan.Stored.NameLeft != "AMENA" || plan.Stored.NameRight != "SALMAN" ||
		plan.Stored.Hearts != 2 || plan.Stored.Template != params.Template {
		t.Errorf("stored = %+v; a plank's record must read as it always did", plan.Stored)
	}
	if len(plan.Stored.Args) != 0 {
		t.Error("a plank's record carries mapped Args; that field is the registry path's")
	}
}

// A plank's stored record must serialise exactly as it did before Args existed,
// or every model already on disk starts comparing unequal to itself.
func TestPlankStoredRecordJSONIsUnchanged(t *testing.T) {
	raw, err := json.Marshal(storedRenderParams{
		Template: "dnp_two_heart", Hearts: 2, NameLeft: "AMENA", NameRight: "SALMAN",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "args") {
		t.Errorf("json = %s; omitempty must keep the registry field out of a plank's record", got)
	}
	for _, want := range []string{`"template":"dnp_two_heart"`, `"hearts":2`,
		`"name_left":"AMENA"`, `"name_right":"SALMAN"`} {
		if !strings.Contains(got, want) {
			t.Errorf("json = %s, missing %s", got, want)
		}
	}
}

func TestArgsForPartAddsPartWithoutMutating(t *testing.T) {
	plan := renderPlan{Args: map[string]string{"NAME_L": `"AMENA"`}}
	base := plan.argsForPart(personalise.PartBase)
	text := plan.argsForPart(personalise.PartText)

	if base["PART"] != `"base"` || text["PART"] != `"text"` {
		t.Errorf("PART = %q / %q", base["PART"], text["PART"])
	}
	// The two passes are over the SAME arguments. A shared map would have the
	// second render overwrite the first's PART, and since base is rendered
	// first the lettering would be rendered twice - a plank with no plate.
	if _, ok := plan.Args["PART"]; ok {
		t.Error("argsForPart mutated the plan; the second pass would corrupt the first")
	}
	if base["NAME_L"] != `"AMENA"` || text["NAME_L"] != `"AMENA"` {
		t.Error("the mapped arguments did not reach both passes")
	}
}

// compareRenderParams must not answer "correct" just because a registry
// product has no plank fields to disagree about.
func TestCompareRenderParamsHandlesBothShapes(t *testing.T) {
	for _, c := range []struct {
		name       string
		have, want storedRenderParams
		wantState  modelState
	}{
		{
			name:      "plank unchanged",
			have:      storedRenderParams{Template: "dnp_two_heart", Hearts: 2, NameLeft: "A", NameRight: "B"},
			want:      storedRenderParams{Template: "dnp_two_heart", Hearts: 2, NameLeft: "A", NameRight: "B"},
			wantState: modelCorrect,
		},
		{
			name:      "plank renamed",
			have:      storedRenderParams{Template: "dnp_two_heart", NameLeft: "A"},
			want:      storedRenderParams{Template: "dnp_two_heart", NameLeft: "AMINA"},
			wantState: modelWrong,
		},
		{
			name:      "registry unchanged",
			have:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"A"`}},
			want:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"A"`}},
			wantState: modelCorrect,
		},
		{
			name:      "registry value changed",
			have:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"A"`}},
			want:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"B"`}},
			wantState: modelWrong,
		},
		{
			name:      "registry template changed",
			have:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"A"`}},
			want:      storedRenderParams{Template: "keychain_v2", Args: map[string]string{"TEXT": `"A"`}},
			wantState: modelWrong,
		},
		{
			// The product gained a mapping since its model was built. Falling
			// through to the plank fields would find them all equal and empty
			// and call a stale model correct.
			name:      "gained a mapping",
			have:      storedRenderParams{Template: "keychain"},
			want:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"A"`}},
			wantState: modelWrong,
		},
		{
			// A combo's parts share a product and an order. Two of them
			// mapping the same variable name - which the rose and the keychain
			// will, both declaring TEXT - would otherwise compare equal, and
			// the bed would keep the wrong model believing it had checked.
			name:      "a different part of the same product",
			have:      storedRenderParams{Template: "rose", Role: "rose", Args: map[string]string{"TEXT": `"AJ"`}},
			want:      storedRenderParams{Template: "rose", Role: "keychain", Args: map[string]string{"TEXT": `"AJ"`}},
			wantState: modelWrong,
		},
		{
			name:      "the same part",
			have:      storedRenderParams{Template: "rose", Role: "rose", Args: map[string]string{"TEXT": `"AJ"`}},
			want:      storedRenderParams{Template: "rose", Role: "rose", Args: map[string]string{"TEXT": `"AJ"`}},
			wantState: modelCorrect,
		},
		{
			// Every model rendered before parts existed carries no role, and a
			// product with one design file renders as 'body'. These must
			// compare equal, or the first check after this ships calls every
			// model on every bed wrong and rebuilds the lot.
			name:      "no role recorded, rendered as body",
			have:      storedRenderParams{Template: "keychain", Args: map[string]string{"TEXT": `"A"`}},
			want:      storedRenderParams{Template: "keychain", Role: "body", Args: map[string]string{"TEXT": `"A"`}},
			wantState: modelCorrect,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, why := compareRenderParams(c.have, c.want)
			if got != c.wantState {
				t.Errorf("state = %v (%s), want %v", got, why, c.wantState)
			}
			if got == modelWrong && strings.TrimSpace(why) == "" {
				t.Error("reported wrong with no reason; the message reaches somebody deciding to rebuild a bed")
			}
		})
	}
}
