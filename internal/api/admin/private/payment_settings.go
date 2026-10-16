package private

import (
	"github.com/eandstravel/tenantcore/internal/api/apictx"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// paymentSettingsController serves the platform's own bank details. The body
// is never logged and never echoed back beyond the saved document.
type paymentSettingsController struct {
	svc *service.PaymentSettingsService
}

func (h *paymentSettingsController) Get(c *gin.Context) error {
	p, err := h.svc.Get(c.Request.Context())
	if err != nil {
		return err
	}
	response.OK(c, p)
	return nil
}

func (h *paymentSettingsController) Put(c *gin.Context) error {
	var in service.PaymentSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	p, err := h.svc.Put(c.Request.Context(), in, apictx.ActorID(c))
	if err != nil {
		return err
	}
	response.OK(c, p)
	return nil
}
