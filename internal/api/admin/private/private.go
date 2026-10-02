// Package private is the console surface that requires a superadmin token.
//
// Every controller here is registered on a group that already carries the
// role check (see admin.Register). That is why this is a separate package
// from admin/public rather than a convention inside one: adding a route here
// cannot produce an unauthenticated endpoint, and no edit inside a controller
// can weaken it.
package private

import (
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type Deps struct {
	Tenant        *service.TenantService
	Plan          *service.PlanService
	Subscription  *service.SubscriptionService
	PlatformUser  *service.PlatformUserService
	ServiceClient *service.ServiceClientService
	Entitlement   *service.EntitlementService
	Showcase      *service.ShowcaseService
	Quote         *service.QuoteService
	TenantPlan    *service.TenantPlanService
	SiteContent   *service.SiteContentService

	// AuthRateLimit guards the password-changing routes, which take a
	// current password as input and are therefore guessable.
	AuthRateLimit gin.HandlerFunc
}

func Register(base *gin.RouterGroup, d Deps) {
	tenants := &tenantsController{svc: d.Tenant, subs: d.Subscription, ent: d.Entitlement, showcase: d.Showcase}
	plans := &plansController{svc: d.Plan}
	admins := &adminsController{svc: d.PlatformUser}
	clients := &clientsController{svc: d.ServiceClient}
	content := &contentController{showcase: d.Showcase, quotes: d.Quote, tenantPlan: d.TenantPlan, site: d.SiteContent}

	g := httpx.Wrap(base)

	t := g.Group("/tenants")
	t.POST("", tenants.Create)
	t.GET("", tenants.List)
	t.GET("/:id", tenants.Get)
	t.PUT("/:id/status", tenants.UpdateStatus)
	t.PUT("/:id/domain", tenants.UpdateDomain)
	t.PUT("/:id/hosts", tenants.UpdateHosts)
	t.POST("/:id/rotate-key", tenants.RotateAPIKey)
	// The subscription lives under the tenant it belongs to: there is one
	// per tenant, so it is a property of the tenant rather than a collection
	// with its own identity.
	t.GET("/:id/subscription", tenants.GetSubscription)
	t.POST("/:id/subscription", tenants.CreateSubscription)
	t.PUT("/:id/subscription/plan", tenants.UpdateSubscriptionPlan)
	t.POST("/:id/subscription/cancel", tenants.CancelSubscription)
	t.POST("/:id/subscription/renew", tenants.RenewSubscription)
	t.PUT("/:id/subscription/billing-day", tenants.SetSubscriptionBillingDay)
	// The entitlement a product service would receive for this tenant,
	// rendered for a human. Being able to see exactly what carwash sees,
	// without impersonating carwash, is what turns "the customer says the
	// module is missing" into a ten-second check.
	t.GET("/:id/entitlement", tenants.GetEntitlement)

	// The operator's own content about a tenant: its case study, the leads
	// it produced, and which pricing cards it displays. None of this affects
	// what the tenant is entitled to run.
	t.GET("/:id/project", content.GetProject)
	t.PUT("/:id/project", content.UpdateProject)
	t.GET("/:id/quotes", content.ListTenantQuotes)
	t.GET("/:id/packages", content.ListTenantPlans)
	t.POST("/:id/packages", content.AssignPlan)
	t.DELETE("/:id/packages/:package_id", content.UnassignPlan)

	p := g.Group("/plans")
	p.POST("", plans.Create)
	p.GET("", plans.List)
	p.GET("/:id", plans.Get)
	p.PUT("/:id", plans.Update)
	p.DELETE("/:id", plans.Delete)

	q := g.Group("/quotes")
	q.GET("", content.ListQuotes)
	q.PUT("/:id/status", content.UpdateQuoteStatus)

	// The marketing site's editable copy. Under /content rather than hung
	// off a tenant: this is the operator's own site, which belongs to no
	// tenant at all.
	copy := g.Group("/content")
	copy.GET("", content.ListPages)
	copy.GET("/:page", content.GetPage)
	copy.PUT("/:page", content.SavePage)

	a := g.Group("/admins")
	a.POST("", admins.Create)
	a.GET("", admins.List)
	a.PUT("/:id/status", admins.UpdateStatus)
	a.Group("", d.AuthRateLimit).PUT("/:id/password", admins.ResetPassword)

	g.Group("/account", d.AuthRateLimit).PUT("/password", admins.ChangePassword)

	// Service clients: the product services allowed to ask this service
	// questions. Managed here rather than by configuration so a key can be
	// revoked without a deploy.
	sc := g.Group("/service-clients")
	sc.POST("", clients.Create)
	sc.GET("", clients.List)
	sc.POST("/:id/revoke", clients.Revoke)
	sc.POST("/:id/rotate", clients.Rotate)
}
