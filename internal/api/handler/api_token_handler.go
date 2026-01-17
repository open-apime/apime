package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/pkg/response"
	apiTokenSvc "github.com/open-apime/apime/internal/service/api_token"
)

type APITokenHandler struct {
	service *apiTokenSvc.Service
}

func NewAPITokenHandler(service *apiTokenSvc.Service) *APITokenHandler {
	return &APITokenHandler{service: service}
}

func (h *APITokenHandler) Register(r *gin.RouterGroup) {
	tokens := r.Group("/tokens")
	{
		tokens.GET("", h.list)
		tokens.POST("", h.create)
		tokens.DELETE("/:id", h.delete)
	}
}

type createTokenRequest struct {
	Name      string  `json:"name" binding:"required"`
	ExpiresAt *string `json:"expiresAt,omitempty"`
}

func (h *APITokenHandler) create(c *gin.Context) {
	var req createTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	userID := c.GetString("userID")
	if userID == "" {
		response.ErrorWithMessage(c, http.StatusUnauthorized, "usuário não autenticado")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			response.Error(c, http.StatusBadRequest, err)
			return
		}
		expiresAt = &parsed
	}

	token, plainToken, err := h.service.Create(c.Request.Context(), userID, req.Name, expiresAt)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusCreated, gin.H{
		"id":        token.ID,
		"name":      token.Name,
		"token":     plainToken, // Retornar apenas na criação
		"createdAt": token.CreatedAt,
	})
}

func (h *APITokenHandler) list(c *gin.Context) {
	// Obter userID do contexto de autenticação
	userID := c.GetString("userID")
	if userID == "" {
		response.ErrorWithMessage(c, http.StatusUnauthorized, "usuário não autenticado")
		return
	}

	tokens, err := h.service.ListByUser(c.Request.Context(), userID)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	result := make([]gin.H, len(tokens))
	for i, token := range tokens {
		result[i] = gin.H{
			"id":         token.ID,
			"name":       token.Name,
			"userId":     token.UserID,
			"lastUsedAt": token.LastUsedAt,
			"expiresAt":  token.ExpiresAt,
			"isActive":   token.IsActive,
			"createdAt":  token.CreatedAt,
			"updatedAt":  token.UpdatedAt,
		}
	}

	response.Success(c, http.StatusOK, result)
}

func (h *APITokenHandler) delete(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.Error(c, http.StatusBadRequest, gin.Error{Err: errors.New("id é obrigatório")})
		return
	}

	err := h.service.Delete(c.Request.Context(), id)
	if err != nil {
		if err == apiTokenSvc.ErrTokenNotFound {
			response.Error(c, http.StatusNotFound, err)
			return
		}
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, gin.H{"message": "token deletado com sucesso"})
}
