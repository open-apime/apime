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
	// Root endpoint with version info
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": config.Version,
			"name":    "ApiMe",
		})
	})

	// Health check endpoint
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"version": config.Version,
		})
	})
}
