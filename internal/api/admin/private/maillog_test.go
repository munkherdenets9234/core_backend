package private

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type stubMailLog struct {
	status, template string
	page, limit      int
	called           bool
}

func (s *stubMailLog) List(_ context.Context, status, template string, page, limit int) ([]*models.MailLog, int64, error) {
	s.called, s.status, s.template, s.page, s.limit = true, status, template, page, limit
	return []*models.MailLog{{Template: "password_changed", To: "ann@example.com", Status: "sent", Source: "system", CreatedAt: time.Now()}}, 1, nil
}

func mailLogEngine(store *stubMailLog) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(middleware.ErrorHandler(zap.NewNop(), false))
	Register(e.Group("/admin"), Deps{MailLog: service.NewMailLogService(store)})
	return e
}

func getMailLog(e *gin.Engine, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/mail-log"+query, nil))
	return w
}

func TestMailLogQueryValidationRejectsBadStatusAndTemplate(t *testing.T) {
	for _, q := range []string{"?status=bounced", "?template=welcome", "?status=sent&template=nope"} {
		store := &stubMailLog{}
		w := getMailLog(mailLogEngine(store), q)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422", q, w.Code)
		}
		if store.called {
			t.Errorf("%s: store was queried", q)
		}
	}
}

func TestMailLogListEnvelopeAndBounds(t *testing.T) {
	store := &stubMailLog{}
	w := getMailLog(mailLogEngine(store), "?status=sent&template=password_changed&page=2&limit=500")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if store.page != 2 || store.limit != 20 || store.status != "sent" || store.template != "password_changed" {
		t.Fatalf("unexpected store call %+v", store)
	}
	var env struct {
		Success bool `json:"success"`
		Data    []struct {
			To string `json:"to"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Success || len(env.Data) != 1 || env.Data[0].To != "ann@example.com" || env.Meta.Total != 1 {
		t.Fatalf("unexpected envelope %s", w.Body.String())
	}
}

func TestMailLogLimitMaxIs100(t *testing.T) {
	store := &stubMailLog{}
	_ = getMailLog(mailLogEngine(store), "?limit=100")
	if store.limit != 100 {
		t.Fatalf("limit = %d, want 100", store.limit)
	}
}
