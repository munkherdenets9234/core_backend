// Package admin is the platform operator's own console surface.
//
// It mounts two sub-packages: public, reachable by anyone, and private,
// mounted on a group carrying the superadmin check. They never share a
// controller type, which is what stops a field meant for an authenticated
// console from reaching an anonymous caller through a widened struct.
package admin

import (
	privateapi "github.com/eandstravel/tenantcore/internal/api/admin/private"
	publicapi "github.com/eandstravel/tenantcore/internal/api/admin/public"
	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/token"
	"github.com/gin-gonic/gin"
)

type Deps struct {
	Auth *middleware.Auth

	Tenant        *service.TenantService
	Plan          *service.PlanService
	Subscription  *service.SubscriptionService
	PlatformUser  *service.PlatformUserService
	ServiceClient *service.ServiceClientService
	Entitlement   *service.EntitlementService

	AuthRateLimit gin.HandlerFunc
}

// Register mounts the console onto base, already prefixed with /admin.
func Register(base *gin.RouterGroup, d Deps) {
	publicapi.Register(base, publicapi.Deps{
		PlatformUser:  d.PlatformUser,
		AuthRateLimit: d.AuthRateLimit,
	})

	// The gate for everything below, applied here on the group itself —
	// not inside the private package, which must not be able to choose its
	// own authentication.
	priv := base.Group("", d.Auth.Require(token.RoleSuperadmin))

	privateapi.Register(priv, privateapi.Deps{
		Tenant:        d.Tenant,
		Plan:          d.Plan,
		Subscription:  d.Subscription,
		PlatformUser:  d.PlatformUser,
		ServiceClient: d.ServiceClient,
		Entitlement:   d.Entitlement,
		AuthRateLimit: d.AuthRateLimit,
	})
}
