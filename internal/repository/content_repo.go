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

// ── Tenant case studies ───────────────────────────────────────────────────

type TenantDetailRepo struct {
	col *mongo.Collection
}

func NewTenantDetailRepo(db *mongo.Database) *TenantDetailRepo {
	return &TenantDetailRepo{col: db.Collection("tenant_details")}
}

func (r *TenantDetailRepo) FindByTenantID(ctx context.Context, tenantID primitive.ObjectID) (*models.TenantDetail, error) {
	var d models.TenantDetail
	if err := r.col.FindOne(ctx, bson.M{"tenant_id": tenantID}).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Upsert writes the case study for a tenant, creating it if this is the
// first edit.
//
// Upsert rather than create-then-update because the console has no "create
// case study" step: an operator opens a tenant's showcase form and saves.
// Making the first save a different operation from the second would put that
// distinction in the UI for no reason a user could name.
func (r *TenantDetailRepo) Upsert(ctx context.Context, tenantID primitive.ObjectID, set bson.M, userID *primitive.ObjectID) error {
	now := time.Now()
	set["updated_at"] = now
	if userID != nil {
		set["user_id"] = userID
	}
	_, err := r.col.UpdateOne(ctx,
		bson.M{"tenant_id": tenantID},
		bson.M{
			"$set": set,
			// tenant_id and created_at are set only when the document is
			// first inserted; a later save must not rewrite either.
			"$setOnInsert": bson.M{"tenant_id": tenantID, "created_at": now},
		},
		options.Update().SetUpsert(true),
	)
	return err
}

// ListShowcased returns the case studies the public site may display, in the
// order it should display them.
//
// The showcase filter lives here rather than in the caller because this is
// the query the public surface runs, and a filter that can be forgotten at a
// call site is a filter that will be.
func (r *TenantDetailRepo) ListShowcased(ctx context.Context, limit int) ([]*models.TenantDetail, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "sort_order", Value: 1}, {Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))

	cur, err := r.col.Find(ctx, bson.M{"showcase": true}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	out := []*models.TenantDetail{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FindShowcasedByTenantID is the single-case-study read. It repeats the
// showcase filter for the same reason: an unpublished case study must 404 on
// the public surface, not render because someone knew the slug.
func (r *TenantDetailRepo) FindShowcasedByTenantID(ctx context.Context, tenantID primitive.ObjectID) (*models.TenantDetail, error) {
	var d models.TenantDetail
	err := r.col.FindOne(ctx, bson.M{"tenant_id": tenantID, "showcase": true}).Decode(&d)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ShowcaseFlags is the publication state of one case study — the part the
// console's tenant LIST needs, as opposed to the whole document the editor
// needs.
type ShowcaseFlags struct {
	TenantID primitive.ObjectID `bson:"tenant_id"`
	Showcase bool               `bson:"showcase"`
	Featured bool               `bson:"featured"`
}

// FlagsByTenantIDs answers "is this published?" for a page of tenants in one
// query rather than one per row. A tenant with no case study is simply
// absent from the result, which the caller reads as "nothing published".
func (r *TenantDetailRepo) FlagsByTenantIDs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]ShowcaseFlags, error) {
	out := map[primitive.ObjectID]ShowcaseFlags{}
	if len(ids) == 0 {
		return out, nil
	}

	cur, err := r.col.Find(ctx,
		bson.M{"tenant_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"tenant_id": 1, "showcase": 1, "featured": 1}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	for cur.Next(ctx) {
		var f ShowcaseFlags
		if err := cur.Decode(&f); err != nil {
			return nil, err
		}
		out[f.TenantID] = f
	}
	return out, cur.Err()
}

// ── Quotes ────────────────────────────────────────────────────────────────

type QuoteRepo struct {
	col *mongo.Collection
}

func NewQuoteRepo(db *mongo.Database) *QuoteRepo {
	return &QuoteRepo{col: db.Collection("quotes")}
}

func (r *QuoteRepo) Create(ctx context.Context, q *models.Quote) error {
	now := time.Now()
	q.CreatedAt, q.UpdatedAt = now, now
	if q.Status == "" {
		q.Status = models.QuoteNew
	}
	res, err := r.col.InsertOne(ctx, q)
	if err != nil {
		return err
	}
	q.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

// List returns one page of quotes, newest first. A nil tenantID lists every
// quote on the platform, which is the console's default view — most quotes
// belong to no tenant at all.
func (r *QuoteRepo) List(ctx context.Context, tenantID *primitive.ObjectID, page, limit int) ([]*models.Quote, int64, error) {
	filter := bson.M{}
	if tenantID != nil {
		filter["tenant_id"] = *tenantID
	}

	total, err := r.col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64((page - 1) * limit)).
		SetLimit(int64(limit))

	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	out := []*models.Quote{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *QuoteRepo) UpdateStatus(ctx context.Context, id primitive.ObjectID, status models.QuoteStatus, userID *primitive.ObjectID) error {
	set := bson.M{"status": status}
	if userID != nil {
		set["user_id"] = userID
	}
	return updateOne(ctx, r.col, id, set)
}

// ── Which pricing cards a tenant shows ────────────────────────────────────

type TenantPlanRepo struct {
	col *mongo.Collection
}

func NewTenantPlanRepo(db *mongo.Database) *TenantPlanRepo {
	return &TenantPlanRepo{col: db.Collection("tenant_plans")}
}

// Assign is idempotent: assigning a plan twice is the same state as
// assigning it once, so it upserts rather than erroring on the unique index.
// A console that double-submits should not have to explain a conflict.
func (r *TenantPlanRepo) Assign(ctx context.Context, tenantID, planID primitive.ObjectID, userID *primitive.ObjectID) error {
	setOnInsert := bson.M{"created_at": time.Now()}
	if userID != nil {
		setOnInsert["user_id"] = userID
	}
	_, err := r.col.UpdateOne(ctx,
		bson.M{"tenant_id": tenantID, "plan_id": planID},
		bson.M{"$setOnInsert": setOnInsert},
		options.Update().SetUpsert(true),
	)
	return err
}

func (r *TenantPlanRepo) Unassign(ctx context.Context, tenantID, planID primitive.ObjectID) error {
	res, err := r.col.DeleteOne(ctx, bson.M{"tenant_id": tenantID, "plan_id": planID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// PlanIDs returns which plans a tenant displays. Ids only: the caller reads
// the plans themselves through PlanRepo, so there is one place that knows
// what a plan looks like.
func (r *TenantPlanRepo) PlanIDs(ctx context.Context, tenantID primitive.ObjectID) ([]primitive.ObjectID, error) {
	cur, err := r.col.Find(ctx, bson.M{"tenant_id": tenantID})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	out := []primitive.ObjectID{}
	for cur.Next(ctx) {
		var tp models.TenantPlan
		if err := cur.Decode(&tp); err != nil {
			return nil, err
		}
		out = append(out, tp.PlanID)
	}
	return out, cur.Err()
}
