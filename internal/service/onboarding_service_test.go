package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type fakeOnbTenants struct {
	tenant   *models.Tenant
	marked   int
	sentAt   time.Time
	sentTo   string
	markErr  error
	allowRes []bool
}

func (f *fakeOnbTenants) FindByID(_ context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	if f.tenant == nil || f.tenant.ID != id {
		return nil, mongo.ErrNoDocuments
	}
	t := *f.tenant
	return &t, nil
}

func (f *fakeOnbTenants) MarkOnboardingSent(_ context.Context, id primitive.ObjectID, to string, at time.Time, allowResend bool) error {
	f.allowRes = append(f.allowRes, allowResend)
	if f.markErr != nil {
		return f.markErr
	}
	if f.tenant == nil || f.tenant.ID != id {
		return mongo.ErrNoDocuments
	}
	if !allowResend && f.tenant.OnboardingSentAt != nil {
		return mongo.ErrNoDocuments
	}
	f.marked++
	f.sentAt, f.sentTo = at, to
	f.tenant.OnboardingSentAt, f.tenant.OnboardingSentTo = &at, to
	return nil
}

type fakeOnbDomains struct {
	docs   []*models.TenantDomain
	called []primitive.ObjectID
}

func (f *fakeOnbDomains) ListByTenant(_ context.Context, id primitive.ObjectID) ([]*models.TenantDomain, error) {
	f.called = append(f.called, id)
	return f.docs, nil
}

type failMail struct{ sent int }

func (f *failMail) Available() bool { return true }
func (f *failMail) Send(string, mailer.Template, map[string]string) error {
	f.sent++
	return errors.New("smtp down")
}

type onbSettings struct {
	ps  *models.PaymentSettings
	err error
}

func (s onbSettings) Get(context.Context) (*models.PaymentSettings, error) { return s.ps, s.err }

type onbRig struct {
	svc     *OnboardingService
	tenants *fakeOnbTenants
	domains *fakeOnbDomains
	mail    *fakeMail
	tid     primitive.ObjectID
}

func newOnbRig() *onbRig {
	tid := primitive.NewObjectID()
	r := &onbRig{
		tid:     tid,
		tenants: &fakeOnbTenants{tenant: &models.Tenant{ID: tid, Name: "Tower LLC", ContactEmail: "owner@tower.example"}},
		domains: &fakeOnbDomains{docs: []*models.TenantDomain{{Name: "tower.example", IsPrimary: true, Registrar: "Namecheap"}}},
		mail:    &fakeMail{available: true},
	}
	ps := &models.PaymentSettings{CompanyName: "Acme Co", ContactPerson: "Bat"}
	r.svc = NewOnboardingService(r.tenants, r.domains, r.mail, onbSettings{ps: ps}, "TenantCore")
	r.svc.now = func() time.Time { return time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC) }
	return r
}

func (r *onbRig) send(in OnboardingSendInput) (*OnboardingStatus, error) {
	return r.svc.Send(context.Background(), r.tid.Hex(), in)
}

func onbCode(err error) int {
	var e *apierr.APIError
	if errors.As(err, &e) {
		return e.HTTPStatus
	}
	return 0
}

func TestOnboardingSendUsesTenantContactEmail(t *testing.T) {
	r := newOnbRig()
	if _, err := r.send(OnboardingSendInput{}); err != nil {
		t.Fatal(err)
	}
	if len(r.mail.sent) != 1 || r.mail.sent[0].to != "owner@tower.example" || r.mail.sent[0].tmpl != mailer.TemplateOnboarding {
		t.Fatalf("sent = %+v", r.mail.sent)
	}
}

func TestOnboardingSendRecordsSentState(t *testing.T) {
	r := newOnbRig()
	st, err := r.send(OnboardingSendInput{})
	if err != nil {
		t.Fatal(err)
	}
	if r.tenants.marked != 1 || r.tenants.sentTo != "owner@tower.example" || r.tenants.sentAt.IsZero() {
		t.Fatalf("store = %+v", r.tenants)
	}
	if st.SentAt == nil || st.SentTo != "owner@tower.example" || !st.CanSend {
		t.Fatalf("status = %+v", st)
	}
}

func TestOnboardingSendEmailOffIs503AndRecordsNothing(t *testing.T) {
	r := newOnbRig()
	r.mail.available = false
	_, err := r.send(OnboardingSendInput{})
	if onbCode(err) != http.StatusServiceUnavailable {
		t.Fatalf("err = %v", err)
	}
	if r.tenants.marked != 0 || len(r.mail.sent) != 0 {
		t.Fatal("something was recorded or sent")
	}
}

func TestOnboardingSendNoContactEmailIs409(t *testing.T) {
	r := newOnbRig()
	r.tenants.tenant.ContactEmail = ""
	_, err := r.send(OnboardingSendInput{})
	if onbCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "tenant has no valid contact email") {
		t.Fatalf("err = %v", err)
	}
}

func TestOnboardingSendNoDomainsIs409(t *testing.T) {
	r := newOnbRig()
	r.domains.docs = nil
	_, err := r.send(OnboardingSendInput{})
	if onbCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "add at least one domain first") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.svc.Preview(context.Background(), r.tid.Hex(), ""); onbCode(err) != http.StatusConflict {
		t.Fatalf("preview err = %v", err)
	}
}

func TestOnboardingResendNeedsFlag(t *testing.T) {
	r := newOnbRig()
	if _, err := r.send(OnboardingSendInput{}); err != nil {
		t.Fatal(err)
	}
	first := *r.tenants.tenant.OnboardingSentAt
	_, err := r.send(OnboardingSendInput{})
	if onbCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "set resend") {
		t.Fatalf("err = %v", err)
	}
	if len(r.mail.sent) != 1 {
		t.Fatalf("mails = %d", len(r.mail.sent))
	}
	r.svc.now = func() time.Time { return first.Add(time.Hour) }
	if _, err := r.send(OnboardingSendInput{Resend: true}); err != nil {
		t.Fatal(err)
	}
	if len(r.mail.sent) != 2 || !r.tenants.sentAt.After(first) {
		t.Fatalf("mails = %d, sentAt = %v", len(r.mail.sent), r.tenants.sentAt)
	}
}

func TestOnboardingSendFailureRecordsNothing(t *testing.T) {
	r := newOnbRig()
	fm := &failMail{}
	r.svc.mail = fm
	_, err := r.send(OnboardingSendInput{})
	if onbCode(err) != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if fm.sent != 1 || r.tenants.marked != 0 || len(r.tenants.allowRes) != 0 {
		t.Fatal("state recorded after failed send")
	}
}

func TestOnboardingMarkLostRaceIs409(t *testing.T) {
	r := newOnbRig()
	r.tenants.markErr = mongo.ErrNoDocuments
	_, err := r.send(OnboardingSendInput{})
	if onbCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "onboarding was already sent") {
		t.Fatalf("err = %v", err)
	}
}

func TestOnboardingCrossTenantIs404(t *testing.T) {
	r := newOnbRig()
	other := primitive.NewObjectID().Hex()
	if _, err := r.svc.Status(context.Background(), other); onbCode(err) != http.StatusNotFound {
		t.Fatalf("status err = %v", err)
	}
	if _, err := r.svc.Send(context.Background(), other, OnboardingSendInput{}); onbCode(err) != http.StatusNotFound {
		t.Fatalf("send err = %v", err)
	}
	if _, err := r.svc.Preview(context.Background(), other, ""); onbCode(err) != http.StatusNotFound {
		t.Fatalf("preview err = %v", err)
	}
	if _, err := r.svc.Status(context.Background(), "nope"); onbCode(err) != http.StatusBadRequest {
		t.Fatalf("bad id err = %v", err)
	}
	if _, err := r.send(OnboardingSendInput{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range r.domains.called {
		if id != r.tid {
			t.Fatalf("domains listed for %s", id.Hex())
		}
	}
}

func TestOnboardingNoteTooLongIs400(t *testing.T) {
	r := newOnbRig()
	long := strings.Repeat("a", 501)
	if _, err := r.send(OnboardingSendInput{Note: long}); onbCode(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "invalid note") {
		t.Fatalf("send err = %v", err)
	}
	if _, err := r.svc.Preview(context.Background(), r.tid.Hex(), long); onbCode(err) != http.StatusBadRequest {
		t.Fatalf("preview err = %v", err)
	}
	if len(r.mail.sent) != 0 {
		t.Fatal("mail sent")
	}
}

func TestOnboardingNoteReachesTemplateData(t *testing.T) {
	r := newOnbRig()
	if _, err := r.send(OnboardingSendInput{Note: "  Welcome aboard  "}); err != nil {
		t.Fatal(err)
	}
	if got := r.mail.sent[0].data["note"]; got != "Welcome aboard" {
		t.Fatalf("note = %q", got)
	}
}

func TestOnboardingStatusBlockers(t *testing.T) {
	r := newOnbRig()
	st, err := r.svc.Status(context.Background(), r.tid.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if st.Blockers == nil || len(st.Blockers) != 0 || !st.CanSend {
		t.Fatalf("status = %+v", st)
	}
	r.tenants.tenant.ContactEmail = ""
	r.domains.docs = nil
	r.mail.available = false
	st, _ = r.svc.Status(context.Background(), r.tid.Hex())
	want := map[string]bool{"no_contact_email": true, "no_domains": true, "email_off": true}
	if len(st.Blockers) != 3 || st.CanSend {
		t.Fatalf("status = %+v", st)
	}
	for _, b := range st.Blockers {
		if !want[b] {
			t.Fatalf("unexpected blocker %q", b)
		}
	}
}

func TestOnboardingMailDataCapsAndOrders(t *testing.T) {
	r := newOnbRig()
	var docs []*models.TenantDomain
	for i := 0; i < 60; i++ {
		docs = append(docs, &models.TenantDomain{Name: fmt.Sprintf("d%02d.example", i)})
	}
	docs[40].IsPrimary = true
	for i := 0; i < 15; i++ {
		docs[40].Subdomains = append(docs[40].Subdomains, models.Subdomain{Name: fmt.Sprintf("s%d.d40.example", i), Purpose: "web"})
	}
	d := r.svc.mailData(r.tenants.tenant, docs, nil, "")
	if d["domain_count"] != "50" || d["domain_1_name"] != "d40.example" || d["domain_1_primary"] != "1" {
		t.Fatalf("count=%s first=%s", d["domain_count"], d["domain_1_name"])
	}
	if _, ok := d["domain_51_name"]; ok {
		t.Fatal("more than 50 domains")
	}
	if n := len(strings.Split(d["domain_1_subs"], "\n")); n != 10 || d["domain_1_subs_more"] != "5" {
		t.Fatalf("subs lines = %d, more = %q", n, d["domain_1_subs_more"])
	}
	if d["site_url"] != "https://d40.example" {
		t.Fatalf("site_url = %q", d["site_url"])
	}
	if d["issuer_name"] != "TenantCore" {
		t.Fatalf("issuer fallback = %q", d["issuer_name"])
	}
	if _, ok := d["admin_url"]; ok {
		t.Fatal("admin_url should be omitted")
	}
}

func TestOnboardingPreviewEqualsSentBody(t *testing.T) {
	r := newOnbRig()
	r.domains.docs[0].Subdomains = []models.Subdomain{{Name: "admin.tower.example", Purpose: "admin", Status: "live"}}
	pv, err := r.svc.Preview(context.Background(), r.tid.Hex(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.send(OnboardingSendInput{Note: "hello"}); err != nil {
		t.Fatal(err)
	}
	_, text, html, err := mailer.Render(mailer.TemplateOnboarding, r.mail.sent[0].data)
	if err != nil {
		t.Fatal(err)
	}
	if pv.HTML != html || pv.Text != text {
		t.Fatal("preview differs from the sent body")
	}
	if r.mail.sent[0].data["admin_url"] != "https://admin.tower.example" {
		t.Fatalf("admin_url = %q", r.mail.sent[0].data["admin_url"])
	}
}

func TestOnboardingBlankIssuerStillRenders(t *testing.T) {
	r := newOnbRig()
	r.svc.settings = onbSettings{ps: &models.PaymentSettings{CompanyName: "   ", ContactPerson: "  "}}
	r.svc.appName = "  "
	pv, err := r.svc.Preview(context.Background(), r.tid.Hex(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pv.Text, "TenantCore") {
		t.Fatal("fallback issuer missing")
	}
	if _, err := r.send(OnboardingSendInput{}); err != nil {
		t.Fatal(err)
	}
}

func TestOnboardingSettingsErrorDegrades(t *testing.T) {
	r := newOnbRig()
	r.svc.settings = onbSettings{err: errors.New("db down")}
	if _, err := r.svc.Preview(context.Background(), r.tid.Hex(), ""); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := r.send(OnboardingSendInput{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if r.mail.sent[0].data["issuer_name"] != "TenantCore" {
		t.Fatalf("issuer = %q", r.mail.sent[0].data["issuer_name"])
	}
}

func TestOnboardingResendVanishedTenantIs404(t *testing.T) {
	r := newOnbRig()
	r.tenants.markErr = mongo.ErrNoDocuments
	_, err := r.send(OnboardingSendInput{Resend: true})
	if onbCode(err) != http.StatusNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestOnboardingFullDataRendersThroughMailer(t *testing.T) {
	r := newOnbRig()
	exp := time.Date(2027, 3, 4, 0, 0, 0, 0, time.UTC)
	doc := &models.TenantDomain{
		Name: "tower.example", IsPrimary: true, RegistrantName: "Tower LLC", RegistrantOwner: models.DomainOwnerUs,
		Registrar: "Namecheap", RegistrarAccountEmail: "reg@tower.example", DNSProvider: "Cloudflare", DNSProviderEmail: "dns@tower.example",
		ExpiresAt: &exp, AutoRenew: true,
		Deployment:       models.DomainDeployment{Provider: "Vercel", Environment: "production"},
		TechContactEmail: "tech@tower.example", BillingContactEmail: "bill@tower.example",
	}
	for i := 0; i < 12; i++ {
		doc.Subdomains = append(doc.Subdomains, models.Subdomain{Name: fmt.Sprintf("s%d.tower.example", i), Purpose: "web", Status: "live"})
	}
	data := r.svc.mailData(r.tenants.tenant, []*models.TenantDomain{doc}, &models.PaymentSettings{CompanyName: "Acme Co", ContactPerson: "Bat"}, "hi")
	_, text, html, err := mailer.Render(mailer.TemplateOnboarding, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2027.03.04", "Бид", "+2 бусад", "Vercel", "tech@tower.example", "bill@tower.example", "Bat", "s0.tower.example · web · live"} {
		if !strings.Contains(text, want) || !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestOnboardingSixtyDomainsRender(t *testing.T) {
	r := newOnbRig()
	var docs []*models.TenantDomain
	for i := 0; i < 60; i++ {
		docs = append(docs, &models.TenantDomain{Name: fmt.Sprintf("d%02d.example", i)})
	}
	docs[40].IsPrimary = true
	for i := 0; i < 15; i++ {
		docs[40].Subdomains = append(docs[40].Subdomains, models.Subdomain{Name: fmt.Sprintf("s%d.d40.example", i), Purpose: "web"})
	}
	if _, _, _, err := mailer.Render(mailer.TemplateOnboarding, r.svc.mailData(r.tenants.tenant, docs, nil, "")); err != nil {
		t.Fatal(err)
	}
}
