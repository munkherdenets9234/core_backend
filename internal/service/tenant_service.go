// Package service holds tenantcore's business rules.
//
// Everything here returns *apierr.APIError, never a raw driver error: the
// repository layer reports what the database did, this layer decides what
// that means to a caller, and the API layer only renders. A mongo error that
// reaches a handler is a bug in this package.
package service

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"unicode"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/apikey"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/net/idna"
)

type TenantService struct {
	repo *repository.TenantRepo
}

func NewTenantService(repo *repository.TenantRepo) *TenantService {
	return &TenantService{repo: repo}
}

// Create provisions a tenant and returns its API key ONCE.
//
// The raw key is returned here and never again: only its hash is stored, so
// a lost key is rotated rather than looked up. That is a deliberate trade —
// it makes "someone read the key out of the database" impossible and makes
// "I lost the key" a two-minute job instead of an impossible one.
func (s *TenantService) Create(ctx context.Context, t *models.Tenant) (*models.Tenant, string, error) {
	t.Name = strings.TrimSpace(t.Name)
	t.Slug = strings.TrimSpace(t.Slug)
	if t.Name == "" {
		return nil, "", apierr.BadRequest("name is required")
	}
	if t.Slug == "" {
		return nil, "", apierr.BadRequest("slug is required")
	}

	raw, hash, err := apikey.Generate()
	if err != nil {
		return nil, "", apierr.Internal(err)
	}
	t.APIKeyHash = hash
	t.APIKeyLast4 = apikey.Last4(raw)
	t.Status = models.TenantActive

	if err := s.repo.Create(ctx, t); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, "", apierr.Conflict("a tenant with this slug already exists").In(apierr.DomainTenant)
		}
		return nil, "", apierr.Internal(err)
	}
	return t, raw, nil
}

func (s *TenantService) List(ctx context.Context, page, limit int) ([]*models.Tenant, int64, error) {
	out, total, err := s.repo.List(ctx, page, limit)
	if err != nil {
		return nil, 0, apierr.Internal(err)
	}
	return out, total, nil
}

func (s *TenantService) GetByID(ctx context.Context, idStr string) (*models.Tenant, error) {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return nil, apierr.BadRequest("invalid tenant id")
	}
	t, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return nil, apierr.Internal(err)
	}
	return t, nil
}

func (s *TenantService) UpdateStatus(ctx context.Context, idStr string, status models.TenantStatus) error {
	switch status {
	case models.TenantActive, models.TenantSuspended:
	default:
		return apierr.BadRequest("status must be active or suspended")
	}
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid tenant id")
	}
	if err := s.repo.UpdateStatus(ctx, id, status); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	return nil
}

// UpdateDomain binds a tenant's API key to one origin. Product services
// enforce it; tenantcore only records it, because the product is the thing
// that sees the browser request.
func (s *TenantService) UpdateDomain(ctx context.Context, idStr, domain string) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid tenant id")
	}
	domain = strings.TrimSpace(strings.ToLower(domain))

	// Two tenants on one domain would make the origin check meaningless for
	// both: a key leaked from either would pass the check on the other.
	if domain != "" {
		existing, err := s.repo.FindByDomain(ctx, domain)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.Internal(err)
		}
		if err == nil && existing.ID != id {
			return apierr.Conflict("domain is already assigned to another tenant").In(apierr.DomainTenant)
		}
	}

	if err := s.repo.UpdateDomain(ctx, id, domain); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	return nil
}

// RotateAPIKey issues a new key and invalidates the old one immediately.
func (s *TenantService) RotateAPIKey(ctx context.Context, idStr string) (string, error) {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return "", apierr.BadRequest("invalid tenant id")
	}
	raw, hash, err := apikey.Generate()
	if err != nil {
		return "", apierr.Internal(err)
	}
	if err := s.repo.RotateAPIKey(ctx, id, hash, apikey.Last4(raw)); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return "", apierr.Internal(err)
	}
	return raw, nil
}

// Resolve turns a raw tenant API key into a tenant.
//
// A key that matches nothing and a key belonging to a suspended tenant get
// different answers — 401 and 403 — because the caller can act on the
// difference and an attacker learns nothing useful from it: they already
// hold the key in both cases.
func (s *TenantService) Resolve(ctx context.Context, rawKey string) (*models.Tenant, error) {
	t, err := s.repo.FindByAPIKeyHash(ctx, apikey.Hash(rawKey))
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.Unauthorized("").In(apierr.DomainTenant)
		}
		return nil, apierr.Internal(err)
	}
	if t.Status != models.TenantActive {
		return nil, apierr.Forbidden("tenant suspended").In(apierr.DomainTenant)
	}
	return t, nil
}

// hostStore is the slice of the repository setHosts needs, narrow so the
// ownership rules can be tested without MongoDB.
type hostStore interface {
	FindByHost(ctx context.Context, host string) (*models.Tenant, error)
	UpdateHosts(ctx context.Context, id primitive.ObjectID, hosts []string) error
}

// UpdateHosts sets the public site hostnames that resolve to this tenant. An
// empty list clears them. A host already owned by another tenant is a 409.
func (s *TenantService) UpdateHosts(ctx context.Context, idStr string, hosts []string) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid tenant id").In(apierr.DomainTenant)
	}
	return setHosts(ctx, s.repo, id, hosts)
}

// Bounds on what an administrator may bind. A host is a bare DNS name or IP
// literal: anything that looks like a URL, a pattern or userinfo is a mistake
// that would otherwise be stored and then never match a real Host header.
const (
	maxHosts   = 20
	maxHostLen = 253
)

func setHosts(ctx context.Context, st hostStore, id primitive.ObjectID, in []string) error {
	seen := map[string]bool{}
	hosts := make([]string, 0, len(in))
	for _, h := range in {
		// Validate the RAW value: NormalizeHost cuts at the first colon, so
		// "https://x.com/p" would otherwise be reduced to "https" and pass.
		raw := strings.TrimSpace(h)
		if strings.ContainsAny(raw, `/\*@?#`) || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
			return apierr.BadRequest("invalid host " + strconv.Quote(raw)).In(apierr.DomainTenant)
		}
		if len(raw) > maxHostLen+8 { // room for brackets and a port; checked properly below
			return apierr.BadRequest("host is longer than " + strconv.Itoa(maxHostLen) + " characters").In(apierr.DomainTenant)
		}
		if raw == "" {
			continue
		}
		var ok bool
		if h, ok = canonicalHost(raw); !ok {
			return apierr.BadRequest("invalid host " + strconv.Quote(raw)).In(apierr.DomainTenant)
		}
		if len(h) > maxHostLen {
			return apierr.BadRequest("host is longer than " + strconv.Itoa(maxHostLen) + " characters").In(apierr.DomainTenant)
		}
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	if len(hosts) > maxHosts {
		return apierr.BadRequest("at most " + strconv.Itoa(maxHosts) + " hosts per tenant").In(apierr.DomainTenant)
	}

	// Friendly pre-check so the caller is told WHICH host is taken. It is
	// racy by nature; the unique index below is what actually guarantees it.
	for _, h := range hosts {
		owner, err := st.FindByHost(ctx, h)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.Internal(err)
		}
		if err == nil && owner.ID != id {
			return apierr.Conflict("host " + h + " is already assigned to another tenant").In(apierr.DomainTenant)
		}
	}

	if err := st.UpdateHosts(ctx, id, hosts); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		if mongo.IsDuplicateKeyError(err) {
			return apierr.Conflict("a host is already assigned to another tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	return nil
}

// canonicalHost turns an administrator-entered host into the stored form, or
// reports that it is not a host at all.
//
// The stored form is exactly what models.NormalizeHost (and the realestate
// consumer's entitlement.NormalizeHost, which mirrors it) makes of the Host
// header a browser sends for that site: lowercase, no port, no trailing dot,
// IPv6 without brackets. Browsers send internationalised names as punycode,
// so a Unicode name is converted with IDNA (Lookup profile) and stored as
// ASCII; storing "münchen.example" would never match a request.
//
// Accepted: a DNS name of [a-z0-9-] labels (1-63 chars, no leading or
// trailing hyphen, not all-numeric in the last label), or an IPv4/IPv6
// literal with no zone. An optional :port must be digits. Everything else
// (unbalanced brackets, '<', ',', '%', '_', control or invisible characters)
// is refused rather than stored as a host that can never be requested.
func canonicalHost(raw string) (string, bool) {
	if strings.IndexFunc(raw, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return "", false
	}
	if !validPort(raw) {
		return "", false
	}
	bracketed := strings.HasPrefix(raw, "[")
	h := models.NormalizeHost(raw)
	if h == "" || strings.ContainsAny(h, "[]%") {
		return "", false
	}
	if ip := net.ParseIP(h); ip != nil {
		return h, true
	}
	if bracketed || strings.Contains(h, ":") {
		return "", false // brackets and bare colons are for IPv6 only
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil || a != strings.ToLower(a) {
		return "", false
	}
	labels := strings.Split(a, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	// A numeric last label is an IPv4 attempt (WHATWG URL parsing treats it
	// as one), and net.ParseIP already refused it.
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
		return "", false
	}
	return a, true
}

// validPort checks the optional ":port" on a raw host: "x.com:443" and
// "[::1]:443" pass, "x.com:" and "x.com:abc" do not. A bare IPv6 literal
// (more than one colon, no brackets) has no port.
func validPort(raw string) bool {
	var port string
	switch {
	case strings.HasPrefix(raw, "["):
		end := strings.Index(raw, "]")
		if end < 0 {
			return false
		}
		rest := raw[end+1:]
		if rest == "" {
			return true
		}
		if !strings.HasPrefix(rest, ":") {
			return false
		}
		port = rest[1:]
	case strings.Count(raw, ":") == 1:
		port = raw[strings.Index(raw, ":")+1:]
	default:
		return true
	}
	if port == "" || len(port) > 5 {
		return false
	}
	return strings.Trim(port, "0123456789") == ""
}
