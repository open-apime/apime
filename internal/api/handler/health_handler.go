package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/config"
)

type HealthHandler struct{}

func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

func (h *HealthHandler) Register(r *gin.RouterGroup) {
	r.Match([]string{"GET", "HEAD"}, "/", func(c *gin.Context) {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": config.Version,
			"name":    "ApiMe",
		})
	})

	r.Match([]string{"GET", "HEAD"}, "/healthz", func(c *gin.Context) {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": config.Version,
		})
	})
}
