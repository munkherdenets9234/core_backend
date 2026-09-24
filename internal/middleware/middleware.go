// Package middleware holds the cross-cutting request handlers.
//
// Middleware here never writes a response body. It attaches an
// *apierr.APIError via fail() and aborts; ErrorHandler renders it in the same
// envelope as every handler error, so a caller cannot tell from the shape of
// a 401 whether it was refused by middleware or by a controller.
package middleware

import "github.com/gin-gonic/gin"

// fail aborts the chain with err, leaving the rendering to ErrorHandler.
func fail(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}
