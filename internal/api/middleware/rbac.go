package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	userSvc "github.com/open-apime/apime/internal/service/user"
)

func RequireAdmin(userService *userSvc.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, exists := c.Get("userID")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "usuário não autenticado"})
			return
		}

		user, err := userService.Get(c.Request.Context(), userID.(string))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "usuário não encontrado"})
			return
		}

		if user.Role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "acesso negado: apenas administradores"})
			return
		}

		c.Set("userRole", user.Role)
		c.Next()
	}
}

func RequireRole(userService *userSvc.Service, requiredRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, exists := c.Get("userID")
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "usuário não autenticado"})
			return
		}

		user, err := userService.Get(c.Request.Context(), userID.(string))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "usuário não encontrado"})
			return
		}

		if user.Role != requiredRole {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "acesso negado: permissão insuficiente"})
			return
		}

		c.Set("userRole", user.Role)
		c.Next()
	}
}

func AddUserInfo(userService *userSvc.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, exists := c.Get("userID")
		if !exists {
			c.Next()
			return
		}

		user, err := userService.Get(c.Request.Context(), userID.(string))
		if err != nil {
			c.Next()
			return
		}

		c.Set("userRole", user.Role)
		c.Set("userEmail", user.Email)
		c.Next()
	}
}
