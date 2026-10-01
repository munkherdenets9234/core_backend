package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ── Plans ─────────────────────────────────────────────────────────────────

type PlanService struct {
	repo *repository.PlanRepo
}

func NewPlanService(repo *repository.PlanRepo) *PlanService {
	return &PlanService{repo: repo}
}

func (s *PlanService) Create(ctx context.Context, p *models.Plan, userID *primitive.ObjectID) error {
	p.Slug = strings.TrimSpace(strings.ToLower(p.Slug))
	if p.Slug == "" {
		return apierr.BadRequest("slug is required").In(apierr.DomainPlan)
	}
	if strings.TrimSpace(p.Name) == "" {
		return apierr.BadRequest("name is required").In(apierr.DomainPlan)
	}
	if p.Price < 0 {
		return apierr.ValidationFailed("price cannot be negative").In(apierr.DomainPlan)
	}
	if err := validateModules(p.Modules); err != nil {
		return err
	}
	if err := validateLimits(p.Limits); err != nil {
		return err
	}

	if err := s.repo.Create(ctx, p, userID); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return apierr.Conflict("a plan with this slug already exists").In(apierr.DomainPlan)
		}
		return apierr.Internal(err)
	}
	return nil
}

func (s *PlanService) List(ctx context.Context, page, limit int) ([]*models.Plan, int64, error) {
	out, total, err := s.repo.List(ctx, page, limit)
	if err != nil {
		return nil, 0, apierr.Internal(err)
	}
	return out, total, nil
}

func (s *PlanService) GetByID(ctx context.Context, idStr string) (*models.Plan, error) {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return nil, apierr.BadRequest("invalid plan id").In(apierr.DomainPlan)
	}
	p, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("plan").In(apierr.DomainPlan)
		}
		return nil, apierr.Internal(err)
	}
	return p, nil
}

// Update takes a partial document. The entitlement-bearing keys are
// validated when present, because a typo in `modules` is not a cosmetic
// problem — it silently removes a product from everyone on the plan.
func (s *PlanService) Update(ctx context.Context, idStr string, update bson.M, userID *primitive.ObjectID) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid plan id").In(apierr.DomainPlan)
	}
	if raw, ok := update["modules"]; ok {
		mods, err := toStringSlice(raw)
		if err != nil {
			return apierr.ValidationFailed("modules must be a list of strings").In(apierr.DomainPlan)
		}
		if err := validateModules(mods); err != nil {
			return err
		}
	}

	if err := s.repo.Update(ctx, id, update, userID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("plan").In(apierr.DomainPlan)
		}
		return apierr.Internal(err)
	}
	return nil
}

func (s *PlanService) Delete(ctx context.Context, idStr string) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid plan id").In(apierr.DomainPlan)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("plan").In(apierr.DomainPlan)
		}
		return apierr.Internal(err)
	}
	return nil
}

// validateModules rejects blanks and duplicates.
//
// A blank module name would be granted to nobody and match nothing, and a
// duplicate is a sign the console posted a stale list. Both are cheap to
// catch here and expensive to diagnose from "the car wash routes started
// returning 402 for everyone".
func validateModules(mods []string) error {
	seen := map[string]bool{}
	for _, m := range mods {
		m = strings.TrimSpace(m)
		if m == "" {
			return apierr.ValidationFailed("module names cannot be blank").In(apierr.DomainPlan)
		}
		if seen[m] {
			return apierr.ValidationFailed("duplicate module: " + m).In(apierr.DomainPlan)
		}
		seen[m] = true
	}
	return nil
}

// validateLimits rejects negatives. Zero is allowed and meaningful — it is a
// real ceiling of none — but a negative ceiling has no interpretation, and
// entitlement.Within would silently treat it as "refuse everything".
func validateLimits(limits map[string]int) error {
	for k, v := range limits {
		if v < 0 {
			return apierr.ValidationFailed("limit " + k + " cannot be negative").In(apierr.DomainPlan)
		}
	}
	return nil
}

func toStringSlice(raw interface{}) ([]string, error) {
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("not a string")
			}
			out = append(out, s)
		}
		return out, nil
	case bson.A:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("not a string")
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, errors.New("not a list")
	}
}

// ── Subscriptions ─────────────────────────────────────────────────────────

type SubscriptionService struct {
	repo     *repository.SubscriptionRepo
	planRepo *repository.PlanRepo
}

func NewSubscriptionService(repo *repository.SubscriptionRepo, planRepo *repository.PlanRepo) *SubscriptionService {
	return &SubscriptionService{repo: repo, planRepo: planRepo}
}

// Create starts a tenant's subscription. One per tenant, enforced by a unique
// index — the duplicate key is translated here rather than leaking a driver
// error, because "this tenant already has one" is a thing the console can
// act on.
func (s *SubscriptionService) Create(ctx context.Context, tenantID, planID primitive.ObjectID, userID *primitive.ObjectID) (*models.Subscription, error) {
	plan, err := s.plan(ctx, planID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	sub := &models.Subscription{
		TenantID:           tenantID,
		PlanID:             planID,
		Status:             models.SubscriptionActive,
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 0, plan.Period()),
	}
	if err := s.repo.Create(ctx, sub, userID); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, apierr.Conflict("tenant already has a subscription")
		}
		return nil, apierr.Internal(err)
	}
	sub.Plan = plan
	return sub, nil
}

// Get returns the tenant's subscription with its plan resolved.
func (s *SubscriptionService) Get(ctx context.Context, tenantID primitive.ObjectID) (*models.Subscription, error) {
	sub, err := s.repo.FindByTenantID(ctx, tenantID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("subscription")
		}
		return nil, apierr.Internal(err)
	}
	// A deleted plan leaves Plan nil rather than failing the read: the
	// billing state is still true and still needs to be visible.
	if plan, err := s.planRepo.FindByID(ctx, sub.PlanID); err == nil {
		sub.Plan = plan
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, apierr.Internal(err)
	}
	return sub, nil
}

// UpdatePlan moves the tenant onto a different plan.
func (s *SubscriptionService) UpdatePlan(ctx context.Context, tenantID, planID primitive.ObjectID, userID *primitive.ObjectID) error {
	plan, err := s.plan(ctx, planID)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := s.repo.UpdatePlan(ctx, tenantID, planID, now, now.AddDate(0, 0, plan.Period()), userID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("subscription")
		}
		return apierr.Internal(err)
	}
	return nil
}

// Cancel marks the subscription canceled but leaves the plan and period
// intact, so access can still be reasoned about against current_period_end
// and so the record says what the tenant had rather than only that they
// stopped.
func (s *SubscriptionService) Cancel(ctx context.Context, tenantID primitive.ObjectID, userID *primitive.ObjectID) error {
	if err := s.repo.UpdateStatus(ctx, tenantID, models.SubscriptionCanceled, userID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("subscription")
		}
		return apierr.Internal(err)
	}
	return nil
}

func (s *SubscriptionService) plan(ctx context.Context, planID primitive.ObjectID) (*models.Plan, error) {
	p, err := s.planRepo.FindByID(ctx, planID)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.BadRequest("unknown plan_id").In(apierr.DomainPlan)
		}
		return nil, apierr.Internal(err)
	}
	if !p.IsActive {
		return nil, apierr.ValidationFailed("plan is not active").In(apierr.DomainPlan)
	}
	return p, nil
}

// renewedEnd is the new period end after a renewal: one period beyond
// whichever is later, today or the current end.
//
// Extending from the current end keeps the days a live tenant already paid
// for. Extending from today when the subscription has lapsed stops a renewal
// of a long-dead subscription from landing in the past and leaving the tenant
// still expired. This is what separates Renew from Change plan, which restarts
// the period from today and so throws the remaining days away.
func renewedEnd(now, currentEnd time.Time, periodDays int) time.Time {
	base := currentEnd
	if base.Before(now) {
		base = now
	}
	return base.AddDate(0, 0, periodDays)
}

// renewable reports whether a subscription may be renewed. Only a cancelled
// one may not: cancelling is a deliberate act, and renewing it quietly would
// erase the fact that the tenant was cancelled on purpose. Change plan is the
// explicit way back.
func renewable(status models.SubscriptionStatus) bool {
	return status != models.SubscriptionCanceled
}

// periodFor is the length of one renewal. A plan deleted out from under a live
// subscription, or one that never set a period, renews by the default rather
// than by zero days.
func periodFor(plan *models.Plan) int {
	if plan == nil {
		return models.DefaultPeriodDays
	}
	return plan.Period()
}

// Renew extends a subscription by one plan period, keeping the plan and the
// period start.
//
// It reads the plan through Get rather than the plan() helper because that
// helper rejects an inactive plan, and an existing subscription on a plan
// that has since been retired must still be renewable.
func (s *SubscriptionService) Renew(ctx context.Context, tenantID primitive.ObjectID, userID *primitive.ObjectID) (*models.Subscription, error) {
	sub, err := s.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !renewable(sub.Status) {
		return nil, apierr.Conflict("subscription is canceled — use Change plan to reactivate it").
			In(apierr.DomainSubscription)
	}

	newEnd := renewedEnd(time.Now(), sub.CurrentPeriodEnd, periodFor(sub.Plan))
	if err := s.repo.ExtendPeriod(ctx, tenantID, newEnd, userID); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.NotFound("subscription")
		}
		return nil, apierr.Internal(err)
	}
	return s.Get(ctx, tenantID)
}
