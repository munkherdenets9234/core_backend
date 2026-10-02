package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PasswordReset is one outstanding reset code for one platform user.
//
// Three things about this document are security decisions, not storage
// details:
//
//  1. CodeHash, never the code. A reset code is a credential that stands in
//     for a password for the length of its life. Anyone who can read the
//     database — a backup, a log of a slow query, a support export — would
//     otherwise be able to take over every account with a pending reset.
//
//  2. Attempts, with a ceiling. Six digits is a million possibilities, which
//     sounds like a lot and is not: unlimited guesses against a ten-minute
//     window is a few minutes of scripted requests. The counter is what makes
//     the code short enough to type and still safe.
//
//  3. UsedAt rather than deletion. A consumed code is kept until it expires
//     so a replay is rejected explicitly, rather than being indistinguishable
//     from a code that never existed.
type PasswordReset struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID primitive.ObjectID `bson:"user_id" json:"user_id"`
	// Email is stored alongside UserID so a confirm can be looked up by what
	// the user actually types, without exposing an id they have no way to
	// know.
	Email    string `bson:"email" json:"email"`
	CodeHash string `bson:"code_hash" json:"-"`

	Attempts int `bson:"attempts" json:"attempts"`

	ExpiresAt time.Time  `bson:"expires_at" json:"expires_at"`
	UsedAt    *time.Time `bson:"used_at,omitempty" json:"used_at,omitempty"`
	CreatedAt time.Time  `bson:"created_at" json:"created_at"`
}

// MaxResetAttempts is how many wrong codes a single reset tolerates before it
// is burned. Low on purpose: a legitimate user reads the code out of their
// inbox and types it, and five tries is already generous for that. Anyone
// needing more is guessing.
const MaxResetAttempts = 5

// Spent reports whether this reset can no longer be used, for any reason.
// One predicate rather than three checks at the call site, so a future caller
// cannot check expiry and forget replay.
func (p *PasswordReset) Spent(now time.Time) bool {
	return p.UsedAt != nil || now.After(p.ExpiresAt) || p.Attempts >= MaxResetAttempts
}
