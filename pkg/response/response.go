// Package response owns the wire format. Every HTTP body this API writes is
// produced here, so the envelope cannot drift between endpoints.
//
// No handler writes an error response itself. Handlers return an error
// (see pkg/httpx) and middleware.ErrorHandler calls Err exactly once.
package response

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/gin-gonic/gin"
)

type envelope struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
	Meta    *Meta       `json:"meta,omitempty"`
	Error   *errBody    `json:"error,omitempty"`
}

// errBody is the structured half of an error response. Clients branch on Code.
type errBody struct {
	Domain     string `json:"domain"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	StackTrace string `json:"stack_trace,omitempty"` // development only
}

// Meta describes which slice of a list a response carries.
type Meta struct {
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
}

// OK writes a 200 success response.
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, envelope{Success: true, Data: data})
}

// Created writes a 201 success response.
func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, envelope{Success: true, Data: data})
}

// List writes a 200 success response for one page of a list.
func List(c *gin.Context, data interface{}, meta Meta) {
	c.JSON(http.StatusOK, envelope{Success: true, Data: data, Meta: &meta})
}

// NoContent writes a 204.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Err writes the error response for err.
//
// The top-level "message" field is kept alongside the structured "error"
// object on purpose: it is what every existing client reads. New clients
// should branch on error.code, which is stable, rather than on the prose.
// Dropping "message" would be a breaking change for no gain.
//
// The stack trace is included only in development. In production it would
// hand a caller our package layout for free.
func Err(c *gin.Context, err error, devMode bool) {
	var appErr *apierr.APIError
	if !errors.As(err, &appErr) {
		appErr = apierr.Internal(err)
	}

	if appErr.RetryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(appErr.RetryAfter))
	}

	body := &errBody{
		Domain:  appErr.Domain,
		Code:    appErr.Code,
		Message: appErr.Message,
	}
	if devMode && appErr.Stack != "" {
		body.StackTrace = appErr.Stack
	}

	c.AbortWithStatusJSON(appErr.HTTPStatus, envelope{
		Success: false,
		Message: appErr.Message,
		Error:   body,
	})
}
