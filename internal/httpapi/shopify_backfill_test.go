package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

func TestToWebhookPayload(t *testing.T) {
	productID := int64(7001)
	summary := shopify.OrderSummary{
		ID: 5001, Name: "#1001", FinancialStatus: "paid", TotalPrice: "1499.00", Currency: "INR",
		Customer: &shopify.OrderCustomer{
			ID: 9001, FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", Phone: "+1234",
		},
		LineItems: []shopify.OrderLineItem{
			{
				ProductID: &productID, SKU: "ART-UNICORN-01", Name: "Unicorn", Title: "Unicorn", Quantity: 2,
				Properties: []shopify.OrderLineProp{{Name: "material", Value: "PLA"}},
			},
		},
	}

	payload := toWebhookPayload(summary)

	if payload.ID != 5001 || payload.Name != "#1001" || payload.FinancialStatus != "paid" {
		t.Errorf("unexpected payload: %+v", payload)
	}
	if payload.Customer == nil || payload.Customer.ID != 9001 || payload.Customer.Email != "ada@example.com" {
		t.Errorf("unexpected customer: %+v", payload.Customer)
	}
	if len(payload.LineItems) != 1 {
		t.Fatalf("got %d line items, want 1", len(payload.LineItems))
	}
	li := payload.LineItems[0]
	if li.SKU != "ART-UNICORN-01" || li.Quantity != 2 || li.ProductID == nil || *li.ProductID != 7001 {
		t.Errorf("unexpected line item: %+v", li)
	}
	if len(li.Properties) != 1 || li.Properties[0].Name != "material" || li.Properties[0].Value != "PLA" {
		t.Errorf("unexpected properties: %+v", li.Properties)
	}
}

func TestToWebhookPayloadNoCustomer(t *testing.T) {
	payload := toWebhookPayload(shopify.OrderSummary{ID: 5002, Name: "#1002", FinancialStatus: "pending"})
	if payload.Customer != nil {
		t.Errorf("expected nil customer, got %+v", payload.Customer)
	}
	if len(payload.LineItems) != 0 {
		t.Errorf("expected no line items, got %+v", payload.LineItems)
	}
}
