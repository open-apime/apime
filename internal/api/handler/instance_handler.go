package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/response"
	instanceSvc "github.com/open-apime/apime/internal/service/instance"
)

type InstanceHandler struct {
	service        *instanceSvc.Service
	log            *zap.Logger
	sessionManager SessionManager
}

type SessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
}

func NewInstanceHandler(service *instanceSvc.Service, log *zap.Logger) *InstanceHandler {
	return &InstanceHandler{service: service, log: log}
}

func NewInstanceHandlerWithSession(service *instanceSvc.Service, log *zap.Logger, sessionManager SessionManager) *InstanceHandler {
	return &InstanceHandler{
		service:        service,
		log:            log,
		sessionManager: sessionManager,
	}
}

func (h *InstanceHandler) Register(r *gin.RouterGroup) {
	r.GET("/instances", h.list)
	r.GET("/instances/:id", h.get)
	r.POST("/instances", h.create)
	r.PUT("/instances/:id", h.update)
	r.DELETE("/instances/:id", h.delete)
	r.POST("/instances/:id/token/rotate", h.rotateToken)
	r.GET("/instances/:id/qr", h.getQR)
	r.POST("/instances/:id/disconnect", h.disconnect)
	r.GET("/instances/:id/info", h.getInstanceInfo)
	r.GET("/instances/:id/profile/:jid", h.getProfile)
	r.GET("/instances/:id/business/:jid", h.getBusinessProfile)
	r.GET("/instances/:id/profile/:jid/picture", h.getProfilePicture)
	r.GET("/instances/:id/events", h.listEvents)
}

type createInstanceRequest struct {
	Name          string `json:"name" binding:"required,min=2"`
	WebhookURL    string `json:"webhook_url"`
	WebhookSecret string `json:"webhook_secret"`
}

type updateInstanceRequest struct {
	Name          string `json:"name" binding:"required,min=2"`
	WebhookURL    string `json:"webhook_url"`
	WebhookSecret string `json:"webhook_secret"`
}

func (h *InstanceHandler) create(c *gin.Context) {
	if c.GetString("authType") == "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "token de instância não pode criar instâncias")
		return
	}
	var req createInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	userID := c.GetString("userID")

	instance, err := h.service.Create(c.Request.Context(), instanceSvc.CreateInput{
		Name:          req.Name,
		WebhookURL:    req.WebhookURL,
		WebhookSecret: req.WebhookSecret,
		OwnerUserID:   userID,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusCreated, instance)
}

func (h *InstanceHandler) list(c *gin.Context) {
	if c.GetString("authType") == "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "token de instância não pode listar instâncias")
		return
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	instances, _, err := h.service.ListByUser(c.Request.Context(), userID, userRole, "", 0, 0)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, instances)
}

func (h *InstanceHandler) get(c *gin.Context) {
	id := c.Param("id")
	if c.GetString("authType") == "instance_token" {
		if c.GetString("instanceID") != id {
			response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
			return
		}
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	instance, err := h.service.GetByUser(c.Request.Context(), id, userID, userRole)
	if err != nil {
		response.Error(c, http.StatusNotFound, err)
		return
	}
	response.Success(c, http.StatusOK, instance)
}

func (h *InstanceHandler) update(c *gin.Context) {
	id := c.Param("id")
	if c.GetString("authType") == "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de usuário")
		return
	}

	var req updateInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	// Same convention as the dashboard (dashboard.go updateInstance): owner = userID; admin uses "admin".
	callerID := userID
	if userRole == "admin" {
		callerID = "admin"
	}

	inst, err := h.service.UpdateByUser(c.Request.Context(), id, instanceSvc.UpdateInput{
		Name:          req.Name,
		WebhookURL:    req.WebhookURL,
		WebhookSecret: req.WebhookSecret,
		OwnerUserID:   callerID,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, inst)
}

func (h *InstanceHandler) delete(c *gin.Context) {
	id := c.Param("id")
	if c.GetString("authType") == "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de usuário")
		return
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	if err := h.service.DeleteByUser(c.Request.Context(), id, userID, userRole); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"message": "instância removida"})
}

func (h *InstanceHandler) rotateToken(c *gin.Context) {
	id := c.Param("id")
	if c.GetString("authType") == "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de usuário")
		return
	}
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	plain, err := h.service.RotateTokenByUser(c.Request.Context(), id, userID, userRole)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"token": plain})
}

func (h *InstanceHandler) getQR(c *gin.Context) {
	id := c.Param("id")
	h.log.Info("solicitando QR code", zap.String("instance_id", id))

	var qr string
	var err error

	if c.GetString("authType") == "instance_token" {
		if c.GetString("instanceID") != id {
			response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
			return
		}
		qr, err = h.service.GetQR(c.Request.Context(), id)
	} else {
		userID := c.GetString("userID")
		userRole := c.GetString("userRole")
		qr, err = h.service.GetQRByUser(c.Request.Context(), id, userID, userRole)
	}

	if err != nil {
		h.log.Error("erro ao obter QR code",
			zap.String("instance_id", id),
			zap.Error(err),
			zap.String("error_type", getErrorType(err)))

		statusCode := http.StatusInternalServerError
		errorMsg := err.Error()

		if strings.Contains(err.Error(), "timeout") {
			statusCode = http.StatusRequestTimeout
			errorMsg = "Timeout ao gerar QR code. Tente novamente."
		} else if strings.Contains(err.Error(), "contexto cancelado") {
			statusCode = http.StatusRequestTimeout
			errorMsg = "Requisição cancelada. Tente novamente."
		} else if strings.Contains(err.Error(), "sessão já existe") {
			statusCode = http.StatusConflict
			errorMsg = "Sessão já existe para esta instância."
		} else if strings.Contains(err.Error(), "not found") {
			statusCode = http.StatusNotFound
			errorMsg = "Instância não encontrada."
		}

		response.ErrorWithMessage(c, statusCode, errorMsg)
		return
	}

	h.log.Info("QR code gerado com sucesso", zap.String("instance_id", id))
	response.Success(c, http.StatusOK, gin.H{"qr": qr})
}

func getErrorType(err error) string {
	if err == nil {
		return "unknown"
	}
	errStr := err.Error()
	if strings.Contains(errStr, "timeout") {
		return "timeout"
	}
	if strings.Contains(errStr, "contexto cancelado") {
		return "context_cancelled"
	}
	if strings.Contains(errStr, "sessão já existe") {
		return "session_exists"
	}
	if strings.Contains(errStr, "not found") {
		return "not_found"
	}
	return "internal_error"
}

func (h *InstanceHandler) disconnect(c *gin.Context) {
	id := c.Param("id")

	if c.GetString("authType") == "instance_token" {
		if c.GetString("instanceID") != id {
			response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
			return
		}
		if err := h.service.Disconnect(c.Request.Context(), id); err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
		response.Success(c, http.StatusOK, gin.H{"message": "instância desconectada"})
		return
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	if err := h.service.DisconnectByUser(c.Request.Context(), id, userID, userRole); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"message": "instância desconectada"})
}

func (h *InstanceHandler) listEvents(c *gin.Context) {
	instanceID := c.Param("id")
	if instanceID == "" {
		response.Error(c, http.StatusBadRequest, fmt.Errorf("id é obrigatório"))
		return
	}

	limit := 10
	if q := c.Query("limit"); q != "" {
		if n, err := fmt.Sscanf(q, "%d", &limit); err != nil || n != 1 || limit < 1 {
			limit = 10
		}
		if limit > 100 {
			limit = 100
		}
	}

	events, err := h.service.ListEvents(c.Request.Context(), instanceID, limit)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, events)
}
