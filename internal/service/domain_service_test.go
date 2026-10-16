package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// fakeDomainStore mimics the repository's tenant scoping and the unique name
// index.
type fakeDomainStore struct {
	docs []*models.TenantDomain
	// writeErr, when set, makes Insert and Update fail with it (a DB error or
	// a race lost on an index) after the uniqueness check passed.
	writeErr error
}

func (f *fakeDomainStore) NameTaken(_ context.Context, name string, except primitive.ObjectID) (bool, error) {
	for _, e := range f.docs {
		if e.Name == name && e.ID != except {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeDomainStore) SetPrimary(_ context.Context, tid, id primitive.ObjectID) error {
	for _, e := range f.docs {
		if e.ID == id && e.TenantID == tid {
			e.IsPrimary = true
		}
	}
	return nil
}

// MarkClientPaid mimics the repository: monotone, tenant scoped, missing is not an error.
func (f *fakeDomainStore) MarkClientPaid(_ context.Context, tid, id primitive.ObjectID, paidAt, through time.Time, method, reference string) error {
	for _, e := range f.docs {
		if e.ID == id && e.TenantID == tid {
			cc := &e.Payment.ClientCharge
			if cc.PaidThrough == nil || cc.PaidThrough.Before(through) {
				cc.PaidAt, cc.PaidThrough = &paidAt, &through
				if method != "" {
					cc.Method = method
				}
				if reference != "" {
					cc.Reference = reference
				}
			}
		}
	}
	return nil
}

// fakeDomainFees counts active fees per domain id.
type fakeDomainFees map[primitive.ObjectID]int64

func (f fakeDomainFees) CountActiveByDomain(_ context.Context, _, did primitive.ObjectID) (int64, error) {
	return f[did], nil
}

func (f *fakeDomainStore) ListByTenant(_ context.Context, tid primitive.ObjectID) ([]*models.TenantDomain, error) {
	out := []*models.TenantDomain{}
	for _, d := range f.docs {
		if d.TenantID == tid {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDomainStore) CountByTenant(ctx context.Context, tid primitive.ObjectID) (int64, error) {
	l, _ := f.ListByTenant(ctx, tid)
	return int64(len(l)), nil
}

func (f *fakeDomainStore) Find(_ context.Context, tid, id primitive.ObjectID) (*models.TenantDomain, error) {
	for _, d := range f.docs {
		if d.ID == id && d.TenantID == tid {
			c := *d
			return &c, nil
		}
	}
	return nil, mongo.ErrNoDocuments
}

var errDup = mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000}}}

func (f *fakeDomainStore) Insert(_ context.Context, d *models.TenantDomain) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	for _, e := range f.docs {
		if e.Name == d.Name {
			return errDup
		}
	}
	d.ID = primitive.NewObjectID()
	c := *d
	f.docs = append(f.docs, &c)
	return nil
}

func (f *fakeDomainStore) Update(_ context.Context, d *models.TenantDomain) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	for _, e := range f.docs {
		if e.Name == d.Name && e.ID != d.ID {
			return errDup
		}
	}
	for i, e := range f.docs {
		if e.ID == d.ID && e.TenantID == d.TenantID {
			c := *d
			f.docs[i] = &c
			return nil
		}
	}
	return mongo.ErrNoDocuments
}

func (f *fakeDomainStore) Delete(_ context.Context, tid, id primitive.ObjectID) error {
	for i, e := range f.docs {
		if e.ID == id && e.TenantID == tid {
			f.docs = append(f.docs[:i], f.docs[i+1:]...)
			return nil
		}
	}
	return mongo.ErrNoDocuments
}

func (f *fakeDomainStore) ClearPrimary(_ context.Context, tid, except primitive.ObjectID) error {
	for _, e := range f.docs {
		if e.TenantID == tid && e.ID != except {
			e.IsPrimary = false
		}
	}
	return nil
}

type domFakeTenants map[primitive.ObjectID]bool

func (f domFakeTenants) FindByID(_ context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	if f[id] {
		return &models.Tenant{ID: id}, nil
	}
	return nil, mongo.ErrNoDocuments
}

func newDomainSvc() (*DomainService, *fakeDomainStore, primitive.ObjectID, primitive.ObjectID) {
	a, b := primitive.NewObjectID(), primitive.NewObjectID()
	st := &fakeDomainStore{}
	return NewDomainService(st, domFakeTenants{a: true, b: true}, nil), st, a, b
}

func domStatus(err error) int {
	var ae *apierr.APIError
	if errors.As(err, &ae) {
		return ae.HTTPStatus
	}
	return 0
}

func TestBuildDomainValidation(t *testing.T) {
	long := strings.Repeat("x", maxDomainTextLen+1)
	manySubs := make([]models.Subdomain, MaxSubdomainsPerDomain+1)
	for i := range manySubs {
		manySubs[i] = models.Subdomain{Name: "s" + string(rune('a'+i%26)) + string(rune('a'+i/26))}
	}
	cases := []struct {
		name string
		mut  func(*DomainInput)
		ok   bool
	}{
		{"valid minimal", func(*DomainInput) {}, true},
		{"uppercase and trailing dot normalised", func(i *DomainInput) { i.Name = "Example.COM." }, true},
		{"empty name", func(i *DomainInput) { i.Name = "" }, false},
		{"url as name", func(i *DomainInput) { i.Name = "https://example.com" }, false},
		{"port in name", func(i *DomainInput) { i.Name = "example.com:443" }, false},
		{"ip as name", func(i *DomainInput) { i.Name = "10.0.0.1" }, false},
		{"single label name", func(i *DomainInput) { i.Name = "localhost" }, false},
		{"wildcard name", func(i *DomainInput) { i.Name = "*.example.com" }, false},
		{"underscore name", func(i *DomainInput) { i.Name = "a_b.example.com" }, false},
		{"owner us", func(i *DomainInput) { i.RegistrantOwner = "us" }, true},
		{"owner client", func(i *DomainInput) { i.RegistrantOwner = "client" }, true},
		{"owner bad", func(i *DomainInput) { i.RegistrantOwner = "them" }, false},
		{"env prod", func(i *DomainInput) { i.Deployment.Environment = "prod" }, true},
		{"env bad", func(i *DomainInput) { i.Deployment.Environment = "dev" }, false},
		{"good email", func(i *DomainInput) { i.TechContactEmail = "ops@example.com" }, true},
		{"bad email", func(i *DomainInput) { i.TechContactEmail = "nope" }, false},
		{"display-name email", func(i *DomainInput) { i.BillingContactEmail = "Bob <b@example.com>" }, false},
		{"bad registrar email", func(i *DomainInput) { i.RegistrarAccountEmail = "a@b" }, false},
		{"bad dns email", func(i *DomainInput) { i.DNSProviderEmail = "a b@example.com" }, false},
		{"bad deployment email", func(i *DomainInput) { i.Deployment.ProviderEmail = "x" }, false},
		{"long registrar", func(i *DomainInput) { i.Registrar = long }, false},
		{"long notes ok", func(i *DomainInput) { i.Notes = strings.Repeat("n", maxDomainNotesLen) }, true},
		{"too long notes", func(i *DomainInput) { i.Notes = strings.Repeat("n", maxDomainNotesLen+1) }, false},
		{"control char", func(i *DomainInput) { i.Registrar = "a\x00b" }, false},
		{"newline in notes ok", func(i *DomainInput) { i.Notes = "a\nb" }, true},
		{"newline in registrar", func(i *DomainInput) { i.Registrar = "a\nb" }, false},
		{"subdomain ok", func(i *DomainInput) {
			i.Subdomains = []models.Subdomain{{Name: "app", Purpose: "app"}, {Name: "api.example.com", Purpose: "api"}}
		}, true},
		{"subdomain bad purpose", func(i *DomainInput) { i.Subdomains = []models.Subdomain{{Name: "app", Purpose: "mail"}} }, false},
		{"subdomain bad name", func(i *DomainInput) { i.Subdomains = []models.Subdomain{{Name: "a/b"}} }, false},
		{"subdomain duplicate", func(i *DomainInput) {
			i.Subdomains = []models.Subdomain{{Name: "App"}, {Name: "app"}}
		}, false},
		{"subdomain long target", func(i *DomainInput) { i.Subdomains = []models.Subdomain{{Name: "app", Target: long}} }, false},
		{"too many subdomains", func(i *DomainInput) { i.Subdomains = manySubs }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := DomainInput{Name: "example.com"}
			tc.mut(&in)
			d, err := buildDomain(in)
			if tc.ok != (err == nil) {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
			if err != nil && domStatus(err) != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", domStatus(err))
			}
			if err != nil && (strings.Contains(err.Error(), "nope") || strings.Contains(err.Error(), "https://")) {
				t.Fatalf("error echoes input: %v", err)
			}
			if err == nil && d.Name != strings.ToLower(strings.TrimRight(in.Name, ".")) {
				t.Fatalf("name = %q", d.Name)
			}
		})
	}
}

func TestDomainCreateTenantScopingAndUniqueness(t *testing.T) {
	svc, st, a, b := newDomainSvc()
	ctx := context.Background()

	d, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "acme.mn"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if d.TenantID != a {
		t.Fatalf("tenant = %v", d.TenantID)
	}

	// Same name for another tenant is a 409.
	if _, err := svc.Create(ctx, b.Hex(), DomainInput{Name: "ACME.mn"}); domStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate name status = %d (%v)", domStatus(err), err)
	}

	// Cross-tenant get / update / delete are 404 and change nothing.
	if _, err := svc.Get(ctx, b.Hex(), d.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Fatalf("get status = %d", domStatus(err))
	}
	if _, err := svc.Update(ctx, b.Hex(), d.ID.Hex(), DomainInput{Name: "evil.mn"}); domStatus(err) != http.StatusNotFound {
		t.Fatalf("update status = %d", domStatus(err))
	}
	if err := svc.Delete(ctx, b.Hex(), d.ID.Hex()); domStatus(err) != http.StatusNotFound {
		t.Fatalf("delete status = %d", domStatus(err))
	}
	if len(st.docs) != 1 || st.docs[0].Name != "acme.mn" {
		t.Fatalf("store changed: %+v", st.docs)
	}

	// Lists only show the tenant's own.
	if l, _ := svc.List(ctx, b.Hex()); len(l) != 0 {
		t.Fatalf("tenant b sees %d domains", len(l))
	}
	if l, _ := svc.List(ctx, a.Hex()); len(l) != 1 {
		t.Fatalf("tenant a sees %d domains", len(l))
	}

	// Owner can delete.
	if err := svc.Delete(ctx, a.Hex(), d.ID.Hex()); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestDomainUnknownTenantAndBadIDs(t *testing.T) {
	svc, _, _, _ := newDomainSvc()
	ctx := context.Background()
	if _, err := svc.Create(ctx, primitive.NewObjectID().Hex(), DomainInput{Name: "x.mn"}); domStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown tenant create = %d", domStatus(err))
	}
	if _, err := svc.List(ctx, primitive.NewObjectID().Hex()); domStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown tenant list = %d", domStatus(err))
	}
	if _, err := svc.List(ctx, "zzz"); domStatus(err) != http.StatusBadRequest {
		t.Fatalf("bad tenant id = %d", domStatus(err))
	}
	if _, err := svc.Get(ctx, primitive.NewObjectID().Hex(), "zzz"); domStatus(err) != http.StatusBadRequest {
		t.Fatalf("bad domain id = %d", domStatus(err))
	}
}

func TestDomainLimitPerTenant(t *testing.T) {
	svc, _, a, b := newDomainSvc()
	ctx := context.Background()
	for i := 0; i < MaxDomainsPerTenant; i++ {
		name := "d" + strings.Repeat("a", i/26) + string(rune('a'+i%26)) + ".mn"
		if _, err := svc.Create(ctx, a.Hex(), DomainInput{Name: name}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "over.mn"}); domStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("over limit status = %d", domStatus(err))
	}
	// The limit is per tenant.
	if _, err := svc.Create(ctx, b.Hex(), DomainInput{Name: "over.mn"}); err != nil {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestDomainOnePrimaryPerTenant(t *testing.T) {
	svc, st, a, b := newDomainSvc()
	ctx := context.Background()

	d1, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "one.mn", IsPrimary: true})
	other, _ := svc.Create(ctx, b.Hex(), DomainInput{Name: "other.mn", IsPrimary: true})
	d2, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "two.mn", IsPrimary: true})
	if err != nil {
		t.Fatal(err)
	}
	got := func(id primitive.ObjectID) bool {
		for _, d := range st.docs {
			if d.ID == id {
				return d.IsPrimary
			}
		}
		t.Fatal("missing doc")
		return false
	}
	if got(d1.ID) || !got(d2.ID) || !got(other.ID) {
		t.Fatalf("primary flags wrong: d1=%v d2=%v other=%v", got(d1.ID), got(d2.ID), got(other.ID))
	}

	// Promote d1 again via update: d2 is demoted.
	if _, err := svc.Update(ctx, a.Hex(), d1.ID.Hex(), DomainInput{Name: "one.mn", IsPrimary: true}); err != nil {
		t.Fatal(err)
	}
	if !got(d1.ID) || got(d2.ID) {
		t.Fatalf("after update d1=%v d2=%v", got(d1.ID), got(d2.ID))
	}
}

func TestDomainUpdateKeepsIdentity(t *testing.T) {
	svc, _, a, _ := newDomainSvc()
	ctx := context.Background()
	d, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "one.mn"})
	u, err := svc.Update(ctx, a.Hex(), d.ID.Hex(), DomainInput{Name: "renamed.mn", Registrar: "R"})
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != d.ID || u.TenantID != a || u.Name != "renamed.mn" || u.Registrar != "R" {
		t.Fatalf("unexpected: %+v", u)
	}
	// Renaming onto an existing name is a 409.
	svc.Create(ctx, a.Hex(), DomainInput{Name: "taken.mn"})
	if _, err := svc.Update(ctx, a.Hex(), d.ID.Hex(), DomainInput{Name: "taken.mn"}); domStatus(err) != http.StatusConflict {
		t.Fatalf("rename conflict = %d", domStatus(err))
	}
}

func isPrimaryOf(t *testing.T, st *fakeDomainStore, id primitive.ObjectID) bool {
	t.Helper()
	for _, d := range st.docs {
		if d.ID == id {
			return d.IsPrimary
		}
	}
	t.Fatal("missing doc")
	return false
}

func apiMessage(err error) string {
	var ae *apierr.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return ""
}

// A duplicate name is refused before the current primary is demoted.
func TestDomainDuplicateNameKeepsPrimary(t *testing.T) {
	svc, st, a, b := newDomainSvc()
	ctx := context.Background()
	p, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "main.mn", IsPrimary: true})
	if _, err := svc.Create(ctx, b.Hex(), DomainInput{Name: "taken.mn"}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "taken.mn", IsPrimary: true}); domStatus(err) != http.StatusConflict {
		t.Fatalf("create duplicate = %d (%v)", domStatus(err), err)
	}
	if !isPrimaryOf(t, st, p.ID) {
		t.Fatal("create: the primary was lost to a duplicate-name request")
	}

	o, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "other.mn"})
	if _, err := svc.Update(ctx, a.Hex(), o.ID.Hex(), DomainInput{Name: "taken.mn", IsPrimary: true}); domStatus(err) != http.StatusConflict {
		t.Fatalf("update duplicate = %d (%v)", domStatus(err), err)
	}
	if !isPrimaryOf(t, st, p.ID) {
		t.Fatal("update: the primary was lost to a duplicate-name request")
	}
}

// If the write fails after the demotion, the previous primary is restored.
func TestDomainWriteFailureRestoresPrimary(t *testing.T) {
	svc, st, a, _ := newDomainSvc()
	ctx := context.Background()
	p, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "main.mn", IsPrimary: true})
	o, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "other.mn"})

	st.writeErr = errors.New("db down")
	if _, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "new.mn", IsPrimary: true}); domStatus(err) != http.StatusInternalServerError {
		t.Fatalf("create = %d (%v)", domStatus(err), err)
	}
	if !isPrimaryOf(t, st, p.ID) {
		t.Fatal("create: previous primary not restored")
	}
	if _, err := svc.Update(ctx, a.Hex(), o.ID.Hex(), DomainInput{Name: "other.mn", IsPrimary: true}); domStatus(err) != http.StatusInternalServerError {
		t.Fatalf("update = %d (%v)", domStatus(err), err)
	}
	if !isPrimaryOf(t, st, p.ID) || isPrimaryOf(t, st, o.ID) {
		t.Fatal("update: previous primary not restored")
	}
}

// A lost race on the primary index is a retryable 409 with its own message.
func TestDomainPrimaryIndexConflictMessage(t *testing.T) {
	svc, st, a, _ := newDomainSvc()
	ctx := context.Background()
	p, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "main.mn", IsPrimary: true})

	st.writeErr = mongo.WriteException{WriteErrors: mongo.WriteErrors{{
		Code: 11000, Message: "E11000 duplicate key error collection: x.tenant_domains index: tenant_primary_unique dup key",
	}}}
	_, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "new.mn", IsPrimary: true})
	if domStatus(err) != http.StatusConflict {
		t.Fatalf("status = %d (%v)", domStatus(err), err)
	}
	if msg := apiMessage(err); msg != "primary domain changed at the same time; try again" {
		t.Fatalf("message = %q", msg)
	}
	if !isPrimaryOf(t, st, p.ID) {
		t.Fatal("previous primary not restored")
	}

	// A plain duplicate keeps the registered message.
	st.writeErr = errDup
	if _, err := svc.Create(ctx, a.Hex(), DomainInput{Name: "new.mn"}); apiMessage(err) != "domain is already registered" {
		t.Fatalf("message = %q", apiMessage(err))
	}
}

func TestDomainDeleteRefusedWhileActiveFeeLinked(t *testing.T) {
	svc, st, a, _ := newDomainSvc()
	ctx := context.Background()
	d, _ := svc.Create(ctx, a.Hex(), DomainInput{Name: "billed.mn"})
	fees := fakeDomainFees{d.ID: 1}
	svc.fees = fees

	err := svc.Delete(ctx, a.Hex(), d.ID.Hex())
	if domStatus(err) != http.StatusConflict {
		t.Fatalf("delete with active fee = %d (%v)", domStatus(err), err)
	}
	if len(st.docs) != 1 {
		t.Fatal("domain was deleted")
	}
	if strings.Contains(apiMessage(err), "billed.mn") {
		t.Fatalf("message echoes the name: %q", apiMessage(err))
	}

	fees[d.ID] = 0
	if err := svc.Delete(ctx, a.Hex(), d.ID.Hex()); err != nil {
		t.Fatalf("delete without active fee: %v", err)
	}
}

func TestDomainTextRejectsFormatCharacters(t *testing.T) {
	for _, s := range []string{"a\u200bb", "\u200b", "x\u202ey", "\ufeffnote"} {
		if _, ok := cleanText(s, 100, true); ok {
			t.Errorf("cleanText accepted %q", s)
		}
	}
	if v, ok := cleanText("  plain note \nline two", 100, true); !ok || v != "plain note \nline two" {
		t.Fatalf("plain text rejected: %q %v", v, ok)
	}
}
