// Package httpx lets handlers return errors instead of writing them.
//
// A gin.HandlerFunc has no return value, so every handler that wanted to fail
// had to render the failure itself. That is how error envelopes drift: one
// endpoint writes {"error": "..."}, the next writes {"message": "..."}, a
// third forgets to abort and writes a second body on top of the first.
//
// Here a handler returns an error and stops. middleware.ErrorHandler renders
// it — once, in one place, in one format.
package httpx

import "github.com/gin-gonic/gin"

// HandlerFunc is a gin handler that may return an error. A nil return means
// the handler already wrote its own success response.
type HandlerFunc func(*gin.Context) error

// H adapts an error-returning handler to gin. The error is attached to the
// context and the chain is aborted; middleware.ErrorHandler does the writing.
func H(fn HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := fn(c); err != nil {
			_ = c.Error(err)
			c.Abort()
		}
	}
}
