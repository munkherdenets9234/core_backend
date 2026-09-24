package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Logger records one line per request.
//
// It takes the logger as an argument rather than reading the package-level
// logger.Log. That global is only initialised by main, so anything else that
// built a router — a test, a tool, a future second binary — got a nil logger
// and panicked on the first request, *after* the response had been written.
// The result was a logged 500 stapled onto an already-correct 200, and a
// panic stack instead of the actual failure. A dependency the router needs is
// an argument, not an assumption about who ran first.
func Logger(log *zap.Logger) gin.HandlerFunc {
	reqLog := log.WithOptions(zap.WithCaller(false))

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		reqLog.Info("request",
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("ip", c.ClientIP()),
		)
	}
}

func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-API-Key")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
