package service

import (
	"context"
	"errors"
	"net"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Limits on a tenant's domain records.
const (
	MaxDomainsPerTenant    = 50
	MaxSubdomainsPerDomain = 50

	maxDomainTextLen  = 200  // names, providers, regions, ids, addresses
	maxDomainNotesLen = 2000 // free text
	maxEmailLen       = 254
	maxPaymentRefLen  = 200
)

// domainStore is the slice of the repository the service needs, narrow so the
// rules can be tested without MongoDB. Every method is scoped by tenant: a
// domain id of another tenant must come back as mongo.ErrNoDocuments.
type domainStore interface {
	ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantDomain, error)
	CountByTenant(ctx context.Context, tenantID primitive.ObjectID) (int64, error)
	Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.TenantDomain, error)
	Insert(ctx context.Context, d *models.TenantDomain) error
	Update(ctx context.Context, d *models.TenantDomain) error
	Delete(ctx context.Context, tenantID, id primitive.ObjectID) error
	ClearPrimary(ctx context.Context, tenantID, except primitive.ObjectID) error
	// NameTaken reports whether a record other than except (of any tenant) has the name.
	NameTaken(ctx context.Context, name string, except primitive.ObjectID) (bool, error)
	// SetPrimary re-marks a domain primary, to undo a demotion.
	SetPrimary(ctx context.Context, tenantID, id primitive.ObjectID) error
}

// domainFees counts the active fees that bill a domain; a nil one skips the
// check on Delete.
type domainFees interface {
	CountActiveByDomain(ctx context.Context, tenantID, domainID primitive.ObjectID) (int64, error)
}

type tenantFinder interface {
	FindByID(ctx context.Context, id primitive.ObjectID) (*models.Tenant, error)
}

// DomainInput is the client-editable part of a domain record. Identity,
// tenant and timestamps are never taken from the client.
type DomainInput struct {
	Name                  string                  `json:"name"`
	IsPrimary             bool                    `json:"is_primary"`
	RegistrantName        string                  `json:"registrant_name"`
	RegistrantOwner       string                  `json:"registrant_owner"`
	Registrar             string                  `json:"registrar"`
	RegistrarAccountEmail string                  `json:"registrar_account_email"`
	DNSProvider           string                  `json:"dns_provider"`
	DNSProviderEmail      string                  `json:"dns_provider_email"`
	ExpiresAt             *time.Time              `json:"expires_at"`
	AutoRenew             bool                    `json:"auto_renew"`
	RegistrarLock         bool                    `json:"registrar_lock"`
	Subdomains            []models.Subdomain      `json:"subdomains"`
	Deployment            models.DomainDeployment `json:"deployment"`
	DNSVerified           bool                    `json:"dns_verified"`
	DNSCheckedAt          *time.Time              `json:"dns_checked_at"`
	SSLProvider           string                  `json:"ssl_provider"`
	SSLExpiresAt          *time.Time              `json:"ssl_expires_at"`
	TechContactEmail      string                  `json:"tech_contact_email"`
	BillingContactEmail   string                  `json:"billing_contact_email"`
	Notes                 string                  `json:"notes"`
	// Payment replaces the stored payment record; omitted means empty. The
	// derived statuses are output only and ignored if sent.
	Payment models.DomainPayment `json:"payment"`
}

// DomainService manages the operator's domain records for a tenant. It is
// bookkeeping: nothing here changes Tenant.Domain, Tenant.Hosts, routing or auth.
type DomainService struct {
	repo    domainStore
	tenants tenantFinder
	fees    domainFees
	now     func() time.Time
}

func NewDomainService(repo domainStore, tenants tenantFinder, fees domainFees) *DomainService {
	return &DomainService{repo: repo, tenants: tenants, fees: fees, now: time.Now}
}

func parseTenantID(s string) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return primitive.NilObjectID, apierr.BadRequest("invalid tenant id").In(apierr.DomainTenant)
	}
	return id, nil
}

func parseDomainID(s string) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return primitive.NilObjectID, apierr.BadRequest("invalid domain id").In(apierr.DomainTenant)
	}
	return id, nil
}

func (s *DomainService) requireTenant(ctx context.Context, id primitive.ObjectID) error {
	if _, err := s.tenants.FindByID(ctx, id); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	return nil
}

func (s *DomainService) List(ctx context.Context, tenantIDStr string) ([]*models.TenantDomain, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	if err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	out, err := s.repo.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

func (s *DomainService) Get(ctx context.Context, tenantIDStr, domainIDStr string) (*models.TenantDomain, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	did, err := parseDomainID(domainIDStr)
	if err != nil {
		return nil, err
	}
	d, err := s.repo.Find(ctx, tid, did)
	if err != nil {
		return nil, notFoundOr(err)
	}
	return d, nil
}

func (s *DomainService) Create(ctx context.Context, tenantIDStr string, in DomainInput) (*models.TenantDomain, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	d, err := buildDomain(in)
	if err != nil {
		return nil, err
	}
	if err := s.requireTenant(ctx, tid); err != nil {
		return nil, err
	}
	n, err := s.repo.CountByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if n >= MaxDomainsPerTenant {
		return nil, apierr.ValidationFailed("too many domains for this tenant").In(apierr.DomainTenant)
	}
	d.TenantID = tid
	if err := s.requireNameFree(ctx, d.Name, primitive.NilObjectID); err != nil {
		return nil, err
	}
	var demoted []primitive.ObjectID
	if d.IsPrimary {
		if demoted, err = s.demotePrimaries(ctx, tid, primitive.NilObjectID); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Insert(ctx, d); err != nil {
		s.restorePrimaries(ctx, tid, demoted)
		return nil, writeErr(err)
	}
	return d, nil
}

// requireNameFree is the up-front uniqueness check, so a duplicate name never
// gets as far as demoting the current primary. The unique index still decides
// a race.
func (s *DomainService) requireNameFree(ctx context.Context, name string, except primitive.ObjectID) error {
	taken, err := s.repo.NameTaken(ctx, name, except)
	if err != nil {
		return apierr.Internal(err)
	}
	if taken {
		return errDomainTaken()
	}
	return nil
}

// demotePrimaries clears the tenant's primary flag (except one id) and returns
// the ids it cleared, for restorePrimaries.
func (s *DomainService) demotePrimaries(ctx context.Context, tid, except primitive.ObjectID) ([]primitive.ObjectID, error) {
	list, err := s.repo.ListByTenant(ctx, tid)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	var prev []primitive.ObjectID
	for _, e := range list {
		if e.IsPrimary && e.ID != except {
			prev = append(prev, e.ID)
		}
	}
	if len(prev) == 0 {
		return nil, nil
	}
	if err := s.repo.ClearPrimary(ctx, tid, except); err != nil {
		return nil, apierr.Internal(err)
	}
	return prev, nil
}

// restorePrimaries undoes demotePrimaries after a failed write, best effort:
// if someone else became primary meanwhile the unique index refuses and that
// newer primary stands.
func (s *DomainService) restorePrimaries(ctx context.Context, tid primitive.ObjectID, ids []primitive.ObjectID) {
	for _, id := range ids {
		_ = s.repo.SetPrimary(ctx, tid, id)
	}
}

func (s *DomainService) Update(ctx context.Context, tenantIDStr, domainIDStr string, in DomainInput) (*models.TenantDomain, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	did, err := parseDomainID(domainIDStr)
	if err != nil {
		return nil, err
	}
	d, err := buildDomain(in)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.Find(ctx, tid, did)
	if err != nil {
		return nil, notFoundOr(err)
	}
	d.ID, d.TenantID, d.CreatedAt = did, tid, existing.CreatedAt
	if err := s.requireNameFree(ctx, d.Name, did); err != nil {
		return nil, err
	}
	var demoted []primitive.ObjectID
	if d.IsPrimary {
		if demoted, err = s.demotePrimaries(ctx, tid, did); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Update(ctx, d); err != nil {
		s.restorePrimaries(ctx, tid, demoted)
		return nil, writeErr(err)
	}
	return d, nil
}

func (s *DomainService) Delete(ctx context.Context, tenantIDStr, domainIDStr string) error {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return err
	}
	did, err := parseDomainID(domainIDStr)
	if err != nil {
		return err
	}
	if s.fees != nil {
		n, err := s.fees.CountActiveByDomain(ctx, tid, did)
		if err != nil {
			return apierr.Internal(err)
		}
		if n > 0 {
			return apierr.Conflict("domain has an active fee; deactivate or delete the fee first").In(apierr.DomainTenant)
		}
	}
	if err := s.repo.Delete(ctx, tid, did); err != nil {
		return notFoundOr(err)
	}
	return nil
}

func notFoundOr(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return apierr.NotFound("domain").In(apierr.DomainTenant)
	}
	return apierr.Internal(err)
}

func writeErr(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return apierr.NotFound("domain").In(apierr.DomainTenant)
	}
	if mongo.IsDuplicateKeyError(err) {
		// Two primaries raced; the loser can simply try again.
		if strings.Contains(err.Error(), primaryIndexName) {
			return apierr.Conflict("primary domain changed at the same time; try again").In(apierr.DomainTenant)
		}
		return errDomainTaken()
	}
	return apierr.Internal(err)
}

// primaryIndexName is the partial unique index behind "one primary per tenant".
const primaryIndexName = "tenant_primary_unique"

func errDomainTaken() error {
	return apierr.Conflict("domain is already registered").In(apierr.DomainTenant)
}

func invalid(field string) error {
	return apierr.ValidationFailed("invalid " + field).In(apierr.DomainTenant)
}

// buildDomain validates in and returns the record to store. Errors are fixed
// strings naming only the field, never the submitted value.
func buildDomain(in DomainInput) (*models.TenantDomain, error) {
	name, ok := canonicalDomainName(in.Name)
	if !ok {
		return nil, invalid("domain name")
	}

	d := &models.TenantDomain{
		Name:          name,
		IsPrimary:     in.IsPrimary,
		AutoRenew:     in.AutoRenew,
		RegistrarLock: in.RegistrarLock,
		DNSVerified:   in.DNSVerified,
		ExpiresAt:     in.ExpiresAt,
		DNSCheckedAt:  in.DNSCheckedAt,
		SSLExpiresAt:  in.SSLExpiresAt,
		Subdomains:    []models.Subdomain{},
	}

	texts := []struct {
		field string
		in    string
		out   *string
		max   int
	}{
		{"registrant name", in.RegistrantName, &d.RegistrantName, maxDomainTextLen},
		{"registrar", in.Registrar, &d.Registrar, maxDomainTextLen},
		{"dns provider", in.DNSProvider, &d.DNSProvider, maxDomainTextLen},
		{"ssl provider", in.SSLProvider, &d.SSLProvider, maxDomainTextLen},
		{"notes", in.Notes, &d.Notes, maxDomainNotesLen},
		{"deployment provider", in.Deployment.Provider, &d.Deployment.Provider, maxDomainTextLen},
		{"deployment region", in.Deployment.Region, &d.Deployment.Region, maxDomainTextLen},
		{"deployment resource id", in.Deployment.ResourceID, &d.Deployment.ResourceID, maxDomainTextLen},
		{"deployment address", in.Deployment.Address, &d.Deployment.Address, maxDomainTextLen},
		{"deployment dns target", in.Deployment.DNSTarget, &d.Deployment.DNSTarget, maxDomainTextLen},
	}
	for _, t := range texts {
		v, ok := cleanText(t.in, t.max, t.field == "notes")
		if !ok {
			return nil, invalid(t.field)
		}
		*t.out = v
	}

	emails := []struct {
		field string
		in    string
		out   *string
	}{
		{"registrar account email", in.RegistrarAccountEmail, &d.RegistrarAccountEmail},
		{"dns provider email", in.DNSProviderEmail, &d.DNSProviderEmail},
		{"deployment provider email", in.Deployment.ProviderEmail, &d.Deployment.ProviderEmail},
		{"tech contact email", in.TechContactEmail, &d.TechContactEmail},
		{"billing contact email", in.BillingContactEmail, &d.BillingContactEmail},
	}
	for _, e := range emails {
		v, ok := cleanEmail(e.in)
		if !ok {
			return nil, invalid(e.field)
		}
		*e.out = v
	}

	pay, err := buildPayment(in.Payment)
	if err != nil {
		return nil, err
	}
	d.Payment = pay

	switch o := strings.TrimSpace(in.RegistrantOwner); o {
	case "", models.DomainOwnerUs, models.DomainOwnerClient:
		d.RegistrantOwner = o
	default:
		return nil, invalid("registrant owner")
	}
	switch e := strings.TrimSpace(in.Deployment.Environment); e {
	case "", models.EnvProd, models.EnvStaging:
		d.Deployment.Environment = e
	default:
		return nil, invalid("deployment environment")
	}

	if len(in.Subdomains) > MaxSubdomainsPerDomain {
		return nil, apierr.ValidationFailed("too many subdomains").In(apierr.DomainTenant)
	}
	seen := map[string]bool{}
	for _, sd := range in.Subdomains {
		n, ok := canonicalSubdomainName(sd.Name)
		if !ok {
			return nil, invalid("subdomain name")
		}
		if seen[n] {
			return nil, invalid("subdomain name")
		}
		seen[n] = true
		purpose := strings.TrimSpace(sd.Purpose)
		switch purpose {
		case "":
			purpose = models.SubdomainOther
		case models.SubdomainApp, models.SubdomainAPI, models.SubdomainAdmin, models.SubdomainOther:
		default:
			return nil, invalid("subdomain purpose")
		}
		target, ok := cleanText(sd.Target, maxDomainTextLen, false)
		if !ok {
			return nil, invalid("subdomain target")
		}
		status, ok := cleanText(sd.Status, 64, false)
		if !ok {
			return nil, invalid("subdomain status")
		}
		d.Subdomains = append(d.Subdomains, models.Subdomain{Name: n, Purpose: purpose, Target: target, Status: status})
	}
	return d, nil
}

// cleanPayDate normalises a payment date to UTC and keeps it sane (2000..2200).
func cleanPayDate(t *time.Time) (*time.Time, bool) {
	if t == nil {
		return nil, true
	}
	u := t.UTC()
	if u.Year() < 2000 || u.Year() > 2200 {
		return nil, false
	}
	return &u, true
}

// cleanPayMethod accepts "" or one of cash|bank|card|other.
func cleanPayMethod(m string) (string, bool) {
	switch m = strings.TrimSpace(m); m {
	case "", models.PayMethodCash, models.PayMethodBank, models.PayMethodCard, models.PayMethodOther:
		return m, true
	}
	return "", false
}

// payPart is the validated shape shared by both payment parts.
type payPart struct {
	amount      int64
	paidAt      *time.Time
	paidThrough *time.Time
	method      string
	reference   string
}

func buildPayPart(field string, amount int64, paidAt, paidThrough *time.Time, method, reference string) (payPart, error) {
	var p payPart
	if amount < 0 || amount > MaxFeeAmount {
		return p, invalid(field + " amount")
	}
	p.amount = amount
	var ok bool
	if p.paidAt, ok = cleanPayDate(paidAt); !ok {
		return p, invalid(field + " paid date")
	}
	if p.paidThrough, ok = cleanPayDate(paidThrough); !ok {
		return p, invalid(field + " paid through date")
	}
	if p.paidAt != nil && p.paidThrough != nil && dayStart(*p.paidThrough).Before(dayStart(*p.paidAt)) {
		return p, invalid(field + " paid through date")
	}
	if p.method, ok = cleanPayMethod(method); !ok {
		return p, invalid(field + " method")
	}
	if p.reference, ok = cleanText(reference, maxPaymentRefLen, false); !ok {
		return p, invalid(field + " reference")
	}
	return p, nil
}

// buildPayment validates both payment parts. Errors are fixed strings naming
// only the field.
func buildPayment(in models.DomainPayment) (models.DomainPayment, error) {
	var out models.DomainPayment
	c := in.ClientCharge
	cp, err := buildPayPart("client charge", c.Amount, c.PaidAt, c.PaidThrough, c.Method, c.Reference)
	if err != nil {
		return out, err
	}
	out.ClientCharge = models.ClientCharge{
		Amount: cp.amount, PaidAt: cp.paidAt, PaidThrough: cp.paidThrough, Method: cp.method, Reference: cp.reference,
	}
	r := in.RegistrarCost
	rp, err := buildPayPart("registrar cost", r.Amount, r.PaidAt, r.PaidThrough, r.Method, r.Reference)
	if err != nil {
		return out, err
	}
	switch p := strings.TrimSpace(r.Payer); p {
	case "", models.PayerUs, models.PayerClient:
		out.RegistrarCost.Payer = p
	default:
		return out, invalid("registrar cost payer")
	}
	out.RegistrarCost.Amount, out.RegistrarCost.PaidAt, out.RegistrarCost.PaidThrough = rp.amount, rp.paidAt, rp.paidThrough
	out.RegistrarCost.Method, out.RegistrarCost.Reference = rp.method, rp.reference
	return out, nil
}

// RegistrarPaidInput is the optional body of the registrar-paid call. Every
// field is optional; see RegistrarPaid for the defaults.
type RegistrarPaidInput struct {
	PaidAt      *time.Time `json:"paid_at"`
	PaidThrough *time.Time `json:"paid_through"`
	Payer       string     `json:"payer"`
	Method      string     `json:"method"`
	Reference   string     `json:"reference"`
}

// RegistrarPaid settles the registrar cost of one domain of the tenant. A
// domain of another tenant is a 404. Defaults: paid_at is now; paid_through
// is the domain's expires_at (today + 365 days when it has none), never
// earlier than a paid_through already recorded; payer is the recorded one,
// else "us". Method and reference are kept unless given. The amount is
// untouched (a registrar cost with amount 0 stays "none").
func (s *DomainService) RegistrarPaid(ctx context.Context, tenantIDStr, domainIDStr string, in RegistrarPaidInput) (*models.TenantDomain, error) {
	tid, err := parseTenantID(tenantIDStr)
	if err != nil {
		return nil, err
	}
	did, err := parseDomainID(domainIDStr)
	if err != nil {
		return nil, err
	}
	cur, err := s.repo.Find(ctx, tid, did)
	if err != nil {
		return nil, notFoundOr(err)
	}
	rc := cur.Payment.RegistrarCost
	explicit := models.RegistrarCost{
		Amount: rc.Amount, PaidAt: in.PaidAt, PaidThrough: in.PaidThrough, Payer: in.Payer, Method: in.Method, Reference: in.Reference,
	}
	// Validates the given values (and paid_through against paid_at when both
	// are given); defaults are filled in after.
	v, err := buildPayment(models.DomainPayment{RegistrarCost: explicit})
	if err != nil {
		return nil, err
	}
	out := v.RegistrarCost
	now := s.now().UTC()
	if out.PaidAt == nil {
		out.PaidAt = &now
	}
	if out.PaidThrough == nil {
		var through time.Time
		if cur.ExpiresAt != nil {
			through = cur.ExpiresAt.UTC()
		} else {
			through = dayStart(now).AddDate(0, 0, 365)
		}
		if rc.PaidThrough != nil && rc.PaidThrough.After(through) {
			through = rc.PaidThrough.UTC()
		}
		out.PaidThrough = &through
	}
	if out.Payer == "" {
		out.Payer = rc.Payer
		if out.Payer == "" {
			out.Payer = models.PayerUs
		}
	}
	if out.Method == "" {
		out.Method = rc.Method
	}
	if out.Reference == "" {
		out.Reference = rc.Reference
	}
	cur.Payment.RegistrarCost = out
	if err := s.repo.Update(ctx, cur); err != nil {
		return nil, writeErr(err)
	}
	return cur, nil
}

// canonicalDomainName reuses canonicalHost and then insists on a DNS name:
// no port, no IP literal.
func canonicalDomainName(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxHostLen+8 ||
		strings.ContainsAny(raw, `/\*@?#:[]`) || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", false
	}
	h, ok := canonicalHost(raw)
	if !ok || len(h) > maxHostLen || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return "", false
	}
	return h, true
}

// canonicalSubdomainName accepts a full host or a bare label ("app").
func canonicalSubdomainName(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxHostLen+8 ||
		strings.ContainsAny(raw, `/\*@?#:[]`) || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", false
	}
	h, ok := canonicalHost(raw)
	if !ok || len(h) > maxHostLen || net.ParseIP(h) != nil {
		return "", false
	}
	return h, true
}

// cleanText trims s and enforces a rune cap, no control characters (newlines
// and tabs allowed only when multiline) and no format characters (zero-width
// spaces, bidi marks, ...), which would let a value look empty or reorder
// text. Callers test for required (non-empty) values on the result, so a
// value made only of invisible characters is rejected, not stored.
func cleanText(s string, max int, multiline bool) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			return "", false
		}
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return "", false
		}
	}
	return s, true
}

// cleanEmail accepts empty or a bare address (no display name).
func cleanEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if len(s) > maxEmailLen || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || !strings.Contains(a.Address[strings.LastIndex(a.Address, "@"):], ".") {
		return "", false
	}
	return s, true
}
