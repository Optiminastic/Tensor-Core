// Command shipmentcheck answers one question: what would the Deliveries page
// show right now, and where did each number come from?
//
// READ-ONLY. It runs exactly what the handler runs - Shopify for the waybills,
// Delhivery to track them, the classifier over the carrier's own sentence - and
// prints each step's count. Nothing here can write to either upstream;
// Delhivery's /api/p/update, which changes a real parcel's delivery
// instructions, is not reachable from the client this uses.
//
// It exists because a quiet page has two causes that look identical from
// outside: nothing went wrong, or a link in the chain returned nothing. This
// says which.
//
//	go run ./cmd/shipmentcheck -brand <slug> [-days 30] [-rows 20]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/delhivery"
	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

func main() {
	brand := flag.String("brand", "", "brand slug (required)")
	days := flag.Int("days", 30, "how far back to look")
	rows := flag.Int("rows", 20, "how many rows to print")
	flag.Parse()

	if strings.TrimSpace(*brand) == "" {
		log.Fatal("-brand is required")
	}
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	if strings.TrimSpace(cfg.DelhiveryAPIKey) == "" {
		log.Fatal("DELHIVERY_API_KEY is not set, so there is nothing to track against")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Pool.Close()

	conn, err := store.Q.GetConnectionWithToken(ctx, gen.GetConnectionWithTokenParams{
		BrandSlug: *brand, Provider: "shopify",
	})
	if err != nil || conn.ExternalAccountID == nil || conn.AccessToken == nil {
		log.Fatalf("brand %q has no connected Shopify store", *brand)
	}
	shop, token := *conn.ExternalAccountID, *conn.AccessToken
	fmt.Printf("store      : %s\nwindow     : last %d days\n\n", shop, *days)

	since := time.Now().AddDate(0, 0, -*days)
	parcels, err := shopify.New(cfg.ShopifyAPIVersion, cfg.ShopifyTimeout).
		ListShipments(ctx, shop, token, since, "")
	if err != nil {
		log.Fatalf("shopify: %v", err)
	}

	// Step 1: the carriers in play. A store shipping by more than one is why
	// the page cannot claim to be complete.
	byCarrier := map[string]int{}
	delhiveryParcels := make([]shopify.Shipment, 0, len(parcels))
	for _, p := range parcels {
		name := strings.TrimSpace(p.Carrier)
		if name == "" {
			name = "(unnamed)"
		}
		byCarrier[name]++
		if strings.Contains(strings.ToLower(p.Carrier), "delhivery") {
			delhiveryParcels = append(delhiveryParcels, p)
		}
	}
	fmt.Printf("=== step 1: Shopify fulfilments (%d waybills) ===\n", len(parcels))
	for _, name := range sortedKeys(byCarrier) {
		mark := " "
		if strings.Contains(strings.ToLower(name), "delhivery") {
			mark = "*"
		}
		fmt.Printf("  %s %-24s %4d\n", mark, name, byCarrier[name])
	}
	fmt.Printf("  (* is the only carrier Tensor can track)\n\n")

	// Step 2: the carrier's answer.
	waybills := make([]string, 0, len(delhiveryParcels))
	for _, p := range delhiveryParcels {
		waybills = append(waybills, p.Waybill)
	}
	tracked, err := delhivery.New(delhivery.Config{
		APIKey: cfg.DelhiveryAPIKey, BaseURL: cfg.DelhiveryBaseURL,
	}).Track(ctx, waybills)
	if err != nil {
		log.Fatalf("delhivery: %v", err)
	}
	fmt.Printf("=== step 2: Delhivery tracking ===\n")
	fmt.Printf("  asked   %4d\n  tracked %4d\n", len(waybills), len(tracked))
	if len(tracked) < len(waybills) {
		fmt.Printf("  MISSING %4d - booked elsewhere, or aged out of the carrier's window\n",
			len(waybills)-len(tracked))
	}
	fmt.Println()

	// Step 3: the classification, which is what the page is filtered on.
	byWaybill := make(map[string]delhivery.Shipment, len(tracked))
	for _, t := range tracked {
		byWaybill[t.AWB.String()] = t
	}
	counts := map[string]int{}
	actionable := 0
	type row struct {
		parcel  shopify.Shipment
		carrier delhivery.Shipment
		reason  delhivery.Reason
		found   bool
	}
	all := make([]row, 0, len(delhiveryParcels))
	for _, p := range delhiveryParcels {
		carrierView, found := byWaybill[p.Waybill]
		if !found {
			counts["untracked"]++
			actionable++
			all = append(all, row{parcel: p})
			continue
		}
		reason := delhivery.Classify(carrierView)
		counts[string(reason)]++
		if reason.Actionable() {
			actionable++
		}
		all = append(all, row{parcel: p, carrier: carrierView, reason: reason, found: true})
	}

	fmt.Printf("=== step 3: why each parcel is where it is ===\n")
	for _, name := range sortedKeys(counts) {
		flag := ""
		if delhivery.Reason(name).Actionable() || name == "untracked" {
			flag = "  <- somebody's work"
		}
		fmt.Printf("  %-24s %4d%s\n", name, counts[name], flag)
	}
	fmt.Printf("\n  needing attention: %d of %d tracked\n\n", actionable, len(tracked))

	// Problems first, oldest first - the page's own ordering.
	sort.SliceStable(all, func(i, j int) bool {
		li := !all[i].found || all[i].reason.Actionable()
		lj := !all[j].found || all[j].reason.Actionable()
		if li != lj {
			return li
		}
		if li {
			return all[i].carrier.Status.StatusDateTime.Time.Before(all[j].carrier.Status.StatusDateTime.Time)
		}
		return all[i].carrier.Status.StatusDateTime.Time.After(all[j].carrier.Status.StatusDateTime.Time)
	})

	fmt.Printf("=== the first %d rows the page would render ===\n", *rows)
	for i, r := range all {
		if i >= *rows {
			break
		}
		if !r.found {
			fmt.Printf("  %-14s %-16s %-24s NOT TRACKED\n",
				r.parcel.Waybill, r.parcel.OrderName, "untracked")
			continue
		}
		fmt.Printf("  %-14s %-16s %-24s %-11s %-10s tries=%d  %s\n",
			r.carrier.AWB, r.parcel.OrderName, r.reason,
			r.carrier.Status.Status, r.carrier.Status.StatusType,
			r.carrier.Attempts(), r.carrier.Status.Instructions)
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
