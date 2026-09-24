package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/config"
	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/token"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// These assert the property the audience split exists to provide: a caller
// without the right credential cannot reach a controller behind a gate.
//
// The engine is built with a Deps whose services are all nil. That is the
// assertion, not a shortcut: registration only stores pointers, so a nil
// service is harmless until a handler actually runs. If one of these requests
// ever reaches a controller it dereferences nil and the test panics — a much
// louder failure than a 200 with an empty body, and exactly what should
// happen if a gate is ever removed.

// publicRoutes are reachable with no credential at all. Everything else must
// refuse. Keep this list deliberately: if a route moves between public and
// private, this is where it shows up.
var publicRoutes = map[string]bool{
	"POST /api/v1/admin/login": true,
}

func testKeys(t *testing.T) *token.Maker {
	t.Helper()
	priv, _, err := token.GenerateKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	m, err := token.NewMaker(priv)
	if err != nil {
		t.Fatalf("maker: %v", err)
	}
	return m
}

func testEngine(t *testing.T) (*gin.Engine, *token.Maker) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	maker := testKeys(t)
	e := NewServer(Deps{
		Log: zap.NewNop(),
		Config: &config.Config{
			AppEnv:            config.EnvTest,
			PublishPublicKey:  true,
			RateLimitEnabled:  false,
			AuthRatePerMinute: 10,
			RateLimitBurst:    5,
		},
		Auth:         middleware.NewAuth(maker.Verifier()),
		PublicKeyB64: maker.PublicKeyB64(),
		KeyID:        maker.KeyID(),
	}).Handler()
	return e, maker
}

// TestEveryAdminRouteRequiresASuperadminToken walks the real route table, so
// a route added tomorrow is covered without anyone remembering to add it.
func TestEveryAdminRouteRequiresASuperadminToken(t *testing.T) {
	e, _ := testEngine(t)

	checked := 0
	for _, r := range e.Routes() {
		if !strings.HasPrefix(r.Path, "/api/v1/admin/") {
			continue
		}
		if publicRoutes[r.Method+" "+r.Path] {
			continue
		}
		checked++
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			assertRefused(t, e, r.Method, fillParams(r.Path), nil, http.StatusUnauthorized, apierr.CodeUnauthorized)
		})
	}
	if checked == 0 {
		t.Fatal("no private admin routes were checked — the route table or publicRoutes is wrong")
	}
	t.Logf("checked %d private admin routes", checked)
}

// A valid token with the wrong role must be refused too. Authentication is
// not authorisation, and a tenant-scoped token reaching the console would be
// the worst possible confusion of the two.
func TestATenantTokenCannotReachTheConsole(t *testing.T) {
	e, maker := testEngine(t)

	signed, _, err := maker.Create("user-1", token.RoleTenantAdmin, "tenant-1", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	headers := map[string]string{"Authorization": "Bearer " + signed}
	assertRefused(t, e, http.MethodGet, "/api/v1/admin/tenants", headers,
		http.StatusForbidden, apierr.CodeForbidden)
}

// The machine-to-machine surface authenticates with a service key, not a
// token. A caller holding neither must not reach it.
func TestEverySvcRouteRequiresAServiceKey(t *testing.T) {
	e, _ := testEngine(t)

	checked := 0
	for _, r := range e.Routes() {
		if !strings.HasPrefix(r.Path, "/api/v1/svc/") {
			continue
		}
		checked++
		t.Run(r.Method+" "+r.Path, func(t *testing.T) {
			assertRefused(t, e, r.Method, fillParams(r.Path), nil,
				http.StatusUnauthorized, apierr.CodeUnauthorized)
		})
	}
	if checked == 0 {
		t.Fatal("no svc routes were checked — the route table is wrong")
	}
	t.Logf("checked %d svc routes", checked)
}

// A platform-staff token must NOT open the machine-to-machine surface. The
// two credentials are separate on purpose; accepting either would make the
// separation decorative.
func TestASuperadminTokenIsNotAServiceKey(t *testing.T) {
	e, maker := testEngine(t)

	signed, _, err := maker.Create("user-1", token.RoleSuperadmin, "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	headers := map[string]string{"Authorization": "Bearer " + signed}
	assertRefused(t, e, http.MethodGet, "/api/v1/svc/entitlements", headers,
		http.StatusUnauthorized, apierr.CodeUnauthorized)
}

func TestOperationalRoutesAreOpen(t *testing.T) {
	e, _ := testEngine(t)

	for _, path := range []string{"/healthz", "/readyz", "/.well-known/tenantcore"} {
		if res := do(e, http.MethodGet, path, nil); res.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", path, res.Code)
		}
	}
}

// The verifying key is public by construction and useless for forging.
// Requiring a credential to fetch it would mean a product needs a credential
// before it can check a credential.
func TestPublicKeyEndpointServesTheVerifyingKey(t *testing.T) {
	e, maker := testEngine(t)

	res := do(e, http.MethodGet, "/.well-known/tenantcore", nil)
	var body struct {
		Alg       string `json:"alg"`
		Kid       string `json:"kid"`
		PublicKey string `json:"public_key"`
		Issuer    string `json:"issuer"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.PublicKey != maker.PublicKeyB64() {
		t.Error("the served key is not the one this service signs with")
	}
	if body.Kid != maker.KeyID() {
		t.Errorf("kid = %q, want %q", body.Kid, maker.KeyID())
	}
	if body.Alg != "EdDSA" {
		t.Errorf("alg = %q, want EdDSA", body.Alg)
	}

	// And it must be the PUBLIC half. Serving a private key here would be
	// the single worst bug this service could have.
	if _, err := token.NewMaker(body.PublicKey); err == nil {
		t.Fatal("the endpoint served something usable as a SIGNING key")
	}
}

func TestReadyzReportsDisabledFeatures(t *testing.T) {
	e, _ := testEngine(t) // no superadmin bootstrap, no rate limiting

	res := do(e, http.MethodGet, "/readyz", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — degraded is not unhealthy", res.Code)
	}

	var body struct {
		Degraded bool `json:"degraded"`
		Features []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
			Detail  string `json:"detail"`
		} `json:"features"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Degraded {
		t.Error("degraded should be true with no superadmin bootstrap configured")
	}
	for _, f := range body.Features {
		if !f.Enabled && f.Detail == "" {
			t.Errorf("feature %q is disabled with no detail saying how to enable it", f.Name)
		}
	}
}

func assertRefused(t *testing.T, e *gin.Engine, method, path string, headers map[string]string, wantStatus int, wantCode string) {
	t.Helper()

	res := do(e, method, path, headers)
	if res.Code != wantStatus {
		t.Fatalf("got %d, want %d — this route is reachable without the right credential\nbody: %s",
			res.Code, wantStatus, res.Body.String())
	}

	var env struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   struct {
			Domain string `json:"domain"`
			Code   string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, res.Body.String())
	}
	if env.Success {
		t.Error("success should be false on an error response")
	}
	if env.Error.Code != wantCode {
		t.Errorf("code = %q, want %q", env.Error.Code, wantCode)
	}
	if env.Message == "" {
		t.Error("top-level message is empty — clients read this field")
	}
}

func do(e *gin.Engine, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	return w
}

// fillParams substitutes a placeholder for each :param so the request reaches
// the route under test. The values are never looked up: every one of these
// requests is refused before a handler runs.
func fillParams(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "placeholder"
		}
	}
	return strings.Join(parts, "/")
}
