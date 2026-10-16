package models

import (
	"encoding/json"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Allowed values of the closed-set fields of a TenantDomain.
const (
	DomainOwnerUs     = "us"
	DomainOwnerClient = "client"

	SubdomainApp   = "app"
	SubdomainAPI   = "api"
	SubdomainAdmin = "admin"
	SubdomainOther = "other"

	EnvProd    = "prod"
	EnvStaging = "staging"

	// Payment methods of a charge or a registrar cost (or "" when unknown).
	PayMethodCash  = "cash"
	PayMethodBank  = "bank"
	PayMethodCard  = "card"
	PayMethodOther = "other"

	// Who pays the registrar for a domain (or "" when unknown).
	PayerUs     = "us"
	PayerClient = "client"

	// Derived payment statuses. They are never stored (see PaymentStatus).
	PaymentNone   = "none"
	PaymentPaid   = "paid"
	PaymentUnpaid = "unpaid"
)

// TenantDomain is the operator's ownership, operations and billing record of
// one domain a tenant uses. It is bookkeeping only: it never feeds routing
// (Tenant.Hosts) or API-key origin binding (Tenant.Domain), and it holds
// account identifiers and emails, never credentials.
type TenantDomain struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	// Name is the canonical host form; unique across all tenants.
	Name      string `bson:"name" json:"name"`
	IsPrimary bool   `bson:"is_primary" json:"is_primary"`

	RegistrantName        string `bson:"registrant_name,omitempty" json:"registrant_name"`
	RegistrantOwner       string `bson:"registrant_owner,omitempty" json:"registrant_owner"`
	Registrar             string `bson:"registrar,omitempty" json:"registrar"`
	RegistrarAccountEmail string `bson:"registrar_account_email,omitempty" json:"registrar_account_email"`
	DNSProvider           string `bson:"dns_provider,omitempty" json:"dns_provider"`
	DNSProviderEmail      string `bson:"dns_provider_email,omitempty" json:"dns_provider_email"`

	ExpiresAt     *time.Time `bson:"expires_at,omitempty" json:"expires_at"`
	AutoRenew     bool       `bson:"auto_renew" json:"auto_renew"`
	RegistrarLock bool       `bson:"registrar_lock" json:"registrar_lock"`

	Subdomains []Subdomain      `bson:"subdomains" json:"subdomains"`
	Deployment DomainDeployment `bson:"deployment" json:"deployment"`

	DNSVerified  bool       `bson:"dns_verified" json:"dns_verified"`
	DNSCheckedAt *time.Time `bson:"dns_checked_at,omitempty" json:"dns_checked_at"`

	SSLProvider  string     `bson:"ssl_provider,omitempty" json:"ssl_provider"`
	SSLExpiresAt *time.Time `bson:"ssl_expires_at,omitempty" json:"ssl_expires_at"`

	TechContactEmail    string `bson:"tech_contact_email,omitempty" json:"tech_contact_email"`
	BillingContactEmail string `bson:"billing_contact_email,omitempty" json:"billing_contact_email"`
	Notes               string `bson:"notes,omitempty" json:"notes"`

	// Payment records both sides of the domain money: what the client is
	// charged and what the registrar charges us. The status of each part is
	// derived from its dates and ExpiresAt, never stored.
	Payment DomainPayment `bson:"payment" json:"payment"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// Subdomain is one hostname under a domain and what it is for.
type Subdomain struct {
	Name    string `bson:"name" json:"name"`
	Purpose string `bson:"purpose" json:"purpose"`
	Target  string `bson:"target,omitempty" json:"target"`
	Status  string `bson:"status,omitempty" json:"status"`
}

// DomainDeployment records where a domain's site is hosted.
type DomainDeployment struct {
	Provider      string `bson:"provider,omitempty" json:"provider"`
	ProviderEmail string `bson:"provider_email,omitempty" json:"provider_email"`
	Environment   string `bson:"environment,omitempty" json:"environment"`
	Region        string `bson:"region,omitempty" json:"region"`
	ResourceID    string `bson:"resource_id,omitempty" json:"resource_id"`
	Address       string `bson:"address,omitempty" json:"address"`
	DNSTarget     string `bson:"dns_target,omitempty" json:"dns_target"`
}

// DomainPayment is the payment record of a domain. All money is int64 MNT
// minor units (>= 0). Dates are dates; only their UTC day matters.
type DomainPayment struct {
	ClientCharge  ClientCharge  `bson:"client_charge" json:"client_charge"`
	RegistrarCost RegistrarCost `bson:"registrar_cost" json:"registrar_cost"`
}

// ClientCharge is what we bill the client for keeping the domain. PaidThrough
// is the date the paid period covers up to.
type ClientCharge struct {
	Amount      int64      `bson:"amount" json:"amount"`
	PaidAt      *time.Time `bson:"paid_at,omitempty" json:"paid_at"`
	PaidThrough *time.Time `bson:"paid_through,omitempty" json:"paid_through"`
	Method      string     `bson:"method,omitempty" json:"method"`
	Reference   string     `bson:"reference,omitempty" json:"reference"`
}

// RegistrarCost is what the registrar charges for the domain and who pays it.
// Registrar costs are never put on a tenant invoice.
type RegistrarCost struct {
	Amount      int64      `bson:"amount" json:"amount"`
	PaidAt      *time.Time `bson:"paid_at,omitempty" json:"paid_at"`
	PaidThrough *time.Time `bson:"paid_through,omitempty" json:"paid_through"`
	Payer       string     `bson:"payer,omitempty" json:"payer"`
	Method      string     `bson:"method,omitempty" json:"method"`
	Reference   string     `bson:"reference,omitempty" json:"reference"`
}

// PaymentStatus derives the status of one payment part. It is "none" when
// amount is 0; "paid" when paidThrough is set and covers the domain's expiry
// (UTC date compare; a domain with no expiry has nothing to outrun, so any
// paidThrough settles it); "unpaid" otherwise. Because it is derived, moving
// the domain's expires_at forward (a renewal) makes it unpaid again with no
// write to the payment record.
func PaymentStatus(amount int64, paidThrough, expiresAt *time.Time) string {
	switch {
	case amount <= 0:
		return PaymentNone
	case paidThrough == nil:
		return PaymentUnpaid
	case expiresAt == nil:
		return PaymentPaid
	case utcDay(*paidThrough).Before(utcDay(*expiresAt)):
		return PaymentUnpaid
	}
	return PaymentPaid
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ClientStatus is the derived status of the client charge.
func (d *TenantDomain) ClientStatus() string {
	c := d.Payment.ClientCharge
	return PaymentStatus(c.Amount, c.PaidThrough, d.ExpiresAt)
}

// RegistrarStatus is the derived status of the registrar cost.
func (d *TenantDomain) RegistrarStatus() string {
	c := d.Payment.RegistrarCost
	return PaymentStatus(c.Amount, c.PaidThrough, d.ExpiresAt)
}

// Margin is client charge minus registrar cost, or nil unless both are > 0.
func (d *TenantDomain) Margin() *int64 {
	c, r := d.Payment.ClientCharge.Amount, d.Payment.RegistrarCost.Amount
	if c <= 0 || r <= 0 {
		return nil
	}
	m := c - r
	return &m
}

type clientChargeView struct {
	ClientCharge
	Status string `json:"status"`
}

type registrarCostView struct {
	RegistrarCost
	Status string `json:"status"`
}

type paymentView struct {
	ClientCharge  clientChargeView  `json:"client_charge"`
	RegistrarCost registrarCostView `json:"registrar_cost"`
	Margin        *int64            `json:"margin"`
}

// MarshalJSON adds the derived status of each payment part and the margin to
// the payment object; none of them is stored.
func (d TenantDomain) MarshalJSON() ([]byte, error) {
	type plain TenantDomain
	return json.Marshal(struct {
		plain
		Payment paymentView `json:"payment"`
	}{
		plain: plain(d),
		Payment: paymentView{
			ClientCharge:  clientChargeView{ClientCharge: d.Payment.ClientCharge, Status: d.ClientStatus()},
			RegistrarCost: registrarCostView{RegistrarCost: d.Payment.RegistrarCost, Status: d.RegistrarStatus()},
			Margin:        d.Margin(),
		},
	})
}
