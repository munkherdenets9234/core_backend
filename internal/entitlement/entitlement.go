// Package entitlement is the contract tenantcore serves and every product
// service consumes: what is this tenant currently allowed to run?
//
// tenantcore is the PRODUCING side. The identical package in digitalservice
// (and in every product service after it) is the consuming side. The two are
// separate copies of one wire contract rather than a shared import, because
// the whole point of the split is that a product can be deployed, and later
// rewritten, without being compiled against the platform. What they must
// agree on is the JSON, and the JSON is what these tags describe.
//
// Change this struct and you have changed an API. Adding a field is safe in
// both directions; renaming or removing one is not, and the older side will
// silently read a zero value rather than fail — which for Modules means
// "unenforced" and for Status means "unknown". Add, do not rename.
//
// Three rules hold the design together.
//
//  1. Entitlement is not authentication. Auth answers "who are you";
//     entitlement answers "what may this tenant run right now". Mixing them
//     is how you end up re-checking a password to discover whether someone
//     paid.
//
//  2. The platform stores the number, the product decides what it means.
//     Limits are an opaque map here. This package does not know that
//     "locations" is a car wash branch, and the manage-tenant service must
//     not know either — otherwise every new product feature needs a
//     coordinated change in two repositories and a synchronised deploy.
//
//  3. A missing answer is not a denial. See Provider.
//
// This package imports nothing from the rest of the service on purpose. It
// must stay describable over the wire, because one day it will be.
package entitlement

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Status is the billing state of a tenant's subscription. It mirrors
// models.SubscriptionStatus rather than importing it: the values travel over
// the wire once the platform is its own service, and a wire contract that
// moves whenever a database enum is renamed is not a contract.
type Status string

const (
	StatusActive   Status = "active"
	StatusTrialing Status = "trialing"
	StatusPastDue  Status = "past_due"
	StatusCanceled Status = "canceled"
	// StatusUnknown is what a degraded provider reports when it has never
	// successfully fetched this tenant. It is not a denial on its own — see
	// Entitlement.Stale.
	StatusUnknown Status = ""
)

// Entitlement is the document the platform hands a product service about one
// tenant. This is the shape that will go over the wire.
//
// Two axes, deliberately separate:
//
//   - Modules is WHICH PRODUCTS the tenant has bought. A tenant on
//     ["travel"] cannot reach the car wash routes at all.
//   - Limits and Features are the BUSINESS LEVEL within a product they do
//     have. Same product, different tier.
//
// Collapsing those into one list looks tidy for about a week, until you need
// "has carwash, but only three branches" and have to encode the tier into the
// module name.
type Entitlement struct {
	TenantID primitive.ObjectID `json:"tenant_id"`

	// Name and Slug are the tenant's own identity — the trading name a
	// product puts on its pages, and the short handle it is filed under.
	//
	// They sit in the entitlement document rather than behind a separate
	// "fetch the tenant" call, and that is a deliberate trade. Strictly they
	// are identity, not entitlement. But a product service already makes
	// exactly one cached call per request to resolve which tenant it is
	// serving, and a second endpoint would mean a second call, a second
	// cache and a second thing to be stale — to deliver two strings that
	// change about as often as the company is renamed.
	//
	// The alternative that was rejected: letting each product keep the
	// tenant's name in its own configuration. That works until somebody
	// renames the business in the console and every storefront keeps showing
	// the old name, with nothing to say why.
	Name string `json:"name"`
	Slug string `json:"slug"`

	Status Status `json:"status"`
	// PeriodEnd is when the current paid period lapses. Zero means no period
	// is being tracked, which Active treats as not expired — a tenant on a
	// plan with no billing period (an internal or comped account) is not
	// retroactively expired by leaving the field empty.
	PeriodEnd time.Time `json:"period_end"`

	// Modules the tenant may reach. An EMPTY list means no module gate is
	// being enforced for this tenant, not that they may reach nothing — see
	// HasModule, which documents why, and the note in the middleware.
	Modules []string `json:"modules"`

	// Limits are numeric ceilings, keyed by a name the product defines.
	// Absent means no ceiling, not zero. See Limit.
	Limits map[string]int `json:"limits"`

	// Features are on/off switches, keyed by a name the product defines.
	// Absent means off.
	Features map[string]bool `json:"features"`

	// Stale is set when this answer came from a cache because the platform
	// could not be reached.
	//
	// It exists so a caller can distinguish "the tenant is not entitled"
	// from "we could not find out". The service must keep working in the
	// second case — an entitlement lookup that fails closed turns the
	// manage-tenant service into a single point of failure for every product
	// at once, which is strictly worse than the monolith it replaced. It must
	// not fail silently either, which is what Stale is for: surface it on
	// /readyz and alert on it.
	//
	// The in-process provider never sets this; there is nothing to be stale
	// about when the data is one query away. The HTTP client will.
	Stale bool `json:"stale,omitempty"`
}

// Provider answers for one tenant.
//
// An implementation MUST NOT return an error simply because a tenant has no
// subscription record. That is a legitimate state — see
// middleware.SubscriptionMiddleware, where a tenant with no subscription is
// deliberately not held to any subscription state, because provisioning one
// is a separate explicit act by a platform superadmin. Report it as an
// Entitlement with StatusUnknown and no modules, and let the caller decide.
//
// An error from For means the lookup itself failed: the database was
// unreachable, the platform returned a 500. Callers treat that as "we could
// not find out", never as "no".
type Provider interface {
	For(ctx context.Context, tenantID primitive.ObjectID) (Entitlement, error)
}

// ProviderFunc adapts a function to Provider, for tests and for wrapping one
// provider in another (a cache, a metric, a fallback to last-known).
type ProviderFunc func(ctx context.Context, tenantID primitive.ObjectID) (Entitlement, error)

func (f ProviderFunc) For(ctx context.Context, tenantID primitive.ObjectID) (Entitlement, error) {
	return f(ctx, tenantID)
}

// Active reports whether the subscription is in a state that permits use.
//
// This is the same rule the subscription middleware has always applied —
// active or trialing, and not past its period end — stated once here so the
// gate, the limits and any future product all agree on what "paying" means.
func (e Entitlement) Active() bool {
	switch e.Status {
	case StatusActive, StatusTrialing:
	default:
		return false
	}
	if e.PeriodEnd.IsZero() {
		return true
	}
	return time.Now().Before(e.PeriodEnd)
}

// HasModule reports whether the tenant may reach the named product.
//
// An EMPTY Modules list means "not enforced" and returns true for everything.
// That is a migration affordance with a deliberate expiry date: every
// existing plan today predates modules, and a strict reading would 402 every
// live tenant the moment the first gate is mounted.
//
// It is also exactly the kind of default that quietly becomes permanent, so:
// the gate is safe to mount only while no plan lists modules. Once ANY plan
// does, a plan that lists none is almost certainly an unfinished plan rather
// than an unlimited one. Tighten this to `return false` for an empty list as
// soon as every plan carries its modules, and delete this paragraph.
func (e Entitlement) HasModule(name string) bool {
	if len(e.Modules) == 0 {
		return true
	}
	for _, m := range e.Modules {
		if m == name {
			return true
		}
	}
	return false
}

// Limit returns the ceiling the plan sets for key.
//
// ok is false when the plan sets no ceiling, which means unlimited — NOT
// zero. The two must stay distinguishable: a plan that forgot to mention
// "locations" and a plan that grants zero locations are different situations,
// and a bare int cannot tell them apart. Callers that collapse them will
// eventually ship the one where a missing key silently forbids everything.
func (e Entitlement) Limit(key string) (value int, ok bool) {
	if e.Limits == nil {
		return 0, false
	}
	v, ok := e.Limits[key]
	return v, ok
}

// Within reports whether adding one more of key stays inside the plan.
//
// current is how many the tenant already has, which only the product can
// count — which is why limits are checked in the service layer at the write,
// not in middleware. Middleware sees a request; it does not see a row count,
// and quota checks bolted into middleware are abandoned about as often as
// they are attempted.
func (e Entitlement) Within(key string, current int) bool {
	limit, ok := e.Limit(key)
	if !ok {
		return true
	}
	return current < limit
}

// Feature reports whether an on/off capability is granted. Absent means off:
// a feature nobody has written into a plan is one nobody has sold.
func (e Entitlement) Feature(key string) bool {
	if e.Features == nil {
		return false
	}
	return e.Features[key]
}
