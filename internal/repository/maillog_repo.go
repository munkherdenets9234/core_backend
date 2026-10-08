package repository

import (
	"context"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MailLogRepo stores the mail log. It satisfies mailer.Recorder, so the mailer
// can write to it without importing this package.
type MailLogRepo struct {
	col *mongo.Collection
}

func NewMailLogRepo(db *mongo.Database) *MailLogRepo {
	return &MailLogRepo{col: db.Collection(mailLogCollection)}
}

const mailLogCollection = "mail_log"

var _ mailer.Recorder = (*MailLogRepo)(nil)

// Record inserts one entry. Only the fields of mailer.Entry exist, so nothing
// else can be stored by mistake.
func (r *MailLogRepo) Record(ctx context.Context, e mailer.Entry) error {
	_, err := r.col.InsertOne(ctx, &models.MailLog{
		CreatedAt: e.At,
		Template:  e.Template,
		To:        e.To,
		Status:    e.Status,
		Error:     e.Error,
		Source:    e.Source,
		TenantID:  e.TenantID,
	})
	return err
}

// List returns one page, newest first. An empty status or template means
// "any". Callers validate both before they get here.
func (r *MailLogRepo) List(ctx context.Context, status, template string, page, limit int) ([]*models.MailLog, int64, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	if template != "" {
		filter["template"] = template
	}

	total, err := r.col.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	cur, err := r.col.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64((page-1)*limit)).
		SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	var out []*models.MailLog
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
