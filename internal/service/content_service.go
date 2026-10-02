package service

import (
	"context"
	"errors"
	"strings"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ── Case studies ──────────────────────────────────────────────────────────

// ShowcaseService owns the operator's case studies about its tenants.
//
// It reads tenants as well as details because a case study on its own is
// half a record: the name, slug and bound domain live on the tenant, and the
// public site needs all of it in one response. Joining here rather than
// making the site fetch twice keeps that decision in one place.
type ShowcaseService struct {
	details *repository.TenantDetailRepo
	tenants *repository.TenantRepo
}

func NewShowcaseService(details *repository.TenantDetailRepo, tenants *repository.TenantRepo) *ShowcaseService {
	return &ShowcaseService{details: details, tenants: tenants}
}

// Showcased pairs a case study with the tenant it is about. Both halves are
// always present — an orphaned detail whose tenant was deleted is skipped by
// List rather than returned with a nil tenant for every caller to guard.
type Showcased struct {
	Tenant *models.Tenant
	Detail *models.TenantDetail
}

// Get returns a tenant's case study for the console, which may not exist
// yet. A missing one is not an error here: the console renders an empty form
// so the operator can write the first version.
func (s *ShowcaseService) Get(ctx context.Context, tenantID primitive.ObjectID) (*models.TenantDetail, error) {
	d, err := s.details.FindByTenantID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, apierr.Internal(err)
	}
	return d, nil
}

// Save writes the case study. The tenant must exist: a detail keyed to a
// tenant that does not is unreachable by every read path here.
func (s *ShowcaseService) Save(ctx context.Context, tenantID primitive.ObjectID, set bson.M, userID *primitive.ObjectID) error {
	if _, err := s.tenants.FindByID(ctx, tenantID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	if err := s.details.Upsert(ctx, tenantID, set, userID); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// Flags reports which of these tenants have a published case study, for the
// console's tenant list. Separate from Get because a list must not make one
// round trip per row, and separate from List because the console shows every
// tenant, published or not.
func (s *ShowcaseService) Flags(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]repository.ShowcaseFlags, error) {
	out, err := s.details.FlagsByTenantIDs(ctx, ids)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

// List returns the showcased case studies for the public site.
func (s *ShowcaseService) List(ctx context.Context, limit int) ([]Showcased, error) {
	details, err := s.details.ListShowcased(ctx, limit)
	if err != nil {
		return nil, apierr.Internal(err)
	}

	out := make([]Showcased, 0, len(details))
	for _, d := range details {
		t, err := s.tenants.FindByID(ctx, d.TenantID)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				// The tenant is gone but its case study was left behind.
				// Skipping is right: there is no name or slug to render, and
				// failing the whole page over one stale row would take the
				// marketing site down for a data-tidying oversight.
				continue
			}
			return nil, apierr.Internal(err)
		}
		out = append(out, Showcased{Tenant: t, Detail: d})
	}
	return out, nil
}

// GetBySlug is the public single-case-study read. The slug identifies the
// TENANT — the case study has no slug of its own, because the public URL
// should stay the same when the copy is rewritten.
func (s *ShowcaseService) GetBySlug(ctx context.Context, slug string) (*Showcased, error) {
	t, err := s.tenants.FindBySlug(ctx, strings.TrimSpace(strings.ToLower(slug)))
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("project")
		}
		return nil, apierr.Internal(err)
	}

	d, err := s.details.FindShowcasedByTenantID(ctx, t.ID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// The tenant exists but is not showcased. Reported as "no such
			// project" rather than "forbidden": whether a tenant exists is
			// not something an anonymous caller should be able to probe.
			return nil, apierr.NotFound("project")
		}
		return nil, apierr.Internal(err)
	}
	return &Showcased{Tenant: t, Detail: d}, nil
}

// ── Quotes ────────────────────────────────────────────────────────────────

type QuoteService struct {
	repo *repository.QuoteRepo
}

func NewQuoteService(repo *repository.QuoteRepo) *QuoteService {
	return &QuoteService{repo: repo}
}

// Create records an enquiry from the public contact form.
//
// Validation is deliberately thin. This is the one write on the whole
// service that an anonymous stranger can perform, and the cost of rejecting
// a real lead over a formatting opinion is far higher than the cost of
// storing a messy one. Name and email are required because a lead with
// neither cannot be followed up; everything else is optional.
func (s *QuoteService) Create(ctx context.Context, q *models.Quote) error {
	q.Name = strings.TrimSpace(q.Name)
	q.Email = strings.TrimSpace(strings.ToLower(q.Email))

	if q.Name == "" {
		return apierr.BadRequest("name is required")
	}
	if q.Email == "" || !strings.Contains(q.Email, "@") {
		return apierr.BadRequest("a valid email is required")
	}

	// Status is never taken from the caller: a quote arriving as "closed"
	// would vanish from the console's working list before anyone saw it.
	q.Status = models.QuoteNew
	q.UserID = nil

	if err := s.repo.Create(ctx, q); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

func (s *QuoteService) List(ctx context.Context, tenantID *primitive.ObjectID, page, limit int) ([]*models.Quote, int64, error) {
	out, total, err := s.repo.List(ctx, tenantID, page, limit)
	if err != nil {
		return nil, 0, apierr.Internal(err)
	}
	return out, total, nil
}

func (s *QuoteService) UpdateStatus(ctx context.Context, idStr string, status models.QuoteStatus, userID *primitive.ObjectID) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid quote id")
	}
	if !status.Valid() {
		return apierr.ValidationFailed("status must be one of: new, contacted, quoted, closed")
	}
	if err := s.repo.UpdateStatus(ctx, id, status, userID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("quote")
		}
		return apierr.Internal(err)
	}
	return nil
}

// ── Which pricing cards a tenant shows ────────────────────────────────────

// TenantPlanService manages the many-to-many between tenants and the pricing
// cards they display.
//
// Worth restating because the names invite the opposite reading: assigning a
// plan here grants the tenant NOTHING. Entitlement comes from the
// subscription alone. This is display, and a tenant can show a card it is
// not on.
type TenantPlanService struct {
	repo    *repository.TenantPlanRepo
	plans   *repository.PlanRepo
	tenants *repository.TenantRepo
}

func NewTenantPlanService(repo *repository.TenantPlanRepo, plans *repository.PlanRepo, tenants *repository.TenantRepo) *TenantPlanService {
	return &TenantPlanService{repo: repo, plans: plans, tenants: tenants}
}

// List returns the plans a tenant displays, resolved to full documents.
//
// A plan deleted after assignment is skipped rather than failing the read —
// the same rule the entitlement assembly follows for a deleted plan, for the
// same reason: a tidy-up in the price list must not break a page.
func (s *TenantPlanService) List(ctx context.Context, tenantID primitive.ObjectID) ([]*models.Plan, error) {
	ids, err := s.repo.PlanIDs(ctx, tenantID)
	if err != nil {
		return nil, apierr.Internal(err)
	}

	out := make([]*models.Plan, 0, len(ids))
	for _, id := range ids {
		p, err := s.plans.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				continue
			}
			return nil, apierr.Internal(err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *TenantPlanService) Assign(ctx context.Context, tenantID, planID primitive.ObjectID, userID *primitive.ObjectID) error {
	if _, err := s.tenants.FindByID(ctx, tenantID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("tenant").In(apierr.DomainTenant)
		}
		return apierr.Internal(err)
	}
	if _, err := s.plans.FindByID(ctx, planID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.BadRequest("unknown plan id").In(apierr.DomainPlan)
		}
		return apierr.Internal(err)
	}
	if err := s.repo.Assign(ctx, tenantID, planID, userID); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

func (s *TenantPlanService) Unassign(ctx context.Context, tenantID, planID primitive.ObjectID) error {
	if err := s.repo.Unassign(ctx, tenantID, planID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("assignment")
		}
		return apierr.Internal(err)
	}
	return nil
}

// ── The public price list ─────────────────────────────────────────────────

// PublicPlans returns the active plans for the marketing site, in display
// order.
//
// It lives on PlanService rather than in the public controller because
// "active only, sorted" is a rule about the price list, not about HTTP.
func (s *PlanService) PublicPlans(ctx context.Context, limit int) ([]*models.Plan, error) {
	all, _, err := s.repo.List(ctx, 1, limit)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := make([]*models.Plan, 0, len(all))
	for _, p := range all {
		if p.IsActive {
			out = append(out, p)
		}
	}
	return out, nil
}
