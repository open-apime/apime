package middleware

import (
	"fmt"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/pkg/sentryx"
)

// SentryReport reports to Sentry any errors accumulated via c.Error() and any
// 5xx responses, tagged with the route and request-id. It only runs when Sentry
// is enabled (conditional registration in the router).
func SentryReport() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		tags := map[string]string{
			"method": c.Request.Method,
			"route":  routeOrPath(c),
		}
		if rid := c.GetString(HeaderRequestID); rid != "" {
			tags["request_id"] = rid
		}

		for _, ginErr := range c.Errors {
			sentryx.CaptureError(ginErr.Err, tags)
		}

		if c.Writer.Status() >= 500 {
			sentryx.CaptureMessage(
				fmt.Sprintf("HTTP %d %s %s", c.Writer.Status(), c.Request.Method, routeOrPath(c)),
				sentry.LevelError,
				tags,
			)
		}
	}
}

func routeOrPath(c *gin.Context) string {
	if fp := c.FullPath(); fp != "" {
		return fp
	}
	return c.Request.URL.Path
}
