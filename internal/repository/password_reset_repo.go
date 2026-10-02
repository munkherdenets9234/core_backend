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

type PasswordResetRepo struct {
	col *mongo.Collection
}

func NewPasswordResetRepo(db *mongo.Database) *PasswordResetRepo {
	return &PasswordResetRepo{col: db.Collection("password_resets")}
}

// Create stores a new reset, after invalidating any outstanding one for the
// same user.
//
// Invalidating first is what makes "request a code again" behave the way
// people expect. Without it, an account with three requested codes has three
// working credentials, and the oldest — the one most likely to have been
// exposed in a forwarded email — keeps working for its full lifetime. Only
// the newest code is ever valid.
func (r *PasswordResetRepo) Create(ctx context.Context, p *models.PasswordReset) error {
	if err := r.InvalidateForUser(ctx, p.UserID); err != nil {
		return err
	}
	p.CreatedAt = time.Now()
	res, err := r.col.InsertOne(ctx, p)
	if err != nil {
		return err
	}
	p.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

// FindActiveByEmail returns the newest unused, unexpired reset for an email.
//
// Sorted newest-first rather than assuming one exists: Create invalidates
// prior codes, but a crash between the two writes would leave two, and
// silently picking an arbitrary one is how a burned code starts working
// again.
func (r *PasswordResetRepo) FindActiveByEmail(ctx context.Context, email string) (*models.PasswordReset, error) {
	var p models.PasswordReset
	err := r.col.FindOne(ctx,
		bson.M{
			"email":      email,
			"used_at":    bson.M{"$exists": false},
			"expires_at": bson.M{"$gt": time.Now()},
		},
		options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	).Decode(&p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// RecordAttempt increments the failed-guess counter.
//
// Separate from verification and always persisted, including when the guess
// is wrong — a counter that only advances on some paths is a counter an
// attacker can avoid.
func (r *PasswordResetRepo) RecordAttempt(ctx context.Context, id primitive.ObjectID) error {
	_, err := r.col.UpdateByID(ctx, id, bson.M{"$inc": bson.M{"attempts": 1}})
	return err
}

// MarkUsed consumes a reset. Conditional on it still being unused, so two
// concurrent confirms with the same code cannot both succeed — the second
// matches nothing and is reported as spent.
func (r *PasswordResetRepo) MarkUsed(ctx context.Context, id primitive.ObjectID) (bool, error) {
	now := time.Now()
	res, err := r.col.UpdateOne(ctx,
		bson.M{"_id": id, "used_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"used_at": now}},
	)
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// InvalidateForUser burns every outstanding code for a user. Called when a
// new one is issued, and again after a successful reset — a password change
// should not leave a working way to change it again.
func (r *PasswordResetRepo) InvalidateForUser(ctx context.Context, userID primitive.ObjectID) error {
	now := time.Now()
	_, err := r.col.UpdateMany(ctx,
		bson.M{"user_id": userID, "used_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"used_at": now}},
	)
	return err
}
