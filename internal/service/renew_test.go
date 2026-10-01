package service

import (
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
)

// Renewing a live subscription must keep the days the tenant already paid for;
// renewing a lapsed one starts from today, not from a date in the past. Both now
// land on the billing day instead of drifting by 30 days each time.
func TestRenewedEnd(t *testing.T) {
	cases := []struct {
		name       string
		now        time.Time
		currentEnd time.Time
		want       time.Time
	}{
		// E&S before it was moved: it ended 31 Oct, which is not the 20th.
		{"live, ending 31 Oct", utc(2026, 10, 1, 12, 0), utc(2026, 10, 31, 4, 12), utc(2026, 11, 20, 0, 0)},
		{"live, ending on the day", utc(2026, 10, 1, 12, 0), utc(2026, 10, 20, 0, 0), utc(2026, 11, 20, 0, 0)},
		{"lapsed renews from today", utc(2026, 10, 3, 12, 0), utc(2026, 8, 1, 0, 0), utc(2026, 10, 20, 0, 0)},
		{"ending exactly now renews from today", utc(2026, 10, 20, 0, 0), utc(2026, 10, 20, 0, 0), utc(2026, 11, 20, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renewedEnd(tc.now, tc.currentEnd, 30, 20); !got.Equal(tc.want) {
				t.Fatalf("renewedEnd = %v, want %v", got, tc.want)
			}
		})
	}
}

// Cancelling is a deliberate act. Renewing a cancelled subscription quietly
// would erase the fact that the tenant was cancelled on purpose, so that path
// goes through Change plan instead. Everything else can be renewed, including
// past_due, which is the state most worth renewing.
func TestRenewable(t *testing.T) {
	cases := map[models.SubscriptionStatus]bool{
		models.SubscriptionCanceled: false,
		models.SubscriptionActive:   true,
		models.SubscriptionPastDue:  true,
		models.SubscriptionTrialing: true,
	}
	for status, want := range cases {
		if got := renewable(status); got != want {
			t.Fatalf("renewable(%q) = %v, want %v", status, got, want)
		}
	}
}

// A plan deleted out from under a live subscription, or one that never set a
// period, must still renew by the default length rather than by zero days.
func TestPeriodFor(t *testing.T) {
	cases := []struct {
		name string
		plan *models.Plan
		want int
	}{
		{"missing plan", nil, models.DefaultPeriodDays},
		{"unset period", &models.Plan{PeriodDays: 0}, models.DefaultPeriodDays},
		{"explicit period", &models.Plan{PeriodDays: 90}, 90},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := periodFor(tc.plan); got != tc.want {
				t.Fatalf("periodFor = %d, want %d", got, tc.want)
			}
		})
	}
}
