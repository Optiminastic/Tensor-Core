package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// resetSeedData clears everything a previous run of this script created, so
// the script is safe to re-run: designs.sku has a unique index, so simply
// re-inserting the same 14 designs would collide with the last run's SKUs.
// Order matters - production_jobs and batches aren't FK'd to orders/designs
// (order_id is ON DELETE SET NULL, and jobs link to designs only by SKU), so
// they're cleared explicitly before the rows they reference:
//  1. production_jobs from seed orders (batches then have zero jobs)
//  2. batches left with zero jobs (i.e. the ones step 1 just emptied)
//  3. seed orders (order_line_items cascade via FK)
//  4. seed-script designs (slice_jobs/slice_metrics/design_pricing cascade via FK)
func resetSeedData(ctx context.Context, store *db.Store) error {
	stmts := []string{
		"DELETE FROM production_jobs WHERE order_id IN (SELECT id FROM orders WHERE source = 'seed')",
		"DELETE FROM batches WHERE id NOT IN (SELECT DISTINCT batch_id FROM production_jobs WHERE batch_id IS NOT NULL)",
		"DELETE FROM orders WHERE source = 'seed'",
		"DELETE FROM designs WHERE created_by = 'seed-script'",
	}
	for _, stmt := range stmts {
		if _, err := store.Pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

type realCustomer struct {
	name  string
	email string
	phone string
	id    int64
}

var realCustomers = []realCustomer{
	{"Ananya Rao", "ananya.rao@example.com", "+91-90000-10001", 600000001},
	{"Vikram Singh", "vikram.singh@example.com", "+91-90000-10002", 600000002},
	{"Priya Menon", "priya.menon@example.com", "+91-90000-10003", 600000003},
	{"Rahul Verma", "rahul.verma@example.com", "+91-90000-10004", 600000004},
	{"Sneha Kapoor", "sneha.kapoor@example.com", "+91-90000-10005", 600000005},
	{"Arjun Nair", "arjun.nair@example.com", "+91-90000-10006", 600000006},
	{"Divya Iyer", "divya.iyer@example.com", "+91-90000-10007", 600000007},
	{"Karthik Reddy", "karthik.reddy@example.com", "+91-90000-10008", 600000008},
}

const (
	numRealOrders   = 20
	realOrderIDBase = 3_000_000
	realUnitPrice   = 499.0
)

// seedRealOrders builds 20 orders against the just-approved designs: some
// customers repeat across multiple orders, some orders carry several
// distinct products, some carry just one. Both order_line_items (for the
// order list UI) and the orders.line_items JSONB column are populated - the
// latter is what CreateJobsForOrder actually reads (see
// internal/httpapi/production_jobs.go#CreateJobsForOrder), so it must not be
// left empty the way the old dummy seed left it.
func seedRealOrders(ctx context.Context, store *db.Store, designs []seededDesign) ([]uuid.UUID, error) {
	if len(designs) == 0 {
		return nil, fmt.Errorf("no priced designs to build orders from")
	}
	rng := rand.New(rand.NewSource(42))
	ids := make([]uuid.UUID, 0, numRealOrders)

	for n := 1; n <= numRealOrders; n++ {
		customer := realCustomers[rng.Intn(len(realCustomers))]
		itemCount := 1
		if rng.Intn(3) != 0 { // ~2/3 of orders carry more than one distinct product
			itemCount = 1 + rng.Intn(3)
		}
		picks := pickDesigns(rng, designs, itemCount)

		lineItems := make([]production.LineItem, 0, len(picks))
		var total float64
		for _, d := range picks {
			qty := 1 + rng.Intn(3)
			sku := d.SKU
			lineItems = append(lineItems, production.LineItem{
				ProductID: d.ID.String(), SKU: &sku, ProductName: d.Name,
				Quantity: qty, Unit: "pcs",
			})
			total += float64(qty) * realUnitPrice
		}

		lineItemsJSON, err := json.Marshal(lineItems)
		if err != nil {
			return nil, err
		}
		financial := "paid"
		if rng.Intn(5) == 0 {
			financial = "pending"
		}
		shopifyOrderID := int64(realOrderIDBase + n)

		order, err := store.Q.InsertOrder(ctx, gen.InsertOrderParams{
			ID: uuid.New(), ShopifyOrderID: shopifyOrderID,
			OrderNumber:  fmt.Sprintf("#TEST%04d", n),
			CustomerName: &customer.name, ShopifyCustomerID: &customer.id,
			CustomerEmail: &customer.email, CustomerPhone: &customer.phone,
			FinancialStatus: financial, TotalPrice: total, Currency: "INR",
			LineItems: lineItemsJSON, Status: "queued", Source: "seed",
		})
		if err != nil {
			return nil, fmt.Errorf("insert order %d: %w", n, err)
		}

		for _, item := range lineItems {
			_, err := store.Q.InsertOrderLineItem(ctx, gen.InsertOrderLineItemParams{
				ID: uuid.New(), OrderID: order.ID, ShopifyOrderID: shopifyOrderID,
				ShopifyCustomerID: &customer.id, Sku: item.SKU,
				ProductName: item.ProductName, Quantity: int32(item.Quantity),
			})
			if err != nil {
				return nil, fmt.Errorf("insert line item for order %d: %w", n, err)
			}
		}
		ids = append(ids, order.ID)
	}
	return ids, nil
}

func pickDesigns(rng *rand.Rand, designs []seededDesign, n int) []seededDesign {
	if n > len(designs) {
		n = len(designs)
	}
	idx := rng.Perm(len(designs))[:n]
	out := make([]seededDesign, n)
	for i, x := range idx {
		out[i] = designs[x]
	}
	return out
}
