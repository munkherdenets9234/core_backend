package private

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/token"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.uber.org/zap"
)

// ── Fakes: the service's own seams, so the real PromoteService runs ─────────

type fakeQuotes struct {
	quote  *models.Quote
	linked bool
}

func (f *fakeQuotes) FindByID(_ context.Context, id primitive.ObjectID) (*models.Quote, error) {
	if f.quote == nil || f.quote.ID != id {
		return nil, mongo.ErrNoDocuments
	}
	q := *f.quote
	return &q, nil
}

func (f *fakeQuotes) LinkPromoted(context.Context, primitive.ObjectID, primitive.ObjectID, *primitive.ObjectID) (bool, error) {
	return f.linked, nil
}

type fakeTenants struct {
	err error
	key string
	// created counts successful creations, so a refused request can be shown
	// not to have made a tenant.
	created int
}

func (f *fakeTenants) Create(_ context.Context, t *models.Tenant) (*models.Tenant, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	f.created++
	t.ID = primitive.NewObjectID()
	t.APIKeyHash = "hash-must-not-leak"
	t.APIKeyLast4 = "abcd"
	t.Status = models.TenantActive
	return t, f.key, nil
}

type noUsers struct{}

func (noUsers) ListActive(context.Context) ([]*models.PlatformUser, error) { return nil, nil }

// ── Harness ───────────────────────────────────────────────────────────────

type promoteHarness struct {
	e       *gin.Engine
	maker   *token.Maker
	quotes  *fakeQuotes
	tenants *fakeTenants
	quoteID string
}

func newPromoteHarness(t *testing.T) *promoteHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)

	priv, _, err := token.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	maker, err := token.NewMaker(priv)
	if err != nil {
		t.Fatal(err)
	}

	qid := primitive.NewObjectID()
	h := &promoteHarness{
		maker:   maker,
		quotes:  &fakeQuotes{quote: &models.Quote{ID: qid}, linked: true},
		tenants: &fakeTenants{key: "raw-key-" + "for-test"},
		quoteID: qid.Hex(),
	}
	svc := service.NewPromoteService(h.quotes, h.tenants, noUsers{}, nil, "App", zap.NewNop())

	e := gin.New()
	e.Use(middleware.ErrorHandler(zap.NewNop(), false))
	// Same gate admin.Register puts in front of this package.
	gate := e.Group("/api/v1/admin", middleware.NewAuth(maker.Verifier()).Require(token.RoleSuperadmin))
	Register(gate, Deps{Promote: svc})
	h.e = e
	return h
}

func (h *promoteHarness) bearer(t *testing.T, role token.Role) string {
	t.Helper()
	tenantID := ""
	if role != token.RoleSuperadmin {
		tenantID = "tenant-1"
	}
	signed, _, err := h.maker.Create(primitive.NewObjectID().Hex(), role, tenantID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + signed
}

func (h *promoteHarness) post(id, body, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/quotes/"+id+"/promote", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.e.ServeHTTP(w, req)
	return w
}

const goodBody = `{"name":"Acme","slug":"acme","contact_email":"a@example.com","domain":"acme.example"}`

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return b.Error.Code
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestPromoteRouteRequiresSuperadmin(t *testing.T) {
	h := newPromoteHarness(t)

	if w := h.post(h.quoteID, goodBody, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: got %d, want 401", w.Code)
	}
	if w := h.post(h.quoteID, goodBody, h.bearer(t, token.RoleTenantAdmin)); w.Code != http.StatusForbidden {
		t.Fatalf("non-superadmin: got %d, want 403", w.Code)
	}
	if h.tenants.created != 0 {
		t.Fatal("a refused caller created a tenant")
	}
}

func TestPromoteReturns201WithKeyOnce(t *testing.T) {
	h := newPromoteHarness(t)

	w := h.post(h.quoteID, goodBody, h.bearer(t, token.RoleSuperadmin))
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201: %s", w.Code, w.Body.String())
	}

	var b struct {
		Data struct {
			Tenant      map[string]any `json:"tenant"`
			APIKey      string         `json:"api_key"`
			QuoteLinked *bool          `json:"quote_linked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Data.APIKey != h.tenants.key {
		t.Errorf("api_key = %q, want the one the tenant was created with", b.Data.APIKey)
	}
	if b.Data.QuoteLinked == nil || !*b.Data.QuoteLinked {
		t.Error("quote_linked should be true")
	}
	if b.Data.Tenant["slug"] != "acme" || b.Data.Tenant["api_key_last4"] != "abcd" {
		t.Errorf("tenant view wrong: %v", b.Data.Tenant)
	}
	if strings.Contains(w.Body.String(), "hash-must-not-leak") {
		t.Error("the key hash reached the response")
	}
	for k := range b.Data.Tenant {
		if strings.Contains(strings.ToLower(k), "hash") {
			t.Errorf("tenant has a hash-like field %q", k)
		}
	}
}

func TestPromoteReportsAnUnlinkedQuote(t *testing.T) {
	h := newPromoteHarness(t)
	h.quotes.linked = false

	w := h.post(h.quoteID, goodBody, h.bearer(t, token.RoleSuperadmin))
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"quote_linked":false`) {
		t.Errorf("quote_linked:false must be explicit: %s", w.Body.String())
	}
}

func TestPromoteMapsServiceErrors(t *testing.T) {
	auth := func(h *promoteHarness) string { return h.bearer(t, token.RoleSuperadmin) }

	t.Run("unknown quote is 404", func(t *testing.T) {
		h := newPromoteHarness(t)
		w := h.post(primitive.NewObjectID().Hex(), goodBody, auth(h))
		if w.Code != http.StatusNotFound {
			t.Fatalf("got %d, want 404", w.Code)
		}
	})
	t.Run("invalid id is 400", func(t *testing.T) {
		h := newPromoteHarness(t)
		w := h.post("not-an-id", goodBody, auth(h))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("got %d, want 400", w.Code)
		}
	})
	t.Run("already promoted is 409", func(t *testing.T) {
		h := newPromoteHarness(t)
		pid := primitive.NewObjectID()
		h.quotes.quote.PromotedTenantID = &pid
		w := h.post(h.quoteID, goodBody, auth(h))
		if w.Code != http.StatusConflict {
			t.Fatalf("got %d, want 409", w.Code)
		}
		if h.tenants.created != 0 {
			t.Error("a tenant was created for an already promoted quote")
		}
	})
	t.Run("duplicate slug is 409", func(t *testing.T) {
		h := newPromoteHarness(t)
		h.tenants.err = apierr.Conflict("slug already in use")
		w := h.post(h.quoteID, goodBody, auth(h))
		if w.Code != http.StatusConflict {
			t.Fatalf("got %d, want 409", w.Code)
		}
	})
	t.Run("missing fields are 400", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"name":"Acme"}`, `{"slug":"acme"}`, `{"name":"","slug":"acme"}`} {
			h := newPromoteHarness(t)
			w := h.post(h.quoteID, body, auth(h))
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d, want 400", body, w.Code)
			}
			if h.tenants.created != 0 {
				t.Errorf("%s: a tenant was created", body)
			}
		}
	})
}

func TestPromoteBodyRejectsUnknownOrBadTypes(t *testing.T) {
	cases := map[string]string{
		"unknown field":  `{"name":"Acme","slug":"acme","status":"suspended"}`,
		"number name":    `{"name":5,"slug":"acme"}`,
		"array slug":     `{"name":"Acme","slug":["a"]}`,
		"bool domain":    `{"name":"Acme","slug":"acme","domain":true}`,
		"not json":       `name=Acme`,
		"empty body":     ``,
		"trailing value": `{"name":"Acme","slug":"acme"}{"x":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPromoteHarness(t)
			w := h.post(h.quoteID, body, h.bearer(t, token.RoleSuperadmin))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
			}
			if errCode(t, w) != apierr.CodeBadRequest {
				t.Errorf("code = %q", errCode(t, w))
			}
			if h.tenants.created != 0 {
				t.Error("a tenant was created from a bad body")
			}
		})
	}
}
