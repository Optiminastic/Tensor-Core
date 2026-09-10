package httpapi

// The names an order can be found by.

import (
	"reflect"
	"testing"
)

// Every property shape the live storefront actually sends.
//
// The step numbers move between products - STEP 4/5 on the main plank, STEP 2/3
// on another, no number at all on a third - which is exactly why this matches on
// what the label SAYS rather than on where it sits.
func TestPersonalisationNamesForTheLiveShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		json string
		want []string
	}{
		{
			"the main plank",
			`[{"properties":[{"name":"_has_gpo","value":"622778"},
			   {"name":"STEP 3-","value":"2 RED HEART"},
			   {"name":"STEP 4-First Name-","value":"VASU"},
			   {"name":"STEP 5-Second Name","value":"PADMANABH"},
			   {"name":"STEP 6 - WhatsApp Number","value":"8709192686"}]}]`,
			[]string{"VASU", "PADMANABH"},
		},
		{
			"the 2-STEP shape, where STEP 3 is the second name",
			`[{"properties":[{"name":"STEP 2 - First Name-","value":"KALYANI"},
			   {"name":"STEP 3 -Second Name","value":"KRISHNA"}]}]`,
			[]string{"KALYANI", "KRISHNA"},
		},
		{
			"the unnumbered shape",
			`[{"properties":[{"name":"First Name on Plank","value":"HABEEB"},
			   {"name":"Second Name on Plank","value":"FARSANA"}]}]`,
			[]string{"HABEEB", "FARSANA"},
		},
		{
			// A combo carries other items' engraving too. All of it is a name
			// somebody might search for, so all of it is indexed.
			"a combo's other items",
			`[{"properties":[{"name":"STEP 2 - First Name-","value":"AATHI"},
			   {"name":"STEP 4 - Name On 3D Rose","value":"PAVITHRA"}]}]`,
			[]string{"AATHI", "PAVITHRA"},
		},
		{
			// Shopify's hidden attributes are never a customer's name, and the
			// heart count and phone number are not names either.
			"nothing name-like",
			`[{"properties":[{"name":"_has_gpo","value":"622778"},
			   {"name":"STEP 3-","value":"2 RED HEART"},
			   {"name":"STEP 6 - WhatsApp Number","value":"8709192686"}]}]`,
			nil,
		},
		{"no properties", `[{"properties":[]}]`, nil},
		{"not json at all", `not json`, nil},
		{"empty", ``, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := personalisationNamesFor([]byte(c.json))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("personalisationNamesFor = %v, want %v", got, c.want)
			}
		})
	}
}

// One order with several lines contributes each name once.
func TestPersonalisationNamesDeduplicates(t *testing.T) {
	raw := `[{"properties":[{"name":"First Name","value":"VASU"}]},
	         {"properties":[{"name":"First Name","value":"vasu"}]},
	         {"properties":[{"name":"Second Name","value":"REYA"}]}]`
	got := personalisationNamesFor([]byte(raw))
	if want := []string{"VASU", "REYA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v - the same name twice is one searchable string", got, want)
	}
}

// "nameplate" is not "name": whole words only, or a search index becomes a
// list of every property the storefront has ever invented.
func TestIsNameLikeKeyMatchesWholeWordsOnly(t *testing.T) {
	for label, want := range map[string]bool{
		"first name":             true,
		"step 4 first name":      true,
		"second name on plank":   true,
		"name on heart keychain": true,
		"name":                   true,
		"step 3":                 false,
		"whatsapp number":        false,
		"nameplate colour":       false,
	} {
		if got := isNameLikeKey(label); got != want {
			t.Errorf("isNameLikeKey(%q) = %v, want %v", label, got, want)
		}
	}
}
