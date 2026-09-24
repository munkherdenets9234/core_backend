// Package models holds what tenantcore is the authority for.
//
// The rule that decides whether something belongs here: does every product
// need to agree on it? A tenant's identity, who its staff are, what it has
// bought — yes. A tenant's blog posts or its car wash bays — no, those belong
// to the product that serves them. tenantcore never learns what a product
// sells; it only knows who the customer is and what they are entitled to.
package models

import (
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
