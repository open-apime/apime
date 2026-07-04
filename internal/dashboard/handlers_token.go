package dashboard

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

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
