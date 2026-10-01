package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

// The notifier decides whether an operator hears about a lapsing subscription.
// A silent failure here looks exactly like "nothing is expiring", so every
// rule below is pinned: nothing about it shows when it is wrong.

// expFakeSubs is an in-memory store with the same conditional-claim semantics
// as the real one, guarded by a mutex so a concurrency test is meaningful.
type expFakeSubs struct {
	mu                   sync.Mutex
	subs                 []*models.Subscription
	gotAfter, gotThrough time.Time
	findErr              error
	claimCalls           int

	// barrier, when set, holds every FindExpiring caller after it has read its
	// candidates until all of them have. That forces two runs to be holding
	// stale candidates at the same time, which is the only situation where
	// the claim is what prevents a double send. Without it, goroutines this
	// small run nearly back to back and the second one's find already sees the
	// first one's marker, so the claim is never actually exercised.
	barrier *sync.WaitGroup
}

func (f *expFakeSubs) FindExpiring(_ context.Context, after, through time.Time) ([]*models.Subscription, error) {
	f.mu.Lock()
	f.gotAfter, f.gotThrough = after, through
	if f.findErr != nil {
		f.mu.Unlock()
		return nil, f.findErr
	}
	var out []*models.Subscription
	for _, s := range f.subs {
		paying := s.Status == models.SubscriptionActive || s.Status == models.SubscriptionTrialing
		if paying && s.CurrentPeriodEnd.After(after) && !s.CurrentPeriodEnd.After(through) {
			c := *s // a copy, as a real query returns, so callers never share memory
			if s.ExpiryNoticeFor != nil {
				m := *s.ExpiryNoticeFor
				c.ExpiryNoticeFor = &m
			}
			out = append(out, &c)
		}
	}
	barrier := f.barrier
	f.mu.Unlock()

	if barrier != nil {
		barrier.Done()
		barrier.Wait()
	}
	return out, nil
}

func (f *expFakeSubs) ClaimExpiryNotice(_ context.Context, id primitive.ObjectID, end time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	for _, s := range f.subs {
		if s.ID != id {
			continue
		}
		if !s.CurrentPeriodEnd.Equal(end) {
			return false, nil
		}
		if s.ExpiryNoticeFor != nil && s.ExpiryNoticeFor.Equal(end) {
			return false, nil
		}
		m := end
		s.ExpiryNoticeFor = &m
		return true, nil
	}
	return false, nil
}

func (f *expFakeSubs) ReleaseExpiryNotice(_ context.Context, id primitive.ObjectID, end time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs {
		if s.ID == id && s.ExpiryNoticeFor != nil && s.ExpiryNoticeFor.Equal(end) {
			s.ExpiryNoticeFor = nil
		}
	}
	return nil
}

func (f *expFakeSubs) marker(id primitive.ObjectID) *time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs {
		if s.ID == id {
			return s.ExpiryNoticeFor
		}
	}
	return nil
}

type expFakeTenants map[primitive.ObjectID]*models.Tenant

func (f expFakeTenants) FindByID(_ context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	if t, ok := f[id]; ok {
		return t, nil
	}
	return nil, mongo.ErrNoDocuments
}

type expFakePlans map[primitive.ObjectID]*models.Plan

func (f expFakePlans) FindByID(_ context.Context, id primitive.ObjectID) (*models.Plan, error) {
	if p, ok := f[id]; ok {
		return p, nil
	}
	return nil, mongo.ErrNoDocuments
}

// expFakeMail records sends and can be told to fail the next N of them.
type expFakeMail struct {
	mu        sync.Mutex
	sent      []sentMail
	failTimes int
}

func (f *expFakeMail) Available() bool { return true }
func (f *expFakeMail) Send(to string, tmpl mailer.Template, data map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failTimes > 0 {
		f.failTimes--
		return errors.New("smtp: connection refused")
	}
	f.sent = append(f.sent, sentMail{to, tmpl, data})
	return nil
}
func (f *expFakeMail) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

var expNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type expFixture struct {
	n       *ExpiryNotifier
	subs    *expFakeSubs
	mail    *expFakeMail
	tenants expFakeTenants
	plans   expFakePlans
}

// addSub registers a tenant, a plan and an active subscription ending at end.
func (x *expFixture) addSub(tenantName string, end time.Time) *models.Subscription {
	tid, pid := primitive.NewObjectID(), primitive.NewObjectID()
	x.tenants[tid] = &models.Tenant{ID: tid, Name: tenantName}
	x.plans[pid] = &models.Plan{ID: pid, Slug: "travel-pro", Name: "Travel Pro"}
	s := &models.Subscription{
		ID: primitive.NewObjectID(), TenantID: tid, PlanID: pid,
		Status: models.SubscriptionActive, CurrentPeriodEnd: end,
	}
	x.subs.subs = append(x.subs.subs, s)
	return s
}

func newExpFixture() *expFixture {
	x := &expFixture{
		subs:    &expFakeSubs{},
		mail:    &expFakeMail{},
		tenants: expFakeTenants{},
		plans:   expFakePlans{},
	}
	x.n = NewExpiryNotifier(x.subs, x.tenants, x.plans, x.mail, "ops@example.com", zap.NewNop())
	return x
}

func TestNotifyExpiring_WarnsOncePerPeriod(t *testing.T) {
	x := newExpFixture()
	end := expNow.Add(3 * 24 * time.Hour)
	x.addSub("E and S Discovery Mongolia", end)

	sent, err := x.n.NotifyExpiring(context.Background(), expNow)
	if err != nil || sent != 1 {
		t.Fatalf("first run: sent=%d err=%v, want 1 nil", sent, err)
	}
	got := x.mail.sent[0]
	if got.to != "ops@example.com" || got.tmpl != mailer.TemplateSubscriptionExpiring {
		t.Fatalf("mail = %+v, want to ops@example.com via subscription_expiring", got)
	}
	want := map[string]string{
		"app":       AppName,
		"tenant":    "E and S Discovery Mongolia",
		"plan":      "Travel Pro",
		"ends_on":   end.UTC().Format("2006-01-02"),
		"days_left": "3",
	}
	for k, v := range want {
		if got.data[k] != v {
			t.Fatalf("data[%q] = %q, want %q", k, got.data[k], v)
		}
	}

	sent, _ = x.n.NotifyExpiring(context.Background(), expNow)
	if sent != 0 || x.mail.count() != 1 {
		t.Fatalf("second run sent=%d total=%d, want 0 and 1: one warning per period", sent, x.mail.count())
	}
}

// Review Focus 1. The filter makes `after` exclusive and `through` inclusive;
// this pins the arguments the notifier hands it, so the window edges are the
// ones the spec names: nothing already lapsed, and exactly seven days is in.
func TestNotifyExpiring_QueriesTheSpecWindow(t *testing.T) {
	x := newExpFixture()
	if _, err := x.n.NotifyExpiring(context.Background(), expNow); err != nil {
		t.Fatalf("NotifyExpiring: %v", err)
	}
	if !x.subs.gotAfter.Equal(expNow) {
		t.Fatalf("after = %v, want now (%v)", x.subs.gotAfter, expNow)
	}
	if !x.subs.gotThrough.Equal(expNow.Add(ExpiryWarnWindow)) || ExpiryWarnWindow != 7*24*time.Hour {
		t.Fatalf("through = %v, want now + 7 days", x.subs.gotThrough)
	}
}

func TestNotifyExpiring_ReWarnsAfterPeriodEndChanges(t *testing.T) {
	x := newExpFixture()
	s := x.addSub("E and S", expNow.Add(3*24*time.Hour))
	if sent, _ := x.n.NotifyExpiring(context.Background(), expNow); sent != 1 {
		t.Fatalf("first period: sent=%d, want 1", sent)
	}

	// Renew: the end date moves a period out. No reset step touches the marker.
	s.CurrentPeriodEnd = s.CurrentPeriodEnd.Add(30 * 24 * time.Hour)
	later := expNow.Add(30 * 24 * time.Hour) // the new end is again 3 days away

	sent, err := x.n.NotifyExpiring(context.Background(), later)
	if err != nil || sent != 1 {
		t.Fatalf("second period: sent=%d err=%v, want 1 nil", sent, err)
	}
	if x.mail.count() != 2 {
		t.Fatalf("total mails = %d, want 2 (one per period)", x.mail.count())
	}
}

func TestNotifyExpiring_SkipsWhenMarkerAlreadyMatches(t *testing.T) {
	x := newExpFixture()
	end := expNow.Add(2 * 24 * time.Hour)
	s := x.addSub("E and S", end)
	m := end
	s.ExpiryNoticeFor = &m

	sent, _ := x.n.NotifyExpiring(context.Background(), expNow)
	if sent != 0 || x.mail.count() != 0 {
		t.Fatalf("sent=%d mails=%d, want 0 0 for an already-warned period", sent, x.mail.count())
	}
	if x.subs.claimCalls != 0 {
		t.Fatalf("claim attempted %d times, want 0: nothing to claim", x.subs.claimCalls)
	}
}

func TestNotifyExpiring_ReleasesClaimWhenSendFails(t *testing.T) {
	x := newExpFixture()
	s := x.addSub("E and S", expNow.Add(3*24*time.Hour))
	x.mail.failTimes = 1

	sent, err := x.n.NotifyExpiring(context.Background(), expNow)
	if err != nil || sent != 0 {
		t.Fatalf("failing send: sent=%d err=%v, want 0 nil (logged, not returned)", sent, err)
	}
	if x.subs.marker(s.ID) != nil {
		t.Fatal("claim was not released after the send failed: the warning would be lost for this period")
	}

	// The next tick retries and succeeds.
	if sent, _ := x.n.NotifyExpiring(context.Background(), expNow); sent != 1 {
		t.Fatalf("retry: sent=%d, want 1", sent)
	}
}

// Review Focus 2. A lookup that fails AFTER the claim must release it, or the
// warning is silently lost for the whole period.
func TestNotifyExpiring_ReleasesClaimWhenLookupFails(t *testing.T) {
	x := newExpFixture()
	s := x.addSub("Ghost Tenant", expNow.Add(3*24*time.Hour))
	delete(x.tenants, s.TenantID)

	sent, err := x.n.NotifyExpiring(context.Background(), expNow)
	if err != nil || sent != 0 || x.mail.count() != 0 {
		t.Fatalf("sent=%d mails=%d err=%v, want 0 0 nil", sent, x.mail.count(), err)
	}
	if x.subs.marker(s.ID) != nil {
		t.Fatal("claim was not released after the tenant lookup failed")
	}
}

// A plan deleted out from under a live subscription is a legitimate state
// elsewhere in this service and must not suppress the warning here.
func TestNotifyExpiring_DeletedPlanStillWarns(t *testing.T) {
	x := newExpFixture()
	s := x.addSub("E and S", expNow.Add(3*24*time.Hour))
	delete(x.plans, s.PlanID)

	if sent, _ := x.n.NotifyExpiring(context.Background(), expNow); sent != 1 {
		t.Fatalf("sent=%d, want 1: a missing plan is not a reason to stay silent", sent)
	}
	if got := x.mail.sent[0].data["plan"]; got != "(plan removed)" {
		t.Fatalf("plan = %q, want %q", got, "(plan removed)")
	}
}

// Review Focus 3. One bad subscription must not stop the rest of the tick.
func TestNotifyExpiring_OneFailureDoesNotBlockOthers(t *testing.T) {
	x := newExpFixture()
	x.addSub("First", expNow.Add(2*24*time.Hour))
	x.addSub("Second", expNow.Add(3*24*time.Hour))
	x.mail.failTimes = 1 // the first send fails, the second works

	sent, err := x.n.NotifyExpiring(context.Background(), expNow)
	if err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v, want 1 nil", sent, err)
	}
	if x.mail.count() != 1 {
		t.Fatalf("mails = %d, want 1", x.mail.count())
	}
}

// Two runs racing over the same subscription (a restart overlapping a tick, or
// a second replica) must produce one email. The claim is what guarantees it.
func TestNotifyExpiring_ConcurrentRunsSendOnce(t *testing.T) {
	x := newExpFixture()
	x.addSub("E and S", expNow.Add(3*24*time.Hour))

	// Both runs must hold the candidate before either claims it. See barrier.
	x.subs.barrier = &sync.WaitGroup{}
	x.subs.barrier.Add(2)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = x.n.NotifyExpiring(context.Background(), expNow)
		}()
	}
	wg.Wait()

	if x.mail.count() != 1 {
		t.Fatalf("mails = %d, want exactly 1 from two concurrent runs", x.mail.count())
	}
}

func TestNotifyExpiring_StoreErrorIsReturned(t *testing.T) {
	x := newExpFixture()
	x.subs.findErr = errors.New("mongo: connection refused")

	sent, err := x.n.NotifyExpiring(context.Background(), expNow)
	if err == nil || sent != 0 || x.mail.count() != 0 {
		t.Fatalf("sent=%d mails=%d err=%v, want 0 0 and an error", sent, x.mail.count(), err)
	}
}

// Review Focus 4. A subscription that is still live must never read "0 days"
// or round down to a number that undersells how soon it ends.
func TestDaysLeft(t *testing.T) {
	cases := []struct {
		name string
		left time.Duration
		want int
	}{
		{"six days twenty-three hours", 6*24*time.Hour + 23*time.Hour, 7},
		{"exactly seven days", 7 * 24 * time.Hour, 7},
		{"exactly one day", 24 * time.Hour, 1},
		{"one hour", time.Hour, 1},
		{"one day and a minute", 24*time.Hour + time.Minute, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := daysLeft(expNow, expNow.Add(tc.left)); got != tc.want {
				t.Fatalf("daysLeft(%v) = %d, want %d", tc.left, got, tc.want)
			}
		})
	}
}
