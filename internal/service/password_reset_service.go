package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"github.com/eandstravel/tenantcore/pkg/password"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

// CodeTTL is how long a reset code lives. Long enough to find the mail on a
// phone, short enough that a code glimpsed over a shoulder is worthless by
// the time anyone acts on it.
const CodeTTL = 10 * time.Minute

// AppName is what the mail calls this thing. One constant so the subject
// line, the body and any future template cannot drift apart.
const AppName = "Inno Nomads Console"

// The reads and writes this flow needs, as interfaces rather than the
// concrete repositories.
//
// The same reasoning as EntitlementService: depending on *repository.X makes
// the rules below impossible to exercise without a live MongoDB, and these
// rules — same answer for every failure, a capped attempt count, single use,
// expiry — are the entire security of an unauthenticated endpoint. Rules that
// can only be tested against a running stack are rules that stop being
// tested.
type (
	resetUserSource interface {
		FindByEmail(ctx context.Context, email string) (*models.PlatformUser, error)
		FindByID(ctx context.Context, id primitive.ObjectID) (*models.PlatformUser, error)
		UpdatePassword(ctx context.Context, id primitive.ObjectID, hash string) error
	}
	resetStore interface {
		Create(ctx context.Context, p *models.PasswordReset) error
		FindActiveByEmail(ctx context.Context, email string) (*models.PasswordReset, error)
		RecordAttempt(ctx context.Context, id primitive.ObjectID) error
		MarkUsed(ctx context.Context, id primitive.ObjectID) (bool, error)
		InvalidateForUser(ctx context.Context, userID primitive.ObjectID) error
	}
	// sender is the mailer, narrowed to the one method used here so a test
	// can capture what would have been sent.
	sender interface {
		Available() bool
		Send(to string, tmpl mailer.Template, data map[string]string) error
	}
)

// PasswordResetService runs the emailed-code reset for platform admins.
//
// The whole flow is two calls — Request, then Confirm — and almost every
// decision in it is about what NOT to reveal. A password reset endpoint is
// unauthenticated by definition: it is the one door that answers questions
// for someone who has proved nothing.
type PasswordResetService struct {
	users  resetUserSource
	resets resetStore
	mail   sender
	log    *zap.Logger

	// sending tracks mail being sent in the background, so Drain can wait for it.
	sending sync.WaitGroup
}

// Drain waits for every mail Request started in the background to finish.
//
// Request does not wait for SMTP (see Request), so a mail can still be in flight
// when the process is asked to stop. Draining at shutdown means a reset
// requested a moment before a restart is still delivered; tests use it to check
// what was sent.
func (s *PasswordResetService) Drain() { s.sending.Wait() }

func NewPasswordResetService(
	users resetUserSource,
	resets resetStore,
	mail sender,
	log *zap.Logger,
) *PasswordResetService {
	return &PasswordResetService{users: users, resets: resets, mail: mail, log: log}
}

// Request issues a code and mails it, if the address belongs to an active
// platform admin.
//
// It returns nil in every one of those cases — unknown address, suspended
// account, mail failure — and the caller answers the same 200 regardless.
//
// That is not sloppiness, it is the point. An endpoint that says "no such
// account" is an account-existence oracle: anyone can enumerate who
// administers this platform, which is a list worth having before you start
// guessing passwords or writing a phishing mail. The cost is that a genuine
// typo looks like success; the log line below is how an operator tells the
// two apart, and it stays on our side of the wire.
func (s *PasswordResetService) Request(_ context.Context, email string) error {
	email = normalizeEmail(email)

	// Mail being unconfigured IS worth reporting: nothing the caller does
	// will ever produce a code, and pretending otherwise leaves someone
	// waiting for an email that cannot arrive. This depends on configuration
	// only, never on the account, so it does not leak anything.
	if !s.mail.Available() {
		return apierr.FeatureUnavailable("email")
	}

	// EVERYTHING that depends on the account happens in the background, and that
	// is the point of the shape of this function.
	//
	// The response body is identical for a real and an unknown address, but a
	// response TIME that differs is just as much an oracle. Waiting for the SMTP
	// send made a real account answer about 1.7s later than a fake one on a live
	// request; once the send moved out, the lookup, the code insert and the
	// invalidation of the old code (database round trips an unknown address
	// skips) still left it about 250ms slower, which is averaged out in a few
	// dozen requests. So nothing account-dependent runs before this returns, and
	// both paths do the same amount of work here: none.
	//
	// The cost is that a failure inside cannot be reported to the caller, which
	// was already true of a failed send (reporting either is an oracle of its
	// own). Everything is logged instead.
	s.sending.Add(1)
	go func() {
		defer s.sending.Done()
		// The request's context is cancelled the moment the handler returns, so
		// the background work gets its own, bounded one.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.issue(ctx, email)
	}()
	return nil
}

// issue looks the account up, stores a code and mails it. It runs after the
// response has been sent, so it logs rather than returns.
func (s *PasswordResetService) issue(ctx context.Context, email string) {
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			s.log.Info("password reset requested for an unknown address", zap.String("email", email))
			return
		}
		s.log.Error("password reset: account lookup failed", zap.String("email", email), zap.Error(err))
		return
	}
	if user.Status != models.PlatformUserActive {
		// A suspended admin getting back in via email is precisely what
		// suspension exists to prevent.
		s.log.Warn("password reset requested for a non-active account",
			zap.String("email", email), zap.String("status", string(user.Status)))
		return
	}

	code, err := generateCode()
	if err != nil {
		s.log.Error("password reset: could not generate a code", zap.Error(err))
		return
	}

	reset := &models.PasswordReset{
		UserID:    user.ID,
		Email:     email,
		CodeHash:  hashCode(code),
		ExpiresAt: time.Now().Add(CodeTTL),
	}
	if err := s.resets.Create(ctx, reset); err != nil {
		s.log.Error("password reset: could not store the code", zap.String("email", email), zap.Error(err))
		return
	}

	if err := s.mail.Send(email, mailer.TemplatePasswordResetCode, map[string]string{
		"app":        AppName,
		"name":       user.Name,
		"code":       code,
		"expires_in": "10 minutes",
	}); err != nil {
		s.log.Error("password reset code could not be mailed",
			zap.String("email", email), zap.Error(err))
	}
}

// Confirm verifies a code and sets the new password.
//
// Every failure below is the same error on the wire — one message, one code —
// so a caller cannot learn whether the address exists, whether a code was
// ever issued, or whether the one they guessed was merely expired. The
// distinctions are real and they belong in our log, not in the response.
func (s *PasswordResetService) Confirm(ctx context.Context, email, code, newPassword string) error {
	email = normalizeEmail(email)
	code = strings.TrimSpace(code)

	if len(newPassword) < 8 {
		// The one thing that IS safe to be specific about: it says nothing
		// about the account, only about what the caller typed.
		return apierr.BadRequest("new password must be at least 8 characters")
	}

	invalid := apierr.BadRequest("that code is not valid — request a new one").
		In(apierr.DomainAuth)

	reset, err := s.resets.FindActiveByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Burn a little time so a request for an address with no
			// outstanding code does not return measurably faster than one
			// that has to verify a hash.
			password.DummyCompare()
			s.log.Info("password reset confirm with no outstanding code", zap.String("email", email))
			return invalid
		}
		return apierr.Internal(err)
	}

	if reset.Spent(time.Now()) {
		s.log.Info("password reset confirm against a spent code",
			zap.String("email", email), zap.Int("attempts", reset.Attempts))
		return invalid
	}

	// Constant-time: a byte-by-byte comparison that returns early leaks how
	// much of the code was right, which turns a million guesses into sixty.
	if subtle.ConstantTimeCompare([]byte(hashCode(code)), []byte(reset.CodeHash)) != 1 {
		if err := s.resets.RecordAttempt(ctx, reset.ID); err != nil {
			s.log.Error("could not record a failed reset attempt", zap.Error(err))
		}
		return invalid
	}

	// Consume before writing the password. Conditional on it still being
	// unused, so two confirms racing with the same code cannot both proceed.
	ok, err := s.resets.MarkUsed(ctx, reset.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	if !ok {
		return invalid
	}

	hash, err := password.Hash(newPassword)
	if err != nil {
		return apierr.Internal(err)
	}
	if err := s.users.UpdatePassword(ctx, reset.UserID, hash); err != nil {
		return apierr.Internal(err)
	}

	// Any other outstanding code is now worthless. Leaving one alive would
	// mean a second reset mail, possibly sent by an attacker, still works
	// after the owner has taken the account back.
	if err := s.resets.InvalidateForUser(ctx, reset.UserID); err != nil {
		s.log.Error("could not invalidate remaining reset codes", zap.Error(err))
	}

	user, err := s.users.FindByID(ctx, reset.UserID)
	if err == nil {
		if err := s.mail.Send(email, mailer.TemplatePasswordChanged, map[string]string{
			"app":  AppName,
			"name": user.Name,
		}); err != nil {
			s.log.Error("password changed but the notice could not be mailed", zap.Error(err))
		}
	}

	// Worth stating plainly: tokens are verified offline by every product
	// service, so changing a password does NOT invalidate sessions already
	// issued. Whoever holds a valid token keeps it until TOKEN_TTL expires —
	// at most an hour by default. Revoking on password change would need
	// online verification, which is the trade the Ed25519 design made
	// deliberately.
	s.log.Info("platform user password reset via emailed code", zap.String("email", email))
	return nil
}

// generateCode returns a six-digit code from crypto/rand.
//
// math/rand would be seeded predictably enough to guess the next code from
// an earlier one, which defeats the entire flow.
func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	// Zero-padded: 000123 is a valid code, and trimming it to "123" would
	// make a sixth of all codes shorter and easier to guess.
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// hashCode hashes a reset code for storage and comparison.
//
// SHA-256 rather than bcrypt, deliberately, and the reasoning is worth
// keeping: bcrypt's cost exists to make guessing a LOW-ENTROPY human-chosen
// secret slow. This secret is machine-chosen, single-use, ten minutes old and
// capped at five attempts, so the guessing budget is already closed off by
// the attempt counter. What matters here is that a database leak does not
// hand over live codes, which a fast hash does just as well — while staying
// cheap enough that the endpoint cannot be used to burn CPU.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
