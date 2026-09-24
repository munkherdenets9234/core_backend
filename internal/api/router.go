package api

import (
	"net/http"

	"github.com/eandstravel/tenantcore/internal/api/admin"
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
		ServiceClient: d.ServiceClient,
		Entitlement:   d.Entitlement,
		AuthRateLimit: s.limit("admin-auth", d.Config.AuthRatePerMinute),
	})

	// The machine-to-machine surface. Deliberately NOT under /admin: it
	// authenticates with a different credential, serves a different
	// audience, and putting it under the console's prefix would invite
	// someone to mount it behind the console's middleware by accident.
	svc.Register(v1.Group("/svc"), svc.Deps{
		ServiceClient: d.ServiceClient,
		Entitlement:   d.Entitlement,
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
