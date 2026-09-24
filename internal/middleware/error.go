package middleware

import (
	"errors"

	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/response"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ErrorHandler is the single place an error becomes an HTTP response.
//
// It reads whatever httpx.H attached via c.Error() and renders it. Anything
// that is not already an *apierr.APIError becomes a 500 with its cause
// logged and withheld from the caller — an error that escaped the taxonomy is
// a bug, and a bug's text is not a contract.
//
// devMode controls one thing only: whether the stack trace reaches the client.
func ErrorHandler(log *zap.Logger, devMode bool) gin.HandlerFunc {
	// Caller is the middleware itself on every line, which is noise.
	errLog := log.WithOptions(zap.WithCaller(false))

	return func(c *gin.Context) {
		c.Next()

		if len(c.Errors) == 0 {
			return
		}

		err := c.Errors.Last().Err

		var appErr *apierr.APIError
		if !errors.As(err, &appErr) {
			appErr = apierr.Internal(err)
		}

		fields := []zap.Field{
			zap.String("domain", appErr.Domain),
			zap.String("code", appErr.Code),
			zap.Int("status", appErr.HTTPStatus),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
		}
		if appErr.Err != nil {
			fields = append(fields, zap.Error(appErr.Err))
		}
		if devMode && appErr.Stack != "" {
			fields = append(fields, zap.String("stack", appErr.Stack))
		}

		// 4xx is the caller's problem and routine; 5xx is ours and is not.
		// Logging both at error level makes the dashboard useless.
		if appErr.HTTPStatus >= 500 {
			errLog.Error(appErr.Message, fields...)
		} else {
			errLog.Warn(appErr.Message, fields...)
		}

		response.Err(c, appErr, devMode)
	}
}

// Recovery turns a panic into a logged 500 rather than a dropped connection.
//
// gin's own Recovery writes its response directly, which would bypass the
// envelope above; this one routes the panic through the same renderer as
// every other error, so a client cannot tell a panic from any other 500.
func Recovery(log *zap.Logger, devMode bool) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered interface{}) {
		log.Error("panic recovered",
			zap.Any("panic", recovered),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Stack("stack"),
		)
		response.Err(c, apierr.Internal(errFromPanic(recovered)), devMode)
	})
}

func errFromPanic(v interface{}) error {
	if err, ok := v.(error); ok {
		return err
	}
	return errors.New("panic: " + toString(v))
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "non-string panic value"
}
