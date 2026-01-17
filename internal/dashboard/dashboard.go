package dashboard

import (
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/api/middleware"
	"github.com/open-apime/apime/internal/service/api_token"
	"github.com/open-apime/apime/internal/service/auth"
	device_config "github.com/open-apime/apime/internal/service/device_config"
	"github.com/open-apime/apime/internal/service/instance"
	"github.com/open-apime/apime/internal/service/user"
	"github.com/open-apime/apime/internal/storage/model"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type SessionManager interface {
	GetDiagnostics(instanceID string) interface{}
}

type Options struct {
	AuthService         *auth.Service
	InstanceService     *instance.Service
	UserService         *user.Service
	APITokenService     *api_token.Service
	DeviceConfigService *device_config.Service
	SessionManager      SessionManager
	JWTSecret           string
	DocsDirectory       string
	BaseURL             string
	Logger              *zap.Logger
	EnableDashboard     bool
}

type Handler struct {
	auth           *auth.Service
	instances      *instance.Service
	users          *user.Service
	tokens         *api_token.Service
	deviceConfig   *device_config.Service
	sessionManager SessionManager
	logger         *zap.Logger
	docsDir        string
	baseURL        string
}

type PageData struct {
	Title           string
	UserEmail       string
	UserRole        string
	Path            string
	BaseURL         string
	Flash           *Flash
	ContentTemplate string
	Data            any
}

type Flash struct {
	Type    string
	Message string
}

func HTMLTemplate() *template.Template {
	return template.Must(template.New("T").Funcs(templateFuncMap()).ParseFS(templateFS, "templates/*.tmpl"))
}

func Register(router *gin.Engine, opts Options) error {
	if !opts.EnableDashboard {
		return nil
	}

	h := &Handler{
		auth:           opts.AuthService,
		instances:      opts.InstanceService,
		users:          opts.UserService,
		tokens:         opts.APITokenService,
		deviceConfig:   opts.DeviceConfigService,
		sessionManager: opts.SessionManager,
		logger:         opts.Logger,
		docsDir:        opts.DocsDirectory,
		baseURL:        opts.BaseURL,
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

	group.GET("/settings", h.settingsPage)
	group.POST("/settings", h.updateSettings)

	group.GET("/docs", h.docsPage)
	group.GET("/docs/openapi", h.downloadOpenAPI)

	return nil
}

func templateFuncMap() template.FuncMap {
	return template.FuncMap{
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.Local().Format("02/01/2006 15:04")
		},
		"formatOptionalTime": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.Local().Format("02/01/2006 15:04")
		},
		"statusBadge": func(status model.InstanceStatus) string {
			switch status {
			case model.InstanceStatusActive:
				return "status-active"
			case model.InstanceStatusPending:
				return "status-pending"
			case model.InstanceStatusDisconnected:
				return "status-disconnected"
			default:
				return "status-error"
			}
		},
		"translateStatus": func(status model.InstanceStatus) string {
			switch status {
			case model.InstanceStatusActive:
				return "Ativo"
			case model.InstanceStatusPending:
				return "Aguardando"
			case model.InstanceStatusDisconnected:
				return "Desconectado"
			case model.InstanceStatusError:
				return "Erro"
			default:
				return string(status)
			}
		},
		"div": func(a, b any) float64 {
			toFloat := func(v any) float64 {
				switch i := v.(type) {
				case int:
					return float64(i)
				case int8:
					return float64(i)
				case int16:
					return float64(i)
				case int32:
					return float64(i)
				case int64:
					return float64(i)
				case uint:
					return float64(i)
				case uint8:
					return float64(i)
				case uint16:
					return float64(i)
				case uint32:
					return float64(i)
				case uint64:
					return float64(i)
				case float32:
					return float64(i)
				case float64:
					return i
				default:
					return 0
				}
			}
			valA := toFloat(a)
			valB := toFloat(b)
			if valB == 0 {
				return 0
			}
			return valA / valB
		},
	}
}

func (h *Handler) showLogin(c *gin.Context) {
	data := h.pageData(c, "", "login_content", nil)
	c.HTML(http.StatusOK, "layout", data)
}

func (h *Handler) handleLogin(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")

	token, err := h.auth.Login(c.Request.Context(), email, password)
	if err != nil {
		redirectWithMessage(c, "/dashboard/login", "error", "Credenciais inválidas")
		return
	}

	middleware.SetDashboardCookie(c, token)

	redirectWithMessage(c, "/dashboard", "success", "Bem-vindo!")
}

func (h *Handler) logout(c *gin.Context) {
	middleware.ClearDashboardCookie(c)
	flash := url.Values{}
	flash.Set("success", "Sessão encerrada com sucesso.")
	c.Redirect(http.StatusSeeOther, "/dashboard/login?"+flash.Encode())
}

func (h *Handler) overview(c *gin.Context) {
	ctx := c.Request.Context()

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	var instances []model.Instance
	var err error

	if userRole == "admin" {
		instances, err = h.instances.List(ctx)
	} else {
		instances, err = h.instances.ListByUser(ctx, userID, userRole)
	}

	if err != nil {
		h.renderError(c, err)
		return
	}

	data := map[string]any{
		"Instances":                  instances,
		"NewInstanceToken":           c.Query("newInstanceToken"),
		"NewInstanceTokenInstanceID": c.Query("newInstanceTokenInstanceID"),
		"UserRole":                   userRole,
	}

	page := h.pageData(c, "", "instances_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) createInstance(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		redirectWithMessage(c, "/dashboard", "error", "Nome é obrigatório.")
		return
	}

	userID := c.GetString("userID")

	_, err := h.instances.Create(c.Request.Context(), instance.CreateInput{
		Name:          name,
		WebhookURL:    strings.TrimSpace(c.PostForm("webhook_url")),
		WebhookSecret: strings.TrimSpace(c.PostForm("webhook_secret")),
		OwnerUserID:   userID,
	})
	if err != nil {
		h.logger.Warn("erro ao criar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao criar instância.")
		return
	}

	redirectWithMessage(c, "/dashboard", "success", "Instância criada com sucesso.")
}

func (h *Handler) updateInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard", "error", "Instância inválida.")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		redirectWithMessage(c, "/dashboard", "error", "Nome é obrigatório.")
		return
	}
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	callerID := userID
	if userRole == "admin" {
		callerID = "admin"
	}

	_, err := h.instances.UpdateByUser(c.Request.Context(), id, instance.UpdateInput{
		Name:          name,
		WebhookURL:    strings.TrimSpace(c.PostForm("webhook_url")),
		WebhookSecret: strings.TrimSpace(c.PostForm("webhook_secret")),
		OwnerUserID:   callerID,
	})
	if err != nil {
		h.logger.Warn("erro ao atualizar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao atualizar instância.")
		return
	}
	redirectWithMessage(c, "/dashboard", "success", "Instância atualizada.")
}

func (h *Handler) rotateInstanceToken(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard", "error", "Instância inválida.")
		return
	}
	// Obter informações do usuário do contexto
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	plain, err := h.instances.RotateTokenByUser(c.Request.Context(), id, userID, userRole)
	if err != nil {
		h.logger.Warn("erro ao rotacionar token da instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao gerar token da instância.")
		return
	}
	values := url.Values{}
	values.Set("success", "Token da instância gerado. Copie o valor exibido abaixo.")
	values.Set("newInstanceToken", plain)
	values.Set("newInstanceTokenInstanceID", id)
	c.Redirect(http.StatusSeeOther, "/dashboard?"+values.Encode())
}

func (h *Handler) showInstanceQR(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard", "error", "Instância inválida.")
		return
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	code, err := h.instances.GetQRByUser(ctx, id, userID, userRole)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			h.logger.Info("request cancelado pelo cliente durante geração de QR", zap.String("instance_id", id))
			redirectWithMessage(c, "/dashboard", "error", "Requisição cancelada.")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.logger.Warn("timeout ao gerar QR", zap.String("instance_id", id))
			redirectWithMessage(c, "/dashboard", "error", "Timeout ao gerar QR code.")
			return
		}

		h.logger.Warn("erro ao gerar QR", zap.String("instance_id", id), zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao obter QR code: "+err.Error())
		return
	}

	if code == "" {
		redirectWithMessage(c, "/dashboard", "error", "QR code não disponível (já conectado?).")
		return
	}

	png, err := qrcode.Encode(code, qrcode.Medium, 256)
	if err != nil {
		h.logger.Warn("erro ao construir PNG do QR", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "QR code inválido.")
		return
	}

	data := map[string]any{
		"InstanceID": id,
		"QRCode":     base64.StdEncoding.EncodeToString(png),
		"Raw":        code,
	}
	page := h.pageData(c, "", "instance_qr_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) getQRImage(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	code, err := h.instances.GetQRByUser(ctx, id, userID, userRole)
	if err != nil || code == "" {
		c.Status(http.StatusNotFound)
		return
	}

	png, err := qrcode.Encode(code, qrcode.Medium, 256)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	c.Data(http.StatusOK, "image/png", png)
}

func (h *Handler) getInstanceQRStatus(c *gin.Context) {
	id := c.Param("id")

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	instance, err := h.instances.GetByUser(c.Request.Context(), id, userID, userRole)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "instância não encontrada"})
		return
	}

	hasQR := false
	qrCode := ""
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	code, err := h.instances.GetQRByUser(ctx, id, userID, userRole)
	if err == nil && code != "" {
		hasQR = true
		qrCode = code
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    instance.Status,
		"hasQR":     hasQR,
		"qrCode":    qrCode,
		"connected": instance.Status == model.InstanceStatusActive,
	})
}

func (h *Handler) instanceDiagnostics(c *gin.Context) {
	instanceID := c.Param("id")
	ctx := c.Request.Context()

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	inst, err := h.instances.GetByUser(ctx, instanceID, userID, userRole)
	if err != nil {
		h.renderError(c, err)
		return
	}

	var diagnostics interface{}
	if h.sessionManager != nil {
		diagnostics = h.sessionManager.GetDiagnostics(instanceID)
	}

	data := map[string]any{
		"Instance":    inst,
		"Diagnostics": diagnostics,
	}

	page := h.pageData(c, "", "instance_diagnostics_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) disconnectInstance(c *gin.Context) {
	id := c.Param("id")
	// Obter informações do usuário do contexto
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	if err := h.instances.DisconnectByUser(c.Request.Context(), id, userID, userRole); err != nil {
		h.logger.Warn("erro ao desconectar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Não foi possível desconectar.")
		return
	}
	redirectWithMessage(c, "/dashboard", "success", "Instância desconectada.")
}

func (h *Handler) deleteInstance(c *gin.Context) {
	id := c.Param("id")
	// Obter informações do usuário do contexto
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	if err := h.instances.DeleteByUser(c.Request.Context(), id, userID, userRole); err != nil {
		h.logger.Warn("erro ao deletar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Não foi possível deletar instância.")
		return
	}
	redirectWithMessage(c, "/dashboard", "success", "Instância deletada com sucesso.")
}

func (h *Handler) usersPage(c *gin.Context) {
	users, err := h.users.List(c.Request.Context())
	if err != nil {
		h.renderError(c, err)
		return
	}

	adminCount := 0
	for _, u := range users {
		if u.Role == "admin" {
			adminCount++
		}
	}

	data := map[string]any{
		"Users":         users,
		"NewUserToken":  c.Query("newUserToken"),
		"NewUserEmail":  c.Query("newUserEmail"),
		"CurrentUserID": c.GetString("userID"),
		"AdminCount":    adminCount,
	}
	page := h.pageData(c, "", "users_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) createUser(c *gin.Context) {
	input := user.CreateInput{
		Email:    strings.TrimSpace(c.PostForm("email")),
		Password: c.PostForm("password"),
		Role:     strings.TrimSpace(c.PostForm("role")),
	}
	user, token, err := h.users.Create(c.Request.Context(), input)
	if err != nil {
		h.logger.Warn("erro ao criar usuário", zap.Error(err))
		redirectWithMessage(c, "/dashboard/users", "error", "Não foi possível criar usuário.")
		return
	}

	values := url.Values{}
	values.Set("success", "Usuário criado com sucesso! Copie o token de acesso abaixo.")
	values.Set("newUserToken", token)
	values.Set("newUserEmail", user.Email)
	c.Redirect(http.StatusSeeOther, "/dashboard/users?"+values.Encode())
}

func (h *Handler) updateUserPassword(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard/users", "error", "Usuário inválido.")
		return
	}
	if err := h.users.UpdatePassword(c.Request.Context(), id, c.PostForm("password")); err != nil {
		h.logger.Warn("erro ao atualizar senha", zap.Error(err))
		redirectWithMessage(c, "/dashboard/users", "error", "Falha ao atualizar senha.")
		return
	}
	redirectWithMessage(c, "/dashboard/users", "success", "Senha atualizada.")
}

func (h *Handler) deleteUser(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard/users", "error", "Usuário inválido.")
		return
	}
	if current := c.GetString("userID"); current == id {
		redirectWithMessage(c, "/dashboard/users", "error", "Você não pode remover o próprio usuário logado.")
		return
	}

	if err := h.users.Delete(c.Request.Context(), id); err != nil {
		h.logger.Warn("erro ao remover usuário", zap.Error(err))
		if err.Error() == "não é possível remover o último administrador" {
			redirectWithMessage(c, "/dashboard/users", "error", "Não é possível remover o único administrador do sistema.")
			return
		}
		redirectWithMessage(c, "/dashboard/users", "error", "Falha ao remover usuário.")
		return
	}
	redirectWithMessage(c, "/dashboard/users", "success", "Usuário removido.")
}

func (h *Handler) listUserTokens(c *gin.Context) {
	if h.tokens == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "serviço de tokens indisponível"})
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usuário inválido"})
		return
	}
	tokens, err := h.tokens.ListByUser(c.Request.Context(), userID)
	if err != nil {
		h.logger.Warn("erro ao listar tokens do usuário", zap.Error(err), zap.String("user_id", userID))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao listar tokens"})
		return
	}
	result := make([]gin.H, len(tokens))
	for i, t := range tokens {
		result[i] = gin.H{
			"id":         t.ID,
			"name":       t.Name,
			"lastUsedAt": t.LastUsedAt,
			"expiresAt":  t.ExpiresAt,
			"isActive":   t.IsActive,
			"createdAt":  t.CreatedAt,
			"updatedAt":  t.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, gin.H{"tokens": result})
}

func (h *Handler) createUserToken(c *gin.Context) {
	if h.tokens == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "serviço de tokens indisponível"})
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usuário inválido"})
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nome é obrigatório"})
		return
	}
	token, plain, err := h.tokens.Create(c.Request.Context(), userID, name, nil)
	if err != nil {
		h.logger.Warn("erro ao criar token de usuário", zap.Error(err), zap.String("user_id", userID))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao gerar token"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"token": plain,
		"entry": gin.H{
			"id":         token.ID,
			"name":       token.Name,
			"lastUsedAt": token.LastUsedAt,
			"expiresAt":  token.ExpiresAt,
			"isActive":   token.IsActive,
			"createdAt":  token.CreatedAt,
			"updatedAt":  token.UpdatedAt,
		},
	})
}

func (h *Handler) deleteUserToken(c *gin.Context) {
	if h.tokens == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "serviço de tokens indisponível"})
		return
	}
	tokenID := strings.TrimSpace(c.Param("tokenID"))
	if tokenID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token inválido"})
		return
	}
	if err := h.tokens.Delete(c.Request.Context(), tokenID); err != nil {
		h.logger.Warn("erro ao remover token de usuário", zap.Error(err), zap.String("token_id", tokenID))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "falha ao remover token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) docsPage(c *gin.Context) {
	preview, exists := h.readOpenAPIPreview()
	data := map[string]any{
		"HasSpec": exists,
		"Preview": preview,
	}
	page := h.pageData(c, "", "docs_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) downloadOpenAPI(c *gin.Context) {
	path := h.openAPIPath()
	if path == "" {
		c.String(http.StatusNotFound, "Arquivo não disponível")
		return
	}
	c.FileAttachment(path, filepath.Base(path))
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
		Flash:           flash,
		ContentTemplate: content,
		Data:            data,
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

func (h *Handler) openAPIPath() string {
	if _, err := os.Stat("openapi.yaml"); err == nil {
		return "openapi.yaml"
	}
	if h.docsDir != "" {
		path := filepath.Join(h.docsDir, "openapi.yaml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (h *Handler) readOpenAPIPreview() (string, bool) {
	path := h.openAPIPath()
	if path == "" {
		return "", false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		h.logger.Warn("não foi possível ler openapi", zap.Error(err))
		return "", false
	}
	text := string(content)
	if len(text) > 2000 {
		text = text[:2000] + "\n..."
	}
	return text, true
}

func (h *Handler) settingsPage(c *gin.Context) {
	if h.deviceConfig == nil {
		h.logger.Error("deviceConfig service não inicializado")
		redirectWithMessage(c, "/dashboard", "error", "Serviço de configurações não disponível.")
		return
	}

	config, err := h.deviceConfig.Get(c.Request.Context())
	if err != nil {
		h.logger.Warn("erro ao buscar configurações", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Erro ao carregar configurações.")
		return
	}

	data := map[string]any{
		"Config": config,
	}
	page := h.pageData(c, "", "settings_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) updateSettings(c *gin.Context) {
	platformType := strings.TrimSpace(c.PostForm("platform_type"))
	osName := strings.TrimSpace(c.PostForm("os_name"))

	_, err := h.deviceConfig.Update(c.Request.Context(), device_config.UpdateInput{
		PlatformType: platformType,
		OSName:       osName,
	})
	if err != nil {
		h.logger.Warn("erro ao atualizar configurações", zap.Error(err))
		redirectWithMessage(c, "/dashboard/settings", "error", "Erro ao salvar configurações: "+err.Error())
		return
	}

	redirectWithMessage(c, "/dashboard/settings", "success", "Configurações salvas com sucesso!")
}
