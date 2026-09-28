package service

import (
	"context"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"github.com/eandstravel/tenantcore/pkg/password"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

// These pin the rules that make an UNAUTHENTICATED reset endpoint safe. Every
// one of them is invisible in a happy-path click-through, which is exactly
// why they are worth asserting: the flow works fine with all of them broken.

type fakeUsers struct {
	byEmail map[string]*models.PlatformUser
	updated map[primitive.ObjectID]string
}

func (f *fakeUsers) FindByEmail(_ context.Context, email string) (*models.PlatformUser, error) {
	u, ok := f.byEmail[email]
	if !ok {
		return nil, mongo.ErrNoDocuments
	}
	return u, nil
}

func (f *fakeUsers) FindByID(_ context.Context, id primitive.ObjectID) (*models.PlatformUser, error) {
	for _, u := range f.byEmail {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, mongo.ErrNoDocuments
}

func (f *fakeUsers) UpdatePassword(_ context.Context, id primitive.ObjectID, hash string) error {
	if f.updated == nil {
		f.updated = map[primitive.ObjectID]string{}
	}
	f.updated[id] = hash
	return nil
}

type fakeResets struct {
	current     *models.PasswordReset
	invalidated int
	attempts    int
}

func (f *fakeResets) Create(_ context.Context, p *models.PasswordReset) error {
	p.ID = primitive.NewObjectID()
	f.current = p
	return nil
}

func (f *fakeResets) FindActiveByEmail(_ context.Context, email string) (*models.PasswordReset, error) {
	if f.current == nil || f.current.Email != email || f.current.UsedAt != nil {
		return nil, mongo.ErrNoDocuments
	}
	return f.current, nil
}

func (f *fakeResets) RecordAttempt(_ context.Context, _ primitive.ObjectID) error {
	f.attempts++
	if f.current != nil {
		f.current.Attempts++
	}
	return nil
}

func (f *fakeResets) MarkUsed(_ context.Context, _ primitive.ObjectID) (bool, error) {
	if f.current == nil || f.current.UsedAt != nil {
		return false, nil
	}
	now := time.Now()
	f.current.UsedAt = &now
	return true, nil
}

func (f *fakeResets) InvalidateForUser(_ context.Context, _ primitive.ObjectID) error {
	f.invalidated++
	return nil
}

type sentMail struct {
	to   string
	tmpl mailer.Template
	data map[string]string
}

type fakeMail struct {
	available bool
	sent      []sentMail
}

func (f *fakeMail) Available() bool { return f.available }
func (f *fakeMail) Send(to string, tmpl mailer.Template, data map[string]string) error {
	f.sent = append(f.sent, sentMail{to, tmpl, data})
	return nil
}

const testEmail = "munkherdene.ts9234@gmail.com"

func newFixture(status models.PlatformUserStatus) (*PasswordResetService, *fakeUsers, *fakeResets, *fakeMail) {
	users := &fakeUsers{byEmail: map[string]*models.PlatformUser{
		testEmail: {ID: primitive.NewObjectID(), Name: "Munkh-Erdene", Email: testEmail, Status: status},
	}}
	resets := &fakeResets{}
	mail := &fakeMail{available: true}
	return NewPasswordResetService(users, resets, mail, zap.NewNop()), users, resets, mail
}

func TestRequestMailsACodeToAnActiveAdmin(t *testing.T) {
	svc, _, resets, mail := newFixture(models.PlatformUserActive)

	if err := svc.Request(context.Background(), testEmail); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("expected 1 mail, got %d", len(mail.sent))
	}
	got := mail.sent[0]
	if got.tmpl != mailer.TemplatePasswordResetCode {
		t.Fatalf("template = %q", got.tmpl)
	}
	code := got.data["code"]
	if len(code) != 6 {
		t.Fatalf("code = %q, want six digits", code)
	}

	// The stored value must be a hash, never the code itself: a database
	// leak would otherwise hand over every pending reset.
	if resets.current.CodeHash == code {
		t.Fatal("the reset code was stored in plaintext")
	}
	if resets.current.CodeHash != hashCode(code) {
		t.Fatal("stored hash does not match the mailed code")
	}
	if !resets.current.ExpiresAt.After(time.Now()) {
		t.Fatal("stored reset is already expired")
	}
}

// The oracle rules. An unknown address and a suspended account must be
// indistinguishable from success, or this endpoint enumerates who administers
// the platform.
func TestRequestRevealsNothingAboutTheAccount(t *testing.T) {
	t.Run("unknown address", func(t *testing.T) {
		svc, _, _, mail := newFixture(models.PlatformUserActive)
		if err := svc.Request(context.Background(), "nobody@example.com"); err != nil {
			t.Fatalf("an unknown address must not error, got %v", err)
		}
		if len(mail.sent) != 0 {
			t.Fatal("mail was sent to an address with no account")
		}
	})

	t.Run("suspended account", func(t *testing.T) {
		svc, _, _, mail := newFixture(models.PlatformUserSuspended)
		if err := svc.Request(context.Background(), testEmail); err != nil {
			t.Fatalf("a suspended account must not error, got %v", err)
		}
		if len(mail.sent) != 0 {
			t.Fatal("a suspended admin was mailed a way back in")
		}
	})
}

// Mail being unconfigured is the one thing worth reporting: nothing the
// caller does will ever produce a code.
func TestRequestReportsWhenMailIsOff(t *testing.T) {
	svc, _, _, mail := newFixture(models.PlatformUserActive)
	mail.available = false
	if err := svc.Request(context.Background(), testEmail); err == nil {
		t.Fatal("expected an error when mail is not configured")
	}
}

func TestConfirmSetsThePasswordAndConsumesTheCode(t *testing.T) {
	svc, users, resets, mail := newFixture(models.PlatformUserActive)
	if err := svc.Request(context.Background(), testEmail); err != nil {
		t.Fatalf("Request: %v", err)
	}
	code := mail.sent[0].data["code"]

	if err := svc.Confirm(context.Background(), testEmail, code, "a-new-strong-password"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	hash, ok := users.updated[resets.current.UserID]
	if !ok {
		t.Fatal("password was not updated")
	}
	if !password.Verify(hash, "a-new-strong-password") {
		t.Fatal("stored hash does not verify against the new password")
	}
	if resets.current.UsedAt == nil {
		t.Fatal("code was not consumed")
	}
	if resets.invalidated == 0 {
		t.Fatal("remaining codes for the user were not invalidated")
	}

	// The owner must hear about it — a change they did not make is the one
	// thing they need to know immediately.
	if len(mail.sent) != 2 || mail.sent[1].tmpl != mailer.TemplatePasswordChanged {
		t.Fatal("no password-changed notice was sent")
	}
}

// Replay. A consumed code must not work twice.
func TestConfirmRejectsAReusedCode(t *testing.T) {
	svc, _, _, mail := newFixture(models.PlatformUserActive)
	_ = svc.Request(context.Background(), testEmail)
	code := mail.sent[0].data["code"]

	if err := svc.Confirm(context.Background(), testEmail, code, "a-new-strong-password"); err != nil {
		t.Fatalf("first Confirm: %v", err)
	}
	if err := svc.Confirm(context.Background(), testEmail, code, "another-password-x"); err == nil {
		t.Fatal("a consumed code was accepted a second time")
	}
}

func TestConfirmRejectsAnExpiredCode(t *testing.T) {
	svc, _, resets, mail := newFixture(models.PlatformUserActive)
	_ = svc.Request(context.Background(), testEmail)
	code := mail.sent[0].data["code"]
	resets.current.ExpiresAt = time.Now().Add(-time.Minute)

	if err := svc.Confirm(context.Background(), testEmail, code, "a-new-strong-password"); err == nil {
		t.Fatal("an expired code was accepted")
	}
}

// Six digits is only safe because guessing is capped. Without this, a
// ten-minute window is a few minutes of scripted requests.
func TestConfirmBurnsTheCodeAfterTooManyWrongGuesses(t *testing.T) {
	svc, _, resets, mail := newFixture(models.PlatformUserActive)
	_ = svc.Request(context.Background(), testEmail)
	code := mail.sent[0].data["code"]

	for i := 0; i < models.MaxResetAttempts; i++ {
		if err := svc.Confirm(context.Background(), testEmail, "000000", "a-new-strong-password"); err == nil {
			t.Fatal("a wrong code was accepted")
		}
	}
	if resets.attempts != models.MaxResetAttempts {
		t.Fatalf("attempts recorded = %d, want %d", resets.attempts, models.MaxResetAttempts)
	}

	// Even the RIGHT code must now fail: the reset is spent.
	if err := svc.Confirm(context.Background(), testEmail, code, "a-new-strong-password"); err == nil {
		t.Fatal("the correct code still worked after the attempt cap was reached")
	}
}

// Every failure must look the same from outside, or the differences become a
// map of which addresses have accounts and which codes were close.
func TestConfirmFailuresAreIndistinguishable(t *testing.T) {
	svc, _, _, mail := newFixture(models.PlatformUserActive)
	_ = svc.Request(context.Background(), testEmail)

	wrongCode := svc.Confirm(context.Background(), testEmail, "000000", "a-new-strong-password")
	noAccount := svc.Confirm(context.Background(), "nobody@example.com", "000000", "a-new-strong-password")

	if wrongCode == nil || noAccount == nil {
		t.Fatal("both cases should fail")
	}
	if wrongCode.Error() != noAccount.Error() {
		t.Fatalf("failures are distinguishable:\n  wrong code: %v\n  no account: %v", wrongCode, noAccount)
	}
	_ = mail
}

// Password length is the one thing safe to be specific about: it describes
// what the caller typed, not whether the account exists.
func TestConfirmRejectsAShortPassword(t *testing.T) {
	svc, _, _, mail := newFixture(models.PlatformUserActive)
	_ = svc.Request(context.Background(), testEmail)
	code := mail.sent[0].data["code"]

	if err := svc.Confirm(context.Background(), testEmail, code, "short"); err == nil {
		t.Fatal("a short password was accepted")
	}
}

// Addresses are normalised, so a reset requested as Munkherdene.TS9234@Gmail.com
// can be confirmed as typed in lowercase — and vice versa.
func TestEmailIsCaseInsensitive(t *testing.T) {
	svc, _, _, mail := newFixture(models.PlatformUserActive)
	if err := svc.Request(context.Background(), "  Munkherdene.TS9234@Gmail.com  "); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(mail.sent) != 1 {
		t.Fatal("a differently-cased address did not resolve to the account")
	}
	code := mail.sent[0].data["code"]
	if err := svc.Confirm(context.Background(), testEmail, code, "a-new-strong-password"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
}
