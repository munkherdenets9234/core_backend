// Package token issues and verifies the platform's access tokens.
//
// It signs with Ed25519 (JWT "EdDSA"), not the shared HMAC secret the product
// services use today, and that difference is the whole point.
//
// With HS256 the signing key and the verifying key are the same string. The
// moment a second service needs to verify a token, it must hold that string —
// and anything that can verify can also MINT. A compromised car wash
// deployment could issue itself a platform-superadmin token and every service
// would accept it, because there is no cryptographic difference between the
// issuer and a verifier.
//
// Ed25519 splits them. tenantcore holds the private key and is the only thing
// that can issue. Every product service holds only the public key: it can
// check a token and cannot forge one. That is the property that makes it safe
// to have one identity across several deployments, and it has to be decided
// now — retrofitting it once three services share a secret means rotating
// every one of them at the same moment.
package token

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Issuer is the iss claim. Products check it so a token minted by some other
// system that happens to share our key format is still refused.
const Issuer = "tenantcore"

// Role is the closed set of identities this platform issues tokens for.
type Role string

const (
	// RoleSuperadmin is platform staff: the people who administer tenants,
	// plans and subscriptions. Never tenant-scoped — see Claims.Valid.
	RoleSuperadmin Role = "superadmin"

	// RoleTenantAdmin and RoleTenantStaff are people inside one tenant.
	// Always tenant-scoped. These mirror digitalservice's existing
	// admin/staff roles so tokens stay interchangeable through the cutover.
	RoleTenantAdmin Role = "admin"
	RoleTenantStaff Role = "staff"
)

func (r Role) Valid() bool {
	switch r {
	case RoleSuperadmin, RoleTenantAdmin, RoleTenantStaff:
		return true
	}
	return false
}

// Claims is the token payload.
//
// Field names match digitalservice's existing claims exactly (user_id, role,
// tenant_id) so that during the cutover a token from either issuer decodes
// the same way. Changing the shape and the signing algorithm in one step
// would make a failure impossible to attribute to either.
type Claims struct {
	UserID   string `json:"user_id"`
	Role     Role   `json:"role"`
	TenantID string `json:"tenant_id,omitempty"` // empty for platform superadmins
	jwt.RegisteredClaims
}

// Valid enforces the one invariant that is not about signatures: a superadmin
// token must never carry a tenant scope.
//
// That combination is how a tenant-scoped role smuggled in through a bad role
// value would pass as platform staff on routes that never resolve a tenant.
// digitalservice already checks this in its auth middleware; checking it at
// mint time as well means such a token cannot be created in the first place,
// rather than only being refused by services that remembered to look.
func (c Claims) Valid() error {
	if !c.Role.Valid() {
		return fmt.Errorf("token: unknown role %q", c.Role)
	}
	if c.Role == RoleSuperadmin && c.TenantID != "" {
		return errors.New("token: a superadmin token must not be tenant-scoped")
	}
	if c.Role != RoleSuperadmin && c.TenantID == "" {
		return errors.New("token: a tenant role must be tenant-scoped")
	}
	return nil
}

// ── Issuing ───────────────────────────────────────────────────────────────

// Maker signs tokens. Only tenantcore builds one.
type Maker struct {
	private ed25519.PrivateKey
	kid     string
}

// NewMaker builds an issuer from a base64-encoded Ed25519 private key, as
// produced by GenerateKeyPair.
func NewMaker(privateKeyB64 string) (*Maker, error) {
	raw, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("token: private key is not valid base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("token: private key is %d bytes, want %d — generate one with `make keygen`",
			len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	return &Maker{private: priv, kid: KeyID(priv.Public().(ed25519.PublicKey))}, nil
}

// PublicKey returns the verifying half, for serving to product services.
func (m *Maker) PublicKey() ed25519.PublicKey {
	return m.private.Public().(ed25519.PublicKey)
}

// PublicKeyB64 is PublicKey in the form a product service puts in its config.
func (m *Maker) PublicKeyB64() string {
	return base64.StdEncoding.EncodeToString(m.PublicKey())
}

// KeyID identifies which key signed a token, so the signing key can be
// rotated without a flag day: publish both, sign with the new one, and let
// verifiers pick by kid until the old tokens have expired.
func (m *Maker) KeyID() string { return m.kid }

// Create mints a token. It refuses to sign claims that fail Valid — an
// invalid token that exists is worse than an error at the call site.
func (m *Maker) Create(userID string, role Role, tenantID string, ttl time.Duration) (string, *Claims, error) {
	now := time.Now()
	claims := &Claims{
		UserID:   userID,
		Role:     role,
		TenantID: tenantID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Issuer:    Issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	if err := claims.Valid(); err != nil {
		return "", nil, err
	}

	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["kid"] = m.kid

	signed, err := t.SignedString(m.private)
	if err != nil {
		return "", nil, fmt.Errorf("token: sign: %w", err)
	}
	return signed, claims, nil
}

// ── Verifying ─────────────────────────────────────────────────────────────

// Verifier checks tokens. This is what a product service holds: it can prove
// a token came from tenantcore and cannot produce one.
type Verifier struct {
	public ed25519.PublicKey
}

// NewVerifier builds a verifier from a base64-encoded Ed25519 public key.
func NewVerifier(publicKeyB64 string) (*Verifier, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("token: public key is not valid base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("token: public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return &Verifier{public: ed25519.PublicKey(raw)}, nil
}

// Verifier for the issuer's own routes: tenantcore verifies what it signed.
func (m *Maker) Verifier() *Verifier { return &Verifier{public: m.PublicKey()} }

// ErrInvalidToken is returned for every verification failure.
//
// One error for all of them on purpose: a caller learns nothing from being
// told whether their token was expired, malformed, signed by the wrong key or
// carrying an impossible role. The specifics go in the server log.
var ErrInvalidToken = errors.New("invalid or expired token")

// Verify checks the signature, the registered claims and the role invariant.
func (v *Verifier) Verify(tokenStr string) (*Claims, error) {
	claims := &Claims{}

	t, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		// Pin the algorithm. Accepting whatever the token's header asks for
		// is the classic JWT hole: "alg": "none", or an HMAC token verified
		// against a public key that is, by definition, public.
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return v.public, nil
	},
		jwt.WithIssuer(Issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
	)
	if err != nil || !t.Valid {
		return nil, ErrInvalidToken
	}
	if err := claims.Valid(); err != nil {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ── Key management ────────────────────────────────────────────────────────

// GenerateKeyPair returns a fresh base64 Ed25519 keypair: the private half
// for tenantcore's TOKEN_PRIVATE_KEY, the public half for every product
// service's TOKEN_PUBLIC_KEY. See cmd/keygen.
func GenerateKeyPair() (privateB64, publicB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub), nil
}

// KeyID derives a short stable identifier from a public key. Derived rather
// than configured so it cannot disagree with the key it names.
func KeyID(pub ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub)[:12]
}
