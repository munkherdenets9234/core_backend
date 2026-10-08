package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
)

type fakeMailLogStore struct {
	called           bool
	status, template string
	page, limit      int
	err              error
}

func (f *fakeMailLogStore) List(_ context.Context, status, template string, page, limit int) ([]*models.MailLog, int64, error) {
	f.called, f.status, f.template, f.page, f.limit = true, status, template, page, limit
	return nil, 0, f.err
}

func TestMailLogListRejectsBadFilters(t *testing.T) {
	for name, c := range map[string]struct{ status, template string }{
		"unknown status":     {"pending", ""},
		"status case":        {"SENT", ""},
		"status injection":   {`{"$ne":""}`, ""},
		"unknown template":   {"", "welcome"},
		"template injection": {"", `{"$gt":""}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeMailLogStore{}
			_, _, err := NewMailLogService(f).List(context.Background(), c.status, c.template, 1, 20)
			var ae *apierr.APIError
			if !errors.As(err, &ae) || ae.HTTPStatus != http.StatusUnprocessableEntity {
				t.Fatalf("err = %v, want a validation error", err)
			}
			if f.called {
				t.Fatal("the store must not be queried with an invalid filter")
			}
		})
	}
}

func TestMailLogListPassesValidFiltersAndBoundsPaging(t *testing.T) {
	f := &fakeMailLogStore{}
	if _, _, err := NewMailLogService(f).List(context.Background(), "failed", "password_reset_code", 0, 1000); err != nil {
		t.Fatalf("list: %v", err)
	}
	if f.status != "failed" || f.template != "password_reset_code" || f.page != 1 || f.limit != 20 {
		t.Fatalf("unexpected store call %+v", f)
	}
}

func TestMailLogListStoreErrorIsInternal(t *testing.T) {
	f := &fakeMailLogStore{err: errors.New("mongo: secret detail")}
	_, _, err := NewMailLogService(f).List(context.Background(), "", "", 1, 20)
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("err = %v, want internal", err)
	}
}
