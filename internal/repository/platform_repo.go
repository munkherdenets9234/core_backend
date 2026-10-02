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

// ── Platform users ────────────────────────────────────────────────────────

type PlatformUserRepo struct {
	col *mongo.Collection
}

func NewPlatformUserRepo(db *mongo.Database) *PlatformUserRepo {
	return &PlatformUserRepo{col: db.Collection("platform_users")}
}

func (r *PlatformUserRepo) Create(ctx context.Context, u *models.PlatformUser) error {
	now := time.Now()
	u.CreatedAt, u.UpdatedAt = now, now
	if u.Status == "" {
		u.Status = models.PlatformUserActive
	}
	res, err := r.col.InsertOne(ctx, u)
	if err != nil {
		return err
	}
	u.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *PlatformUserRepo) FindByEmail(ctx context.Context, email string) (*models.PlatformUser, error) {
	var u models.PlatformUser
	if err := r.col.FindOne(ctx, bson.M{"email": email}).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *PlatformUserRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*models.PlatformUser, error) {
	var u models.PlatformUser
	if err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *PlatformUserRepo) List(ctx context.Context, page, limit int) ([]*models.PlatformUser, int64, error) {
	total, err := r.col.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, err
	}
	cur, err := r.col.Find(ctx, bson.M{}, options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64((page-1)*limit)).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)

	out := []*models.PlatformUser{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// CountActive backs the rule that the last active platform user cannot be
// suspended. Locking every administrator out of the service that administers
// every tenant is not a recoverable mistake through the API.
func (r *PlatformUserRepo) CountActive(ctx context.Context) (int64, error) {
	return r.col.CountDocuments(ctx, bson.M{"status": models.PlatformUserActive})
}

func (r *PlatformUserRepo) UpdateStatus(ctx context.Context, id primitive.ObjectID, status models.PlatformUserStatus) error {
	return updateOne(ctx, r.col, id, bson.M{"status": status})
}

func (r *PlatformUserRepo) UpdatePassword(ctx context.Context, id primitive.ObjectID, hash string) error {
	return updateOne(ctx, r.col, id, bson.M{"password_hash": hash})
}

// ── Service clients ───────────────────────────────────────────────────────

type ServiceClientRepo struct {
	col *mongo.Collection
}

func NewServiceClientRepo(db *mongo.Database) *ServiceClientRepo {
	return &ServiceClientRepo{col: db.Collection("service_clients")}
}

func (r *ServiceClientRepo) Create(ctx context.Context, c *models.ServiceClient) error {
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Status == "" {
		c.Status = models.ServiceClientActive
	}
	res, err := r.col.InsertOne(ctx, c)
	if err != nil {
		return err
	}
	c.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *ServiceClientRepo) FindByKeyHash(ctx context.Context, hash string) (*models.ServiceClient, error) {
	var c models.ServiceClient
	if err := r.col.FindOne(ctx, bson.M{"key_hash": hash}).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ServiceClientRepo) List(ctx context.Context) ([]*models.ServiceClient, error) {
	cur, err := r.col.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	out := []*models.ServiceClient{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ServiceClientRepo) UpdateStatus(ctx context.Context, id primitive.ObjectID, status models.ServiceClientStatus) error {
	return updateOne(ctx, r.col, id, bson.M{"status": status})
}

// FindByID returns a service client of any status.
func (r *ServiceClientRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*models.ServiceClient, error) {
	var c models.ServiceClient
	if err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ReplaceKey swaps the key on an active client in one write and returns the
// updated record from that same write, so there is no second read that could
// fail after the old key is already dead. The status is in the filter so a
// revoked client cannot be revived by rotation; no match (missing or revoked)
// is mongo.ErrNoDocuments and the old key is untouched.
func (r *ServiceClientRepo) ReplaceKey(ctx context.Context, id primitive.ObjectID, keyHash, keyLast4 string) (*models.ServiceClient, error) {
	var c models.ServiceClient
	err := r.col.FindOneAndUpdate(ctx,
		bson.M{"_id": id, "status": models.ServiceClientActive},
		bson.M{"$set": bson.M{"key_hash": keyHash, "key_last4": keyLast4, "updated_at": time.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// TouchLastSeen records that this service authenticated, best-effort.
//
// Deliberately fire-and-forget at the caller: it runs on the hot path of
// every entitlement lookup, and a write failure here must never turn a
// successful authentication into a failed request. Its value is operational
// — noticing a service that is still calling after you thought it was
// retired, or one that stopped calling and nobody noticed.
func (r *ServiceClientRepo) TouchLastSeen(ctx context.Context, id primitive.ObjectID) error {
	now := time.Now()
	_, err := r.col.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"last_seen_at": now}})
	return err
}

// updateOne is the shared write path: it stamps updated_at and turns "no rows
// matched" into ErrNoDocuments, so a caller cannot mistake an update of a
// row that does not exist for a successful one.
func updateOne(ctx context.Context, col *mongo.Collection, id primitive.ObjectID, set bson.M) error {
	set["updated_at"] = time.Now()
	res, err := col.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}
