package entitlement

import (
	"testing"
	"time"
)

func TestActiveFollowsStatusAndPeriod(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	cases := []struct {
		name string
		ent  Entitlement
		want bool
	}{
		{"active within period", Entitlement{Status: StatusActive, PeriodEnd: future}, true},
		{"trialing within period", Entitlement{Status: StatusTrialing, PeriodEnd: future}, true},
		{"active but lapsed", Entitlement{Status: StatusActive, PeriodEnd: past}, false},
		{"past due", Entitlement{Status: StatusPastDue, PeriodEnd: future}, false},
		{"canceled", Entitlement{Status: StatusCanceled, PeriodEnd: future}, false},
		{"unknown", Entitlement{Status: StatusUnknown}, false},

		// A comped or internal account on a plan with no billing period must
		// not be retroactively expired by a zero time.
		{"active with no period tracked", Entitlement{Status: StatusActive}, true},
	}

	for _, c := range cases {
		if got := c.ent.Active(); got != c.want {
			t.Errorf("%s: Active() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHasModule(t *testing.T) {
	ent := Entitlement{Modules: []string{"travel", "carwash"}}

	if !ent.HasModule("carwash") {
		t.Error("a listed module should be granted")
	}
	if ent.HasModule("logistics") {
		t.Error("an unlisted module should not be granted")
	}
}

// The empty-list default is load-bearing during migration: every plan that
// exists today predates modules, and a strict reading would refuse every live
// tenant the moment the first gate is mounted. This test exists to make the
// default deliberate — when the time comes to tighten it, this is the test
// that fails and tells you where the decision was written down.
func TestHasModuleTreatsAnEmptyListAsUnenforced(t *testing.T) {
	var ent Entitlement

	if !ent.HasModule("carwash") {
		t.Error("an empty module list means the gate is not enforced yet, not that nothing is granted")
	}
}

// Absent and zero must stay distinguishable: a plan that never mentioned a
// resource is unlimited, a plan that set it to 0 grants none. A bare int
// cannot tell those apart, which is how a missing key ends up forbidding
// everything.
func TestLimitDistinguishesAbsentFromZero(t *testing.T) {
	ent := Entitlement{Limits: map[string]int{"locations": 0, "staff": 10}}

	if v, ok := ent.Limit("staff"); !ok || v != 10 {
		t.Errorf("staff: got (%d, %v), want (10, true)", v, ok)
	}
	if v, ok := ent.Limit("locations"); !ok || v != 0 {
		t.Errorf("locations: got (%d, %v), want (0, true) — an explicit zero is a real ceiling", v, ok)
	}
	if _, ok := ent.Limit("bays"); ok {
		t.Error("bays: an unmentioned resource should report no ceiling")
	}

	var empty Entitlement
	if _, ok := empty.Limit("anything"); ok {
		t.Error("a nil Limits map should report no ceiling, not panic or report one")
	}
}

func TestWithin(t *testing.T) {
	ent := Entitlement{Limits: map[string]int{"staff": 2, "locations": 0}}

	if !ent.Within("staff", 0) || !ent.Within("staff", 1) {
		t.Error("creating up to the ceiling should be allowed")
	}
	if ent.Within("staff", 2) {
		t.Error("creating the third of a two-item ceiling should be refused")
	}
	if ent.Within("locations", 0) {
		t.Error("a zero ceiling should refuse the first one")
	}
	if !ent.Within("bays", 9999) {
		t.Error("an unmentioned resource is unlimited")
	}
}

func TestFeatureDefaultsOff(t *testing.T) {
	ent := Entitlement{Features: map[string]bool{"custom_domain": true, "sso": false}}

	if !ent.Feature("custom_domain") {
		t.Error("a granted feature should be on")
	}
	if ent.Feature("sso") {
		t.Error("an explicitly false feature should be off")
	}
	if ent.Feature("never_heard_of_it") {
		t.Error("an unmentioned feature should be off — nobody has sold it")
	}

	var empty Entitlement
	if empty.Feature("anything") {
		t.Error("a nil Features map should report off")
	}
}
