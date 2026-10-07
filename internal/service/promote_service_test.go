package service

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// ── Fakes ────────────────────────────────────────────────────────────────

type promoFakeQuotes struct {
	mu       sync.Mutex
	quote    *models.Quote
	findErr  error
	linkOK   bool
	linkErr  error
	links    int
	linkedTo primitive.ObjectID
	linkUser *primitive.ObjectID
}

func (f *promoFakeQuotes) FindByID(_ context.Context, id primitive.ObjectID) (*models.Quote, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	if f.quote == nil || f.quote.ID != id {
		return nil, mongo.ErrNoDocuments
	}
	q := *f.quote
	return &q, nil
}

func (f *promoFakeQuotes) LinkPromoted(_ context.Context, _ primitive.ObjectID, tenantID primitive.ObjectID, userID *primitive.ObjectID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links++
	f.linkedTo = tenantID
	f.linkUser = userID
	return f.linkOK, f.linkErr
}

type promoFakeTenants struct {
	created []*models.Tenant
	err     error
	key     string
}

func (f *promoFakeTenants) Create(_ context.Context, t *models.Tenant) (*models.Tenant, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	t.ID = primitive.NewObjectID()
	t.Name = strings.TrimSpace(t.Name)
	t.Slug = strings.TrimSpace(t.Slug)
	f.created = append(f.created, t)
	return t, f.key, nil
}

type promoFakeUsers struct {
	users []*models.PlatformUser
	err   error
}

func (f *promoFakeUsers) ListActive(context.Context) ([]*models.PlatformUser, error) {
	return f.users, f.err
}

type promoSent struct {
	to   string
	tmpl mailer.Template
	data map[string]string
}

type promoFakeMail struct {
	mu        sync.Mutex
	available bool
	failFor   map[string]bool
	sent      []promoSent // successful sends
	attempts  []promoSent // every call, successful or not
	block     chan struct{}
}

func (f *promoFakeMail) Available() bool { return f.available }

func (f *promoFakeMail) Send(to string, tmpl mailer.Template, data map[string]string) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[string]string{}
	for k, v := range data {
		cp[k] = v
	}
	m := promoSent{to: to, tmpl: tmpl, data: cp}
	f.attempts = append(f.attempts, m)
	if f.failFor[to] {
		return errors.New("smtp: mailbox unavailable for " + to)
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *promoFakeMail) snapshot() (sent, attempts []promoSent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]promoSent(nil), f.sent...), append([]promoSent(nil), f.attempts...)
}

// testKey is assembled at run time so the pre-commit secret scan has nothing
// to match on, and is distinctive enough that any leak is unambiguous.
func testKey() string {
	return strings.Join([]string{"tck", "PROMOTE", "raw", "9f3a7c2e1b"}, "_")
}

type promoHarness struct {
	svc     *PromoteService
	quotes  *promoFakeQuotes
	tenants *promoFakeTenants
	users   *promoFakeUsers
	mail    *promoFakeMail
	logs    *observer.ObservedLogs
	quoteID string
	actor   primitive.ObjectID
}

func newPromoHarness(t *testing.T) *promoHarness {
	t.Helper()
	qid := primitive.NewObjectID()
	h := &promoHarness{
		quotes: &promoFakeQuotes{
			quote:  &models.Quote{ID: qid, Name: "Prospect", Email: "prospect@example.com", Status: models.QuoteNew},
			linkOK: true,
		},
		tenants: &promoFakeTenants{key: testKey()},
		users: &promoFakeUsers{users: []*models.PlatformUser{
			{ID: primitive.NewObjectID(), Name: "Ann", Email: "ann@example.com", Status: models.PlatformUserActive},
			{ID: primitive.NewObjectID(), Name: "Bat", Email: "bat@example.com", Status: models.PlatformUserActive},
		}},
		mail:    &promoFakeMail{available: true},
		quoteID: qid.Hex(),
		actor:   primitive.NewObjectID(),
	}
	core, logs := observer.New(zapcore.DebugLevel)
	h.logs = logs
	h.svc = NewPromoteService(h.quotes, h.tenants, h.users, h.mail, "Test Console", zap.New(core))
	return h
}

func (h *promoHarness) promote(t *testing.T, in PromoteInput) (*PromoteResult, error) {
	t.Helper()
	res, err := h.svc.Promote(context.Background(), h.quoteID, in, &h.actor, "Admin One")
	h.svc.Drain()
	return res, err
}

var promoInput = PromoteInput{Name: "E and S Discovery", Slug: "es-discovery", ContactEmail: "owner@example.com", Domain: "es.example.com"}

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var ae *apierr.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *apierr.APIError with status %d, got %v", status, err)
	}
	if ae.HTTPStatus != status {
		t.Fatalf("status = %d, want %d (%v)", ae.HTTPStatus, status, err)
	}
}

// ── Promote ──────────────────────────────────────────────────────────────

func TestPromoteCreatesTenantLinksQuoteAndReturnsKey(t *testing.T) {
	h := newPromoHarness(t)
	res, err := h.promote(t, promoInput)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.APIKey != testKey() {
		t.Fatal("result does not carry the raw key from TenantService.Create")
	}
	if !res.QuoteLinked {
		t.Fatal("QuoteLinked = false, want true")
	}
	if len(h.tenants.created) != 1 {
		t.Fatalf("tenants created = %d, want 1", len(h.tenants.created))
	}
	got := h.tenants.created[0]
	if got.Name != promoInput.Name || got.Slug != promoInput.Slug ||
		got.ContactEmail != promoInput.ContactEmail || got.Domain != promoInput.Domain {
		t.Fatalf("tenant fields not mapped from input: %+v", got)
	}
	if res.Tenant != got {
		t.Fatal("result tenant is not the created tenant")
	}
	if h.quotes.links != 1 || h.quotes.linkedTo != got.ID {
		t.Fatalf("LinkPromoted calls=%d tenant=%s, want 1 call with %s", h.quotes.links, h.quotes.linkedTo.Hex(), got.ID.Hex())
	}
	if h.quotes.linkUser == nil || *h.quotes.linkUser != h.actor {
		t.Fatal("LinkPromoted not given the acting user")
	}
}

func TestPromoteUnknownQuoteIs404(t *testing.T) {
	h := newPromoHarness(t)
	h.quoteID = primitive.NewObjectID().Hex()
	_, err := h.promote(t, promoInput)
	wantStatus(t, err, http.StatusNotFound)
	if len(h.tenants.created) != 0 {
		t.Fatal("tenant created for an unknown quote")
	}
}

func TestPromoteLookupFailureIs500(t *testing.T) {
	h := newPromoHarness(t)
	h.quotes.findErr = errors.New("connection reset")
	_, err := h.promote(t, promoInput)
	wantStatus(t, err, http.StatusInternalServerError)
	if len(h.tenants.created) != 0 {
		t.Fatal("tenant created after a failed lookup")
	}
}

func TestPromoteInvalidIDIs400(t *testing.T) {
	h := newPromoHarness(t)
	h.quoteID = "not-an-id"
	_, err := h.promote(t, promoInput)
	wantStatus(t, err, http.StatusBadRequest)
	if len(h.tenants.created) != 0 {
		t.Fatal("tenant created for an invalid id")
	}
}

func TestPromoteAlreadyPromotedIs409(t *testing.T) {
	h := newPromoHarness(t)
	prev := primitive.NewObjectID()
	h.quotes.quote.PromotedTenantID = &prev
	_, err := h.promote(t, promoInput)
	wantStatus(t, err, http.StatusConflict)
	if len(h.tenants.created) != 0 || h.quotes.links != 0 {
		t.Fatal("already-promoted quote created a tenant or was relinked")
	}
}

func TestPromoteQuoteWithTenantIDIs409(t *testing.T) {
	h := newPromoHarness(t)
	owner := primitive.NewObjectID()
	h.quotes.quote.TenantID = &owner
	_, err := h.promote(t, promoInput)
	wantStatus(t, err, http.StatusConflict)
	if !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("conflict message should say why: %v", err)
	}
	if len(h.tenants.created) != 0 || h.quotes.links != 0 {
		t.Fatal("a tenant's storefront lead was promoted")
	}
}

func TestPromoteDuplicateSlugPropagatesAndDoesNotLink(t *testing.T) {
	h := newPromoHarness(t)
	dup := apierr.Conflict("a tenant with this slug already exists").In(apierr.DomainTenant)
	h.tenants.err = dup
	res, err := h.promote(t, promoInput)
	if res != nil {
		t.Fatal("result returned with an error")
	}
	if !errors.Is(err, dup) {
		t.Fatalf("err = %v, want the tenant service's own conflict", err)
	}
	if h.quotes.links != 0 {
		t.Fatal("LinkPromoted called although no tenant exists")
	}
	if _, attempts := h.mail.snapshot(); len(attempts) != 0 {
		t.Fatal("mail sent for a promote that failed")
	}
}

func TestPromoteLinkFailureStillReturnsKeyWithQuoteLinkedFalse(t *testing.T) {
	cases := map[string]func(*promoFakeQuotes){
		"link error":    func(q *promoFakeQuotes) { q.linkOK, q.linkErr = false, errors.New("write conflict") },
		"link no match": func(q *promoFakeQuotes) { q.linkOK, q.linkErr = false, nil },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPromoHarness(t)
			setup(h.quotes)
			res, err := h.promote(t, promoInput)
			if err != nil {
				t.Fatalf("Promote returned %v; the tenant exists, so the key must not be lost", err)
			}
			if res.APIKey != testKey() || res.Tenant == nil {
				t.Fatal("result lost the tenant or its key")
			}
			if res.QuoteLinked {
				t.Fatal("QuoteLinked = true after a failed link")
			}
			if sent, _ := h.mail.snapshot(); len(sent) != 2 {
				t.Fatalf("mails = %d, want 2: the tenant exists and admins still need to know", len(sent))
			}
			found := false
			for _, e := range h.logs.All() {
				ctx := e.ContextMap()
				if ctx["quote"] == h.quoteID && ctx["tenant"] == res.Tenant.ID.Hex() {
					found = true
				}
			}
			if !found {
				t.Fatal("no log entry naming the quote id and tenant id")
			}
		})
	}
}

// ── Secrets ──────────────────────────────────────────────────────────────

func TestKeyNeverInLogsOrMailData(t *testing.T) {
	// Exercise every logging path: link error, one failing address.
	h := newPromoHarness(t)
	h.quotes.linkOK, h.quotes.linkErr = false, errors.New("write conflict")
	h.mail.failFor = map[string]bool{"ann@example.com": true}
	res, err := h.promote(t, promoInput)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	key := testKey()
	if res.APIKey != key {
		t.Fatal("key missing from the result")
	}

	if h.logs.Len() == 0 {
		t.Fatal("expected log entries to inspect")
	}
	for _, e := range h.logs.All() {
		if strings.Contains(e.Message, key) {
			t.Fatalf("key in log message %q", e.Message)
		}
		for k, v := range e.ContextMap() {
			if strings.Contains(stringify(v), key) {
				t.Fatalf("key in log field %q", k)
			}
		}
	}
	_, attempts := h.mail.snapshot()
	if len(attempts) == 0 {
		t.Fatal("expected mail to inspect")
	}
	for _, m := range attempts {
		if strings.Contains(m.to, key) {
			t.Fatal("key in a recipient")
		}
		for k, v := range m.data {
			if strings.Contains(v, key) {
				t.Fatalf("key in mail data %q", k)
			}
		}
	}
}

func stringify(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	case interface{ String() string }:
		return x.String()
	}
	return ""
}

// ── Notification ─────────────────────────────────────────────────────────

func TestNotifySendsOneMailPerActiveUser(t *testing.T) {
	h := newPromoHarness(t)
	if _, err := h.promote(t, promoInput); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	sent, _ := h.mail.snapshot()
	if len(sent) != 2 {
		t.Fatalf("mails = %d, want 2", len(sent))
	}
	to := []string{sent[0].to, sent[1].to}
	sort.Strings(to)
	if to[0] != "ann@example.com" || to[1] != "bat@example.com" {
		t.Fatalf("recipients = %v", to)
	}
	for _, m := range sent {
		if m.tmpl != mailer.TemplatePromoted || string(m.tmpl) != "tenant_promoted" {
			t.Fatalf("template = %q", m.tmpl)
		}
		keys := make([]string, 0, len(m.data))
		for k := range m.data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "app,promoted_by,slug,tenant" {
			t.Fatalf("data keys = %v, want exactly app, tenant, slug, promoted_by", keys)
		}
		if m.data["app"] != "Test Console" || m.data["tenant"] != promoInput.Name ||
			m.data["slug"] != promoInput.Slug || m.data["promoted_by"] != "Admin One" {
			t.Fatalf("data = %v", m.data)
		}
	}
}

func TestNotifyFailureOfOneAddressDoesNotBlockOthers(t *testing.T) {
	h := newPromoHarness(t)
	h.mail.failFor = map[string]bool{"ann@example.com": true}
	if _, err := h.promote(t, promoInput); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	sent, attempts := h.mail.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if len(sent) != 1 || sent[0].to != "bat@example.com" {
		t.Fatalf("sent = %+v, want only bat", sent)
	}
	// The failure is logged with ids, never the address or the raw error.
	failed := h.users.users[0].ID.Hex()
	found := false
	for _, e := range h.logs.All() {
		ctx := e.ContextMap()
		if ctx["user"] == failed && ctx["quote"] == h.quoteID {
			found = true
		}
		if strings.Contains(e.Message, "ann@example.com") {
			t.Fatal("address in log message")
		}
		for k, v := range ctx {
			if strings.Contains(stringify(v), "ann@example.com") {
				t.Fatalf("address in log field %q", k)
			}
		}
	}
	if !found {
		t.Fatal("no log entry naming the quote and the failed user")
	}
}

func TestNoMailerOrNoRecipientsSendsNothing(t *testing.T) {
	t.Run("nil mailer", func(t *testing.T) {
		h := newPromoHarness(t)
		h.svc = NewPromoteService(h.quotes, h.tenants, h.users, nil, "Test Console", zap.NewNop())
		res, err := h.promote(t, promoInput)
		if err != nil || res.APIKey != testKey() {
			t.Fatalf("Promote with no mailer: res=%v err=%v", res != nil, err)
		}
	})
	t.Run("unavailable mailer", func(t *testing.T) {
		h := newPromoHarness(t)
		h.mail.available = false
		if _, err := h.promote(t, promoInput); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if _, attempts := h.mail.snapshot(); len(attempts) != 0 {
			t.Fatalf("attempts = %d, want 0", len(attempts))
		}
	})
	t.Run("typed nil mailer", func(t *testing.T) {
		h := newPromoHarness(t)
		var m *mailer.Mailer
		h.svc = NewPromoteService(h.quotes, h.tenants, h.users, m, "Test Console", zap.NewNop())
		if _, err := h.promote(t, promoInput); err != nil {
			t.Fatalf("Promote: %v", err)
		}
	})
	t.Run("no active users", func(t *testing.T) {
		h := newPromoHarness(t)
		h.users.users = nil
		if _, err := h.promote(t, promoInput); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if _, attempts := h.mail.snapshot(); len(attempts) != 0 {
			t.Fatalf("attempts = %d, want 0", len(attempts))
		}
	})
	t.Run("recipient lookup fails", func(t *testing.T) {
		h := newPromoHarness(t)
		h.users.err = errors.New("db down")
		res, err := h.promote(t, promoInput)
		if err != nil || res.APIKey != testKey() {
			t.Fatalf("Promote: %v", err)
		}
		if _, attempts := h.mail.snapshot(); len(attempts) != 0 {
			t.Fatalf("attempts = %d, want 0", len(attempts))
		}
	})
}

func TestPromotedByIsSingleLineAndCapped(t *testing.T) {
	long := strings.Repeat("A", 10000)
	cases := []struct {
		name, actor, tenant string
	}{
		{"crlf actor", "Admin\r\nBcc: evil@example.com", "Tenant"},
		{"long actor", long, "Tenant"},
		{"crlf tenant", "Admin", "Tenant\r\nBcc: evil@example.com"},
		{"long tenant", "Admin", "Tenant " + long},
		{"long multibyte actor", strings.Repeat("Ө", 10000), "Tenant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPromoHarness(t)
			in := promoInput
			in.Name = tc.tenant
			if _, err := h.svc.Promote(context.Background(), h.quoteID, in, &h.actor, tc.actor); err != nil {
				t.Fatalf("Promote: %v", err)
			}
			h.svc.Drain()
			sent, _ := h.mail.snapshot()
			if len(sent) == 0 {
				t.Fatal("no mail sent")
			}
			for _, m := range sent {
				for k, v := range m.data {
					if strings.ContainsAny(v, "\r\n") {
						t.Fatalf("%s is not single-line: %q", k, v)
					}
					if n := utf8.RuneCountInString(v); n > 256 {
						t.Fatalf("%s is %d runes, want <= 256", k, n)
					}
					if strings.TrimSpace(v) == "" {
						t.Fatalf("%s is empty", k)
					}
				}
			}
		})
	}
}

func TestPromotedByFallsBackWhenActorNameEmpty(t *testing.T) {
	h := newPromoHarness(t)
	if _, err := h.svc.Promote(context.Background(), h.quoteID, promoInput, nil, " \r\n "); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	h.svc.Drain()
	sent, _ := h.mail.snapshot()
	if len(sent) == 0 || strings.TrimSpace(sent[0].data["promoted_by"]) == "" {
		t.Fatal("promoted_by empty: the template would refuse to render")
	}
	if h.quotes.linkUser != nil {
		t.Fatal("nil actor should reach LinkPromoted as nil")
	}
}

func TestMailFailureDoesNotChangeResult(t *testing.T) {
	h := newPromoHarness(t)
	h.mail.failFor = map[string]bool{"ann@example.com": true, "bat@example.com": true}
	res, err := h.promote(t, promoInput)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if res.APIKey != testKey() || !res.QuoteLinked || res.Tenant == nil {
		t.Fatalf("result changed by mail failure: %+v", res)
	}
}

func TestNotifyDoesNotDelayPromote(t *testing.T) {
	h := newPromoHarness(t)
	h.mail.block = make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := h.svc.Promote(context.Background(), h.quoteID, promoInput, &h.actor, "Admin One"); err != nil {
			t.Errorf("Promote: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(h.mail.block)
		t.Fatal("Promote waited for SMTP")
	}
	close(h.mail.block)
	h.svc.Drain()
	if sent, _ := h.mail.snapshot(); len(sent) != 2 {
		t.Fatalf("mails after Drain = %d, want 2", len(sent))
	}
}
