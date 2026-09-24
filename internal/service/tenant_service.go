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
	"strings"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/apikey"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
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
