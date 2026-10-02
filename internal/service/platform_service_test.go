package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/apikey"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// fakeClientStore is an in-memory serviceClientStore. ReplaceKey mirrors the
// real repo: it matches only an active record, otherwise ErrNoDocuments.
type fakeClientStore struct {
	mu         sync.Mutex
	byID       map[primitive.ObjectID]*models.ServiceClient
	replaceErr error
	findErr    error // when set, FindByID fails: a read after the swap must not matter
}

func (f *fakeClientStore) Create(context.Context, *models.ServiceClient) error { return nil }
func (f *fakeClientStore) List(context.Context) ([]*models.ServiceClient, error) {
	return nil, nil
}
func (f *fakeClientStore) UpdateStatus(context.Context, primitive.ObjectID, models.ServiceClientStatus) error {
	return nil
}
func (f *fakeClientStore) TouchLastSeen(context.Context, primitive.ObjectID) error { return nil }

func (f *fakeClientStore) FindByKeyHash(_ context.Context, hash string) (*models.ServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.byID {
		if c.KeyHash == hash {
			cp := *c
			return &cp, nil
		}
	}
	return nil, mongo.ErrNoDocuments
}

func (f *fakeClientStore) FindByID(_ context.Context, id primitive.ObjectID) (*models.ServiceClient, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.byID[id]
	if !ok {
		return nil, mongo.ErrNoDocuments
	}
	cp := *c
	return &cp, nil
}

func (f *fakeClientStore) ReplaceKey(_ context.Context, id primitive.ObjectID, keyHash, keyLast4 string) (*models.ServiceClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replaceErr != nil {
		return nil, f.replaceErr
	}
	c, ok := f.byID[id]
	if !ok || c.Status != models.ServiceClientActive {
		return nil, mongo.ErrNoDocuments
	}
	c.KeyHash, c.KeyLast4 = keyHash, keyLast4
	cp := *c
	return &cp, nil
}

func newClientFixture(t *testing.T, status models.ServiceClientStatus) (*ServiceClientService, *fakeClientStore, primitive.ObjectID, string) {
	t.Helper()
	raw, hash, err := apikey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := primitive.NewObjectID()
	store := &fakeClientStore{byID: map[primitive.ObjectID]*models.ServiceClient{
		id: {ID: id, Name: "carwash", KeyHash: hash, KeyLast4: apikey.Last4(raw), Status: status},
	}}
	return &ServiceClientService{repo: store}, store, id, raw
}

func wantAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apierr.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want *apierr.APIError, got %v", err)
	}
	if ae.HTTPStatus != status || ae.Code != code {
		t.Fatalf("got %d/%s, want %d/%s", ae.HTTPStatus, ae.Code, status, code)
	}
}

func TestServiceClientRotate_NewKeyAuthenticatesOldDoesNot(t *testing.T) {
	svc, _, id, oldKey := newClientFixture(t, models.ServiceClientActive)
	ctx := context.Background()

	c, newKey, err := svc.Rotate(ctx, id.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if newKey == "" || newKey == oldKey {
		t.Fatal("rotate must return a different, non-empty key")
	}
	if c.KeyLast4 != apikey.Last4(newKey) {
		t.Fatalf("key_last4 = %q, want last 4 of the new key", c.KeyLast4)
	}
	if _, err := svc.Authenticate(ctx, newKey); err != nil {
		t.Fatalf("new key should authenticate: %v", err)
	}
	_, err = svc.Authenticate(ctx, oldKey)
	wantAPIError(t, err, http.StatusUnauthorized, apierr.CodeUnauthorized)
}

func TestServiceClientRotate_RevokedClientRefused(t *testing.T) {
	svc, store, id, oldKey := newClientFixture(t, models.ServiceClientRevoked)
	before := store.byID[id].KeyHash

	_, _, err := svc.Rotate(context.Background(), id.Hex())
	wantAPIError(t, err, http.StatusConflict, apierr.CodeConflict)

	if store.byID[id].Status != models.ServiceClientRevoked {
		t.Fatal("record must still be revoked")
	}
	if store.byID[id].KeyHash != before || before != apikey.Hash(oldKey) {
		t.Fatal("key hash must be unchanged")
	}
}

func TestServiceClientRotate_UnknownIDNotFound(t *testing.T) {
	svc, _, _, _ := newClientFixture(t, models.ServiceClientActive)
	_, _, err := svc.Rotate(context.Background(), primitive.NewObjectID().Hex())
	wantAPIError(t, err, http.StatusNotFound, apierr.CodeNotFound)
}

func TestServiceClientRotate_BadIDBadRequest(t *testing.T) {
	svc, _, _, _ := newClientFixture(t, models.ServiceClientActive)
	_, _, err := svc.Rotate(context.Background(), "not-hex")
	wantAPIError(t, err, http.StatusBadRequest, apierr.CodeBadRequest)
}

func TestServiceClientRotate_RepoFailureLeavesOldKey(t *testing.T) {
	svc, store, id, oldKey := newClientFixture(t, models.ServiceClientActive)
	store.replaceErr = errors.New("connection reset")

	_, newKey, err := svc.Rotate(context.Background(), id.Hex())
	wantAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)
	if newKey != "" {
		t.Fatal("no key may be returned when the write failed")
	}
	if _, err := svc.Authenticate(context.Background(), oldKey); err != nil {
		t.Fatalf("old key must still authenticate: %v", err)
	}
}

// The swap and the read-back are one write. If a second read were needed, a
// failure of it would answer 500 for a key that has already been replaced, and
// the new key (returned once) would be lost with the old one already dead.
func TestServiceClientRotate_PostSwapReadFailureCannotCause500(t *testing.T) {
	svc, store, id, _ := newClientFixture(t, models.ServiceClientActive)
	store.findErr = errors.New("mongo: read failed")

	c, newKey, err := svc.Rotate(context.Background(), id.Hex())
	if err != nil {
		t.Fatalf("rotate succeeded in the store, so it must succeed: %v", err)
	}
	if newKey == "" || c.KeyLast4 != apikey.Last4(newKey) {
		t.Fatalf("returned record does not match the new key: %+v", c)
	}
}
