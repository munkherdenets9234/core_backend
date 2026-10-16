package repository

import (
	"context"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const platformSettingsCollection = "platform_settings"

// PaymentSettingsRepo stores the single payment settings document.
type PaymentSettingsRepo struct {
	col *mongo.Collection
}

func NewPaymentSettingsRepo(db *mongo.Database) *PaymentSettingsRepo {
	return &PaymentSettingsRepo{col: db.Collection(platformSettingsCollection)}
}

// Get returns mongo.ErrNoDocuments when nothing has been saved yet.
func (r *PaymentSettingsRepo) Get(ctx context.Context) (*models.PaymentSettings, error) {
	var p models.PaymentSettings
	if err := r.col.FindOne(ctx, bson.M{"_id": models.PaymentSettingsID}).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Put replaces the document (upsert).
func (r *PaymentSettingsRepo) Put(ctx context.Context, p *models.PaymentSettings) error {
	p.ID = models.PaymentSettingsID
	_, err := r.col.ReplaceOne(ctx, bson.M{"_id": models.PaymentSettingsID}, p, options.Replace().SetUpsert(true))
	return err
}
