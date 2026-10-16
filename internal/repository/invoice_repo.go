package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	invoicesCollection = "invoices"
	countersCollection = "counters"
)

// InvoiceRepo stores invoices. Every method that takes a tenant id filters on
// it, so an invoice id from another tenant behaves as missing.
type InvoiceRepo struct {
	col      *mongo.Collection
	counters *mongo.Collection
}

func NewInvoiceRepo(db *mongo.Database) *InvoiceRepo {
	return &InvoiceRepo{col: db.Collection(invoicesCollection), counters: db.Collection(countersCollection)}
}

// NextSeq returns the next invoice sequence number of the month (year and
// month are those of the issue time, UTC). The counter is
// advanced by one atomic findOneAndUpdate with $inc and upsert, so concurrent
// callers always get distinct numbers. A number taken by a caller that then
// fails to insert is skipped, never reused. Invoice numbers are therefore
// unique and increasing but may have gaps (a rejected insert, such as a
// period-key conflict, burns its number).
func (r *InvoiceRepo) NextSeq(ctx context.Context, year, month int) (int64, error) {
	var doc struct {
		Seq int64 `bson:"seq"`
	}
	err := r.counters.FindOneAndUpdate(ctx,
		bson.M{"_id": fmt.Sprintf("invoice:%04d-%02d", year, month)},
		bson.M{"$inc": bson.M{"seq": int64(1)}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&doc)
	if err != nil {
		return 0, err
	}
	return doc.Seq, nil
}

func (r *InvoiceRepo) Insert(ctx context.Context, inv *models.Invoice) error {
	now := time.Now().UTC()
	inv.CreatedAt, inv.UpdatedAt = now, now
	res, err := r.col.InsertOne(ctx, inv)
	if err != nil {
		return err
	}
	inv.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *InvoiceRepo) ListByTenant(ctx context.Context, tenantID primitive.ObjectID) ([]*models.Invoice, error) {
	cur, err := r.col.Find(ctx, bson.M{"tenant_id": tenantID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	out := []*models.Invoice{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *InvoiceRepo) CountByTenant(ctx context.Context, tenantID primitive.ObjectID) (int64, error) {
	return r.col.CountDocuments(ctx, bson.M{"tenant_id": tenantID})
}

// Find returns mongo.ErrNoDocuments unless the invoice belongs to tenantID.
func (r *InvoiceRepo) Find(ctx context.Context, tenantID, id primitive.ObjectID) (*models.Invoice, error) {
	var inv models.Invoice
	if err := r.col.FindOne(ctx, bson.M{"_id": id, "tenant_id": tenantID}).Decode(&inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

// LineHolder returns the number of a non-void invoice of the tenant that
// already bills the given kind and period (and fee, when feeID is not nil; else
// the domain, when domainID is not nil: a derived domain line has no fee), or
// "" when there is none. The filter carries the tenant id, so the number is
// always the tenant's own.
func (r *InvoiceRepo) LineHolder(ctx context.Context, tenantID primitive.ObjectID, kind string, feeID, domainID *primitive.ObjectID, period string) (string, error) {
	match := bson.M{"kind": kind, "period": period}
	if feeID != nil {
		match["fee_id"] = *feeID
	} else if domainID != nil {
		match["domain_id"] = *domainID
	}
	var doc struct {
		Number string `bson:"number"`
	}
	err := r.col.FindOne(ctx, bson.M{
		"tenant_id": tenantID,
		"status":    bson.M{"$ne": models.InvoiceVoid},
		"lines":     bson.M{"$elemMatch": match},
	}, options.FindOne().SetProjection(bson.M{"number": 1})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return doc.Number, nil
}

// ListLive returns the tenant's non-void invoices (number and lines only).
func (r *InvoiceRepo) ListLive(ctx context.Context, tenantID primitive.ObjectID) ([]*models.Invoice, error) {
	cur, err := r.col.Find(ctx, bson.M{"tenant_id": tenantID, "status": bson.M{"$ne": models.InvoiceVoid}},
		options.Find().SetProjection(bson.M{"number": 1, "tenant_id": 1, "lines": 1}))
	if err != nil {
		return nil, err
	}
	out := []*models.Invoice{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteDraft deletes the invoice only while it is a draft of tenantID, in one
// conditional delete (a claimed, sent, paid or void invoice stays). Its period
// keys go with it, freeing the periods. mongo.ErrNoDocuments when nothing
// matched.
func (r *InvoiceRepo) DeleteDraft(ctx context.Context, tenantID, id primitive.ObjectID) error {
	res, err := r.col.DeleteOne(ctx, bson.M{"_id": id, "tenant_id": tenantID, "status": models.InvoiceDraft})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// Transition sets the given fields on the invoice only while its status is
// one of from, in a single conditional update, and returns the updated
// invoice. It returns mongo.ErrNoDocuments when the invoice is missing for the
// tenant or no longer in an allowed status; the caller tells which by reading
// it first.
func (r *InvoiceRepo) Transition(ctx context.Context, tenantID, id primitive.ObjectID, from []string, set bson.M) (*models.Invoice, error) {
	set["updated_at"] = time.Now().UTC()
	var inv models.Invoice
	err := r.col.FindOneAndUpdate(ctx,
		bson.M{"_id": id, "tenant_id": tenantID, "status": bson.M{"$in": from}},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&inv)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}
