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

type TenantRepo struct {
	col *mongo.Collection
}

func NewTenantRepo(db *mongo.Database) *TenantRepo {
	return &TenantRepo{col: db.Collection("tenants")}
}

func (r *TenantRepo) Create(ctx context.Context, t *models.Tenant) error {
	now := time.Now()
	t.CreatedAt, t.UpdatedAt = now, now
	if t.Status == "" {
		t.Status = models.TenantActive
	}
	res, err := r.col.InsertOne(ctx, t)
	if err != nil {
		return err
	}
	t.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

// FindByAPIKeyHash is the hot path: every product-service request that
// mentions a tenant arrives as a key. Indexed and unique.
func (r *TenantRepo) FindByAPIKeyHash(ctx context.Context, hash string) (*models.Tenant, error) {
	var t models.Tenant
	if err := r.col.FindOne(ctx, bson.M{"api_key_hash": hash}).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TenantRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*models.Tenant, error) {
	var t models.Tenant
	if err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// FindBySlug resolves the identifier that appears in public URLs. Unique,
// indexed — the slug is how the marketing site asks for one tenant's case
// study without exposing an internal id.
func (r *TenantRepo) FindBySlug(ctx context.Context, slug string) (*models.Tenant, error) {
	var t models.Tenant
	if err := r.col.FindOne(ctx, bson.M{"slug": slug}).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TenantRepo) List(ctx context.Context, page, limit int) ([]*models.Tenant, int64, error) {
	filter := bson.M{}
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

	out := []*models.Tenant{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *TenantRepo) UpdateStatus(ctx context.Context, id primitive.ObjectID, status models.TenantStatus) error {
	return r.update(ctx, id, bson.M{"status": status})
}

func (r *TenantRepo) UpdateDomain(ctx context.Context, id primitive.ObjectID, domain string) error {
	return r.update(ctx, id, bson.M{"domain": domain})
}

// RotateAPIKey replaces the stored hash. The previous key stops working the
// moment this returns; there is deliberately no grace period, because a
// rotation is usually a response to a leak and a leaked key that keeps
// working for an hour is a leaked key.
func (r *TenantRepo) RotateAPIKey(ctx context.Context, id primitive.ObjectID, hash, last4 string) error {
	return r.update(ctx, id, bson.M{"api_key_hash": hash, "api_key_last4": last4})
}

func (r *TenantRepo) FindByDomain(ctx context.Context, domain string) (*models.Tenant, error) {
	var t models.Tenant
	if err := r.col.FindOne(ctx, bson.M{"domain": domain}).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TenantRepo) update(ctx context.Context, id primitive.ObjectID, set bson.M) error {
	set["updated_at"] = time.Now()
	res, err := r.col.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}
