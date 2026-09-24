package response

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/gin-gonic/gin"
)

func render(err error, devMode bool) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	Err(c, err, devMode)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return body
}

func TestErrRendersTheTaxonomy(t *testing.T) {
	w := render(apierr.NotFound("blog"), false)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}

	body := decode(t, w)
	if body["success"] != false {
		t.Error("success should be false")
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object missing: %v", body)
	}
	if errBody["code"] != apierr.CodeNotFound {
		t.Errorf("code = %v, want %s", errBody["code"], apierr.CodeNotFound)
	}
	if errBody["message"] != "blog not found" {
		t.Errorf("message = %v, want %q", errBody["message"], "blog not found")
	}
}

// The top-level "message" is what every existing client reads. Dropping it in
// favour of the structured object would be a breaking change for no gain, so
// both are written.
func TestErrKeepsTheLegacyMessageField(t *testing.T) {
	body := decode(t, render(apierr.BadRequest("invalid id"), false))

	if body["message"] != "invalid id" {
		t.Errorf("top-level message = %v, want %q", body["message"], "invalid id")
	}
}

// An error that never went through the taxonomy is a bug. The caller gets a
// generic 500 and the cause stays in the log, where it belongs — a bug's text
// is not a contract, and it is frequently the text of a database error.
func TestErrHidesTheCauseOfAnUntypedError(t *testing.T) {
	w := render(errors.New("pq: relation \"secret_table\" does not exist"), false)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if got := w.Body.String(); strings.Contains(got, "secret_table") {
		t.Errorf("the underlying cause leaked to the client: %s", got)
	}
}

func TestErrIncludesTheStackOnlyInDev(t *testing.T) {
	prod := decode(t, render(apierr.Internal(errors.New("boom")), false))
	if e, ok := prod["error"].(map[string]any); ok {
		if _, present := e["stack_trace"]; present {
			t.Error("stack_trace must not reach a production client")
		}
	}

	dev := decode(t, render(apierr.Internal(errors.New("boom")), true))
	e, ok := dev["error"].(map[string]any)
	if !ok {
		t.Fatal("error object missing")
	}
	if _, present := e["stack_trace"]; !present {
		t.Error("stack_trace should be present in development")
	}
}

// A 429 without Retry-After leaves a well-behaved client no way to back off.
func TestErrSetsRetryAfterOnRateLimit(t *testing.T) {
	w := render(apierr.RateLimited(30), false)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want %q", got, "30")
	}
}

func TestErrUnwrapsAWrappedAppError(t *testing.T) {
	// A service that wraps a taxonomy error with fmt.Errorf must still
	// produce the right status, not a blanket 500.
	wrapped := errors.Join(errors.New("while loading the page"), apierr.Forbidden("nope"))

	w := render(wrapped, false)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}
