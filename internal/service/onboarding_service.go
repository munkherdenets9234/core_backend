package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// maxOnboardingSubdomains is the most subdomain lines the service puts under
// one domain; the rest is passed on as a count (the mailer caps it again).
const maxOnboardingSubdomains = 10

// onboardingFallbackIssuer is the issuer name of last resort, when neither the
// settings nor appName give one (the template requires it).
const onboardingFallbackIssuer = "TenantCore"

// maxOnboardingNote is the longest note, in runes.
const maxOnboardingNote = 500

// Blockers Status reports; fixed strings the admin console maps to text.
const (
	onbBlockNoContact = "no_contact_email"
	onbBlockNoDomains = "no_domains"
	onbBlockEmailOff  = "email_off"
)

// onboardingTenantStore is the slice of the tenant repository the service
// needs, narrow so the rules can be tested without MongoDB.
type onboardingTenantStore interface {
	FindByID(ctx context.Context, id primitive.ObjectID) (*models.Tenant, error)
	// MarkOnboardingSent records the send. Unless allowResend it only matches a
	// tenant that was never sent to; mongo.ErrNoDocuments otherwise.
	MarkOnboardingSent(ctx context.Context, id primitive.ObjectID, to string, at time.Time, allowResend bool) error
}

// onboardingDomainLister lists the domain records of one tenant.
type onboardingDomainLister interface {
	ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantDomain, error)
}

// OnboardingStatus is what the console shows about a tenant's onboarding mail.
type OnboardingStatus struct {
	SentAt  *time.Time `json:"sent_at"`
	SentTo  string     `json:"sent_to"`
	CanSend bool       `json:"can_send"`
	// Blockers lists what stops a send: no_contact_email, no_domains, email_off.
	// Never null.
	Blockers []string `json:"blockers"`
}

// OnboardingSendInput is the optional body of the send call. There is no
// recipient: the mail always goes to the tenant's contact email.
type OnboardingSendInput struct {
	Note   string `json:"note"`
	Resend bool   `json:"resend"`
}

// OnboardingService previews and sends the welcome email of a tenant. It is
// bookkeeping only: nothing here touches entitlement, routing or
// authentication, and it never writes to the domain records.
type OnboardingService struct {
	tenants  onboardingTenantStore
	domains  onboardingDomainLister
	mail     sender
	settings paymentSettingsReader
	appName  string
	now      func() time.Time
}

// NewOnboardingService wires the service. A nil mail (or one that is not
// configured) makes Send answer 503 and Status report email_off; nil settings
// are fine, the issuer then falls back to appName.
func NewOnboardingService(tenants onboardingTenantStore, domains onboardingDomainLister, mail sender, settings paymentSettingsReader, appName string) *OnboardingService {
	return &OnboardingService{tenants: tenants, domains: domains, mail: mail, settings: settings, appName: appName, now: time.Now}
}

func (s *OnboardingService) requireTenant(ctx context.Context, tenantIDStr string) (primitive.ObjectID, *models.Tenant, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return tid, nil, err
	}
	t, err := s.tenants.FindByID(ctx, tid)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return tid, nil, apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return tid, nil, apierr.Internal(err)
	}
	return tid, t, nil
}

func (s *OnboardingService) mailOn() bool { return s.mail != nil && s.mail.Available() }

func errNoDomains() error {
	return apierr.Conflict("add at least one domain first").In(apierr.DomainTenant)
}

func errNoContactEmail() error {
	return apierr.Conflict("tenant has no valid contact email").In(apierr.DomainTenant)
}

// status builds the status of t from what was already loaded.
func (s *OnboardingService) status(t *models.Tenant, nDomains int) *OnboardingStatus {
	blockers := []string{}
	if to, ok := cleanEmail(t.ContactEmail); !ok || to == "" {
		blockers = append(blockers, onbBlockNoContact)
	}
	if nDomains == 0 {
		blockers = append(blockers, onbBlockNoDomains)
	}
	if !s.mailOn() {
		blockers = append(blockers, onbBlockEmailOff)
	}
	return &OnboardingStatus{SentAt: t.OnboardingSentAt, SentTo: t.OnboardingSentTo, CanSend: len(blockers) == 0, Blockers: blockers}
}

// Status reports when the onboarding mail was last sent and what blocks a send.
func (s *OnboardingService) Status(ctx context.Context, tenantIDStr string) (*OnboardingStatus, error) {
	tid, t, err := s.requireTenant(ctx, tenantIDStr)
	if err != nil {
		return nil, err
	}
	docs, err := s.domains.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return s.status(t, len(docs)), nil
}

// settingsOrNil reads the platform settings, which only supply the issuer
// name and contact person. Onboarding needs no bank details, so settings that
// are missing or unreadable degrade to nil instead of failing the call.
func (s *OnboardingService) settingsOrNil(ctx context.Context) *models.PaymentSettings {
	if s.settings == nil {
		return nil
	}
	ps, err := s.settings.Get(ctx)
	if err != nil {
		return nil
	}
	return ps
}

func cleanOnboardingNote(note string) (string, error) {
	v, ok := cleanText(note, maxOnboardingNote, false)
	if !ok {
		return "", apierr.BadRequest("invalid note").In(apierr.DomainTenant)
	}
	return v, nil
}

// Preview renders the onboarding mail exactly as Send would, sending and
// changing nothing. The template needs a domain, so a tenant without one is 409.
func (s *OnboardingService) Preview(ctx context.Context, tenantIDStr, note string) (*InvoicePreview, error) {
	note, err := cleanOnboardingNote(note)
	if err != nil {
		return nil, err
	}
	tid, t, err := s.requireTenant(ctx, tenantIDStr)
	if err != nil {
		return nil, err
	}
	docs, err := s.domains.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if len(docs) == 0 {
		return nil, errNoDomains()
	}
	ps := s.settingsOrNil(ctx)
	subject, text, htmlBody, err := mailer.Render(mailer.TemplateOnboarding, s.mailData(t, docs, ps, note))
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &InvoicePreview{Subject: subject, Text: text, HTML: htmlBody}, nil
}

// Send mails the onboarding email to the tenant's contact address and records
// it. The recipient is never taken from the request.
func (s *OnboardingService) Send(ctx context.Context, tenantIDStr string, in OnboardingSendInput) (*OnboardingStatus, error) {
	note, err := cleanOnboardingNote(in.Note)
	if err != nil {
		return nil, err
	}
	tid, t, err := s.requireTenant(ctx, tenantIDStr)
	if err != nil {
		return nil, err
	}
	if !s.mailOn() {
		return nil, apierr.FeatureUnavailable("email").In(apierr.DomainTenant)
	}
	to, ok := cleanEmail(t.ContactEmail)
	if !ok || to == "" {
		return nil, errNoContactEmail()
	}
	docs, err := s.domains.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if len(docs) == 0 {
		return nil, errNoDomains()
	}
	if t.OnboardingSentAt != nil && !in.Resend {
		return nil, apierr.Conflict("onboarding was already sent; set resend to send it again").In(apierr.DomainTenant)
	}
	ps := s.settingsOrNil(ctx)

	// Accepted race: the already-sent check above reads the tenant, and the mail
	// goes out before the conditional MarkOnboardingSent. Two truly concurrent
	// first sends can both mail; only one records, the other gets 409. A duplicate
	// welcome mail is harmless, so no claim step is taken before sending.
	if err := s.mail.Send(to, mailer.TemplateOnboarding, s.mailData(t, docs, ps, note)); err != nil {
		// The cause is for the log; the mail log row already records it.
		return nil, apierr.Upstream(apierr.DomainTenant, err)
	}
	at := s.now().UTC()
	if err := s.tenants.MarkOnboardingSent(ctx, tid, to, at, in.Resend); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			if in.Resend {
				// The filter had no sent condition, so only a vanished tenant misses.
				return nil, apierr.NotFound("tenant").In(apierr.DomainTenant)
			}
			return nil, apierr.Conflict("onboarding was already sent").In(apierr.DomainTenant)
		}
		return nil, apierr.Internal(err)
	}
	t.OnboardingSentAt, t.OnboardingSentTo = &at, to
	return s.status(t, len(docs)), nil
}

// mailData is the fixed field set of the onboarding template. Dates are
// formatted here; the mailer escapes and caps every value. docs is not
// modified. site_url and admin_url are left out when unknown, since the mailer
// refuses an empty one.
func (s *OnboardingService) mailData(t *models.Tenant, docs []*models.TenantDomain, ps *models.PaymentSettings, note string) map[string]string {
	ordered := append([]*models.TenantDomain(nil), docs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].IsPrimary && !ordered[j].IsPrimary })
	if len(ordered) > mailer.MaxOnboardingDomains {
		ordered = ordered[:mailer.MaxOnboardingDomains]
	}

	d := map[string]string{
		"tenant":       t.Name,
		"domain_count": strconv.Itoa(len(ordered)),
		"issuer_name":  onboardingFallbackIssuer,
	}
	if v := strings.TrimSpace(s.appName); v != "" {
		d["issuer_name"] = v
	}
	if ps != nil {
		if v := strings.TrimSpace(ps.CompanyName); v != "" {
			d["issuer_name"] = v
		}
		if v := strings.TrimSpace(ps.ContactPerson); v != "" {
			d["contact_person"] = v
		}
	}
	if note != "" {
		d["note"] = note
	}

	for i, doc := range ordered {
		n := strconv.Itoa(i + 1)
		set := func(key, v string) {
			if v = strings.TrimSpace(v); v != "" {
				d["domain_"+n+"_"+key] = v
			}
		}
		d["domain_"+n+"_name"] = doc.Name
		if doc.IsPrimary {
			d["domain_"+n+"_primary"] = "1"
		}
		set("registrant", doc.RegistrantName)
		set("owner", doc.RegistrantOwner)
		set("registrar", doc.Registrar)
		set("registrar_email", doc.RegistrarAccountEmail)
		set("dns", doc.DNSProvider)
		set("dns_email", doc.DNSProviderEmail)
		if doc.ExpiresAt != nil {
			set("expires", doc.ExpiresAt.UTC().Format(displayDateLayout))
		}
		if doc.AutoRenew {
			d["domain_"+n+"_autorenew"] = "1"
		}
		set("deploy", doc.Deployment.Provider)
		set("env", doc.Deployment.Environment)

		var lines []string
		for _, sub := range doc.Subdomains {
			if line := onboardingSubLine(sub); line != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) > maxOnboardingSubdomains {
			d["domain_"+n+"_subs_more"] = strconv.Itoa(len(lines) - maxOnboardingSubdomains)
			lines = lines[:maxOnboardingSubdomains]
		}
		set("subs", strings.Join(lines, "\n"))
	}

	// The site is the primary domain (the sort put it first), else the first.
	if len(ordered) > 0 {
		d["site_url"] = "https://" + ordered[0].Name
	}
	// The admin address is the first subdomain made for admin that is a full
	// host name; anything else would not be a working address.
admin:
	for _, doc := range ordered {
		for _, sub := range doc.Subdomains {
			if sub.Purpose == "admin" && strings.Contains(sub.Name, ".") {
				d["admin_url"] = "https://" + sub.Name
				break admin
			}
		}
	}
	// Contacts come from the first domain that has each.
	for _, doc := range ordered {
		if _, ok := d["tech_contact_email"]; !ok && strings.TrimSpace(doc.TechContactEmail) != "" {
			d["tech_contact_email"] = strings.TrimSpace(doc.TechContactEmail)
		}
		if _, ok := d["billing_contact_email"]; !ok && strings.TrimSpace(doc.BillingContactEmail) != "" {
			d["billing_contact_email"] = strings.TrimSpace(doc.BillingContactEmail)
		}
	}
	return d
}

// onboardingSubLine is "name · purpose · status" with empty parts dropped.
func onboardingSubLine(sub models.Subdomain) string {
	var parts []string
	for _, p := range []string{sub.Name, sub.Purpose, sub.Status} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}
