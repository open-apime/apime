package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/types"

	"github.com/open-apime/apime/internal/pkg/response"
)

func (h *WhatsAppHandler) getStatusPrivacy(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	privacy, err := client.GetStatusPrivacy(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"lists": privacy})
}

func (h *WhatsAppHandler) getPrivacySettings(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	settings := client.GetPrivacySettings(c.Request.Context())
	response.Success(c, http.StatusOK, settings)
}

type setPrivacySettingRequest struct {
	Name  string `json:"name" binding:"required"`
	Value string `json:"value" binding:"required"`
}

func (h *WhatsAppHandler) setPrivacySetting(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	var req setPrivacySettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	name := types.PrivacySettingType(strings.TrimSpace(req.Name))
	value := types.PrivacySetting(strings.TrimSpace(req.Value))

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	settings, err := client.SetPrivacySetting(ctx, name, value)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, settings)
}

type setStatusMessageRequest struct {
	Message string `json:"message" binding:"required"`
}

func (h *WhatsAppHandler) setStatusMessage(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req setStatusMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if err := client.SetStatusMessage(c.Request.Context(), strings.TrimSpace(req.Message)); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

type setDefaultDisappearingTimerRequest struct {
	Seconds int `json:"seconds" binding:"required,min=0"`
}

func (h *WhatsAppHandler) setDefaultDisappearingTimer(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req setDefaultDisappearingTimerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if err := client.SetDefaultDisappearingTimer(c.Request.Context(), time.Duration(req.Seconds)*time.Second); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

func (h *WhatsAppHandler) getChatSettings(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	chatStr := strings.TrimSpace(c.Param("chat"))
	if !strings.Contains(chatStr, "@") {
		chatStr = strings.TrimPrefix(chatStr, "+") + "@s.whatsapp.net"
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if client.Store == nil || client.Store.ChatSettings == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat settings store não disponível")
		return
	}
	settings, err := client.Store.ChatSettings.GetChatSettings(c.Request.Context(), chatJID)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, settings)
}

type setChatSettingsRequest struct {
	MutedUntil *time.Time `json:"muted_until"`
	Pinned     *bool      `json:"pinned"`
	Archived   *bool      `json:"archived"`
}

func (h *WhatsAppHandler) setChatSettings(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	chatStr := strings.TrimSpace(c.Param("chat"))
	if !strings.Contains(chatStr, "@") {
		chatStr = strings.TrimPrefix(chatStr, "+") + "@s.whatsapp.net"
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	var req setChatSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if client.Store == nil || client.Store.ChatSettings == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat settings store não disponível")
		return
	}

	ctx := c.Request.Context()
	if req.MutedUntil != nil {
		if err := client.Store.ChatSettings.PutMutedUntil(ctx, chatJID, req.MutedUntil.UTC()); err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := client.Store.ChatSettings.PutPinned(ctx, chatJID, *req.Pinned); err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Archived != nil {
		if err := client.Store.ChatSettings.PutArchived(ctx, chatJID, *req.Archived); err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
	}

	settings, err := client.Store.ChatSettings.GetChatSettings(ctx, chatJID)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, settings)
}
