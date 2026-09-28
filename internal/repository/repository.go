// Package repository is the only thing in tenantcore that talks to MongoDB.
//
// Every method takes a context and returns driver errors unwrapped —
// mongo.ErrNoDocuments in particular. Translating those into API errors is
// the service layer's job, because only it knows whether a missing row is a
// 404 or a legitimate empty state.
package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureIndexes creates every index this service relies on.
//
// It is called at startup and a failure is FATAL, deliberately. These are not
// performance hints: the unique ones are the constraints that stop two
// tenants sharing an API key and two platform users sharing an email. A
// service running without them is not a degraded service, it is one quietly
// allowing states the rest of the code assumes cannot happen.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	specs := []struct {
		collection string
		model      mongo.IndexModel
	}{
		// A tenant is found by the hash of the key on every product request
		// that reaches us. Unique because two tenants sharing a key hash
		// would make "which tenant is this" ambiguous at the worst moment.
		{"tenants", mongo.IndexModel{
			Keys:    bson.D{{Key: "api_key_hash", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},
		{"tenants", mongo.IndexModel{
			Keys:    bson.D{{Key: "slug", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},

		{"platform_users", mongo.IndexModel{
			Keys:    bson.D{{Key: "email", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},

		{"service_clients", mongo.IndexModel{
			Keys:    bson.D{{Key: "key_hash", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},
		{"service_clients", mongo.IndexModel{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},

		{"plans", mongo.IndexModel{
			Keys:    bson.D{{Key: "slug", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},

		// One document per page of the marketing site.
		{"site_content", mongo.IndexModel{
			Keys:    bson.D{{Key: "page", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},

		// One case study per tenant. Unique because the console upserts by
		// tenant_id: without it, a double-submit writes a second document
		// and every later read picks whichever Mongo returns first.
		{"tenant_details", mongo.IndexModel{
			Keys:    bson.D{{Key: "tenant_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},
		// The public case-study list filters on showcase and sorts on
		// sort_order, on an unauthenticated route anyone can call.
		{"tenant_details", mongo.IndexModel{
			Keys: bson.D{{Key: "showcase", Value: 1}, {Key: "sort_order", Value: 1}},
		}},

		// A tenant shows a given pricing card once. Unique so assigning
		// twice is the same state as assigning once, which is what lets
		// TenantPlanRepo.Assign upsert instead of erroring.
		{"tenant_plans", mongo.IndexModel{
			Keys:    bson.D{{Key: "tenant_id", Value: 1}, {Key: "plan_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},

		// Leads are listed newest-first, filtered by tenant on one screen.
		// Not unique: the same person may enquire twice, and refusing the
		// second enquiry would lose a real lead.
		{"quotes", mongo.IndexModel{
			Keys: bson.D{{Key: "created_at", Value: -1}},
		}},
		{"quotes", mongo.IndexModel{
			Keys: bson.D{{Key: "tenant_id", Value: 1}, {Key: "created_at", Value: -1}},
		}},

		// One subscription per tenant. This is the constraint that encodes
		// the billing model: a tenant buys one plan covering several
		// modules, never several overlapping subscriptions.
		{"subscriptions", mongo.IndexModel{
			Keys:    bson.D{{Key: "tenant_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		}},
	}

	for _, s := range specs {
		if _, err := db.Collection(s.collection).Indexes().CreateOne(ctx, s.model); err != nil {
			return err
		}
	}
	return nil
}
