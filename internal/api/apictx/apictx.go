// Package apictx reads the values middleware puts on the request context.
//
// Each accessor is only valid on routes mounted behind the middleware that
// sets it, which is why they live in one place: one file states those
// dependencies, and one file changes if a key ever moves.
package apictx

import (
	"strconv"

	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// UserID is the caller's platform user id, from the verified token. Only
// valid behind Auth.Require.
func UserID(c *gin.Context) string {
	return c.MustGet(middleware.CtxUserID).(string)
}

// ActorID is UserID as an ObjectID, for stamping who changed a plan or a
// subscription. Nil when there is no authenticated user, which is honest:
// the machine-to-machine surface has no actor, and inventing one would put a
// lie in the audit trail.
func ActorID(c *gin.Context) *primitive.ObjectID {
	raw, ok := c.Get(middleware.CtxUserID)
	if !ok {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil
	}
	id, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return nil
	}
	return &id
}

// ServiceName is the product service that authenticated on this request.
// Only valid behind RequireService.
func ServiceName(c *gin.Context) string {
	v, ok := c.Get(middleware.CtxService)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// Page reads the page/limit query pair with a default, clamping both.
//
// A shared helper rather than a strconv.Atoi at each list endpoint whose
// error is discarded — that pattern makes ?page=abc mean page 0 on some
// routes and page 1 on others, depending on who wrote which.
func Page(c *gin.Context, defaultLimit int) (page, limit int) {
	page = atoiOr(c.Query("page"), 1)
	limit = atoiOr(c.Query("limit"), defaultLimit)
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = defaultLimit
	}
	return page, limit
}

func atoiOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
