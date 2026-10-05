package service

import (
	"context"
	"errors"

	"github.com/eandstravel/tenantcore/internal/entitlement"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/apikey"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// The three reads entitlement assembly needs, as interfaces rather than the
// concrete repositories.
//
// Not ceremony: this is the one piece of logic in tenantcore that every
// product depends on for every request, and depending on *repository.X made
// it impossible to exercise without a live MongoDB. Narrow interfaces mean
// the rules below — which states are errors and which are not — are tested on
// the dev toolchain alone, which is where they will actually get run.
type (
	tenantSource interface {
		FindByID(ctx context.Context, id primitive.ObjectID) (*models.Tenant, error)
		FindByAPIKeyHash(ctx context.Context, hash string) (*models.Tenant, error)
		FindByHost(ctx context.Context, host string) (*models.Tenant, error)
	}
	subscriptionSource interface {
		FindByTenantID(ctx context.Context, tenantID primitive.ObjectID) (*models.Subscription, error)
	}
	planSource interface {
		FindByID(ctx context.Context, id primitive.ObjectID) (*models.Plan, error)
	}
)

// EntitlementService assembles the document every product service asks for.
//
// This is what tenantcore exists to serve. Everything else here — the tenant
// console, the plan editor — is administration around this one answer.
type EntitlementService struct {
	tenants       tenantSource
	subscriptions subscriptionSource
	plans         planSource
}

var _ entitlement.Provider = (*EntitlementService)(nil)

func NewEntitlementService(tenants tenantSource, subscriptions subscriptionSource, plans planSource) *EntitlementService {
	return &EntitlementService{tenants: tenants, subscriptions: subscriptions, plans: plans}
}

// For assembles the entitlement for one tenant.
//
// Three states are deliberately NOT errors, because each is a legitimate
// thing to be and a product service must keep serving through all of them:
//
//   - No subscription record. A tenant provisioned five minutes ago, whose
//     subscription a superadmin has not created yet, should not have their
//     storefront start refusing traffic in the gap.
//   - A plan deleted out from under a live subscription. The billing state
//     still stands; there is simply nothing left to read the modules from.
//   - A suspended tenant. Reported as a status, not raised as an error.
//
// An error from For means the lookup itself failed. Consumers treat that as
// "could not find out" and serve last-known state — never as "no". See the
// note on entitlement.Entitlement.Stale.
func (s *EntitlementService) For(ctx context.Context, tenantID primitive.ObjectID) (entitlement.Entitlement, error) {
	ent := entitlement.Entitlement{TenantID: tenantID, Status: entitlement.StatusUnknown}

	tenant, err := s.tenants.FindByID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return ent, apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return ent, apierr.Internal(err)
	}

	// Identity travels with every answer, including the refusals below. A
	// product that is told "this tenant is suspended" still has a page to
	// render saying so, and rendering it under a blank company name is a
	// worse experience than the suspension itself.
	ent.Name = tenant.Name
	ent.Slug = tenant.Slug

	// Suspension outranks the billing state: it is an administrative
	// decision, and every product must see it the same way rather than each
	// deciding how to combine "suspended" with "paid up".
	if tenant.Status != models.TenantActive {
		ent.Status = entitlement.StatusCanceled
		return ent, nil
	}

	sub, err := s.subscriptions.FindByTenantID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return ent, nil
		}
		return ent, apierr.Internal(err)
	}

	ent.Status = statusOf(sub.Status)
	ent.PeriodEnd = sub.CurrentPeriodEnd

	plan, err := s.plans.FindByID(ctx, sub.PlanID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return ent, nil
		}
		return ent, apierr.Internal(err)
	}

	ent.Modules = plan.Modules
	ent.Limits = plan.Limits
	ent.Features = plan.Capabilities
	return ent, nil
}

// ForAPIKey is the shape a product service actually calls: it holds the
// tenant's API key, not the tenant's id.
//
// Resolving the key here keeps one authority for what a key means. A product
// that looked the id up itself would need its own copy of the tenants
// collection, which is the coupling this whole split exists to remove.
//
// An unknown key is a 401 — that is a failed authentication. A key belonging
// to a SUSPENDED tenant is not: it resolves, and For reports the suspension
// as a status, so a product gets the same uniform document either way rather
// than having to handle suspension differently depending on which endpoint it
// happened to call.
func (s *EntitlementService) ForAPIKey(ctx context.Context, rawKey string) (entitlement.Entitlement, error) {
	t, err := s.tenants.FindByAPIKeyHash(ctx, apikey.Hash(rawKey))
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return entitlement.Entitlement{}, apierr.Unauthorized("").In(apierr.DomainTenant)
		}
		return entitlement.Entitlement{}, apierr.Internal(err)
	}
	return s.For(ctx, t.ID)
}

// TenantIdentity is who a key belongs to, without the entitlement. It is what a
// product with no tenants collection of its own needs to scope its data: the
// id to key rows by, the host list to match a visitor against, and the status
// so it can refuse a suspended tenant itself.
type TenantIdentity struct {
	TenantID primitive.ObjectID `json:"tenant_id"`
	Slug     string             `json:"slug"`
	Name     string             `json:"name"`
	Status   string             `json:"status"`
	Domain   string             `json:"domain"`
	Hosts    []string           `json:"hosts"`
}

// ResolveByAPIKey maps a raw tenant API key to the tenant's identity.
//
// An unknown key is a 401, a failed authentication. A suspended tenant is not:
// it resolves and reports its status, matching ForAPIKey, so the caller decides
// what suspension means. A store failure is an internal error, never a 401, so
// an outage cannot be mistaken for a revoked key.
func (s *EntitlementService) ResolveByAPIKey(ctx context.Context, rawKey string) (TenantIdentity, error) {
	t, err := s.tenants.FindByAPIKeyHash(ctx, apikey.Hash(rawKey))
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return TenantIdentity{}, apierr.Unauthorized("").In(apierr.DomainTenant)
		}
		return TenantIdentity{}, apierr.Internal(err)
	}
	status := "active"
	if t.Status == models.TenantSuspended {
		status = "suspended"
	}
	hosts := make([]string, 0, len(t.Hosts))
	hosts = append(hosts, t.Hosts...)
	return TenantIdentity{
		TenantID: t.ID,
		Slug:     t.Slug,
		Name:     t.Name,
		Status:   status,
		Domain:   t.Domain,
		Hosts:    hosts,
	}, nil
}

// ForHost is the shape a product serving a public site calls: it holds the
// visitor's hostname and nothing else. One call returns the tenant and its
// entitlement, so the product does not keep a hostname-to-tenant table of its
// own.
//
// An unknown host is a 404, like an unknown tenant id: there is no tenant to
// report a status for. A host owned by a suspended or unsubscribed tenant
// resolves, and For reports that as a status, exactly as ForAPIKey does.
func (s *EntitlementService) ForHost(ctx context.Context, host string) (entitlement.Entitlement, error) {
	host = models.NormalizeHost(host)
	if host == "" {
		return entitlement.Entitlement{}, apierr.NotFound("tenant").In(apierr.DomainTenant)
	}
	t, err := s.tenants.FindByHost(ctx, host)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return entitlement.Entitlement{}, apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return entitlement.Entitlement{}, apierr.Internal(err)
	}
	return s.For(ctx, t.ID)
}

// statusOf maps the stored status onto the wire status.
//
// An explicit switch rather than a string conversion, so renaming a stored
// value becomes a compile-time conversation about the wire contract instead
// of a silent change of meaning for every product downstream.
func statusOf(s models.SubscriptionStatus) entitlement.Status {
	switch s {
	case models.SubscriptionActive:
		return entitlement.StatusActive
	case models.SubscriptionTrialing:
		return entitlement.StatusTrialing
	case models.SubscriptionPastDue:
		return entitlement.StatusPastDue
	case models.SubscriptionCanceled:
		return entitlement.StatusCanceled
	default:
		return entitlement.StatusUnknown
	}
}
