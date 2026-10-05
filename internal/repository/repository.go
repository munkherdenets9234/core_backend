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

	// Before creating anything: an index left by an earlier build under a
	// name now reused with different options would make CreateOne fail.
	if err := dropLegacyIndexes(ctx, mongoIndexes{db.Collection("tenants").Indexes()}); err != nil {
		return err
	}

	for _, s := range indexSpecs() {
		if _, err := db.Collection(s.collection).Indexes().CreateOne(ctx, s.model); err != nil {
			return err
		}
	}
	return nil
}

type indexSpec struct {
	collection string
	model      mongo.IndexModel
}

// siteHostsIndex is what makes a site host belong to at most one tenant.
//
// Unique and multikey (site_hosts is an array), so a second tenant claiming a
// host already in anyone else's array fails with a duplicate-key error.
// Partial on $exists so a tenant with no hosts is not indexed at all:
// without it every such tenant would share the missing-key value and the
// second tenant ever created would be refused.
//
// That makes the invariant "site_hosts is never stored as null or []": the
// model tags it omitempty and TenantRepo.UpdateHosts $unsets on an empty list.
// $exists (not $type) so an equality query on site_hosts is provably covered
// by the index. UNVERIFIED against a live MongoDB: no explain() has been run
// yet to confirm FindByHost uses this index rather than scanning.
//
// The index has an explicit name, siteHostsIndexName. Commit 280d276 created
// it under the driver's default name (site_hosts_1) with a $type: "string"
// filter; recreating that name with the $exists filter is an
// IndexOptionsConflict and EnsureIndexes would fail at boot on any database
// that ran 280d276. dropLegacyIndexes removes the old one first.
func siteHostsIndex() mongo.IndexModel {
	return mongo.IndexModel{
		Keys: bson.D{{Key: "site_hosts", Value: 1}},
		Options: options.Index().SetName(siteHostsIndexName).SetUnique(true).SetPartialFilterExpression(
			bson.M{"site_hosts": bson.M{"$exists": true}}),
	}
}

const (
	siteHostsIndexName       = "site_hosts_unique_exists"
	legacySiteHostsIndexName = "site_hosts_1"
)

// indexOps is the slice of a collection's index view dropLegacyIndexes
// needs, narrow so the migration can be tested without MongoDB.
type indexOps interface {
	Names(ctx context.Context) ([]string, error)
	Drop(ctx context.Context, name string) error
}

// dropLegacyIndexes drops the tenants index 280d276 created as site_hosts_1,
// if it is there. Only that name, and only when listed, so it is a no-op on
// every boot after the first and on a fresh database.
//
// Between the drop and the CreateOne that follows it, site_hosts is briefly
// not unique. Acceptable at boot: the service is not serving yet, and the
// create fails loudly if duplicates slipped in.
func dropLegacyIndexes(ctx context.Context, ix indexOps) error {
	names, err := ix.Names(ctx)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == legacySiteHostsIndexName {
			return ix.Drop(ctx, n)
		}
	}
	return nil
}

// mongoIndexes adapts a driver IndexView to indexOps.
type mongoIndexes struct{ v mongo.IndexView }

func (m mongoIndexes) Names(ctx context.Context) ([]string, error) {
	specs, err := m.v.ListSpecifications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out, nil
}

func (m mongoIndexes) Drop(ctx context.Context, name string) error {
	_, err := m.v.DropOne(ctx, name)
	return err
}

func indexSpecs() []indexSpec {
	return []indexSpec{
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
		{"tenants", siteHostsIndex()},

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

		// Reset lookups are by email, newest first.
		{"password_resets", mongo.IndexModel{
			Keys: bson.D{{Key: "email", Value: 1}, {Key: "created_at", Value: -1}},
		}},

		// A TTL index, so spent codes delete themselves an hour past expiry.
		//
		// Not housekeeping: a reset code is a credential, and a collection of
		// old ones is a collection of hashed credentials sitting in every
		// backup for no reason. The hour of slack is deliberate — the code
		// stops working at expires_at, and the row lingering slightly longer
		// means a replay is answered "expired" rather than "no such code",
		// which is the more useful thing to tell someone typing a stale code.
		{"password_resets", mongo.IndexModel{
			Keys:    bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(3600),
		}},
	}
}
