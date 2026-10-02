package public

import (
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

// passwordResetController is the emailed-code reset for platform admins.
//
// It lives in the credential-free package because it has to: someone who has
// forgotten their password cannot present one. That makes these the most
// exposed routes in the service, and the two rules that follow from it are
// enforced in the service layer rather than here — every outcome of Request
// is the same 200, and every failure of Confirm is the same error — so that
// no edit to a controller can turn either into an account-existence oracle.
type passwordResetController struct {
	svc *service.PasswordResetService
}

// Request mails a one-time code to the address, if it belongs to an active
// platform admin.
//
// Always 200. An unknown address, a suspended account and a mail failure are
// indistinguishable to the caller, on purpose: the list of who administers
// this platform is exactly what someone would want before guessing passwords
// or writing a convincing phishing mail.
func (h *passwordResetController) Request(c *gin.Context) error {
	var body struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	if err := h.svc.Request(c.Request.Context(), body.Email); err != nil {
		// The only error that reaches here is mail being unconfigured, which
		// says nothing about the account and everything about the
		// deployment — worth reporting, because otherwise the caller waits
		// for a code that can never be sent.
		return err
	}

	response.OK(c, gin.H{
		"sent": true,
		// Phrased so it is true whether or not an account exists.
		"message": "If that address belongs to an account, a reset code is on its way.",
	})
	return nil
}

// Confirm verifies the code and sets the new password.
func (h *passwordResetController) Confirm(c *gin.Context) error {
	var body struct {
		Email       string `json:"email" binding:"required,email"`
		Code        string `json:"code" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	if err := h.svc.Confirm(c.Request.Context(), body.Email, body.Code, body.NewPassword); err != nil {
		return err
	}

	// No token is issued here. Signing in afterwards with the new password is
	// one extra step and it keeps this endpoint from being a second way to
	// mint a session — a reset flow that hands back a live token turns any
	// weakness in it directly into account access.
	response.OK(c, gin.H{"reset": true})
	return nil
}
