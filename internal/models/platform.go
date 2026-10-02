// Package models holds what tenantcore is the authority for.
//
// The rule that decides whether something belongs here: does every product
// need to agree on it? A tenant's identity, who its staff are, what it has
// bought — yes. A tenant's blog posts or its car wash bays — no, those belong
// to the product that serves them. tenantcore never learns what a product
// sells; it only knows who the customer is and what they are entitled to.
package models

import (
	"encoding/json"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type TenantStatus string

const (
	TenantActive    TenantStatus = "active"
	TenantSuspended TenantStatus = "suspended"
)

// Tenant is one customer of the platform, across every product.
//
// The field set matches digitalservice's Tenant exactly. That is deliberate:
// the cutover is a copy of the collection, and a schema that differs by even
// one field name turns a copy into a migration script nobody wants to debug
// at the moment of switching.
type Tenant struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name         string             `bson:"name" json:"name"`
	Slug         string             `bson:"slug" json:"slug"`
	ContactEmail string             `bson:"contact_email" json:"contact_email"`

	// APIKeyHash is the SHA-256 of the key a product service is shown once.
	// The raw key is never stored and cannot be recovered — rotation issues a
	// new one rather than revealing the old.
	APIKeyHash  string `bson:"api_key_hash" json:"-"`
	APIKeyLast4 string `bson:"api_key_last4" json:"api_key_last4"`

	// Domain, when set, binds the tenant's API key to one origin.
	Domain string `bson:"domain,omitempty" json:"domain,omitempty"`

	// Hosts are the public site hostnames a product maps to this tenant
	// (a visitor arriving on tower.example.com is this tenant's visitor).
	// Deliberately separate from Domain, which binds the API key to an origin:
	// the two answer different questions and must be changeable independently.
	// Always stored via NormalizeHost; unique across tenants (see the index).
	Hosts []string `bson:"site_hosts,omitempty" json:"hosts"`

	Status    TenantStatus `bson:"status" json:"status"`
	CreatedAt time.Time    `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time    `bson:"updated_at" json:"updated_at"`
}

type PlatformUserStatus string

const (
	PlatformUserActive    PlatformUserStatus = "active"
	PlatformUserSuspended PlatformUserStatus = "suspended"
)

// PlatformUser is a member of the platform operator's own staff. Every one of
// them is a superadmin; there is one role at this level, unlike a tenant's
// own users who are admin or staff.
//
// These are the accounts tenantcore issues Ed25519 tokens for.
type PlatformUser struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name         string             `bson:"name" json:"name"`
	Email        string             `bson:"email" json:"email"`
	PasswordHash string             `bson:"password_hash" json:"-"`
	Status       PlatformUserStatus `bson:"status" json:"status"`
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt    time.Time          `bson:"updated_at" json:"updated_at"`
}

type ServiceClientStatus string

const (
	ServiceClientActive  ServiceClientStatus = "active"
	ServiceClientRevoked ServiceClientStatus = "revoked"
)

// ServiceClient is a product service allowed to ask tenantcore questions —
// digitalservice, carwash, whatever comes next.
//
// It exists because the entitlement endpoint is machine-to-machine and has no
// human behind it, so none of the human auth applies. A per-service key
// rather than one shared secret, for the same reason tokens are asymmetric:
// when carwash's key leaks, carwash's key is revoked, and nothing else has to
// be redeployed at three in the morning.
//
// The key is stored as a hash, like a tenant's. Revoking is a status change
// rather than a delete so the audit trail of which service asked what
// survives the revocation.
type ServiceClient struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name string             `bson:"name" json:"name"` // "digitalservice", "carwash"

	KeyHash  string `bson:"key_hash" json:"-"`
	KeyLast4 string `bson:"key_last4" json:"key_last4"`

	Status    ServiceClientStatus `bson:"status" json:"status"`
	CreatedAt time.Time           `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time           `bson:"updated_at" json:"updated_at"`

	// LastSeenAt is stamped on each successful authentication, best-effort.
	// It is how you find out that a service you thought was retired is still
	// calling, or that one you thought was live stopped a week ago.
	LastSeenAt *time.Time `bson:"last_seen_at,omitempty" json:"last_seen_at,omitempty"`
}

// NormalizeHost reduces a hostname to the form hosts are stored and looked up
// in: lowercase, no port, no trailing dot. A visitor's Host header arrives in
// whatever shape their browser and proxy produced, and a stored value that
// differs only in case or a ":443" would be a tenant that exists but is never
// found.
func NormalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	switch {
	case strings.HasPrefix(h, "["):
		// Bracketed IPv6, with or without a port: the address is what is inside.
		if i := strings.Index(h, "]"); i > 0 {
			return h[1:i]
		}
		return h
	case strings.Count(h, ":") > 1:
		// Bare IPv6: colons are address, not a port. Returned unchanged.
		return h
	case strings.Contains(h, ":"):
		h = h[:strings.Index(h, ":")]
	}
	return strings.TrimRight(h, ".")
}

// MarshalJSON renders an unset Hosts as [] rather than null, so a console can
// iterate it without a nil check.
func (t Tenant) MarshalJSON() ([]byte, error) {
	type plain Tenant
	p := plain(t)
	if p.Hosts == nil {
		p.Hosts = []string{}
	}
	return json.Marshal(p)
}
