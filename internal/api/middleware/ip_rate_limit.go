package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/ratelimiter"
)

type IPRateLimitOption struct {
	Enabled        bool
	Requests       int
	WindowSeconds  int
	Limiter        ratelimiter.Limiter
	Logger         *zap.Logger
	SkipPrivateIPs bool
}

// IPRateLimit reforça limites por IP para rotas públicas.
func IPRateLimit(opts IPRateLimitOption) gin.HandlerFunc {
	if !opts.Enabled || opts.Limiter == nil || opts.Requests <= 0 || opts.WindowSeconds <= 0 {
		return func(c *gin.Context) { c.Next() }
	}

	window := time.Duration(opts.WindowSeconds) * time.Second

	return func(c *gin.Context) {
		clientIP := GetClientIP(c)

		if opts.SkipPrivateIPs && IsPrivateIP(clientIP) {
			c.Next()
			return
		}

		key := fmt.Sprintf("ratelimit:ip:%s", hashIP(clientIP))

		res, err := opts.Limiter.Allow(c.Request.Context(), key, opts.Requests, window)
		if err != nil {
			if opts.Logger != nil {
				opts.Logger.Warn("ip rate limit: erro ao consultar limiter", zap.Error(err))
			}
			c.Next()
			return
		}

		if !res.Allowed {
			c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", opts.Requests))
			c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", res.Remaining))
			c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", res.Reset.Unix()))
			c.Header("Retry-After", fmt.Sprintf("%d", int(res.RetryAfter.Seconds())))

			if opts.Logger != nil {
				opts.Logger.Warn("ip rate limit: limite excedido",
					zap.String("ip", clientIP),
				)
			}

			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "muitas tentativas. tente novamente mais tarde",
			})
			return
		}

		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", opts.Requests))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", res.Remaining))
		c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", res.Reset.Unix()))

		c.Next()
	}
}

func hashIP(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}
