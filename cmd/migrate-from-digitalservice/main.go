// Command migrate-from-digitalservice copies the platform half of
// digitalservice's database into tenantcore.
//
// This is the cutover. The admin console now reads tenants, plans and
// subscriptions from tenantcore while digitalservice keeps serving the same
// tenants' showcase content, quotes and invoices — and the ONLY thing that
// keeps those two halves talking about the same customer is that a tenant has
// the SAME _id in both databases. So every document here is written with its
// original id. Nothing is generated, nothing is re-keyed.
//
// What moves:
//
//	tenants        → tenants        (verbatim, including api_key_hash)
//	platform_users → platform_users (verbatim, including password_hash)
//	packages       → plans          (the billing half only)
//	subscriptions  → subscriptions  (package_id becomes plan_id, same value)
//
// What does NOT move, and must not: tenant_details, quotes, invoices,
// tenant_packages, reviews, and everything a product sells. Those stay in
// digitalservice, which is still their owner.
//
// The packages→plans conversion drops the bilingual marketing copy (name
// maps, taglines, feature bullets, the "most popular" ribbon) and keeps the
// entitlement — modules, limits, capabilities — which digitalservice's
// Package already carries under exactly those names. The pricing card stays
// where it is; only the plan moves. Plan.name is taken from the English
// package name, falling back to the Mongolian one and then to the slug,
// because Plan.name is console-facing text and one string is all it can be.
//
// Rerunning is safe: every write is an upsert keyed by _id, and a second run
// over unchanged data is a no-op. It does not delete anything on either side.
//
// Usage:
//
//	go run ./cmd/migrate-from-digitalservice \
//	  -from "mongodb://localhost:27017" -from-db digitalservice \
//	  -to   "mongodb://localhost:27017" -to-db   tenantcore \
//	  -dry-run
//
// Run it with -dry-run first. It reports exactly what it would write.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	var (
		fromURI = flag.String("from", envOr("DIGITALSERVICE_MONGO_URI", "mongodb://localhost:27017"), "source MongoDB URI (digitalservice)")
		fromDB  = flag.String("from-db", envOr("DIGITALSERVICE_MONGO_DB", "digitalservice"), "source database name")
		toURI   = flag.String("to", envOr("MONGO_URI", "mongodb://localhost:27017"), "destination MongoDB URI (tenantcore)")
		toDB    = flag.String("to-db", envOr("MONGO_DB", "tenantcore"), "destination database name")
		dryRun  = flag.Bool("dry-run", false, "report what would be written and change nothing")
		timeout = flag.Duration("timeout", 5*time.Minute, "overall timeout")
	)
	flag.Parse()

	if *fromURI == *toURI && *fromDB == *toDB {
		log.Fatal("source and destination are the same database; refusing to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	src, err := connect(ctx, *fromURI)
	if err != nil {
		log.Fatalf("connect to source: %v", err)
	}
	defer func() { _ = src.Disconnect(context.Background()) }()

	dst, err := connect(ctx, *toURI)
	if err != nil {
		log.Fatalf("connect to destination: %v", err)
	}
	defer func() { _ = dst.Disconnect(context.Background()) }()

	m := &migration{
		from:   src.Database(*fromDB),
		to:     dst.Database(*toDB),
		dryRun: *dryRun,
	}

	if *dryRun {
		log.Printf("DRY RUN — nothing will be written")
	}
	log.Printf("%s/%s → %s/%s", *fromURI, *fromDB, *toURI, *toDB)

	// Order matters only for readability; every step is independent because
	// ids are preserved rather than remapped. Plans before subscriptions so a
	// failure part-way leaves subscriptions pointing at plans that exist.
	steps := []struct {
		name string
		run  func(context.Context) (int, error)
	}{
		{"tenants", m.tenants},
		{"platform_users", m.platformUsers},
		{"plans", m.plans},
		{"subscriptions", m.subscriptions},
	}

	for _, step := range steps {
		n, err := step.run(ctx)
		if err != nil {
			log.Fatalf("%s: %v", step.name, err)
		}
		log.Printf("%-15s %d", step.name, n)
	}

	if *dryRun {
		log.Printf("dry run complete — rerun without -dry-run to apply")
		return
	}
	log.Printf("done. Verify a tenant opens in the console before pointing anything else at tenantcore.")
}

type migration struct {
	from   *mongo.Database
	to     *mongo.Database
	dryRun bool
}

// upsert writes doc at its own _id. Upsert rather than insert so a rerun
// after a partial failure resumes instead of erroring on every document it
// already copied.
func (m *migration) upsert(ctx context.Context, col string, id primitive.ObjectID, doc bson.M) error {
	if m.dryRun {
		return nil
	}
	delete(doc, "_id")
	_, err := m.to.Collection(col).UpdateByID(ctx, id, bson.M{"$set": doc}, options.Update().SetUpsert(true))
	return err
}

func (m *migration) each(ctx context.Context, col string, fn func(bson.M, primitive.ObjectID) error) (int, error) {
	cur, err := m.from.Collection(col).Find(ctx, bson.M{})
	if err != nil {
		return 0, err
	}
	defer cur.Close(ctx)

	count := 0
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return count, err
		}
		id, ok := doc["_id"].(primitive.ObjectID)
		if !ok {
			// A document whose _id is not an ObjectID cannot keep its
			// identity across the copy, which is the one thing this migration
			// exists to preserve. Stop rather than invent one.
			return count, fmt.Errorf("%s: document with non-ObjectID _id %v", col, doc["_id"])
		}
		if err := fn(doc, id); err != nil {
			return count, err
		}
		count++
	}
	return count, cur.Err()
}

// tenants copies the tenant record verbatim. The field sets are identical by
// design — tenantcore's models.Tenant was written to match digitalservice's
// exactly so that this step is a copy and not a translation.
//
// api_key_hash comes along, which is the point: every product service already
// holding a tenant's key keeps working, and no key has to be rotated on
// cutover day.
func (m *migration) tenants(ctx context.Context) (int, error) {
	return m.each(ctx, "tenants", func(doc bson.M, id primitive.ObjectID) error {
		// `project` is never on the stored document (digitalservice resolves
		// it on read from tenant_details), but delete defensively: a stray
		// copy here would become a second source of truth for showcase
		// content, which is precisely the thing the split removes.
		delete(doc, "project")
		return m.upsert(ctx, "tenants", id, doc)
	})
}

// platformUsers copies the operator's staff, password hashes included, so
// everyone signs in to the console afterwards with the password they already
// have. Both services use bcrypt hashes in a `password_hash` field.
func (m *migration) platformUsers(ctx context.Context) (int, error) {
	return m.each(ctx, "platform_users", func(doc bson.M, id primitive.ObjectID) error {
		return m.upsert(ctx, "platform_users", id, doc)
	})
}

// plans converts a digitalservice package into a tenantcore plan, keeping the
// id so existing subscriptions still resolve.
func (m *migration) plans(ctx context.Context) (int, error) {
	return m.each(ctx, "packages", func(doc bson.M, id primitive.ObjectID) error {
		plan := bson.M{
			"slug":     str(doc["slug"]),
			"name":     planName(doc),
			"price":    num(doc["price"]),
			"currency": strOr(doc["currency"], "MNT"),
			// digitalservice hardcoded 30 days everywhere; there is no
			// per-package period to read, so every migrated plan starts
			// monthly and can be edited afterwards.
			"period_days":  models.DefaultPeriodDays,
			"modules":      doc["modules"],
			"limits":       doc["limits"],
			"capabilities": doc["capabilities"],
			"is_active":    boolOr(doc["is_active"], true),
			"sort_order":   int(num(doc["sort_order"])),
			"created_at":   doc["created_at"],
			"updated_at":   doc["updated_at"],
		}
		// Absent rather than null: tenantcore reads a missing `modules` as an
		// unenforced module gate, and a stored null would decode the same way
		// while looking like a deliberate empty list to anyone reading the
		// collection.
		for _, k := range []string{"modules", "limits", "capabilities"} {
			if plan[k] == nil {
				delete(plan, k)
			}
		}
		if plan["created_at"] == nil {
			plan["created_at"] = time.Now()
		}
		if plan["updated_at"] == nil {
			plan["updated_at"] = time.Now()
		}
		return m.upsert(ctx, "plans", id, plan)
	})
}

// subscriptions renames package_id to plan_id and keeps the value, which
// works only because plans kept their packages' ids.
func (m *migration) subscriptions(ctx context.Context) (int, error) {
	return m.each(ctx, "subscriptions", func(doc bson.M, id primitive.ObjectID) error {
		planID, ok := doc["package_id"]
		if !ok {
			return fmt.Errorf("subscription %s has no package_id", id.Hex())
		}
		sub := bson.M{
			"tenant_id":            doc["tenant_id"],
			"plan_id":              planID,
			"status":               strOr(doc["status"], string(models.SubscriptionActive)),
			"current_period_start": doc["current_period_start"],
			"current_period_end":   doc["current_period_end"],
			"created_at":           doc["created_at"],
			"updated_at":           doc["updated_at"],
		}
		if v, ok := doc["canceled_at"]; ok && v != nil {
			sub["canceled_at"] = v
		}
		if v, ok := doc["user_id"]; ok && v != nil {
			sub["user_id"] = v
		}
		return m.upsert(ctx, "subscriptions", id, sub)
	})
}

// ── helpers ───────────────────────────────────────────────────────────────

func connect(ctx context.Context, uri string) (*mongo.Client, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, errors.Join(err, client.Disconnect(context.Background()))
	}
	return client, nil
}

// planName flattens the bilingual package name into the single string a plan
// carries. English first because the console is in English; the slug is the
// last resort, and is always present.
func planName(doc bson.M) string {
	if names, ok := doc["name"].(bson.M); ok {
		for _, key := range []string{"en", "mn"} {
			if v := str(names[key]); v != "" {
				return v
			}
		}
	}
	return str(doc["slug"])
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strOr(v any, fallback string) string {
	if s := str(v); s != "" {
		return s
	}
	return fallback
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func boolOr(v any, fallback bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return fallback
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
