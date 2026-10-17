package private

import (
	"errors"
	"io"

	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// onboardingController serves a tenant's onboarding email. The recipient is
// decided by the service; the request carries only an optional note and the
// resend choice. The note is never logged.
type onboardingController struct {
	svc *service.OnboardingService
}

func (h *onboardingController) Status(c *gin.Context) error {
	st, err := h.svc.Status(c.Request.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	response.OK(c, st)
	return nil
}

// Preview renders the mail ({subject, text, html}) without sending it.
func (h *onboardingController) Preview(c *gin.Context) error {
	p, err := h.svc.Preview(c.Request.Context(), c.Param("id"), c.Query("note"))
	if err != nil {
		return err
	}
	response.OK(c, p)
	return nil
}

// Send takes an optional body: {"note": "...", "resend": true}. An empty body
// is a first send.
func (h *onboardingController) Send(c *gin.Context) error {
	var in service.OnboardingSendInput
	if err := c.ShouldBindJSON(&in); err != nil && !errors.Is(err, io.EOF) {
		return apierr.BadRequest("invalid request body").In(apierr.DomainTenant)
	}
	st, err := h.svc.Send(c.Request.Context(), c.Param("id"), in)
	if err != nil {
		return err
	}
	response.OK(c, st)
	return nil
}
