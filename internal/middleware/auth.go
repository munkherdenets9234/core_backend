package middleware

import (
	"strings"

	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/token"
	"github.com/gin-gonic/gin"
)

// Context keys. Handlers read these through internal/api/apictx rather than
// by string literal, so a typo is a compile error instead of a nil lookup at
// request time.
const (
	CtxUserID  = "user_id"
	CtxRole    = "role"
	CtxService = "service_client"
)

// Auth verifies a tenantcore-issued Ed25519 token.
type Auth struct {
	verifier *token.Verifier
}

func NewAuth(verifier *token.Verifier) *Auth {
	return &Auth{verifier: verifier}
}

// Require verifies the bearer token and, when roles are given, checks the
// token carries one of them.
//
// Note what this does NOT do: reach into the database to confirm the user
// still exists and is still active. The token is self-contained and trusted
// until it expires, which is the property that lets product services verify
// it with nothing but a public key. The cost is that suspending a platform
// user does not take effect until their current token expires — so keep
// TOKEN_TTL short. Making suspension immediate means a lookup per request,
// which is exactly the coupling this design removes; if that becomes
// necessary it belongs behind a revocation list, not a user lookup.
func (a *Auth) Require(roles ...token.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			fail(c, apierr.Unauthorized("missing authorization header"))
			return
		}

		claims, err := a.verifier.Verify(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			// token.Verify collapses every failure into one error on
			// purpose: expired, malformed, wrong key and impossible role are
			// all the same answer to the caller.
			fail(c, apierr.Unauthorized(err.Error()))
			return
		}

		if len(roles) > 0 {
			allowed := false
			for _, r := range roles {
				if claims.Role == r {
					allowed = true
					break
				}
			}
			if !allowed {
				fail(c, apierr.Forbidden(""))
				return
			}
		}

		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxRole, string(claims.Role))
		c.Next()
	}
}
