package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Plan is one tier the platform sells.
//
// This is the billing half of what digitalservice currently calls Package.
// Package there does two unrelated jobs — it is both the plan a tenant is
// charged for and the pricing card a tenant shows its own visitors — and the
// split happens here: the plan moves to tenantcore, the pricing card stays in
// digitalservice as tenant content.
//
// Which is why there are no locale maps on this type. Plan.Name is one
// string, for the platform console. Nothing a visitor reads lives here.
type Plan struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Slug string             `bson:"slug" json:"slug"`
	Name string             `bson:"name" json:"name"`

	Price    float64 `bson:"price" json:"price"`
	Currency string  `bson:"currency" json:"currency"`

	// PeriodDays is how long one billing period runs. Stored per plan rather
	// than hardcoded at 30 the way digitalservice does today, so an annual
	// plan does not require a code change.
	PeriodDays int `bson:"period_days" json:"period_days"`

	// ── What subscribing actually grants ──────────────────────────────────
	//
	// The platform stores these; it does not interpret them. tenantcore does
	// not know that "locations" counts car wash branches, and must not learn:
	// the moment billing understands product semantics, every new product
	// feature needs a change in two services and a synchronised deploy.

	// Modules are the products this plan grants. Empty means no module gate
	// is enforced for tenants on this plan — a migration affordance while
	// plans predating modules still exist. See entitlement.HasModule.
	Modules []string `bson:"modules,omitempty" json:"modules"`

	// Limits are numeric ceilings keyed by a name the PRODUCT defines. An
	// absent key means unlimited, not zero; the two must stay
	// distinguishable, because a plan that forgot to mention a resource and
	// one that grants none of it are different situations.
	Limits map[string]int `bson:"limits,omitempty" json:"limits"`

	// Capabilities are on/off grants, keyed the same way. Absent is off.
	Capabilities map[string]bool `bson:"capabilities,omitempty" json:"capabilities"`

	// Marketing is the pricing card a visitor reads: bilingual copy that the
	// public site renders and the console edits.
	//
	// It sits on the plan rather than in a separate collection because a
	// price list has exactly one row per plan, and splitting it bought
	// nothing but a join. It stays a NESTED document rather than loose
	// fields so the two halves of this type cannot be confused: everything
	// above is enforced by a server, everything in here is read by a human.
	// Nothing in this struct is ever consulted when assembling an
	// entitlement.
	Marketing *PlanMarketing `bson:"marketing,omitempty" json:"marketing,omitempty"`

	IsActive  bool      `bson:"is_active" json:"is_active"`
	SortOrder int       `bson:"sort_order" json:"sort_order"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`

	// UserID is the platform user who last changed this plan. A price list
	// is the kind of thing where "who changed this and when" is the first
	// question asked and the hardest to answer afterwards.
	UserID *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`
}

// DefaultPeriodDays is used when a plan does not state its own. It matches
// the constant digitalservice has always applied.
const DefaultPeriodDays = 30

// Period returns the plan's billing period, falling back to the default.
func (p Plan) Period() int {
	if p.PeriodDays < 1 {
		return DefaultPeriodDays
	}
	return p.PeriodDays
}

// LocaleText is one string per language, keyed by ISO code ("en", "mn").
//
// A map rather than a struct with named fields: adding a third language
// should be a data change, not a schema migration in three repositories.
type LocaleText map[string]string

// LocaleList is the same idea for bullet lists.
type LocaleList map[string][]string

// PlanMarketing is the visitor-facing half of a plan.
//
// Every field here is optional. A plan created for internal use — a comped
// account, a migration placeholder — has no pricing card and should not be
// forced to invent one.
type PlanMarketing struct {
	Name        LocaleText `bson:"name,omitempty" json:"name,omitempty"`
	Tagline     LocaleText `bson:"tagline,omitempty" json:"tagline,omitempty"`
	BillingNote LocaleText `bson:"billing_note,omitempty" json:"billing_note,omitempty"`
	Features    LocaleList `bson:"features,omitempty" json:"features,omitempty"`
	// Highlighted draws the "most popular" ribbon on the public pricing page.
	Highlighted bool `bson:"highlighted,omitempty" json:"highlighted,omitempty"`
}

// Text returns the copy for lang, falling back to English and then to any
// language present. A pricing card with a blank name because one translation
// was never filled in is worse than showing the other language.
func (t LocaleText) Text(lang string) string {
	if v, ok := t[lang]; ok && v != "" {
		return v
	}
	if v, ok := t["en"]; ok && v != "" {
		return v
	}
	for _, v := range t {
		if v != "" {
			return v
		}
	}
	return ""
}

// List is LocaleList's counterpart to Text, with the same fallback order.
func (l LocaleList) List(lang string) []string {
	if v, ok := l[lang]; ok && len(v) > 0 {
		return v
	}
	if v, ok := l["en"]; ok && len(v) > 0 {
		return v
	}
	for _, v := range l {
		if len(v) > 0 {
			return v
		}
	}
	return []string{}
}

type SubscriptionStatus string

const (
	SubscriptionActive   SubscriptionStatus = "active"
	SubscriptionPastDue  SubscriptionStatus = "past_due"
	SubscriptionCanceled SubscriptionStatus = "canceled"
	SubscriptionTrialing SubscriptionStatus = "trialing"
)

// Subscription is what one tenant is paying for.
//
// One per tenant, enforced by a unique index. A tenant who wants both travel
// and car wash holds ONE subscription whose plan lists both modules — not two
// subscriptions. That is one invoice, one renewal date and one past-due
// state; per-module subscriptions buy the ability to be past-due on one
// product and current on another, which is real but multiplies the dunning
// logic and is not worth it before there is a payment provider at all.
//
// There is no payment provider yet. Status and plan are set directly by a
// platform superadmin. When one is wired in, this is the record it drives.
type Subscription struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`
	PlanID   primitive.ObjectID `bson:"plan_id" json:"plan_id"`

	Status             SubscriptionStatus `bson:"status" json:"status"`
	CurrentPeriodStart time.Time          `bson:"current_period_start" json:"current_period_start"`
	CurrentPeriodEnd   time.Time          `bson:"current_period_end" json:"current_period_end"`
	CanceledAt         *time.Time         `bson:"canceled_at,omitempty" json:"canceled_at,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`

	UserID *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`

	// BillingDay is the day of the month this subscription renews on, 1 to 28.
	// Zero means none is stored, which is every subscription that predates the
	// field; read it through EffectiveBillingDay, never directly.
	BillingDay int `bson:"billing_day,omitempty" json:"billing_day"`

	// ExpiryNoticeFor is the current_period_end a warning has already been
	// claimed for. It holds the DATE rather than a boolean on purpose: renewing
	// or changing plan moves current_period_end, so the marker stops matching
	// and the next period is warned about with no reset step anywhere.
	//
	// Internal bookkeeping, so it is not part of the wire contract.
	ExpiryNoticeFor *time.Time `bson:"expiry_notice_for,omitempty" json:"-"`

	// Plan is resolved on read by the service layer, not persisted. Left nil
	// if the plan was deleted out from under the subscription — which does
	// not invalidate the billing state and must not fail the read.
	Plan *Plan `bson:"-" json:"plan,omitempty"`
}

// DefaultBillingDay is the day subscriptions renew on unless told otherwise.
// The business bills on the 20th of the month.
const DefaultBillingDay = 20

// EffectiveBillingDay is the billing day to act on. A subscription with none
// stored behaves as DefaultBillingDay, which is how every existing one keeps
// working with no migration.
func (s Subscription) EffectiveBillingDay() int {
	if s.BillingDay == 0 {
		return DefaultBillingDay
	}
	return s.BillingDay
}
