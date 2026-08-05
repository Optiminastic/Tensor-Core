package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// dummyOrderSourceKey is what distinguishes seeded orders from real
// Shopify-webhook-imported ones (the orders.source column).
const dummyOrderSourceKey = "seed"

type dummyCustomer struct {
	name  string
	email string
	phone string
	id    int64
}

type dummyProduct struct {
	name     string
	sku      string
	imageURL string
	priceINR float64
}

// dummyCustomers is 20 fabricated buyers, one per seeded order.
var dummyCustomers = []dummyCustomer{
	{"Aditi Sharma", "aditi.sharma@example.com", "+91-98765-43210", 500000001},
	{"Rohan Mehta", "rohan.mehta@example.com", "+91-98765-43211", 500000002},
	{"Priya Nair", "priya.nair@example.com", "+91-98765-43212", 500000003},
	{"Karan Malhotra", "karan.malhotra@example.com", "+91-98765-43213", 500000004},
	{"Sneha Iyer", "sneha.iyer@example.com", "+91-98765-43214", 500000005},
	{"Arjun Verma", "arjun.verma@example.com", "+91-98765-43215", 500000006},
	{"Neha Kapoor", "neha.kapoor@example.com", "+91-98765-43216", 500000007},
	{"Vikram Rao", "vikram.rao@example.com", "+91-98765-43217", 500000008},
	{"Ananya Desai", "ananya.desai@example.com", "+91-98765-43218", 500000009},
	{"Siddharth Joshi", "siddharth.joshi@example.com", "+91-98765-43219", 500000010},
	{"Meera Pillai", "meera.pillai@example.com", "+91-98765-43220", 500000011},
	{"Rahul Gupta", "rahul.gupta@example.com", "+91-98765-43221", 500000012},
	{"Divya Menon", "divya.menon@example.com", "+91-98765-43222", 500000013},
	{"Aman Chawla", "aman.chawla@example.com", "+91-98765-43223", 500000014},
	{"Ishita Bhatt", "ishita.bhatt@example.com", "+91-98765-43224", 500000015},
	{"Yash Kulkarni", "yash.kulkarni@example.com", "+91-98765-43225", 500000016},
	{"Pooja Reddy", "pooja.reddy@example.com", "+91-98765-43226", 500000017},
	{"Nikhil Saxena", "nikhil.saxena@example.com", "+91-98765-43227", 500000018},
	{"Kavya Krishnan", "kavya.krishnan@example.com", "+91-98765-43228", 500000019},
	{"Varun Thakur", "varun.thakur@example.com", "+91-98765-43229", 500000020},
}

// dummyProducts is the catalog seeded orders draw their line items from.
var dummyProducts = []dummyProduct{
	{"Personalised Nameplate", "NP-001", "https://picsum.photos/seed/NP-001/400/400", 799},
	{"Custom Keychain", "KC-002", "https://picsum.photos/seed/KC-002/400/400", 249},
	{"Desk Organizer", "DO-003", "https://picsum.photos/seed/DO-003/400/400", 1299},
	{"Phone Stand", "PS-004", "https://picsum.photos/seed/PS-004/400/400", 599},
	{"Miniature Figurine", "MF-005", "https://picsum.photos/seed/MF-005/400/400", 899},
	{"Wall Art Panel", "WA-006", "https://picsum.photos/seed/WA-006/400/400", 1999},
	{"Planter Pot", "PP-007", "https://picsum.photos/seed/PP-007/400/400", 649},
	{"Cable Organizer", "CO-008", "https://picsum.photos/seed/CO-008/400/400", 349},
	{"Coaster Set", "CS-009", "https://picsum.photos/seed/CS-009/400/400", 799},
	{"Photo Frame", "PF-010", "https://picsum.photos/seed/PF-010/400/400", 549},
}

// dummyFinancialStatuses cycles through every status the frontend maps
// (paid/refunded/cancelled/anything-else-as-pending), weighted toward paid.
var dummyFinancialStatuses = []string{
	"paid", "paid", "pending", "paid", "refunded", "paid", "pending", "cancelled",
	"paid", "paid", "pending", "paid", "refunded", "paid", "paid", "cancelled",
	"pending", "paid", "paid", "refunded",
}

type dummyLineItem struct {
	product  dummyProduct
	quantity int32
}

type dummyOrderSpec struct {
	customer dummyCustomer
	status   string
	items    []dummyLineItem
}

// buildDummyOrderSpecs assembles 20 orders, each 1-3 products, cycling
// deterministically through the customer/product/status pools above.
func buildDummyOrderSpecs() []dummyOrderSpec {
	specs := make([]dummyOrderSpec, len(dummyCustomers))
	for i, customer := range dummyCustomers {
		itemCount := 1 + i%3 // 1, 2, or 3 products
		items := make([]dummyLineItem, itemCount)
		for j := range items {
			product := dummyProducts[(i+j)%len(dummyProducts)]
			items[j] = dummyLineItem{product: product, quantity: int32(1 + (i+j)%2)}
		}
		specs[i] = dummyOrderSpec{
			customer: customer,
			status:   dummyFinancialStatuses[i%len(dummyFinancialStatuses)],
			items:    items,
		}
	}
	return specs
}

func (s dummyOrderSpec) totalINR() float64 {
	var total float64
	for _, item := range s.items {
		total += item.product.priceINR * float64(item.quantity)
	}
	return total
}

// seedDummyOrders inserts 20 sample orders (source="seed") with realistic
// customers and 1-3 products each, so the frontend's live/dummy toggle has
// something to show before any real Shopify order comes in. Idempotent: it
// no-ops if any seed order already exists.
func seedDummyOrders(ctx context.Context, store *db.Store) (int, error) {
	source := dummyOrderSourceKey
	existing, err := store.Q.ListOrders(ctx, &source)
	if err != nil {
		return 0, fmt.Errorf("check existing seed orders: %w", err)
	}
	if len(existing) > 0 {
		return 0, nil
	}

	specs := buildDummyOrderSpecs()
	for i, spec := range specs {
		if err := insertDummyOrder(ctx, store, i+1, spec); err != nil {
			return 0, fmt.Errorf("insert dummy order %d: %w", i+1, err)
		}
	}
	return len(specs), nil
}

// insertDummyOrder writes one seed order and its product rows. shopify_order_id
// values (1000001+) sit well below any real Shopify store's order ID range, so
// they can never collide with a genuinely imported order.
func insertDummyOrder(ctx context.Context, store *db.Store, n int, spec dummyOrderSpec) error {
	shopifyOrderID := int64(1000000 + n)
	orderID := uuid.New()
	customerID := spec.customer.id

	order, err := store.Q.InsertOrder(ctx, gen.InsertOrderParams{
		ID: orderID, ShopifyOrderID: shopifyOrderID, OrderNumber: fmt.Sprintf("#D%04d", 1000+n),
		CustomerName: &spec.customer.name, ShopifyCustomerID: &customerID,
		CustomerEmail: &spec.customer.email, CustomerPhone: &spec.customer.phone,
		FinancialStatus: spec.status, TotalPrice: spec.totalINR(), Currency: "INR",
		LineItems: []byte("[]"), Status: "queued", Source: dummyOrderSourceKey,
	})
	if err != nil {
		return err
	}

	for _, item := range spec.items {
		sku := item.product.sku
		imageURL := item.product.imageURL
		if _, err := store.Q.InsertOrderLineItem(ctx, gen.InsertOrderLineItemParams{
			ID: uuid.New(), OrderID: order.ID, ShopifyOrderID: shopifyOrderID,
			ShopifyCustomerID: &customerID, Sku: &sku, ProductName: item.product.name,
			ProductImageUrl: &imageURL, Quantity: item.quantity,
		}); err != nil {
			return err
		}
	}
	return nil
}
