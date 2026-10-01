package view

import (
	"encoding/json"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
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
