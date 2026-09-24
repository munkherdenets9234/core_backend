package token

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newMaker(t *testing.T) *Maker {
	t.Helper()
	priv, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := NewMaker(priv)
	if err != nil {
		t.Fatalf("new maker: %v", err)
	}
	return m
}

func TestRoundTrip(t *testing.T) {
	m := newMaker(t)

	signed, claims, err := m.Create("user-1", RoleSuperadmin, "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if claims.Issuer != Issuer {
		t.Errorf("issuer = %q, want %q", claims.Issuer, Issuer)
	}

	got, err := m.Verifier().Verify(signed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.UserID != "user-1" || got.Role != RoleSuperadmin {
		t.Errorf("claims round-tripped as %+v", got)
	}
}

// The property the whole design rests on: a verifier holds only the public
// key, so a product service can check a token and cannot mint one.
func TestAVerifierCannotForgeAToken(t *testing.T) {
	m := newMaker(t)
	pub := m.PublicKeyB64()

	// Everything a product service has.
	v, err := NewVerifier(pub)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}

	// The only way to get a signing key from it would be to use the public
	// bytes as a private key. Ed25519 key sizes make that a type error
	// rather than a subtle weakness, which is the point of the check.
	if _, err := NewMaker(pub); err == nil {
		t.Fatal("a public key was accepted as a signing key")
	}

	// And the verifier still verifies what the real issuer signed.
	signed, _, err := m.Create("user-1", RoleSuperadmin, "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := v.Verify(signed); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestAnotherKeyIsRejected(t *testing.T) {
	issuer := newMaker(t)
	attacker := newMaker(t)

	signed, _, err := attacker.Create("user-1", RoleSuperadmin, "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := issuer.Verifier().Verify(signed); err == nil {
		t.Fatal("a token signed by a different key was accepted")
	}
}

// The classic JWT hole: a token that nominates its own algorithm. An HMAC
// token verified against a key that is, by definition, published would let
// anyone who can read the public key mint whatever they like.
func TestAnHMACTokenSignedWithThePublicKeyIsRejected(t *testing.T) {
	m := newMaker(t)
	pub := m.PublicKey()

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		UserID: "attacker",
		Role:   RoleSuperadmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := forged.SignedString([]byte(pub))
	if err != nil {
		t.Fatalf("sign forged: %v", err)
	}

	if _, err := m.Verifier().Verify(signed); err == nil {
		t.Fatal("an HS256 token signed with the public key was accepted — the algorithm is not pinned")
	}
}

func TestAlgNoneIsRejected(t *testing.T) {
	m := newMaker(t)

	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{
		UserID: "attacker",
		Role:   RoleSuperadmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}

	if _, err := m.Verifier().Verify(signed); err == nil {
		t.Fatal(`a token with "alg":"none" was accepted`)
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	m := newMaker(t)

	signed, _, err := m.Create("user-1", RoleSuperadmin, "", -time.Minute)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Verifier().Verify(signed); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// A token minted by some other system that happens to use Ed25519 and our
// claim names should still be refused.
func TestForeignIssuerIsRejected(t *testing.T) {
	m := newMaker(t)

	foreign := jwt.NewWithClaims(jwt.SigningMethodEdDSA, &Claims{
		UserID: "user-1",
		Role:   RoleSuperadmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "somewhere-else",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := foreign.SignedString(m.private)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := m.Verifier().Verify(signed); err == nil {
		t.Fatal("a token from another issuer was accepted")
	}
}

// The invariant that is not about signatures: a superadmin token must never
// be tenant-scoped, because that combination would let a smuggled tenant role
// pass as platform staff on routes that never resolve a tenant. Enforced at
// MINT time so such a token cannot be created, not merely refused later by
// services that remembered to look.
func TestASuperadminTokenCannotBeTenantScoped(t *testing.T) {
	m := newMaker(t)

	if _, _, err := m.Create("user-1", RoleSuperadmin, "tenant-1", time.Hour); err == nil {
		t.Fatal("a tenant-scoped superadmin token was minted")
	}
}

func TestATenantRoleMustBeTenantScoped(t *testing.T) {
	m := newMaker(t)

	if _, _, err := m.Create("user-1", RoleTenantAdmin, "", time.Hour); err == nil {
		t.Fatal("an unscoped tenant-admin token was minted")
	}
	if _, _, err := m.Create("user-1", RoleTenantAdmin, "tenant-1", time.Hour); err != nil {
		t.Fatalf("a properly scoped tenant token was refused: %v", err)
	}
}

func TestUnknownRoleIsRefused(t *testing.T) {
	m := newMaker(t)

	if _, _, err := m.Create("user-1", Role("root"), "tenant-1", time.Hour); err == nil {
		t.Fatal("an unknown role was minted")
	}
}

// Even if such a token were somehow produced, verification refuses it.
func TestVerifyRefusesAnImpossibleClaimSet(t *testing.T) {
	m := newMaker(t)

	smuggled := jwt.NewWithClaims(jwt.SigningMethodEdDSA, &Claims{
		UserID:   "user-1",
		Role:     RoleSuperadmin,
		TenantID: "tenant-1", // impossible combination
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := smuggled.SignedString(m.private)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := m.Verifier().Verify(signed); err == nil {
		t.Fatal("a tenant-scoped superadmin token passed verification")
	}
}

func TestMalformedKeysAreRefusedWithAUsefulMessage(t *testing.T) {
	if _, err := NewMaker("not base64!"); err == nil {
		t.Error("non-base64 private key accepted")
	}
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	err := func() error { _, e := NewMaker(short); return e }()
	if err == nil {
		t.Fatal("undersized private key accepted")
	}
	// An operator reading this needs to know what to do next.
	if !strings.Contains(err.Error(), "keygen") {
		t.Errorf("error should point at the fix; got %q", err)
	}

	if _, err := NewVerifier("not base64!"); err == nil {
		t.Error("non-base64 public key accepted")
	}
}

// kid is derived from the key rather than configured, so it cannot disagree
// with the key it names.
func TestKeyIDIsDerivedAndStable(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := NewMaker(priv)
	if err != nil {
		t.Fatalf("new maker: %v", err)
	}

	rawPub, _ := base64.StdEncoding.DecodeString(pub)
	if m.KeyID() != KeyID(ed25519.PublicKey(rawPub)) {
		t.Error("kid does not match the key it names")
	}
	if m.PublicKeyB64() != pub {
		t.Error("PublicKeyB64 does not match the generated public key")
	}

	other := newMaker(t)
	if other.KeyID() == m.KeyID() {
		t.Error("two different keys produced the same kid")
	}
}
