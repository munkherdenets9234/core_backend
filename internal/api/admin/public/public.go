// Package public is the console surface reachable with no credentials.
//
// It contains login and the emailed-code password reset, and that is the
// intended size. Reset is here because it has to be: someone who has
// forgotten their password cannot present one. Its
// counterpart, admin/private, is mounted behind a superadmin check by
// admin.Register, so a controller there cannot be reached without a token
// whatever a later router edit does.
//
// tenantcore has no public read surface at all, unlike digitalservice. That
// is deliberate: everything here is either a customer record or a price list,
// and neither has an anonymous audience.
package public

import (
	"github.com/eandstravel/tenantcore/internal/api/view"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/httpx"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
)

type Deps struct {
	PlatformUser  *service.PlatformUserService
	PasswordReset *service.PasswordResetService

	// AuthRateLimit guards login. It answers differently for a known and an
	// unknown email in timing if not in text, and it is the front door to
	// the service that administers every tenant.
	AuthRateLimit gin.HandlerFunc
}

func Register(base *gin.RouterGroup, d Deps) {
	c := &authController{svc: d.PlatformUser}
	reset := &passwordResetController{svc: d.PasswordReset}

	g := httpx.Wrap(base)

	// All three share the auth limiter. Reset is if anything the more
	// attractive target: it mails on demand, which costs the sending
	// account's daily quota, and its confirm step is a guess against a
	// six-digit code.
	limited := g.Group("", d.AuthRateLimit)
	limited.POST("/login", c.Login)
	limited.POST("/password-reset/request", reset.Request)
	limited.POST("/password-reset/confirm", reset.Confirm)
}

type authController struct {
	svc *service.PlatformUserService
}

func (h *authController) Login(c *gin.Context) error {
	var body struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		return apierr.BadRequest(err.Error())
	}

	tok, user, err := h.svc.Login(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		return err
	}

	response.OK(c, gin.H{
		"token": tok,
		"user":  view.PlatformUserOf(user),
	})
	return nil
}
