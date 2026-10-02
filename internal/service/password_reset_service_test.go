package service

import (
	"context"
	"sync"
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

// request runs Request and then waits for the mail it sends in the background.
// Request deliberately does not wait for SMTP (see TestRequest_DoesNotWaitForTheMail),
// so a test that checks what was sent has to wait for it explicitly.
func request(svc *PasswordResetService, email string) error {
	err := svc.Request(context.Background(), email)
	svc.Drain()
	return err
}

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

	if err := request(svc, testEmail); err != nil {
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
		if err := request(svc, "nobody@example.com"); err != nil {
			t.Fatalf("an unknown address must not error, got %v", err)
		}
		if len(mail.sent) != 0 {
			t.Fatal("mail was sent to an address with no account")
		}
	})

	t.Run("suspended account", func(t *testing.T) {
		svc, _, _, mail := newFixture(models.PlatformUserSuspended)
		if err := request(svc, testEmail); err != nil {
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
	if err := request(svc, testEmail); err == nil {
		t.Fatal("expected an error when mail is not configured")
	}
}

func TestConfirmSetsThePasswordAndConsumesTheCode(t *testing.T) {
	svc, users, resets, mail := newFixture(models.PlatformUserActive)
	if err := request(svc, testEmail); err != nil {
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
	_ = request(svc, testEmail)
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
	_ = request(svc, testEmail)
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
	_ = request(svc, testEmail)
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
	_ = request(svc, testEmail)

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
	_ = request(svc, testEmail)
	code := mail.sent[0].data["code"]

	if err := svc.Confirm(context.Background(), testEmail, code, "short"); err == nil {
		t.Fatal("a short password was accepted")
	}
}

// Addresses are normalised, so a reset requested as Munkherdene.TS9234@Gmail.com
// can be confirmed as typed in lowercase — and vice versa.
func TestEmailIsCaseInsensitive(t *testing.T) {
	svc, _, _, mail := newFixture(models.PlatformUserActive)
	if err := request(svc, "  Munkherdene.TS9234@Gmail.com  "); err != nil {
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

// blockingMail holds every Send until released, like an SMTP server that is slow
// to answer, and records what it was asked to send.
type blockingMail struct {
	release chan struct{}
	mu      sync.Mutex
	sent    []sentMail
}

func (b *blockingMail) Available() bool { return true }
func (b *blockingMail) Send(to string, tmpl mailer.Template, data map[string]string) error {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, sentMail{to, tmpl, data})
	return nil
}

// The response time of an unauthenticated endpoint must not depend on whether
// the address has an account. Sending takes seconds over SMTP and an unknown
// address sends nothing, so waiting for the send made "real account" and "no
// account" differ by about 1.7 seconds on a live request: an account-existence
// oracle that the identical 200 body was supposed to prevent.
func TestRequest_DoesNotWaitForTheMail(t *testing.T) {
	users := &fakeUsers{byEmail: map[string]*models.PlatformUser{
		testEmail: {ID: primitive.NewObjectID(), Name: "Munkh-Erdene", Email: testEmail, Status: models.PlatformUserActive},
	}}
	mail := &blockingMail{release: make(chan struct{})}
	svc := NewPasswordResetService(users, &fakeResets{}, mail, zap.NewNop())

	done := make(chan error, 1)
	go func() { done <- svc.Request(context.Background(), testEmail) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Request waited for the mail to be sent: its response time now reveals whether the account exists")
	}

	// The mail must still go out once the slow send completes.
	close(mail.release)
	svc.Drain()
	mail.mu.Lock()
	defer mail.mu.Unlock()
	if len(mail.sent) != 1 || mail.sent[0].tmpl != mailer.TemplatePasswordResetCode {
		t.Fatalf("sent = %+v, want exactly one password_reset_code mail after the send completed", mail.sent)
	}
}

// slowUsers blocks the account lookup, like a database that is slow to answer.
type slowUsers struct {
	*fakeUsers
	release chan struct{}
}

func (s slowUsers) FindByEmail(ctx context.Context, email string) (*models.PlatformUser, error) {
	<-s.release
	return s.fakeUsers.FindByEmail(ctx, email)
}

// Not waiting for the mail is not enough: looking the account up, storing the
// code and invalidating the old one are database round trips that an unknown
// address skips, and on a live request they still left a real account about
// 250ms slower. The response must not depend on the account at all, so none of
// that work may happen before Request returns.
func TestRequest_ResponseDoesNotDependOnTheAccountLookup(t *testing.T) {
	users := slowUsers{
		fakeUsers: &fakeUsers{byEmail: map[string]*models.PlatformUser{
			testEmail: {ID: primitive.NewObjectID(), Name: "Munkh-Erdene", Email: testEmail, Status: models.PlatformUserActive},
		}},
		release: make(chan struct{}),
	}
	mail := &fakeMail{available: true}
	svc := NewPasswordResetService(users, &fakeResets{}, mail, zap.NewNop())

	done := make(chan error, 1)
	go func() { done <- svc.Request(context.Background(), testEmail) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Request waited for the account lookup: its response time reveals whether the account exists")
	}

	close(users.release)
	svc.Drain()
	if len(mail.sent) != 1 {
		t.Fatalf("mails = %d, want 1 once the lookup finished", len(mail.sent))
	}
}
