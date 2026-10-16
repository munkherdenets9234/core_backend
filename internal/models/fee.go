package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Allowed values of the closed-set fields of a TenantFee and of an invoice
// line. FeeKindSubscription is never stored on a fee: the subscription line
// is derived from the tenant's subscription and plan at read time.
const (
	FeeKindSubscription = "subscription"
	FeeKindDomain       = "domain"
	FeeKindDeployment   = "deployment"
	FeeKindOther        = "other"

	FeeIntervalMonthly = "monthly"
	FeeIntervalYearly  = "yearly"
	FeeIntervalOneTime = "one_time"

	// CurrencyMNT is the only currency. Money is stored as int64 whole MNT.
	CurrencyMNT = "MNT"
)

// TenantFee is one recurring or one-off charge the operator bills a tenant
// for, besides the subscription. It is bookkeeping only and never feeds
// entitlement, routing or authentication.
type TenantFee struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	Kind     string `bson:"kind" json:"kind"`
	Label    string `bson:"label" json:"label"`
	Amount   int64  `bson:"amount" json:"amount"`
	Currency string `bson:"currency" json:"currency"`
	Interval string `bson:"interval" json:"interval"`

	// DomainID links a domain fee to the tenant's domain record, whose
	// expires_at is the fee's next due date.
	DomainID *primitive.ObjectID `bson:"domain_id,omitempty" json:"domain_id"`
	Active   bool                `bson:"active" json:"active"`

	// LastInvoicedThrough is the end of the last period that was invoiced and
	// paid; deployment and other fees fall due one interval after it.
	LastInvoicedThrough *time.Time `bson:"last_invoiced_through,omitempty" json:"last_invoiced_through"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}
