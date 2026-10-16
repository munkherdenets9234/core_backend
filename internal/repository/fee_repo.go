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

const tenantFeesCollection = "tenant_fees"

// TenantFeeRepo stores tenant_fees. Every method that takes a tenant id
// filters on it, so a fee id from another tenant behaves as missing.
type TenantFeeRepo struct {
	col *mongo.Collection
}

func NewTenantFeeRepo(db *mongo.Database) *TenantFeeRepo {
	return &TenantFeeRepo{col: db.Collection(tenantFeesCollection)}
}

func (r *TenantFeeRepo) ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantFee, error) {
	cur, err := r.col.Find(ctx, bson.M{"tenant_id": tenantID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []*models.TenantFee{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *TenantFeeRepo) CountByTenant(ctx context.Context, tenantID primitive.ObjectID) (int64, error) {
	return r.col.CountDocuments(ctx, bson.M{"tenant_id": tenantID})
}

// CountActiveByDomain counts the tenant's active fees linked to the domain.
func (r *TenantFeeRepo) CountActiveByDomain(ctx context.Context, tenantID, domainID primitive.ObjectID) (int64, error) {
	return r.col.CountDocuments(ctx, bson.M{"tenant_id": tenantID, "domain_id": domainID, "active": true})
}

// Find returns mongo.ErrNoDocuments unless the fee belongs to tenantID.
func (r *TenantFeeRepo) Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.TenantFee, error) {
	var f models.TenantFee
	if err := r.col.FindOne(ctx, bson.M{"_id": id, "tenant_id": tenantID}).Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

func (r *TenantFeeRepo) Insert(ctx context.Context, f *models.TenantFee) error {
	now := time.Now().UTC()
	f.CreatedAt, f.UpdatedAt = now, now
	res, err := r.col.InsertOne(ctx, f)
	if err != nil {
		return err
	}
	f.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

// Update replaces the editable fields of a fee of f.TenantID; created_at is
// preserved. mongo.ErrNoDocuments when no such fee exists for the tenant.
func (r *TenantFeeRepo) Update(ctx context.Context, f *models.TenantFee) error {
	f.UpdatedAt = time.Now().UTC()
	set := bson.M{
		"kind":       f.Kind,
		"label":      f.Label,
		"amount":     f.Amount,
		"currency":   f.Currency,
		"interval":   f.Interval,
		"active":     f.Active,
		"updated_at": f.UpdatedAt,
	}
	unset := bson.M{}
	if f.DomainID != nil {
		set["domain_id"] = *f.DomainID
	} else {
		unset["domain_id"] = ""
	}
	if f.LastInvoicedThrough != nil {
		set["last_invoiced_through"] = *f.LastInvoicedThrough
	} else {
		unset["last_invoiced_through"] = ""
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	res, err := r.col.UpdateOne(ctx, bson.M{"_id": f.ID, "tenant_id": f.TenantID}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// Delete returns mongo.ErrNoDocuments unless the fee belongs to tenantID.
func (r *TenantFeeRepo) Delete(ctx context.Context, tenantID, id primitive.ObjectID) error {
	res, err := r.col.DeleteOne(ctx, bson.M{"_id": id, "tenant_id": tenantID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// AdvanceInvoicedThrough moves last_invoiced_through forward to through. It
// never moves it back, so replaying it is harmless, and a fee that no longer
// exists is not an error.
func (r *TenantFeeRepo) AdvanceInvoicedThrough(ctx context.Context, tenantID, id primitive.ObjectID, through time.Time) error {
	_, err := r.col.UpdateOne(ctx,
		bson.M{"_id": id, "tenant_id": tenantID, "$or": bson.A{
			bson.M{"last_invoiced_through": bson.M{"$exists": false}},
			bson.M{"last_invoiced_through": nil},
			bson.M{"last_invoiced_through": bson.M{"$lt": through}},
		}},
		bson.M{"$set": bson.M{"last_invoiced_through": through, "updated_at": time.Now().UTC()}})
	return err
}
