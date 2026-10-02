package private

import (
	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// clientsController manages the product services permitted to call the
// machine-to-machine surface.
//
// These are managed through the API rather than through configuration for
// one reason: revoking a leaked key should not require a deploy. A key in an
// environment variable is revoked by redeploying every instance and hoping
// none were missed; a key in a collection is revoked by one request.
type clientsController struct {
	svc *service.ServiceClientService
}

func (h *clientsController) Create(c *gin.Context) error {
	var body struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	client, rawKey, err := h.svc.Create(c.Request.Context(), body.Name)
	if err != nil {
		return err
	}

	// The key appears here and nowhere else. Put it in that service's
	// TENANTCORE_SERVICE_KEY now; there is no way to retrieve it later.
	response.Created(c, gin.H{
		"service_client": view.ServiceClientOf(client),
		"service_key":    rawKey,
	})
	return nil
}

func (h *clientsController) List(c *gin.Context) error {
	data, err := h.svc.List(c.Request.Context())
	if err != nil {
		return err
	}
	response.OK(c, view.ServiceClientsOf(data))
	return nil
}

// Revoke disables a key immediately. It is a status change, not a delete, so
// the record of which service held which key survives the revocation — which
// is exactly what an incident review needs.
func (h *clientsController) Revoke(c *gin.Context) error {
	if err := h.svc.Revoke(c.Request.Context(), c.Param("id")); err != nil {
		return err
	}
	response.OK(c, gin.H{"revoked": true})
	return nil
}

// Rotate replaces a service key. The new key appears in this response and
// nowhere else; the old one stops working the moment the write lands.
func (h *clientsController) Rotate(c *gin.Context) error {
	client, rawKey, err := h.svc.Rotate(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.OK(c, gin.H{
		"service_client": view.ServiceClientOf(client),
		"service_key":    rawKey,
	})
	return nil
}
