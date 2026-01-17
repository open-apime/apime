package server

import (
	"html/template"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/api/handler"
	"github.com/open-apime/apime/internal/api/middleware"
	api_token "github.com/open-apime/apime/internal/service/api_token"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/webhook"
)

type Options struct {
	Env             string
	AuthSecret      string
	HTMLTemplate    *template.Template
	InstanceHandler *handler.InstanceHandler
	MessageHandler  *handler.MessageHandler
	WhatsAppHandler *handler.WhatsAppHandler
	AuthHandler     *handler.AuthHandler
	APITokenHandler *handler.APITokenHandler
	HealthHandler   *handler.HealthHandler
	UserHandler     *handler.UserHandler
	MediaHandler    *handler.MediaHandler
	WebhookPool     *webhook.Pool
	APITokenService interface{}
	InstanceRepo    interface{}
	RateLimit       middleware.RateLimitOption
}

func NewRouter(opts Options) *gin.Engine {
	if opts.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	if opts.HTMLTemplate != nil {
		router.SetHTMLTemplate(opts.HTMLTemplate)
	}
	router.Use(gin.Recovery())
	router.Use(middleware.RequestID())
	router.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Origin", "Content-Type", "Authorization", middleware.HeaderRequestID},
		MaxAge:       12 * time.Hour,
	}))

	api := router.Group("/api")

	opts.HealthHandler.Register(api)
	opts.AuthHandler.Register(api)

	if opts.MediaHandler != nil {
		api.GET("/media/:instanceId/:mediaId", opts.MediaHandler.GetMedia)
	}

	protected := api.Group("")
	if opts.RateLimit.Enabled {
		protected.Use(middleware.RateLimit(opts.RateLimit))
	}
	if opts.APITokenService != nil {
		// Type assertion para *api_token.Service
		if apiTokenSvc, ok := opts.APITokenService.(*api_token.Service); ok {
			var instanceRepo storage.InstanceRepository
			if repo, ok := opts.InstanceRepo.(storage.InstanceRepository); ok {
				instanceRepo = repo
			}
			protected.Use(middleware.AuthWithOptions(middleware.AuthOption{
				JWTSecret:       opts.AuthSecret,
				APITokenService: apiTokenSvc,
				InstanceRepo:    instanceRepo,
			}))
		} else {
			protected.Use(middleware.Auth(opts.AuthSecret))
		}
	} else {
		protected.Use(middleware.Auth(opts.AuthSecret))
	}

	opts.InstanceHandler.Register(protected)
	opts.MessageHandler.Register(protected)
	if opts.WhatsAppHandler != nil {
		opts.WhatsAppHandler.Register(protected)
	}
	opts.APITokenHandler.Register(protected)
	if opts.UserHandler != nil {
		opts.UserHandler.Register(protected)
	}

	return router
}
