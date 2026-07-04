package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/api/middleware"
	"github.com/open-apime/apime/internal/config"
	"github.com/open-apime/apime/internal/service/api_token"
	"github.com/open-apime/apime/internal/service/auth"
	"github.com/open-apime/apime/internal/service/instance"
	"github.com/open-apime/apime/internal/service/user"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type SessionManager interface {
	GetDiagnostics(instanceID string) interface{}
}

type Options struct {
	AuthService     *auth.Service
	InstanceService *instance.Service
	UserService     *user.Service
	APITokenService *api_token.Service
	SessionManager  SessionManager
	JWTSecret       string
	DocsDirectory   string
	BaseURL         string
	Logger          *zap.Logger
	EnableDashboard bool
	Timezone        string
}

type Handler struct {
	auth           *auth.Service
	instances      *instance.Service
	users          *user.Service
	tokens         *api_token.Service
	sessionManager SessionManager
	logger         *zap.Logger
	docsDir        string
	baseURL        string
	timezone       string
}

type PageData struct {
	Title           string
	UserEmail       string
	UserRole        string
	Path            string
	BaseURL         string
	Version         string
	Flash           *Flash
	ContentTemplate string
	Data            any
	Timezone        string
}

func Register(router *gin.Engine, opts Options) error {
	if !opts.EnableDashboard {
		return nil
	}

	tz := configureTimezone(opts.Timezone, opts.Logger)

	h := &Handler{
		auth:           opts.AuthService,
		instances:      opts.InstanceService,
		users:          opts.UserService,
		tokens:         opts.APITokenService,
		sessionManager: opts.SessionManager,
		logger:         opts.Logger,
		docsDir:        opts.DocsDirectory,
		baseURL:        opts.BaseURL,
		timezone:       tz,
	}

	router.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusSeeOther, "/dashboard")
	})

	staticFiles, _ := fs.Sub(staticFS, "static")
	router.StaticFS("/static", http.FS(staticFiles))

	router.GET("/dashboard/login", h.showLogin)
	router.POST("/dashboard/login", h.handleLogin)
	router.GET("/dashboard/logout", h.logout)

	group := router.Group("/dashboard")
	group.Use(middleware.DashboardAuth(opts.JWTSecret, h.users))

	group.GET("", h.overview)
	group.GET("/instances", func(c *gin.Context) {
		c.Redirect(http.StatusSeeOther, "/dashboard")
	})
	group.POST("/instances", h.createInstance)
	group.POST("/instances/:id/update", h.updateInstance)
	group.POST("/instances/:id/token", h.rotateInstanceToken)
	group.GET("/instances/:id/qr", h.showInstanceQR)
	group.GET("/instances/:id/qr/status", h.getInstanceQRStatus)
	group.GET("/instances/:id/qr/image", h.getQRImage)
	group.POST("/instances/:id/disconnect", h.disconnectInstance)
	group.GET("/instances/:id/diagnostics", h.instanceDiagnostics)
	group.POST("/instances/:id/delete", h.deleteInstance)

	adminGroup := group.Group("")
	adminGroup.Use(middleware.RequireAdmin(opts.UserService))
	adminGroup.GET("/users", h.usersPage)
	adminGroup.POST("/users", h.createUser)
	adminGroup.POST("/users/:id/password", h.updateUserPassword)
	adminGroup.POST("/users/:id/delete", h.deleteUser)
	adminGroup.GET("/users/:id/tokens", h.listUserTokens)
	adminGroup.POST("/users/:id/tokens", h.createUserToken)
	adminGroup.POST("/users/:id/tokens/:tokenID/delete", h.deleteUserToken)

	group.GET("/docs", h.docsPage)
	group.GET("/docs/openapi", h.downloadOpenAPI)

	return nil
}

func (h *Handler) pageData(c *gin.Context, title, content string, data any) PageData {
	flash := flashFromQuery(c)
	path := c.FullPath()
	if path == "" {
		path = c.Request.URL.Path
	}
	return PageData{
		Title:           title,
		UserEmail:       c.GetString("userEmail"),
		UserRole:        c.GetString("userRole"),
		Path:            path,
		BaseURL:         h.baseURL,
		Version:         config.Version,
		Flash:           flash,
		ContentTemplate: content,
		Data:            data,
		Timezone:        h.timezone,
	}
}

func flashFromQuery(c *gin.Context) *Flash {
	if msg := c.Query("success"); msg != "" {
		return &Flash{Type: "success", Message: msg}
	}
	if msg := c.Query("error"); msg != "" {
		return &Flash{Type: "error", Message: msg}
	}
	return nil
}

func redirectWithMessage(c *gin.Context, target, kind, message string) {
	values := url.Values{}
	if message != "" {
		values.Set(kind, message)
	}
	if len(values) > 0 {
		c.Redirect(http.StatusSeeOther, target+"?"+values.Encode())
		return
	}
	c.Redirect(http.StatusSeeOther, target)
}

func (h *Handler) renderError(c *gin.Context, err error) {
	h.logger.Error("erro no dashboard", zap.Error(err))
	c.HTML(http.StatusInternalServerError, "layout", PageData{
		Title:           "Erro",
		ContentTemplate: "error_content",
		Flash:           &Flash{Type: "error", Message: "Ocorreu um erro inesperado."},
	})
}
