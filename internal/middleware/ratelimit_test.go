package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func limiterEngine(t *testing.T, rl *RateLimiter, perMinute, burst int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	// ErrorHandler is mounted because that is how the limiter works in
	// production: it attaches an error and aborts, and the renderer turns
	// that into a 429. Without it, an aborted request falls through to
	// gin's default 200 — which is exactly why no middleware here writes
	// its own response.
	e.Use(ErrorHandler(zap.NewNop(), false))
	e.POST("/login", rl.Limit("test-auth", perMinute, burst), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return e
}

func post(e *gin.Engine, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = ip + ":12345"
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	return w
}

func TestRateLimiterAllowsBurstThenRefuses(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	// 60/minute = one token per second, burst 3. The first three are
	// instantaneous, so no meaningful refill happens between them.
	e := limiterEngine(t, rl, 60, 3)

	for i := 1; i <= 3; i++ {
		if res := post(e, "1.2.3.4"); res.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200 — the burst allowance is too small", i, res.Code)
		}
	}

	res := post(e, "1.2.3.4")
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429 after the burst is spent", res.Code)
	}

	// Retry-After must be present and sane, or a well-behaved client has no
	// way to back off correctly and will simply hammer the endpoint.
	raw := res.Header().Get("Retry-After")
	if raw == "" {
		t.Fatal("Retry-After header is missing on a 429")
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q, want a positive integer", raw)
	}
}

func TestRateLimiterIsPerClient(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	e := limiterEngine(t, rl, 60, 2)

	post(e, "1.2.3.4")
	post(e, "1.2.3.4")
	if res := post(e, "1.2.3.4"); res.Code != http.StatusTooManyRequests {
		t.Fatalf("first client: got %d, want 429", res.Code)
	}

	// A second client must be unaffected. A limiter that is global rather
	// than per-client turns one abusive caller into an outage for everyone.
	if res := post(e, "5.6.7.8"); res.Code != http.StatusOK {
		t.Fatalf("second client: got %d, want 200 — the limiter is not keyed per client", res.Code)
	}
}

func TestRateLimiterNamespacesByGroup(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(ErrorHandler(zap.NewNop(), false))
	e.POST("/login", rl.Limit("auth", 60, 1), func(c *gin.Context) { c.Status(http.StatusOK) })
	e.POST("/contact", rl.Limit("lead", 60, 1), func(c *gin.Context) { c.Status(http.StatusOK) })

	call := func(path string) int {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.RemoteAddr = "9.9.9.9:1"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w.Code
	}

	if code := call("/login"); code != http.StatusOK {
		t.Fatalf("login: got %d, want 200", code)
	}
	if code := call("/login"); code != http.StatusTooManyRequests {
		t.Fatalf("login: got %d, want 429", code)
	}
	// Spending the auth allowance must not spend the lead allowance too.
	if code := call("/contact"); code != http.StatusOK {
		t.Fatalf("contact: got %d, want 200 — the two groups share one bucket", code)
	}
}

func TestRateLimiterReturnsTaxonomyError(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	gin.SetMode(gin.TestMode)
	e := gin.New()
	var captured error
	e.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			captured = c.Errors.Last().Err
		}
	})
	e.POST("/login", rl.Limit("auth", 60, 1), func(c *gin.Context) { c.Status(http.StatusOK) })

	post(e, "4.4.4.4")
	post(e, "4.4.4.4")

	appErr, ok := captured.(*apierr.APIError)
	if !ok {
		t.Fatalf("limiter attached %T, want *apierr.APIError — it must go through the one renderer like every other error", captured)
	}
	if appErr.Code != apierr.CodeRateLimited {
		t.Errorf("code = %q, want %q", appErr.Code, apierr.CodeRateLimited)
	}
	if appErr.HTTPStatus != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", appErr.HTTPStatus)
	}
}
