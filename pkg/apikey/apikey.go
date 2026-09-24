package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

const prefix = "sk_live_"

// Generate returns a new raw API key (shown to the caller once) and its
// SHA-256 hash (what gets stored). API keys are high-entropy, system
// generated secrets, so a fast deterministic hash is used for lookup
// instead of a slow password hash like bcrypt.
func Generate() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = prefix + hex.EncodeToString(buf)
	return raw, Hash(raw), nil
}

func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Last4 returns the last 4 characters of the raw key, safe to store/display
// alongside the hash so admins can recognize a key without seeing it in full.
func Last4(raw string) string {
	if len(raw) < 4 {
		return raw
	}
	return raw[len(raw)-4:]
}
