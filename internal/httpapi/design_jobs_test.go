package httpapi

// The design queue's one security property: a designer's reach stops at the
// products Tensor does not render.
//
// The list filters them out, but a route is not secured by what a list shows -
// :id comes from the caller. These assert the predicate the upload wrapper
// refuses on, which is the same one job creation uses to decide whether a job
// waits for a render or for a person.

import "testing"

func TestTheDesignQueueCoversExactlyTheProductsTensorCannotBuild(t *testing.T) {
	for _, tc := range []struct {
		sku, product string
		designers    bool // may a designer supply this job's file?
	}{
		// Theirs: nothing renders these, so a person draws the 3MF.
		{"", "3D Name Plank - Large - 4 cm height / BLUE / GOLD", true},
		{"", "Couple's Date Plank - BLUE / BLUE / YELLOW", true},
		{"DT-BoneTag-8", "Bone Tag - Black / Gold", true},
		{"T3DPS-DUO-4", "DUOSNAP - Black", true},
		{"SNP-PNK", "Single Name Plank - Pink / NO LIGHT", true},
		{"3DMS01-6", "Night Lamp", true},

		// Not theirs. Attaching a hand-made file to one of these would
		// silently replace a rendered model with one nothing re-renders.
		{"DNP-BLU", "Dual Name Plank - BLUE / NO LIGHT", false},
		{"SC-WHT", "Soulmate COMBO - WHITE", false},
		{"SCWL-RED", "Soulmate COMBO with LIGHT - RED", false},
		{"DNPF-1", "DUAL NAME & PHOTO FRAME - GOLD", false},
		{"", "Dual Name Plank - GREEN / I NEED LIGHT", false},
	} {
		t.Run(tc.product, func(t *testing.T) {
			// IsGeneratedProduct is the pure half of rendersProduct; the
			// registry half needs a database and answers true for strictly
			// more products, so a product refused here is refused there too.
			if got := !IsGeneratedProduct(tc.sku, tc.product); got != tc.designers {
				t.Errorf("designer may supply = %v, want %v", got, tc.designers)
			}
		})
	}
}
