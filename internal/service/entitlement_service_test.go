package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/entitlement"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/apikey"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// These cover the rules that decide whether a product service keeps serving.
// Every one of them is about a state that is legitimate but looks like a
// failure, which is exactly the kind of thing that gets "simplified" into an
// error by someone reading the code six months from now.

type fakeTenants struct {
	byID   map[primitive.ObjectID]*models.Tenant
	byHash map[string]*models.Tenant
	err    error
}

func (f *fakeTenants) FindByID(_ context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	if f.err != nil {
		return nil, f.err
	}
	t, ok := f.byID[id]
	if !ok {
		return nil, mongo.ErrNoDocuments
	}
	return t, nil
}

func (f *fakeTenants) FindByAPIKeyHash(_ context.Context, hash string) (*models.Tenant, error) {
	if f.err != nil {
		return nil, f.err
	}
	t, ok := f.byHash[hash]
	if !ok {
		return nil, mongo.ErrNoDocuments
	}
	return t, nil
}

type fakeSubs struct {
	sub *models.Subscription
	err error
}

func (f *fakeSubs) FindByTenantID(context.Context, primitive.ObjectID) (*models.Subscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.sub == nil {
		return nil, mongo.ErrNoDocuments
	}
	return f.sub, nil
}

type fakePlans struct {
	plan *models.Plan
	err  error
}

func (f *fakePlans) FindByID(context.Context, primitive.ObjectID) (*models.Plan, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.plan == nil {
		return nil, mongo.ErrNoDocuments
	}
	return f.plan, nil
}

func activeTenant() *models.Tenant {
	return &models.Tenant{ID: primitive.NewObjectID(), Status: models.TenantActive}
}

func build(tenant *models.Tenant, sub *models.Subscription, plan *models.Plan) (*EntitlementService, primitive.ObjectID) {
	tenants := &fakeTenants{
		byID:   map[primitive.ObjectID]*models.Tenant{tenant.ID: tenant},
		byHash: map[string]*models.Tenant{},
	}
	return NewEntitlementService(tenants, &fakeSubs{sub: sub}, &fakePlans{plan: plan}), tenant.ID
}

func TestForAssemblesTheDocument(t *testing.T) {
	tenant := activeTenant()
	end := time.Now().Add(24 * time.Hour)
	sub := &models.Subscription{Status: models.SubscriptionActive, CurrentPeriodEnd: end}
	plan := &models.Plan{
		Modules:      []string{"travel", "carwash"},
		Limits:       map[string]int{"locations": 3},
		Capabilities: map[string]bool{"custom_domain": true},
	}

	svc, id := build(tenant, sub, plan)

	ent, err := svc.For(context.Background(), id)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if !ent.Active() {
		t.Error("an active subscription inside its period should be Active")
	}
	if !ent.HasModule("carwash") {
		t.Error("a granted module should be present")
	}
	if v, ok := ent.Limit("locations"); !ok || v != 3 {
		t.Errorf("limits did not carry through: got (%d, %v)", v, ok)
	}
	if !ent.Feature("custom_domain") {
		t.Error("capabilities did not carry through to features")
	}
}

// A tenant provisioned five minutes ago, whose subscription a superadmin has
// not created yet, must not have their storefront start refusing traffic in
// the gap.
func TestATenantWithNoSubscriptionIsNotAnError(t *testing.T) {
	tenant := activeTenant()
	svc, id := build(tenant, nil, nil)

	ent, err := svc.For(context.Background(), id)
	if err != nil {
		t.Fatalf("a missing subscription should not be an error, got: %v", err)
	}
	if ent.Status != entitlement.StatusUnknown {
		t.Errorf("status = %q, want %q", ent.Status, entitlement.StatusUnknown)
	}
	if ent.Active() {
		t.Error("unknown status should not read as active")
	}
}

// Tidying up the price list must not take a paying tenant offline.
func TestADeletedPlanIsNotAnError(t *testing.T) {
	tenant := activeTenant()
	sub := &models.Subscription{
		Status:           models.SubscriptionActive,
		CurrentPeriodEnd: time.Now().Add(24 * time.Hour),
	}
	svc, id := build(tenant, sub, nil) // plan missing

	ent, err := svc.For(context.Background(), id)
	if err != nil {
		t.Fatalf("a deleted plan should not be an error, got: %v", err)
	}
	if !ent.Active() {
		t.Error("the billing state still stands when the plan is gone")
	}
	if len(ent.Modules) != 0 {
		t.Error("a missing plan should grant no modules")
	}
}

// Suspension is an administrative decision that outranks the billing state,
// and every product must see it the same way rather than each combining the
// two for itself.
func TestASuspendedTenantIsReportedNotRefused(t *testing.T) {
	tenant := activeTenant()
	tenant.Status = models.TenantSuspended
	sub := &models.Subscription{
		Status:           models.SubscriptionActive,
		CurrentPeriodEnd: time.Now().Add(24 * time.Hour),
	}
	svc, id := build(tenant, sub, &models.Plan{Modules: []string{"travel"}})

	ent, err := svc.For(context.Background(), id)
	if err != nil {
		t.Fatalf("a suspended tenant should be reported, not raised: %v", err)
	}
	if ent.Status != entitlement.StatusCanceled {
		t.Errorf("status = %q, want %q", ent.Status, entitlement.StatusCanceled)
	}
	if ent.Active() {
		t.Error("a suspended tenant must not read as active even with a paid subscription")
	}
}

func TestAnUnknownTenantIsANotFound(t *testing.T) {
	svc, _ := build(activeTenant(), nil, nil)

	_, err := svc.For(context.Background(), primitive.NewObjectID())
	var appErr *apierr.APIError
	if !errors.As(err, &appErr) || appErr.Code != apierr.CodeNotFound {
		t.Fatalf("got %v, want a NOT_FOUND", err)
	}
}

// A real lookup failure must surface as one. Quietly returning an empty
// entitlement would read as "no modules" to every product at once — the
// entire platform silently losing its features because one query failed.
func TestALookupFailureSurfaces(t *testing.T) {
	tenants := &fakeTenants{err: errors.New("connection refused")}
	svc := NewEntitlementService(tenants, &fakeSubs{}, &fakePlans{})

	_, err := svc.For(context.Background(), primitive.NewObjectID())
	var appErr *apierr.APIError
	if !errors.As(err, &appErr) || appErr.Code != apierr.CodeInternal {
		t.Fatalf("got %v, want an INTERNAL_ERROR", err)
	}
}

func TestForAPIKeyResolvesTheTenant(t *testing.T) {
	tenant := activeTenant()
	raw := "sk_live_whatever"

	tenants := &fakeTenants{
		byID:   map[primitive.ObjectID]*models.Tenant{tenant.ID: tenant},
		byHash: map[string]*models.Tenant{apikey.Hash(raw): tenant},
	}
	sub := &models.Subscription{Status: models.SubscriptionActive, CurrentPeriodEnd: time.Now().Add(time.Hour)}
	svc := NewEntitlementService(tenants, &fakeSubs{sub: sub}, &fakePlans{plan: &models.Plan{Modules: []string{"travel"}}})

	ent, err := svc.ForAPIKey(context.Background(), raw)
	if err != nil {
		t.Fatalf("ForAPIKey: %v", err)
	}
	if ent.TenantID != tenant.ID {
		t.Error("resolved the wrong tenant")
	}
	if !ent.HasModule("travel") {
		t.Error("modules did not carry through the key path")
	}
}

func TestForAPIKeyRefusesAnUnknownKey(t *testing.T) {
	svc := NewEntitlementService(&fakeTenants{byHash: map[string]*models.Tenant{}}, &fakeSubs{}, &fakePlans{})

	_, err := svc.ForAPIKey(context.Background(), "sk_live_nope")
	var appErr *apierr.APIError
	if !errors.As(err, &appErr) || appErr.Code != apierr.CodeUnauthorized {
		t.Fatalf("got %v, want UNAUTHORIZED", err)
	}
}

// The in-process producer has nothing to be stale about. Stale is for the
// caching HTTP client on the consuming side; if it were ever set here it
// would mean a cache appeared somewhere nobody documented.
func TestProducedEntitlementsAreNeverStale(t *testing.T) {
	tenant := activeTenant()
	sub := &models.Subscription{Status: models.SubscriptionActive, CurrentPeriodEnd: time.Now().Add(time.Hour)}
	svc, id := build(tenant, sub, &models.Plan{})

	ent, err := svc.For(context.Background(), id)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if ent.Stale {
		t.Error("the producing side must never mark an answer stale")
	}
}

func (f *fakeTenants) FindByHost(_ context.Context, host string) (*models.Tenant, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, t := range f.byID {
		for _, h := range t.Hosts {
			if h == host {
				return t, nil
			}
		}
	}
	return nil, mongo.ErrNoDocuments
}

func TestForHostResolvesToTheOwningTenant(t *testing.T) {
	tenant := activeTenant()
	tenant.Hosts = []string{"tower.example.com"}
	sub := &models.Subscription{Status: models.SubscriptionActive, CurrentPeriodEnd: time.Now().Add(time.Hour)}
	plan := &models.Plan{Modules: []string{"realestate"}}
	svc, id := build(tenant, sub, plan)

	// Visitor-supplied form: mixed case, port, trailing dot.
	ent, err := svc.ForHost(context.Background(), "Tower.Example.com:443.")
	if err != nil {
		t.Fatalf("ForHost: %v", err)
	}
	if ent.TenantID != id {
		t.Errorf("tenant = %v, want %v", ent.TenantID, id)
	}
	if !ent.HasModule("realestate") {
		t.Error("the entitlement should be the same document For returns")
	}
}

func TestForHostUnknownIsNotFound(t *testing.T) {
	svc, _ := build(activeTenant(), nil, nil)

	_, err := svc.ForHost(context.Background(), "nobody.example.com")
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != 404 {
		t.Fatalf("want a 404 APIError, got %v", err)
	}
	if _, err := svc.ForHost(context.Background(), ""); err == nil {
		t.Fatal("an empty host must not resolve")
	}
}

func resolveFixture(tenant *models.Tenant, raw string) *EntitlementService {
	tenants := &fakeTenants{
		byID:   map[primitive.ObjectID]*models.Tenant{tenant.ID: tenant},
		byHash: map[string]*models.Tenant{apikey.Hash(raw): tenant},
	}
	return NewEntitlementService(tenants, &fakeSubs{}, &fakePlans{})
}

func TestResolveByAPIKey_ActiveTenant(t *testing.T) {
	tenant := activeTenant()
	tenant.Slug = "tower"
	tenant.Name = "Tower Realty"
	tenant.Domain = "tower.example.com"
	svc := resolveFixture(tenant, "test-key-1")

	got, err := svc.ResolveByAPIKey(context.Background(), "test-key-1")
	if err != nil {
		t.Fatalf("ResolveByAPIKey: %v", err)
	}
	if got.TenantID != tenant.ID || got.Slug != "tower" || got.Name != "Tower Realty" ||
		got.Status != "active" || got.Domain != "tower.example.com" {
		t.Errorf("unexpected identity: %+v", got)
	}
	if got.Hosts == nil || len(got.Hosts) != 0 {
		t.Errorf("hosts must be a non-nil empty slice, got %#v", got.Hosts)
	}

	tenant.Hosts = []string{"a.example.com", "b.example.com"}
	got, err = svc.ResolveByAPIKey(context.Background(), "test-key-1")
	if err != nil || len(got.Hosts) != 2 {
		t.Errorf("hosts did not carry through: %v %#v", err, got.Hosts)
	}
}

func TestResolveByAPIKey_SuspendedTenantIsStatusNotError(t *testing.T) {
	tenant := activeTenant()
	tenant.Status = models.TenantSuspended
	svc := resolveFixture(tenant, "test-key-1")

	got, err := svc.ResolveByAPIKey(context.Background(), "test-key-1")
	if err != nil {
		t.Fatalf("a suspended tenant must resolve, got %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("status = %q, want suspended", got.Status)
	}
}

func TestResolveByAPIKey_UnknownKeyIs401InTenantDomain(t *testing.T) {
	svc := resolveFixture(activeTenant(), "test-key-1")

	_, err := svc.ResolveByAPIKey(context.Background(), "test-key-nope")
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.Code != apierr.CodeUnauthorized || ae.HTTPStatus != 401 {
		t.Fatalf("got %v, want 401 UNAUTHORIZED", err)
	}
	if ae.Domain != apierr.DomainTenant {
		t.Errorf("domain = %v, want tenant", ae.Domain)
	}
}

func TestResolveByAPIKey_StoreErrorIsInternalNotUnauthorized(t *testing.T) {
	tenants := &fakeTenants{err: errors.New("store down")}
	svc := NewEntitlementService(tenants, &fakeSubs{}, &fakePlans{})

	_, err := svc.ResolveByAPIKey(context.Background(), "test-key-1")
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.Code != apierr.CodeInternal {
		t.Fatalf("got %v, want INTERNAL_ERROR", err)
	}
}
