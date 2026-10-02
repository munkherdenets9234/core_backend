// Package api builds tenantcore's HTTP surface.
//
// The audience split lives one level down, in admin/ and svc/. This package
// only wires: engine-wide middleware, the operational endpoints, and mounting
// each audience on the group that carries its authentication.
package api

import (
	"github.com/eandstravel/tenantcore/internal/config"
	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Deps is everything the HTTP layer needs.
type Deps struct {
	Config *config.Config
	Log    *zap.Logger

	Auth        *middleware.Auth
	RateLimiter *middleware.RateLimiter

	// Mail is nil when GMAIL_EMAIL/GMAIL_PASSWORD are not both set. Only the
	// /svc notification route uses it, and it answers FEATURE_UNAVAILABLE
	// rather than vanishing when mail is off.
	Mail *mailer.Mailer

	// PublicKeyB64 is served at the well-known endpoint so product services
	// can fetch the verifying key instead of having it copied by hand.
	PublicKeyB64 string
	KeyID        string

	Tenant        *service.TenantService
	Plan          *service.PlanService
	Subscription  *service.SubscriptionService
	PlatformUser  *service.PlatformUserService
	PasswordReset *service.PasswordResetService
	ServiceClient *service.ServiceClientService
	Entitlement   *service.EntitlementService
	Showcase      *service.ShowcaseService
	Quote         *service.QuoteService
	TenantPlan    *service.TenantPlanService
	SiteContent   *service.SiteContentService
}

type Server struct {
	engine *gin.Engine
	deps   Deps
}

func NewServer(d Deps) *Server {
	s := &Server{deps: d}
	s.engine = s.buildEngine()
	return s
}

// Handler exposes the engine, for http.Server and for tests.
func (s *Server) Handler() *gin.Engine { return s.engine }
