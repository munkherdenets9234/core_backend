package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeIndexes records what dropLegacyIndexes asks of a collection.
type fakeIndexes struct {
	names   []string
	dropped []string
	listErr error
	dropErr error
}

func (f *fakeIndexes) Names(context.Context) ([]string, error) { return f.names, f.listErr }

func (f *fakeIndexes) Drop(_ context.Context, name string) error {
	if f.dropErr != nil {
		return f.dropErr
	}
	f.dropped = append(f.dropped, name)
	return nil
}

// The site_hosts index was first created by 280d276 under the driver's
// default name with a $type filter. Recreating it with a $exists filter under
// the same name is an IndexOptionsConflict at boot, so the new index has its
// own name and the old one is dropped first.
func TestSiteHostsIndexHasExplicitName(t *testing.T) {
	m := siteHostsIndex()
	if m.Options == nil || m.Options.Name == nil || *m.Options.Name != siteHostsIndexName {
		t.Fatalf("site_hosts index must be named %q", siteHostsIndexName)
	}
	if siteHostsIndexName == legacySiteHostsIndexName {
		t.Fatal("the new name must differ from the legacy default name")
	}
}

func TestDropLegacyIndexesDropsOnlyTheOldSiteHostsIndex(t *testing.T) {
	f := &fakeIndexes{names: []string{"_id_", "api_key_hash_1", "slug_1", legacySiteHostsIndexName, siteHostsIndexName}}
	if err := dropLegacyIndexes(context.Background(), f); err != nil {
		t.Fatalf("dropLegacyIndexes: %v", err)
	}
	if want := []string{legacySiteHostsIndexName}; !reflect.DeepEqual(f.dropped, want) {
		t.Fatalf("dropped = %v, want %v", f.dropped, want)
	}
}

func TestDropLegacyIndexesIsIdempotent(t *testing.T) {
	f := &fakeIndexes{names: []string{"_id_", "slug_1", siteHostsIndexName}}
	if err := dropLegacyIndexes(context.Background(), f); err != nil {
		t.Fatalf("dropLegacyIndexes: %v", err)
	}
	if len(f.dropped) != 0 {
		t.Fatalf("nothing should be dropped when the legacy index is absent, dropped %v", f.dropped)
	}
}

func TestDropLegacyIndexesReportsErrors(t *testing.T) {
	boom := errors.New("boom")
	if err := dropLegacyIndexes(context.Background(), &fakeIndexes{listErr: boom}); !errors.Is(err, boom) {
		t.Fatalf("list error = %v, want boom", err)
	}
	f := &fakeIndexes{names: []string{legacySiteHostsIndexName}, dropErr: boom}
	if err := dropLegacyIndexes(context.Background(), f); !errors.Is(err, boom) {
		t.Fatalf("drop error = %v, want boom", err)
	}
}
