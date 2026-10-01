package service

import (
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
)

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

// The business bills on the 20th, so a subscription must end on the 20th and
// stay there. These pin the rule, including the edges where a date calculation
// is easiest to get subtly wrong.
func TestBillingAlignedEnd(t *testing.T) {
	cases := []struct {
		name   string
		base   time.Time
		period int
		day    int
		want   time.Time
	}{
		{"ends on the day", utc(2026, 10, 20, 0, 0), 30, 20, utc(2026, 11, 20, 0, 0)},
		{"ends 31 Oct (E&S before it was moved)", utc(2026, 10, 31, 4, 12), 30, 20, utc(2026, 11, 20, 0, 0)},
		// 20 Oct would be only 5 days out; the half-month floor pushes it a month.
		{"ends 15 Oct", utc(2026, 10, 15, 0, 0), 30, 20, utc(2026, 11, 20, 0, 0)},
		// A short first period is accepted: every one after it lands on the day.
		{"lapsed, renewed 3 Oct", utc(2026, 10, 3, 12, 0), 30, 20, utc(2026, 10, 20, 0, 0)},
		// earliest lands exactly on the billing day: "on or after" keeps it.
		{"earliest exactly on the day", utc(2026, 10, 5, 0, 0), 30, 20, utc(2026, 10, 20, 0, 0)},
		{"day 28 reaches February", utc(2026, 1, 31, 0, 0), 30, 28, utc(2026, 2, 28, 0, 0)},
		// The 15-day cap is what keeps this at ~92 days. Without it the floor
		// would be 45 days, and two more months would land near 123.
		{"90-day plan adds whole months", utc(2026, 10, 20, 0, 0), 90, 20, utc(2027, 1, 20, 0, 0)},
		{"year-end rolls the year", utc(2026, 12, 25, 0, 0), 30, 20, utc(2027, 1, 20, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := billingAlignedEnd(tc.base, tc.period, tc.day); !got.Equal(tc.want) {
				t.Fatalf("billingAlignedEnd(%v, %d, %d) = %v, want %v", tc.base, tc.period, tc.day, got, tc.want)
			}
		})
	}
}

// Review Focus 6. However short the plan, the end is strictly after the base: a
// renewal that lands on or before the moment it was made would leave a tenant
// expired the instant they paid.
func TestBillingAlignedEnd_NeverOnOrBeforeTheBase(t *testing.T) {
	bases := []time.Time{
		utc(2026, 10, 20, 12, 0), // mid-day on the billing day itself
		utc(2026, 10, 20, 0, 0),
		utc(2026, 10, 19, 23, 59),
		utc(2026, 2, 28, 0, 0),
	}
	for _, period := range []int{1, 2, 7, 14, 30, 31, 90, 365} {
		for _, base := range bases {
			got := billingAlignedEnd(base, period, 20)
			if !got.After(base) {
				t.Fatalf("billingAlignedEnd(%v, %d days, 20) = %v, not after the base", base, period, got)
			}
			if got.Hour() != 0 || got.Minute() != 0 || got.Day() != 20 {
				t.Fatalf("billingAlignedEnd(%v, %d days, 20) = %v, want 00:00 UTC on the 20th", base, period, got)
			}
		}
	}
}

func TestEffectiveBillingDay(t *testing.T) {
	if got := (models.Subscription{}).EffectiveBillingDay(); got != 20 {
		t.Fatalf("unset billing day = %d, want 20: every existing subscription has none stored", got)
	}
	if got := (models.Subscription{BillingDay: 5}).EffectiveBillingDay(); got != 5 {
		t.Fatalf("billing day 5 = %d, want 5", got)
	}
}

func TestValidBillingDay(t *testing.T) {
	for _, d := range []int{1, 20, 28} {
		if !validBillingDay(d) {
			t.Fatalf("validBillingDay(%d) = false, want true", d)
		}
	}
	// 29 to 31 do not exist in every month, so a subscription anchored to them
	// would drift in February.
	for _, d := range []int{-1, 0, 29, 31, 100} {
		if validBillingDay(d) {
			t.Fatalf("validBillingDay(%d) = true, want false", d)
		}
	}
}
