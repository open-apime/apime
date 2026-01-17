package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const dashboardCookie = "dashboard_token"

func DashboardAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString, err := c.Cookie(dashboardCookie)
		if err != nil || tokenString == "" {
			c.Redirect(http.StatusSeeOther, "/dashboard/login")
			c.Abort()
			return
		}

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			clearDashboardCookie(c)
			c.Redirect(http.StatusSeeOther, "/dashboard/login")
			c.Abort()
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if sub, ok := claims["sub"].(string); ok {
				c.Set("userID", sub)
			}
			if email, ok := claims["email"].(string); ok {
				c.Set("userEmail", email)
			}
			if role, ok := claims["role"].(string); ok {
				c.Set("userRole", role)
			}
		}

		c.Next()
	}
}

func SetDashboardCookie(c *gin.Context, token string) {
	c.SetCookie(dashboardCookie, token, 86400, "/", "", false, true)
}

func ClearDashboardCookie(c *gin.Context) {
	clearDashboardCookie(c)
}

func clearDashboardCookie(c *gin.Context) {
	c.SetCookie(dashboardCookie, "", -1, "/", "", false, true)
}
