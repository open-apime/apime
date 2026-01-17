package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/ratelimiter"
)

// RateLimitOption parametriza o middleware de limite por token.
type RateLimitOption struct {
	Enabled  bool
	Requests int
	Window   time.Duration
	Prefix   string
	Limiter  ratelimiter.Limiter
	Logger   *zap.Logger
}

// RateLimit aplica contagem de requisições por token usando Redis.
func RateLimit(opts RateLimitOption) gin.HandlerFunc {
	if !opts.Enabled || opts.Limiter == nil || opts.Requests <= 0 || opts.Window <= 0 {
		return func(c *gin.Context) { c.Next() }
	}

	prefix := opts.Prefix
	if prefix == "" {
		prefix = "ratelimit:api"
	}

	return func(c *gin.Context) {
		token := extractBearerToken(c.GetHeader("Authorization"))
		if token == "" {
			c.Next()
			return
		}

		key := fmt.Sprintf("%s:%s", prefix, hashToken(token))

		res, err := opts.Limiter.Allow(c.Request.Context(), key, opts.Requests, opts.Window)
		if err != nil {
			if opts.Logger != nil {
				opts.Logger.Warn("rate limit: erro ao consultar limiter", zap.Error(err))
			}
			c.Next()
			return
		}

		if !res.Allowed {
			c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", opts.Requests))
			c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", res.Remaining))
			c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", res.Reset.Unix()))
			c.Header("Retry-After", fmt.Sprintf("%d", int(res.RetryAfter.Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "limite de requisições excedido",
			})
			return
		}

		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", opts.Requests))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", res.Remaining))
		c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", res.Reset.Unix()))

		c.Next()
	}
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
