package service

import (
	"context"
	"errors"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type fakeHostStore struct {
	owner map[string]primitive.ObjectID
	saved []string
	dup   bool
}

func (f *fakeHostStore) FindByHost(_ context.Context, h string) (*models.Tenant, error) {
	id, ok := f.owner[h]
	if !ok {
		return nil, mongo.ErrNoDocuments
	}
	return &models.Tenant{ID: id}, nil
}

func (f *fakeHostStore) UpdateHosts(_ context.Context, _ primitive.ObjectID, hosts []string) error {
	if f.dup {
		return mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000}}}
	}
	f.saved = hosts
	return nil
}

func TestSetHostsNormalisesAndDedupes(t *testing.T) {
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{}}
	err := setHosts(context.Background(), st, primitive.NewObjectID(),
		[]string{"Tower.Example.com:443", "tower.example.com.", " ", "b.example.com"})
	if err != nil {
		t.Fatalf("setHosts: %v", err)
	}
	if len(st.saved) != 2 || st.saved[0] != "tower.example.com" || st.saved[1] != "b.example.com" {
		t.Fatalf("saved = %v", st.saved)
	}
}

func TestSetHostsOwnedByAnotherTenantIs409(t *testing.T) {
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{"tower.example.com": primitive.NewObjectID()}}
	err := setHosts(context.Background(), st, primitive.NewObjectID(), []string{"tower.example.com"})
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
	if st.saved != nil {
		t.Fatal("nothing should be saved on conflict")
	}
}

func TestSetHostsOwnHostIsNotAConflict(t *testing.T) {
	id := primitive.NewObjectID()
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{"tower.example.com": id}}
	if err := setHosts(context.Background(), st, id, []string{"tower.example.com"}); err != nil {
		t.Fatalf("re-saving own host: %v", err)
	}
}

// The pre-check is racy; the unique index is the real guard.
func TestSetHostsRaceLostToTheIndexIs409(t *testing.T) {
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{}, dup: true}
	err := setHosts(context.Background(), st, primitive.NewObjectID(), []string{"x.example.com"})
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}
