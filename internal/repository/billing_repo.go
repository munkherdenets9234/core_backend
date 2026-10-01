package repository

import (
	"context"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ── Plans ─────────────────────────────────────────────────────────────────

type PlanRepo struct {
	col *mongo.Collection
}

func NewPlanRepo(db *mongo.Database) *PlanRepo {
	return &PlanRepo{col: db.Collection("plans")}
}

func (r *PlanRepo) Create(ctx context.Context, p *models.Plan, userID *primitive.ObjectID) error {
	now := time.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	p.UserID = userID
	res, err := r.col.InsertOne(ctx, p)
	if err != nil {
		return err
	}
	p.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *PlanRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*models.Plan, error) {
	var p models.Plan
	if err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PlanRepo) FindBySlug(ctx context.Context, slug string) (*models.Plan, error) {
	var p models.Plan
	if err := r.col.FindOne(ctx, bson.M{"slug": slug}).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PlanRepo) List(ctx context.Context, page, limit int) ([]*models.Plan, int64, error) {
	total, err := r.col.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, err
	}
	cur, err := r.col.Find(ctx, bson.M{}, options.Find().
		SetSort(bson.D{{Key: "sort_order", Value: 1}, {Key: "price", Value: 1}}).
		SetSkip(int64((page-1)*limit)).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	out := []*models.Plan{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Update takes a partial document so the console can change one field
// without resending a whole plan and racing another editor over the rest.
func (r *PlanRepo) Update(ctx context.Context, id primitive.ObjectID, set bson.M, userID *primitive.ObjectID) error {
	if userID != nil {
		set["user_id"] = userID
	}
	return updateOne(ctx, r.col, id, set)
}

// Delete removes a plan. Subscriptions pointing at it are deliberately left
// alone: a tenant does not stop paying because a price list entry was tidied
// up, and the entitlement assembly treats a missing plan as "no modules
// known" rather than as a failure. See service.EntitlementService.
func (r *PlanRepo) Delete(ctx context.Context, id primitive.ObjectID) error {
	res, err := r.col.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// CountSubscribers is what makes a plan deletion an informed decision rather
// than a surprise. The console shows it before offering the button.
func (r *PlanRepo) CountSubscribers(ctx context.Context, db *mongo.Database, planID primitive.ObjectID) (int64, error) {
	return db.Collection("subscriptions").CountDocuments(ctx, bson.M{"plan_id": planID})
}

// ── Subscriptions ─────────────────────────────────────────────────────────

type SubscriptionRepo struct {
	col *mongo.Collection
}

func NewSubscriptionRepo(db *mongo.Database) *SubscriptionRepo {
	return &SubscriptionRepo{col: db.Collection("subscriptions")}
}

func (r *SubscriptionRepo) Create(ctx context.Context, s *models.Subscription, userID *primitive.ObjectID) error {
	now := time.Now()
	s.CreatedAt, s.UpdatedAt = now, now
	s.UserID = userID
	res, err := r.col.InsertOne(ctx, s)
	if err != nil {
		return err
	}
	s.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

// FindByTenantID is the hot path: every entitlement lookup starts here.
func (r *SubscriptionRepo) FindByTenantID(ctx context.Context, tenantID primitive.ObjectID) (*models.Subscription, error) {
	var s models.Subscription
	if err := r.col.FindOne(ctx, bson.M{"tenant_id": tenantID}).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdatePlan moves a tenant onto a different plan and starts a fresh period.
//
// Starting a fresh period on every plan change is what digitalservice does
// today and it is carried over unchanged so the cutover alters no behaviour.
// It is also wrong for a mid-period upgrade — the tenant loses the remainder
// of what they paid for — and it is the first thing to fix when a payment
// provider arrives and proration becomes meaningful. Recorded here rather
// than discovered by a customer.
func (r *SubscriptionRepo) UpdatePlan(ctx context.Context, tenantID, planID primitive.ObjectID, start, end time.Time, userID *primitive.ObjectID) error {
	set := bson.M{
		"plan_id":              planID,
		"status":               models.SubscriptionActive,
		"current_period_start": start,
		"current_period_end":   end,
		"updated_at":           time.Now(),
		"canceled_at":          nil,
	}
	if userID != nil {
		set["user_id"] = userID
	}
	res, err := r.col.UpdateOne(ctx, bson.M{"tenant_id": tenantID}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

func (r *SubscriptionRepo) UpdateStatus(ctx context.Context, tenantID primitive.ObjectID, status models.SubscriptionStatus, userID *primitive.ObjectID) error {
	set := bson.M{"status": status, "updated_at": time.Now()}
	if status == models.SubscriptionCanceled {
		now := time.Now()
		set["canceled_at"] = now
	}
	if userID != nil {
		set["user_id"] = userID
	}
	res, err := r.col.UpdateOne(ctx, bson.M{"tenant_id": tenantID}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// ── Expiry notice ─────────────────────────────────────────────────────────

// expiringFilter selects subscriptions that are paying and end inside
// (after, through]. Pure so the rules can be tested without a database.
func expiringFilter(after, through time.Time) bson.M {
	return bson.M{
		"status": bson.M{"$in": []models.SubscriptionStatus{
			models.SubscriptionActive, models.SubscriptionTrialing,
		}},
		"current_period_end": bson.M{"$gt": after, "$lte": through},
	}
}

// claimFilter matches a subscription only while it still ends at periodEnd and
// has not already been marked for it. See ClaimExpiryNotice.
func claimFilter(id primitive.ObjectID, periodEnd time.Time) bson.M {
	return bson.M{
		"_id":                id,
		"current_period_end": periodEnd,
		"expiry_notice_for":  bson.M{"$ne": periodEnd},
	}
}

// releaseFilter matches only the marker this caller set.
func releaseFilter(id primitive.ObjectID, periodEnd time.Time) bson.M {
	return bson.M{"_id": id, "expiry_notice_for": periodEnd}
}

// FindExpiring returns paying subscriptions whose period ends after `after`
// and no later than `through`.
func (r *SubscriptionRepo) FindExpiring(ctx context.Context, after, through time.Time) ([]*models.Subscription, error) {
	cur, err := r.col.Find(ctx, expiringFilter(after, through))
	if err != nil {
		return nil, err
	}
	var out []*models.Subscription
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimExpiryNotice atomically records that a warning is being sent for
// periodEnd, and reports whether THIS caller won the claim.
//
// Claiming before sending is what keeps a restart or a second replica from
// double-sending: the conditional update succeeds for exactly one of them.
// Requiring current_period_end to still equal periodEnd means a renewal that
// lands between the find and the claim fails the claim rather than marking
// the new period as already warned.
func (r *SubscriptionRepo) ClaimExpiryNotice(ctx context.Context, id primitive.ObjectID, periodEnd time.Time) (bool, error) {
	res, err := r.col.UpdateOne(ctx, claimFilter(id, periodEnd),
		bson.M{"$set": bson.M{"expiry_notice_for": periodEnd}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// ReleaseExpiryNotice clears a claim after a failed send, so the next tick
// retries. It only clears the marker for periodEnd, never a newer one.
func (r *SubscriptionRepo) ReleaseExpiryNotice(ctx context.Context, id primitive.ObjectID, periodEnd time.Time) error {
	_, err := r.col.UpdateOne(ctx, releaseFilter(id, periodEnd),
		bson.M{"$unset": bson.M{"expiry_notice_for": ""}})
	return err
}

// ExtendPeriod moves a subscription's period end and marks it active. Unlike
// UpdatePlan it keeps the period start and the plan, which is the whole point
// of renewing rather than changing plan.
func (r *SubscriptionRepo) ExtendPeriod(ctx context.Context, tenantID primitive.ObjectID, newEnd time.Time, userID *primitive.ObjectID) error {
	set := bson.M{
		"current_period_end": newEnd,
		"status":             models.SubscriptionActive,
		"updated_at":         time.Now(),
	}
	if userID != nil {
		set["user_id"] = userID
	}
	res, err := r.col.UpdateOne(ctx, bson.M{"tenant_id": tenantID}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}
