package password

import (
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

func Hash(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func Verify(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// GenerateRandom returns a random password suitable for auto-provisioned
// login profiles, whose raw value is only ever available here, once, at
// creation time.
func GenerateRandom() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// dummyHash is a real bcrypt hash at the same cost as the ones we store. Its
// plaintext is irrelevant and never matched.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// DummyCompare spends roughly what a real password check costs.
//
// Call it on the "no such account" path. Without it, login answers an unknown
// email in microseconds and a known one in the tens of milliseconds bcrypt
// takes — which hands back exactly the account-existence answer that
// returning an identical error message was meant to withhold. The message
// being the same is not enough when the clock is not.
func DummyCompare() {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte("dummy"))
}
