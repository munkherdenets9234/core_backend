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

type SiteContentRepo struct {
	col *mongo.Collection
}

func NewSiteContentRepo(db *mongo.Database) *SiteContentRepo {
	return &SiteContentRepo{col: db.Collection("site_content")}
}

func (r *SiteContentRepo) FindByPage(ctx context.Context, page string) (*models.SitePage, error) {
	var p models.SitePage
	if err := r.col.FindOne(ctx, bson.M{"page": page}).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// All returns every page's overrides in one query.
//
// The public site needs all of them per render — the nav and footer live in
// "common", and the page itself is a second dictionary — so this is one
// round trip rather than one per page. The whole collection is a handful of
// documents of a few kilobytes; paginating it would cost more than it saves.
func (r *SiteContentRepo) All(ctx context.Context) ([]*models.SitePage, error) {
	cur, err := r.col.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	out := []*models.SitePage{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Save replaces one page's entries.
//
// A whole-page replace rather than an entry-by-entry patch: the console
// edits a page as one form, and an entry the operator cleared has to
// disappear. A patch would leave it behind, still overriding the default it
// was meant to stop overriding.
func (r *SiteContentRepo) Save(ctx context.Context, page string, entries []models.ContentEntry, userID *primitive.ObjectID) error {
	set := bson.M{"entries": entries, "updated_at": time.Now()}
	if userID != nil {
		set["user_id"] = userID
	}
	_, err := r.col.UpdateOne(ctx,
		bson.M{"page": page},
		bson.M{"$set": set, "$setOnInsert": bson.M{"page": page}},
		options.Update().SetUpsert(true),
	)
	return err
}
