package pricing

// These constants are the engine's built-in fallback policy. They mirror
// app/pricing/assumptions.py exactly and are the source the brand seed copies
// into the database. When a brand row exists, the API passes that row's policy
// in and these are not used; when it is absent, the engine falls back to these.

// StressAdSpendPct is the ad-spend share used for the stress test: re-running
// the economics at 40 percent ad spend to check a price still survives.
const StressAdSpendPct = 0.40

// GiftingLadder and DecorLadder are the approved selling-price ladders per brand.
var (
	GiftingLadder = []int{999, 1199, 1299, 1499, 1799, 1999, 2499, 2999, 3499, 3999}
	DecorLadder   = []int{3999, 4999, 5999, 6999, 7999, 9999, 12999, 14999, 19999, 24999}
)

// GiftingEntryMachineHours is the per-unit effective machine-time target at the
// gifting entry rung (₹999). Above it, a green design is downgraded to yellow.
const GiftingEntryMachineHours = 2.0

// GiftingEntryRung is the price the entry machine-time rule applies at or below.
const GiftingEntryRung = 999

// CPThresholds returns the (green_max, yellow_max) CP-share thresholds for a
// brand. Gifting is stricter than decor.
func CPThresholds(b Brand) (greenMax, yellowMax float64) {
	if b == BrandDecor {
		return 0.30, 0.33
	}
	return 0.25, 0.30
}

// LadderFor returns the built-in ladder for a brand. Gifting gets the gifting
// ladder; every other brand (i.e. decor) gets the decor ladder, matching the
// Python ladder_for.
func LadderFor(b Brand) []int {
	if b == BrandGifting {
		return GiftingLadder
	}
	return DecorLadder
}
