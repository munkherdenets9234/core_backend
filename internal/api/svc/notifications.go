package svc

import (
	"github.com/eandstravel/tenantcore/internal/api/apictx"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// notificationsController delivers mail on behalf of a product service.
//
// The division of labour matters and is easy to get backwards: the PRODUCT
// owns the flow, tenantcore owns delivery. digitalservice holds tenant users,
// mints the reset token, builds the link and decides who may have one; it
// then asks this endpoint to put the message in an inbox. tenantcore knows
// nothing about the token and cannot validate it.
//
// It is the other way round from entitlements, where tenantcore is the
// authority and the product is asking. Here tenantcore is a utility. Keeping
// that straight is what stops this from growing into "tenantcore manages
// other services' users", which would invert the dependency the whole split
// is built on.
type notificationsController struct {
	mail *mailer.Mailer
}

type sendEmailRequest struct {
	To       string            `json:"to" binding:"required,email"`
	Template string            `json:"template" binding:"required"`
	Data     map[string]string `json:"data"`
	// TenantID is the tenant the mail is for. Optional, and used only to
	// label the mail log; it does not change what is sent.
	TenantID string `json:"tenant_id" binding:"omitempty,hexadecimal,len=24"`
}

// Send delivers one templated message.
//
// The caller names a template and supplies data. It cannot supply a subject,
// a body, or a From — those come from the template and from configuration.
// That is deliberate: a service key is a machine credential sitting in
// another service's environment, and if one leaks the damage should be
// "someone triggered a password-reset mail" and not "someone sent arbitrary
// mail from our address to anyone they liked".
func (h *notificationsController) Send(c *gin.Context) error {
	if !h.mail.Available() {
		// Not an error the caller can fix, and not a reason to pretend the
		// mail was sent: a password reset whose mail silently vanishes is
		// worse than one that fails loudly, because the user waits for an
		// email that is never coming.
		return apierr.FeatureUnavailable("email")
	}

	var req sendEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return apierr.BadRequest(err.Error())
	}

	tmpl, ok := mailer.Known(req.Template)
	if !ok {
		return apierr.BadRequest("unknown template: " + req.Template)
	}

	// Named so the mail log can say which service asked, and for whom.
	src := mailer.ServiceSource(apictx.ServiceName(c), req.TenantID)
	if err := h.mail.SendFrom(src, req.To, tmpl, req.Data); err != nil {
		// Wrapped rather than surfaced: the underlying error can name the
		// SMTP host and the account, which belongs in our log and not in a
		// response body another service may echo.
		return apierr.Internal(err)
	}

	// No message id: this does not report delivery, only acceptance by the
	// relay. Saying "sent" would be a stronger claim than SMTP supports.
	response.OK(c, gin.H{"accepted": true, "template": string(tmpl)})
	return nil
}
