// Package svc is the machine-to-machine surface: the questions a product
// service asks tenantcore.
//
// It is a third audience, separate from admin/public and admin/private,
// because it authenticates differently (a per-service key, not a human
// token), it answers differently (one document, never a customer record),
// and it has a different availability requirement. A console being slow is an
// inconvenience; this being slow is every product's request path.
//
// The surface is deliberately tiny. tenantcore is not an API a product
// browses — it answers one question, and every route here exists to answer
// some form of it.
package svc

import (
	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/httpx"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Deps struct {
	ServiceClient *service.ServiceClientService
	Entitlement   *service.EntitlementService

	// Mail is nil when no credentials are configured; the route then answers
	// FEATURE_UNAVAILABLE rather than disappearing.
	Mail *mailer.Mailer

	// SendRateLimit guards the mail route. Supplied by the router so the
	// buckets are shared process-wide rather than per group.
	SendRateLimit gin.HandlerFunc
}

// Register mounts the service surface behind the service-key check.
func Register(base *gin.RouterGroup, d Deps) {
	c := &entitlementsController{ent: d.Entitlement}
	notify := &notificationsController{mail: d.Mail}

	g := httpx.Wrap(base.Group("", middleware.RequireService(d.ServiceClient)))

	// By tenant id — what a product uses once it has resolved a tenant of
	// its own, which is the common case during the transitional period where
	// products still hold their own tenants collection.
	g.GET("/entitlements/:tenant_id", c.ByTenantID)

	// By the tenant's API key — what a product uses once it no longer keeps
	// tenants locally. The key arrives in a header rather than the path:
	// a credential in a URL ends up in access logs, proxy logs and browser
	// history, none of which are places to keep one.
	g.GET("/entitlements", c.ByAPIKey)

	// Mail, on behalf of a product. Rate limited on top of the service-key
	// check: the key is a machine credential living in another service's
	// environment, and a limit is what keeps a leaked one from emptying the
	// sending account's daily quota before anyone notices.
	g.Group("", d.SendRateLimit).POST("/notifications/email", notify.Send)
}

type entitlementsController struct {
	ent *service.EntitlementService
}

func (h *entitlementsController) ByTenantID(c *gin.Context) error {
	id, err := primitive.ObjectIDFromHex(c.Param("tenant_id"))
	if err != nil {
		return apierr.BadRequest("invalid tenant id").In(apierr.DomainTenant)
	}

	ent, err := h.ent.For(c.Request.Context(), id)
	if err != nil {
		return err
	}
	response.OK(c, ent)
	return nil
}

func (h *entitlementsController) ByAPIKey(c *gin.Context) error {
	key := c.GetHeader("X-Tenant-Key")
	if key == "" {
		return apierr.BadRequest("missing X-Tenant-Key header").In(apierr.DomainTenant)
	}

	ent, err := h.ent.ForAPIKey(c.Request.Context(), key)
	if err != nil {
		return err
	}
	response.OK(c, ent)
	return nil
}
