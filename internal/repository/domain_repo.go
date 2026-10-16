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

const tenantDomainsCollection = "tenant_domains"

// TenantDomainRepo stores tenant_domains. Every method that takes a tenant id
// filters on it, so a domain id from another tenant behaves as missing.
type TenantDomainRepo struct {
	col *mongo.Collection
}

func NewTenantDomainRepo(db *mongo.Database) *TenantDomainRepo {
	return &TenantDomainRepo{col: db.Collection(tenantDomainsCollection)}
}

func (r *TenantDomainRepo) ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.TenantDomain, error) {
	cur, err := r.col.Find(ctx, bson.M{"tenant_id": tenantID},
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []*models.TenantDomain{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *TenantDomainRepo) CountByTenant(ctx context.Context, tenantID primitive.ObjectID) (int64, error) {
	return r.col.CountDocuments(ctx, bson.M{"tenant_id": tenantID})
}

// Find returns mongo.ErrNoDocuments unless the domain belongs to tenantID.
func (r *TenantDomainRepo) Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.TenantDomain, error) {
	var d models.TenantDomain
	if err := r.col.FindOne(ctx, bson.M{"_id": id, "tenant_id": tenantID}).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *TenantDomainRepo) Insert(ctx context.Context, d *models.TenantDomain) error {
	now := time.Now().UTC()
	d.CreatedAt, d.UpdatedAt = now, now
	res, err := r.col.InsertOne(ctx, d)
	if err != nil {
		return err
	}
	d.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

// Update replaces the editable fields of a domain of d.TenantID; created_at is
// preserved. mongo.ErrNoDocuments when no such domain exists for the tenant.
func (r *TenantDomainRepo) Update(ctx context.Context, d *models.TenantDomain) error {
	d.UpdatedAt = time.Now().UTC()
	res, err := r.col.UpdateOne(ctx, bson.M{"_id": d.ID, "tenant_id": d.TenantID}, bson.M{"$set": bson.M{
		"name":                    d.Name,
		"is_primary":              d.IsPrimary,
		"registrant_name":         d.RegistrantName,
		"registrant_owner":        d.RegistrantOwner,
		"registrar":               d.Registrar,
		"registrar_account_email": d.RegistrarAccountEmail,
		"dns_provider":            d.DNSProvider,
		"dns_provider_email":      d.DNSProviderEmail,
		"expires_at":              d.ExpiresAt,
		"auto_renew":              d.AutoRenew,
		"registrar_lock":          d.RegistrarLock,
		"subdomains":              d.Subdomains,
		"deployment":              d.Deployment,
		"dns_verified":            d.DNSVerified,
		"dns_checked_at":          d.DNSCheckedAt,
		"ssl_provider":            d.SSLProvider,
		"ssl_expires_at":          d.SSLExpiresAt,
		"tech_contact_email":      d.TechContactEmail,
		"billing_contact_email":   d.BillingContactEmail,
		"notes":                   d.Notes,
		"payment":                 d.Payment,
		"updated_at":              d.UpdatedAt,
	}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// MarkClientPaid records a client payment on a domain of the tenant: paid_at
// and paid_through are set only when paid_through moves forward (unset or
// earlier than through), so replays and out-of-order calls are harmless and
// the paid period never shrinks. Method and reference are set in the same
// update, only when given (non-empty), so an empty value never erases one. A
// missing domain or one already covered is not an error.
func (r *TenantDomainRepo) MarkClientPaid(ctx context.Context, tenantID, id primitive.ObjectID, paidAt, through time.Time, method, reference string) error {
	const field = "payment.client_charge.paid_through"
	set := bson.M{
		"payment.client_charge.paid_at": paidAt,
		field:                           through,
		"updated_at":                    time.Now().UTC(),
	}
	if method != "" {
		set["payment.client_charge.method"] = method
	}
	if reference != "" {
		set["payment.client_charge.reference"] = reference
	}
	_, err := r.col.UpdateOne(ctx, bson.M{
		"_id": id, "tenant_id": tenantID,
		"$or": bson.A{bson.M{field: nil}, bson.M{field: bson.M{"$lt": through}}},
	}, bson.M{"$set": set})
	return err
}

// Delete returns mongo.ErrNoDocuments unless the domain belongs to tenantID.
func (r *TenantDomainRepo) Delete(ctx context.Context, tenantID, id primitive.ObjectID) error {
	res, err := r.col.DeleteOne(ctx, bson.M{"_id": id, "tenant_id": tenantID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// NameTaken reports whether any record, of any tenant, other than except
// already has the (canonical) name.
func (r *TenantDomainRepo) NameTaken(ctx context.Context, name string, except primitive.ObjectID) (bool, error) {
	n, err := r.col.CountDocuments(ctx, bson.M{"name": name, "_id": bson.M{"$ne": except}}, options.Count().SetLimit(1))
	return n > 0, err
}

// SetPrimary marks one domain of the tenant primary again (used to undo a
// demotion when the write that needed it failed). The unique primary index
// makes it fail if another primary has appeared meanwhile.
func (r *TenantDomainRepo) SetPrimary(ctx context.Context, tenantID, id primitive.ObjectID) error {
	_, err := r.col.UpdateOne(ctx, bson.M{"_id": id, "tenant_id": tenantID},
		bson.M{"$set": bson.M{"is_primary": true, "updated_at": time.Now().UTC()}})
	return err
}

// ClearPrimary demotes every primary domain of the tenant other than except.
func (r *TenantDomainRepo) ClearPrimary(ctx context.Context, tenantID, except primitive.ObjectID) error {
	_, err := r.col.UpdateMany(ctx,
		bson.M{"tenant_id": tenantID, "is_primary": true, "_id": bson.M{"$ne": except}},
		bson.M{"$set": bson.M{"is_primary": false, "updated_at": time.Now().UTC()}})
	return err
}
