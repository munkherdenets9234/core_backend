// Package view maps stored documents onto response types.
//
// It exists so that what goes on the wire is a decision rather than a
// consequence of what a struct happened to contain. A model gains a field
// because the database needs it; a view gains one because someone decided a
// caller should see it. Where those two are the same type, the first quietly
// becomes the second.
//
// tenantcore has two audiences and they see different things:
//
//   - The platform console (admin) sees tenants, plans, subscriptions and
//     staff in full, because a superadmin administers them.
//   - Product services (svc) see one thing: the entitlement document. They
//     never receive a tenant record, a plan document, or an email address.
//     That is not enforced by filtering — it is enforced by there being no
//     view here that the service audience could return.
package view

import (
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
)

// Tenant is the console's view of a tenant.
//
// The API key hash is absent and unrepresentable: there is no field for it,
// so no future edit to the model can put it on the wire. Last4 is present
// because an administrator needs to recognise which key a tenant is using
// without being able to reconstruct it.
type Tenant struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	ContactEmail string    `json:"contact_email"`
	APIKeyLast4  string    `json:"api_key_last4"`
	Domain       string    `json:"domain,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// Project is the publication state of this tenant's case study, and
	// nothing else — not the case study itself, which the editor fetches on
	// its own. Absent when the tenant has none.
	//
	// It is here because the console's tenant list shows a "showcase"
	// column, and a column with no data source does not render as blank: it
	// renders as "hidden", which is a claim, and a false one for every
	// published tenant.
	Project *TenantProject `json:"project,omitempty"`
}

// TenantProject is the list-sized view of a case study's publication state.
type TenantProject struct {
	Showcase bool `json:"showcase"`
	Featured bool `json:"featured"`
}

func TenantOf(t *models.Tenant) Tenant {
	return Tenant{
		ID:           t.ID.Hex(),
		Name:         t.Name,
		Slug:         t.Slug,
		ContactEmail: t.ContactEmail,
		APIKeyLast4:  t.APIKeyLast4,
		Domain:       t.Domain,
		Status:       string(t.Status),
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
	}
}

func TenantsOf(ts []*models.Tenant) []Tenant {
	out := make([]Tenant, 0, len(ts))
	for _, t := range ts {
		out = append(out, TenantOf(t))
	}
	return out
}

// WithProject returns t carrying its publication state. A separate step from
// TenantOf because most callers have no flags to attach and should not have
// to pass nil to say so.
func (t Tenant) WithProject(showcase, featured bool) Tenant {
	t.Project = &TenantProject{Showcase: showcase, Featured: featured}
	return t
}

// PlatformUser is the console's view of a staff account. No password hash
// field exists here, for the same reason as above.
type PlatformUser struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func PlatformUserOf(u *models.PlatformUser) PlatformUser {
	return PlatformUser{
		ID:        u.ID.Hex(),
		Name:      u.Name,
		Email:     u.Email,
		Status:    string(u.Status),
		CreatedAt: u.CreatedAt,
	}
}

func PlatformUsersOf(us []*models.PlatformUser) []PlatformUser {
	out := make([]PlatformUser, 0, len(us))
	for _, u := range us {
		out = append(out, PlatformUserOf(u))
	}
	return out
}

// ServiceClient is the console's view of a registered product service.
type ServiceClient struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyLast4   string     `json:"key_last4"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

func ServiceClientOf(c *models.ServiceClient) ServiceClient {
	return ServiceClient{
		ID:         c.ID.Hex(),
		Name:       c.Name,
		KeyLast4:   c.KeyLast4,
		Status:     string(c.Status),
		CreatedAt:  c.CreatedAt,
		LastSeenAt: c.LastSeenAt,
	}
}

func ServiceClientsOf(cs []*models.ServiceClient) []ServiceClient {
	out := make([]ServiceClient, 0, len(cs))
	for _, c := range cs {
		out = append(out, ServiceClientOf(c))
	}
	return out
}

// Plan is the console's view of a pricing tier, entitlement included —
// unlike digitalservice's public package read, where the same information is
// withheld because that endpoint is unauthenticated. Here the caller is a
// superadmin who is editing it.
type Plan struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Price        float64         `json:"price"`
	Currency     string          `json:"currency"`
	PeriodDays   int             `json:"period_days"`
	Modules      []string        `json:"modules"`
	Limits       map[string]int  `json:"limits"`
	Capabilities map[string]bool `json:"capabilities"`
	// Marketing is the pricing-card copy. Present here and absent from the
	// svc surface: a product service is told what a tenant may run, never
	// what the tier is called in Mongolian.
	Marketing *models.PlanMarketing `json:"marketing,omitempty"`
	IsActive  bool                  `json:"is_active"`
	SortOrder int                   `json:"sort_order"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}

func PlanOf(p *models.Plan) Plan {
	// Nil maps and slices are rendered as [] and {} rather than null: a
	// console that has to handle both is a console with a bug waiting in it.
	modules := p.Modules
	if modules == nil {
		modules = []string{}
	}
	limits := p.Limits
	if limits == nil {
		limits = map[string]int{}
	}
	caps := p.Capabilities
	if caps == nil {
		caps = map[string]bool{}
	}
	return Plan{
		Marketing:    p.Marketing,
		ID:           p.ID.Hex(),
		Slug:         p.Slug,
		Name:         p.Name,
		Price:        p.Price,
		Currency:     p.Currency,
		PeriodDays:   p.Period(),
		Modules:      modules,
		Limits:       limits,
		Capabilities: caps,
		IsActive:     p.IsActive,
		SortOrder:    p.SortOrder,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
}

func PlansOf(ps []*models.Plan) []Plan {
	out := make([]Plan, 0, len(ps))
	for _, p := range ps {
		out = append(out, PlanOf(p))
	}
	return out
}

// Subscription is the console's view of what a tenant is paying for.
type Subscription struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	PlanID             string     `json:"plan_id"`
	Status             string     `json:"status"`
	CurrentPeriodStart time.Time  `json:"current_period_start"`
	CurrentPeriodEnd   time.Time  `json:"current_period_end"`
	CanceledAt         *time.Time `json:"canceled_at,omitempty"`
	// Plan is nil when the plan was deleted out from under the
	// subscription — which is a real state, not an error. See
	// SubscriptionService.Get.
	Plan *Plan `json:"plan,omitempty"`
}

func SubscriptionOf(s *models.Subscription) Subscription {
	out := Subscription{
		ID:                 s.ID.Hex(),
		TenantID:           s.TenantID.Hex(),
		PlanID:             s.PlanID.Hex(),
		Status:             string(s.Status),
		CurrentPeriodStart: s.CurrentPeriodStart,
		CurrentPeriodEnd:   s.CurrentPeriodEnd,
		CanceledAt:         s.CanceledAt,
	}
	if s.Plan != nil {
		p := PlanOf(s.Plan)
		out.Plan = &p
	}
	return out
}
