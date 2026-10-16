package service

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func days(n int) *time.Time {
	t := testNow.Add(time.Duration(n) * 24 * time.Hour)
	return &t
}

// fakeFeeStore mimics the repository's tenant scoping.
type fakeFeeStore struct {
	docs []*models.TenantFee
}

func (f *fakeFeeStore) ListByTenant(_ context.Context, tid primitive.ObjectID) ([]*models.TenantFee, error) {
	out := []*models.TenantFee{}
	for _, d := range f.docs {
		if d.TenantID == tid {
			c := *d
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeFeeStore) CountByTenant(ctx context.Context, tid primitive.ObjectID) (int64, error) {
	l, _ := f.ListByTenant(ctx, tid)
	return int64(len(l)), nil
}

func (f *fakeFeeStore) Find(_ context.Context, tid, id primitive.ObjectID) (*models.TenantFee, error) {
	for _, d := range f.docs {
		if d.ID == id && d.TenantID == tid {
			c := *d
			return &c, nil
		}
	}
	return nil, mongo.ErrNoDocuments
}

func (f *fakeFeeStore) Insert(_ context.Context, d *models.TenantFee) error {
	d.ID = primitive.NewObjectID()
	d.CreatedAt, d.UpdatedAt = testNow, testNow
	c := *d
	f.docs = append(f.docs, &c)
	return nil
}

func (f *fakeFeeStore) Update(_ context.Context, d *models.TenantFee) error {
	for i, e := range f.docs {
		if e.ID == d.ID && e.TenantID == d.TenantID {
			c := *d
			f.docs[i] = &c
			return nil
		}
	}
	return mongo.ErrNoDocuments
}

func (f *fakeFeeStore) Delete(_ context.Context, tid, id primitive.ObjectID) error {
	for i, e := range f.docs {
		if e.ID == id && e.TenantID == tid {
			f.docs = append(f.docs[:i], f.docs[i+1:]...)
			return nil
		}
	}
	return mongo.ErrNoDocuments
}

func (f *fakeFeeStore) AdvanceInvoicedThrough(_ context.Context, tid, id primitive.ObjectID, through time.Time) error {
	for _, e := range f.docs {
		if e.ID == id && e.TenantID == tid && (e.LastInvoicedThrough == nil || e.LastInvoicedThrough.Before(through)) {
			t := through
			e.LastInvoicedThrough = &t
		}
	}
	return nil
}

type costSubs map[primitive.ObjectID]*models.Subscription

func (f costSubs) Get(_ context.Context, tid primitive.ObjectID) (*models.Subscription, error) {
	if s, ok := f[tid]; ok {
		return s, nil
	}
	return nil, apierr.NotFound("subscription")
}

type costFixture struct {
	svc      *FeeService
	fees     *fakeFeeStore
	domains  *fakeDomainStore
	subs     costSubs
	a, b     primitive.ObjectID
	tenantsM invTenants
}

// invTenants is a tenant lookup with contact emails, shared with the invoice
// tests.
type invTenants map[primitive.ObjectID]*models.Tenant

func (f invTenants) FindByID(_ context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	if t, ok := f[id]; ok {
		return t, nil
	}
	return nil, mongo.ErrNoDocuments
}

func newCostFixture() *costFixture {
	a, b := primitive.NewObjectID(), primitive.NewObjectID()
	ten := invTenants{
		a: {ID: a, Name: "Tower LLC", ContactEmail: "owner@tower.example"},
		b: {ID: b, Name: "Other Ltd", ContactEmail: "boss@other.example"},
	}
	fx := &costFixture{
		fees: &fakeFeeStore{}, domains: &fakeDomainStore{}, subs: costSubs{}, a: a, b: b, tenantsM: ten,
	}
	fx.svc = NewFeeService(fx.fees, fx.domains, fx.subs, ten)
	fx.svc.now = func() time.Time { return testNow }
	return fx
}

func (fx *costFixture) addDomain(t *testing.T, tid primitive.ObjectID, name string, expires *time.Time) primitive.ObjectID {
	t.Helper()
	d := &models.TenantDomain{TenantID: tid, Name: name, ExpiresAt: expires}
	if err := fx.domains.Insert(context.Background(), d); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	return d.ID
}

func (fx *costFixture) addFee(t *testing.T, tid primitive.ObjectID, in FeeInput) *models.TenantFee {
	t.Helper()
	f, err := fx.svc.Create(context.Background(), tid.Hex(), in)
	if err != nil {
		t.Fatalf("create fee %+v: %v", in, err)
	}
	return f
}

func (fx *costFixture) subscribe(tid primitive.ObjectID, price float64, status models.SubscriptionStatus, endsInDays int) {
	fx.subs[tid] = &models.Subscription{
		TenantID: tid, Status: status, CurrentPeriodEnd: *days(endsInDays),
		Plan: &models.Plan{Name: "Pro", Price: price},
	}
}

func TestIsDueWindow(t *testing.T) {
	edge := testNow.Add(DueLeadDays * 24 * time.Hour)
	past, justOver := testNow.Add(-72*time.Hour), edge.Add(time.Second)
	cases := []struct {
		name string
		next *time.Time
		want bool
	}{
		{"nil", nil, false},
		{"exactly at the window edge", &edge, true},
		{"one second beyond", &justOver, false},
		{"already past", &past, true},
		{"today", &testNow, true},
		{"in 10 days", days(10), true},
		{"in 60 days", days(60), false},
	}
	for _, c := range cases {
		if got := IsDue(c.next, testNow); got != c.want {
			t.Errorf("%s: IsDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestFeeNextDue(t *testing.T) {
	created := time.Date(2026, 3, 5, 15, 30, 0, 0, time.UTC)
	lit := func(t time.Time) *time.Time { return &t }
	exp := time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		fee    models.TenantFee
		expiry *time.Time
		want   *time.Time
	}{
		{"domain follows the domain expiry", models.TenantFee{Kind: models.FeeKindDomain, Interval: models.FeeIntervalYearly}, &exp, &exp},
		{"domain with no expiry has no due date", models.TenantFee{Kind: models.FeeKindDomain}, nil, nil},
		{"never invoiced is due at creation (UTC day)", models.TenantFee{Kind: models.FeeKindOther, Interval: models.FeeIntervalMonthly, CreatedAt: created}, nil, lit(date(2026, 3, 5))},
		{"monthly after last_invoiced_through", models.TenantFee{Kind: models.FeeKindDeployment, Interval: models.FeeIntervalMonthly, LastInvoicedThrough: lit(date(2026, 5, 10))}, nil, lit(date(2026, 6, 10))},
		{"monthly clamps 31 Jan to 28 Feb", models.TenantFee{Kind: models.FeeKindDeployment, Interval: models.FeeIntervalMonthly, LastInvoicedThrough: lit(date(2027, 1, 31))}, nil, lit(date(2027, 2, 28))},
		{"monthly clamps to 29 Feb in a leap year", models.TenantFee{Kind: models.FeeKindDeployment, Interval: models.FeeIntervalMonthly, LastInvoicedThrough: lit(date(2028, 1, 31))}, nil, lit(date(2028, 2, 29))},
		{"monthly rolls the year", models.TenantFee{Kind: models.FeeKindOther, Interval: models.FeeIntervalMonthly, LastInvoicedThrough: lit(date(2026, 12, 15))}, nil, lit(date(2027, 1, 15))},
		{"yearly", models.TenantFee{Kind: models.FeeKindDeployment, Interval: models.FeeIntervalYearly, LastInvoicedThrough: lit(date(2026, 2, 28))}, nil, lit(date(2027, 2, 28))},
		{"yearly from 29 Feb", models.TenantFee{Kind: models.FeeKindDeployment, Interval: models.FeeIntervalYearly, LastInvoicedThrough: lit(date(2028, 2, 29))}, nil, lit(date(2029, 2, 28))},
		{"one-time already invoiced is settled", models.TenantFee{Kind: models.FeeKindOther, Interval: models.FeeIntervalOneTime, LastInvoicedThrough: lit(date(2026, 5, 10))}, nil, nil},
		{"one-time not yet invoiced is due at creation", models.TenantFee{Kind: models.FeeKindOther, Interval: models.FeeIntervalOneTime, CreatedAt: created}, nil, lit(date(2026, 3, 5))},
	}
	for _, c := range cases {
		f := c.fee
		got := FeeNextDue(&f, c.expiry)
		switch {
		case got == nil && c.want == nil:
		case got == nil || c.want == nil || !got.Equal(*c.want):
			t.Errorf("%s: next due = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPriceToMNTRoundsOnce(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{0, 0}, {0.4, 0}, {0.5, 1}, {99.4, 99}, {99.5, 100}, {1.4999, 1}, {1234567.5, 1234568},
		{150000, 150000}, {-5, 0}, {math.NaN(), 0}, {math.Inf(1), MaxFeeAmount}, {1e20, MaxFeeAmount},
	}
	for _, c := range cases {
		if got := PriceToMNT(c.in); got != c.want {
			t.Errorf("PriceToMNT(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBuildFeeValidation(t *testing.T) {
	did := primitive.NewObjectID().Hex()
	ok := FeeInput{Kind: "other", Label: "Hosting", Amount: 50000, Interval: "monthly"}
	if _, err := buildFee(ok); err != nil {
		t.Fatalf("valid fee refused: %v", err)
	}
	f, _ := buildFee(ok)
	if f.Currency != models.CurrencyMNT || !f.Active {
		t.Fatalf("defaults: currency=%q active=%v", f.Currency, f.Active)
	}
	zero := ok
	zero.Amount = 0
	if _, err := buildFee(zero); err != nil {
		t.Fatalf("a zero amount is allowed: %v", err)
	}

	mut := func(fn func(*FeeInput)) FeeInput { in := ok; fn(&in); return in }
	bad := map[string]FeeInput{
		"subscription kind (derived, never stored)": mut(func(i *FeeInput) { i.Kind = "subscription" }),
		"unknown kind":                     mut(func(i *FeeInput) { i.Kind = "tax" }),
		"empty label":                      mut(func(i *FeeInput) { i.Label = "  " }),
		"control char in label":            mut(func(i *FeeInput) { i.Label = "a\nb" }),
		"long label":                       mut(func(i *FeeInput) { i.Label = strings.Repeat("x", maxFeeLabelLen+1) }),
		"negative amount":                  mut(func(i *FeeInput) { i.Amount = -1 }),
		"huge amount":                      mut(func(i *FeeInput) { i.Amount = MaxFeeAmount + 1 }),
		"bad interval":                     mut(func(i *FeeInput) { i.Interval = "weekly" }),
		"domain kind without domain id":    mut(func(i *FeeInput) { i.Kind = "domain" }),
		"domain kind with bad domain id":   mut(func(i *FeeInput) { i.Kind = "domain"; i.DomainID = "zzz" }),
		"non-domain kind with a domain id": mut(func(i *FeeInput) { i.DomainID = did }),
	}
	for name, in := range bad {
		_, err := buildFee(in)
		if domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, err %v", name, domStatus(err), err)
		}
	}
}

func TestFeeValidationErrorsDoNotEchoInput(t *testing.T) {
	_, err := buildFee(FeeInput{Kind: "SECRET-KIND", Label: "x", Interval: "monthly"})
	if err == nil || strings.Contains(err.Error(), "SECRET-KIND") {
		t.Fatalf("error must name only the field: %v", err)
	}
}

func TestFeeTenantScoping(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	fee := fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: "Hosting", Amount: 1, Interval: "monthly"})

	if _, err := fx.svc.Update(ctx, fx.b.Hex(), fee.ID.Hex(), FeeInput{Kind: "other", Label: "x", Interval: "monthly"}); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant update: %v", err)
	}
	if err := fx.svc.Delete(ctx, fx.b.Hex(), fee.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Errorf("cross-tenant delete: %v", err)
	}
	if l, _ := fx.svc.List(ctx, fx.b.Hex()); len(l) != 0 {
		t.Errorf("tenant B sees %d of A's fees", len(l))
	}
	if len(fx.fees.docs) != 1 {
		t.Fatal("the cross-tenant calls changed A's fee")
	}

	// A domain fee cannot link another tenant's domain.
	bDomain := fx.addDomain(t, fx.b, "other.example", days(5))
	_, err := fx.svc.Create(ctx, fx.a.Hex(), FeeInput{Kind: "domain", Label: "d", Amount: 1, Interval: "yearly", DomainID: bDomain.Hex()})
	if domStatus(err) != http.StatusUnprocessableEntity {
		t.Errorf("linking B's domain from A: %v", err)
	}

	// Unknown tenant.
	if _, err := fx.svc.Costing(ctx, primitive.NewObjectID().Hex()); domStatus(err) != http.StatusNotFound {
		t.Errorf("costing of an unknown tenant: %v", err)
	}
	if _, err := fx.svc.Costing(ctx, "nope"); domStatus(err) != http.StatusBadRequest {
		t.Errorf("costing with a bad id: %v", err)
	}
}

func TestFeeUpdateKeepsLastInvoicedThroughWhenOmitted(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	fee := fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: "Hosting", Amount: 1, Interval: "monthly"})
	through := date(2026, 9, 1)
	fx.fees.docs[0].LastInvoicedThrough = &through

	got, err := fx.svc.Update(ctx, fx.a.Hex(), fee.ID.Hex(), FeeInput{Kind: "other", Label: "Hosting 2", Amount: 2, Interval: "monthly"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.LastInvoicedThrough == nil || !got.LastInvoicedThrough.Equal(through) || got.Label != "Hosting 2" {
		t.Fatalf("updated fee = %+v", got)
	}
}

func TestFeeLimitPerTenant(t *testing.T) {
	fx := newCostFixture()
	for i := 0; i < MaxFeesPerTenant; i++ {
		fx.fees.docs = append(fx.fees.docs, &models.TenantFee{ID: primitive.NewObjectID(), TenantID: fx.a})
	}
	_, err := fx.svc.Create(context.Background(), fx.a.Hex(), FeeInput{Kind: "other", Label: "x", Interval: "monthly"})
	if domStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("the %dth+1 fee: %v", MaxFeesPerTenant, err)
	}
}

func TestCostingDueWindowAndTotals(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()

	soon := fx.addDomain(t, fx.a, "soon.example", days(10))
	later := fx.addDomain(t, fx.a, "later.example", days(60))
	noExp := fx.addDomain(t, fx.a, "noexp.example", nil)
	fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "soon", Amount: 100000, Interval: "yearly", DomainID: soon.Hex()})
	fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "later", Amount: 200000, Interval: "yearly", DomainID: later.Hex()})
	fx.addFee(t, fx.a, FeeInput{Kind: "domain", Label: "noexp", Amount: 300000, Interval: "yearly", DomainID: noExp.Hex()})
	fx.addFee(t, fx.a, FeeInput{Kind: "deployment", Label: "hosting", Amount: 40000, Interval: "monthly"})
	off := false
	fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: "paused", Amount: 7000, Interval: "monthly", Active: &off})
	fx.subscribe(fx.a, 99.5, models.SubscriptionActive, 5)

	// Another tenant's data never leaks in.
	fx.addFee(t, fx.b, FeeInput{Kind: "other", Label: "B only", Amount: 999, Interval: "monthly"})

	c, err := fx.svc.Costing(ctx, fx.a.Hex())
	if err != nil {
		t.Fatalf("costing: %v", err)
	}
	if c.Currency != "MNT" || c.LeadDays != DueLeadDays || len(c.Fees) != 5 {
		t.Fatalf("costing header: %+v (fees %d)", c, len(c.Fees))
	}
	byLabel := map[string]FeeView{}
	for _, f := range c.Fees {
		byLabel[f.Label] = f
	}
	wantDue := map[string]bool{"soon": true, "later": false, "noexp": false, "hosting": true, "paused": false}
	for label, want := range wantDue {
		if byLabel[label].Due != want {
			t.Errorf("fee %q due = %v, want %v", label, byLabel[label].Due, want)
		}
	}
	if byLabel["soon"].NextDueAt == nil || !byLabel["soon"].NextDueAt.Equal(*days(10)) || byLabel["soon"].DomainName != "soon.example" {
		t.Errorf("domain fee next due / name: %+v", byLabel["soon"])
	}
	if byLabel["noexp"].NextDueAt != nil {
		t.Error("a domain with no expiry has no next due date")
	}
	if c.Subscription == nil || c.Subscription.Amount != 100 || !c.Subscription.Due || c.Subscription.ID != SubscriptionLineID {
		t.Fatalf("subscription line = %+v", c.Subscription)
	}
	// 99.5 -> 100 (rounded once), plus soon 100000 plus hosting 40000.
	if want := int64(100 + 100000 + 40000); c.DueTotal != want {
		t.Fatalf("due total = %d, want %d", c.DueTotal, want)
	}
}

func TestCostingSubscriptionStates(t *testing.T) {
	ctx := context.Background()

	fx := newCostFixture()
	if c, err := fx.svc.Costing(ctx, fx.a.Hex()); err != nil || c.Subscription != nil || c.DueTotal != 0 {
		t.Fatalf("no subscription: %+v %v", c, err)
	}

	fx.subscribe(fx.a, 500000, models.SubscriptionCanceled, 3)
	c, _ := fx.svc.Costing(ctx, fx.a.Hex())
	if c.Subscription == nil || c.Subscription.Due || c.DueTotal != 0 {
		t.Fatalf("a canceled subscription is shown but never due: %+v", c)
	}

	fx.subscribe(fx.a, 500000, models.SubscriptionActive, 45)
	c, _ = fx.svc.Costing(ctx, fx.a.Hex())
	if c.Subscription.Due || c.DueTotal != 0 {
		t.Fatalf("a period ending in 45 days is not due yet: %+v", c)
	}

	// A subscription whose plan was deleted has nothing to price it from.
	fx.subs[fx.a].Plan = nil
	if c, _ := fx.svc.Costing(ctx, fx.a.Hex()); c.Subscription != nil {
		t.Fatalf("no plan, no line: %+v", c.Subscription)
	}
}

func TestFeeUpdateKeepsActiveWhenOmitted(t *testing.T) {
	fx := newCostFixture()
	ctx := context.Background()
	off := false
	fee := fx.addFee(t, fx.a, FeeInput{Kind: "other", Label: "Hosting", Amount: 1, Interval: "monthly", Active: &off})
	if fee.Active {
		t.Fatal("fee should start inactive")
	}

	got, err := fx.svc.Update(ctx, fx.a.Hex(), fee.ID.Hex(), FeeInput{Kind: "other", Label: "Hosting 2", Amount: 2, Interval: "monthly"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Active || fx.fees.docs[0].Active {
		t.Fatal("an omitted active reactivated the fee")
	}

	on := true
	got, err = fx.svc.Update(ctx, fx.a.Hex(), fee.ID.Hex(), FeeInput{Kind: "other", Label: "Hosting 2", Amount: 2, Interval: "monthly", Active: &on})
	if err != nil || !got.Active {
		t.Fatalf("explicit active=true: %+v %v", got, err)
	}
	got, _ = fx.svc.Update(ctx, fx.a.Hex(), fee.ID.Hex(), FeeInput{Kind: "other", Label: "Hosting 2", Amount: 2, Interval: "monthly"})
	if !got.Active {
		t.Fatal("an omitted active deactivated an active fee")
	}
}

func TestFeeLabelRejectsInvisibleCharacters(t *testing.T) {
	ok := FeeInput{Kind: "other", Label: "Hosting", Amount: 1, Interval: "monthly"}
	for _, label := range []string{"\u200b", " \u200b\u200d ", "Host\u200bing", "\u202eHosting"} {
		in := ok
		in.Label = label
		if _, err := buildFee(in); domStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("label %q: status %d, err %v", label, domStatus(err), err)
		}
	}
}

func TestMonthlyDueDateDoesNotDrift(t *testing.T) {
	f := &models.TenantFee{Kind: models.FeeKindOther, Interval: models.FeeIntervalMonthly, CreatedAt: date(2026, time.January, 31)}
	through := date(2026, time.January, 31)
	var got []time.Time
	for i := 0; i < 4; i++ {
		f.LastInvoicedThrough = &through
		next := FeeNextDue(f, nil)
		got = append(got, *next)
		through = *next
	}
	want := []time.Time{date(2026, time.February, 28), date(2026, time.March, 31), date(2026, time.April, 30), date(2026, time.May, 31)}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("period %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}

	f.Interval = models.FeeIntervalYearly
	feb := date(2028, time.February, 29)
	f.CreatedAt = feb
	f.LastInvoicedThrough = &feb
	if n := FeeNextDue(f, nil); !n.Equal(date(2029, time.February, 28)) {
		t.Fatalf("yearly from a leap day = %s", n)
	}
	next := date(2029, time.February, 28)
	f.LastInvoicedThrough = &next
	if n := FeeNextDue(f, nil); !n.Equal(date(2030, time.February, 28)) {
		t.Fatalf("yearly = %s", n)
	}
}
