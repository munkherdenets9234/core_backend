package middleware

import (
	"context"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/gin-gonic/gin"
)

// ServiceAuthenticator resolves a raw service key. Satisfied by
// service.ServiceClientService; an interface here so the middleware can be
// tested without a database.
type ServiceAuthenticator interface {
	Authenticate(ctx context.Context, rawKey string) (*models.ServiceClient, error)
}

// RequireService gates the machine-to-machine surface.
//
// Product services authenticate with their own key in X-Service-Key — one
// key per service, never a shared secret. When carwash's key leaks, carwash's
// key is revoked and nothing else has to be redeployed; a shared secret makes
// every leak a platform-wide rotation.
//
// This is deliberately NOT the same credential as a tenant's API key. A
// tenant key is published in storefront JavaScript by design; anyone who
// could read one must not thereby be able to ask the platform about every
// tenant. The two live in different headers and different collections so they
// cannot be confused for one another.
func RequireService(auth ServiceAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("X-Service-Key")
		if key == "" {
			fail(c, apierr.Unauthorized("missing X-Service-Key header").In(apierr.DomainService))
			return
		}

		client, err := auth.Authenticate(c.Request.Context(), key)
		if err != nil {
			fail(c, err)
			return
		}

		c.Set(CtxService, client.Name)
		c.Next()
	}
}
