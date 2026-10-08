package view

import (
	"encoding/json"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The console reads billing_day off this response. The model has the field, but
// every response is built through this mapper, and a field missing from it is
// invisible: it compiles, the API answers 200, and the value is simply absent.
func TestSubscriptionOfCarriesTheBillingDay(t *testing.T) {
	cases := []struct {
		name   string
		stored int
		want   int
	}{
		{"explicit day", 15, 15},
		// Every subscription that predates the field has none stored. The API
		// reports what the system will ACT on, so a client never has to know
		// that zero means twenty.
		{"none stored reports the default", 0, models.DefaultBillingDay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := SubscriptionOf(&models.Subscription{BillingDay: tc.stored})
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got struct {
				BillingDay *int `json:"billing_day"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.BillingDay == nil {
				t.Fatalf("billing_day missing from the response: %s", raw)
			}
			if *got.BillingDay != tc.want {
				t.Fatalf("billing_day = %d, want %d", *got.BillingDay, tc.want)
			}
		})
	}
}

// A field added to the model but not to this mapper compiles and answers 200
// while being absent from every response; the console needs this one to show
// that a quote already became a tenant.
func TestQuoteOfCarriesPromotedTenantID(t *testing.T) {
	id := primitive.NewObjectID()
	cases := []struct {
		name    string
		set     *primitive.ObjectID
		present bool
	}{
		{"promoted", &id, true},
		{"not promoted", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(QuoteOf(&models.Quote{ID: primitive.NewObjectID(), PromotedTenantID: tc.set}))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			v, ok := got["promoted_tenant_id"]
			if ok != tc.present {
				t.Fatalf("promoted_tenant_id present=%v, want %v: %s", ok, tc.present, raw)
			}
			if tc.present && v != id.Hex() {
				t.Fatalf("promoted_tenant_id = %v, want %s", v, id.Hex())
			}
		})
	}
}
