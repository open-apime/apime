package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	apiTokenSvc "github.com/open-apime/apime/internal/service/api_token"
	"github.com/open-apime/apime/internal/storage"
)

// AuthOption configura o middleware de autenticação.
type AuthOption struct {
	JWTSecret       string
	APITokenService *apiTokenSvc.Service
	InstanceRepo    storage.InstanceRepository
}

func Auth(secret string) gin.HandlerFunc {
	return AuthWithOptions(AuthOption{JWTSecret: secret})
}

func AuthWithOptions(opts AuthOption) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token ausente"})
			return
		}
		tokenString := strings.TrimPrefix(header, "Bearer ")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return []byte(opts.JWTSecret), nil
		})
		if err == nil && token.Valid {
			if claims, ok := token.Claims.(jwt.MapClaims); ok {
				if sub, ok := claims["sub"].(string); ok {
					c.Set("userID", sub)
					c.Set("authType", "user_jwt")
				}
			}
			c.Next()
			return
		}

		if opts.APITokenService != nil {
			apiToken, err := opts.APITokenService.ValidateToken(c.Request.Context(), tokenString)
			if err == nil {
				c.Set("userID", apiToken.UserID)
				c.Set("authType", "api_token")
				c.Next()
				return
			}
		}

		if opts.InstanceRepo != nil {
			hashBytes := sha256.Sum256([]byte(tokenString))
			hash := hex.EncodeToString(hashBytes[:])
			inst, err := opts.InstanceRepo.GetByTokenHash(c.Request.Context(), hash)
			if err == nil && inst.ID != "" {
				c.Set("instanceID", inst.ID)
				c.Set("authType", "instance_token")
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token inválido"})
	}
}
