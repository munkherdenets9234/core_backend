package api

import (
	"net/http"

	"github.com/eandstravel/tenantcore/docs"
	"github.com/eandstravel/tenantcore/internal/api/admin"
	publicapi "github.com/eandstravel/tenantcore/internal/api/public"
	"github.com/eandstravel/tenantcore/internal/api/svc"
	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) buildEngine() *gin.Engine {
	d := s.deps
	devMode := d.Config.IsDev()

	gin.SetMode(gin.ReleaseMode)
	if devMode {
		gin.SetMode(gin.DebugMode)
	}

	e := gin.New()

	// Order matters. Recovery is outermost so a panic anywhere below is
	// still rendered as a proper envelope; ErrorHandler wraps everything
	// under it so it sees errors from middleware as well as controllers.
	e.Use(middleware.Recovery(d.Log, devMode))
	e.Use(middleware.Logger(d.Log))
	e.Use(middleware.CORS())
	e.Use(middleware.ErrorHandler(d.Log, devMode))

	s.registerOperational(e)

	v1 := e.Group("/api/v1")

	admin.Register(v1.Group("/admin"), admin.Deps{
		Auth:          d.Auth,
		Tenant:        d.Tenant,
		Plan:          d.Plan,
		Subscription:  d.Subscription,
		PlatformUser:  d.PlatformUser,
		PasswordReset: d.PasswordReset,
		ServiceClient: d.ServiceClient,
		Entitlement:   d.Entitlement,
		Showcase:      d.Showcase,
		Quote:         d.Quote,
		TenantPlan:    d.TenantPlan,
		SiteContent:   d.SiteContent,
		Promote:       d.Promote,
		MailLog:       d.MailLog,
		AuthRateLimit: s.limit("admin-auth", d.Config.AuthRatePerMinute),
	})

	// The operator's own marketing surface: no credential at all. Mounted
	// as its own group beside /admin rather than under it, for the reason
	// /svc is separate — a different audience should not be one router edit
	// away from inheriting the console's middleware, or losing it.
	publicapi.Register(v1.Group("/public"), publicapi.Deps{
		Plan:          d.Plan,
		Showcase:      d.Showcase,
		Quote:         d.Quote,
		Content:       d.SiteContent,
		LeadRateLimit: s.limit("public-lead", d.Config.AuthRatePerMinute),
	})

	// The machine-to-machine surface. Deliberately NOT under /admin: it
	// authenticates with a different credential, serves a different
	// audience, and putting it under the console's prefix would invite
	// someone to mount it behind the console's middleware by accident.
	svc.Register(v1.Group("/svc"), svc.Deps{
		ServiceClient: d.ServiceClient,
		Entitlement:   d.Entitlement,
		Mail:          d.Mail,
		// Shares the auth bucket's per-minute figure rather than inventing a
		// third knob: both guard an expensive, abusable operation, and the
		// right number for one is the right order of magnitude for the other.
		SendRateLimit: s.limit("svc-email", d.Config.AuthRatePerMinute),
	})

	return e
}

// limit returns the named rate-limit middleware, or a pass-through when rate
// limiting is off. Returning a no-op rather than skipping the middleware
// keeps the route tree identical either way, so a limit that is disabled
// cannot also change which middleware a route runs.
func (s *Server) limit(name string, perMinute int) gin.HandlerFunc {
	if !s.deps.Config.RateLimitEnabled || s.deps.RateLimiter == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return s.deps.RateLimiter.Limit(name, perMinute, s.deps.Config.RateLimitBurst)
}

// registerOperational mounts the endpoints that describe the service rather
// than serve its data.
func (s *Server) registerOperational(e *gin.Engine) {
	// healthz answers "is the process up" and nothing else. A health check
	// that talks to the database turns a slow query into a restart loop.
	e.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// readyz answers "what is this deployment able to do". Always 200: a
	// deployment missing an optional feature is degraded, not unhealthy, and
	// a failure status here would make an orchestrator restart a process
	// that is working exactly as configured. Alert on degraded, not on the
	// status code.
	e.GET("/readyz", func(c *gin.Context) {
		features := s.deps.Config.Features()
		degraded := false
		list := make([]gin.H, 0, len(features))
		for _, f := range features {
			if !f.Enabled {
				degraded = true
			}
			entry := gin.H{"name": f.Name, "enabled": f.Enabled}
			if !f.Enabled {
				entry["detail"] = f.Detail
			}
			list = append(list, entry)
		}
		c.JSON(http.StatusOK, gin.H{
			"status":   "ok",
			"env":      string(s.deps.Config.AppEnv),
			"degraded": degraded,
			"features": list,
		})
	})

	// The API contract, served so an integrating product can fetch it instead
	// of being handed a copy that then goes stale in its own repo.
	// Unauthenticated on purpose: it describes the shape of the API, not any
	// tenant's data, and a contract you need a credential to read is one
	// nobody reads before writing a client against it.
	//
	// internal/api/openapi_test.go walks the live route table and diffs it
	// against this file in both directions, so what is served here cannot
	// silently drift from what the router actually does.
	e.GET("/docs/api.json", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json; charset=utf-8", docs.OpenAPI)
	})

	// The verifying key, unauthenticated by design: it is public, it is
	// useless for forging anything, and requiring a credential to fetch it
	// would mean every product needs a credential before it can check a
	// credential.
	//
	// kid is served alongside so a product can hold two keys during a
	// rotation and pick by the token's header rather than guessing.
	if s.deps.Config.PublishPublicKey && s.deps.PublicKeyB64 != "" {
		e.GET("/.well-known/tenantcore", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"alg":        "EdDSA",
				"kid":        s.deps.KeyID,
				"public_key": s.deps.PublicKeyB64,
				"issuer":     "tenantcore",
			})
		})
	}
}
