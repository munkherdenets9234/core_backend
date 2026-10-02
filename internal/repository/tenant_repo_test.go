package repository

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// Uniqueness of site hosts across tenants is enforced by the database, so the
// guarantee lives in the index definition. There is no MongoDB on the dev
// toolchain (see billing_repo_test.go), so the definition itself is pinned:
// unique, multikey on site_hosts, and partial so tenants with no hosts do not
// collide.
func TestHostsAreUniqueAcrossTenants(t *testing.T) {
	m := siteHostsIndex()

	if want := (bson.D{{Key: "site_hosts", Value: 1}}); !reflect.DeepEqual(m.Keys, want) {
		t.Fatalf("keys = %#v, want %#v", m.Keys, want)
	}
	if m.Options == nil || m.Options.Unique == nil || !*m.Options.Unique {
		t.Fatal("the site_hosts index must be unique, or two tenants can claim one host")
	}
	if m.Options.PartialFilterExpression == nil {
		t.Fatal("the index must be partial, or every tenant with no hosts collides")
	}
	want := bson.M{"site_hosts": bson.M{"$exists": true}}
	if !reflect.DeepEqual(m.Options.PartialFilterExpression, want) {
		t.Fatalf("partial filter = %#v, want %#v", m.Options.PartialFilterExpression, want)
	}
}

func TestSiteHostsIndexIsRegistered(t *testing.T) {
	found := false
	for _, s := range indexSpecs() {
		if s.collection == "tenants" && reflect.DeepEqual(s.model.Keys, siteHostsIndex().Keys) {
			found = true
		}
	}
	if !found {
		t.Fatal("EnsureIndexes does not create the site_hosts index")
	}
}
