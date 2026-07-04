package handler

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/open-apime/apime/internal/pkg/response"
)

type setPresenceRequest struct {
	To    string `json:"to"`
	State string `json:"state" binding:"required"`
}

func (h *WhatsAppHandler) setPresence(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req setPresenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	state := strings.ToLower(strings.TrimSpace(req.State))
	switch state {
	case "available", "online":
		if err := client.SendPresence(c.Request.Context(), types.PresenceAvailable); err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
		response.Success(c, http.StatusOK, gin.H{"status": "ok"})
		return
	case "unavailable", "offline":
		if err := client.SendPresence(c.Request.Context(), types.PresenceUnavailable); err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
		response.Success(c, http.StatusOK, gin.H{"status": "ok"})
		return
	case "composing", "typing", "recording", "audio", "paused", "pause":
	default:
		response.ErrorWithMessage(c, http.StatusBadRequest, "state inválido")
		return
	}

	toStr := strings.TrimSpace(req.To)
	if toStr == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "state requer destinatário (to)")
		return
	}
	if !strings.Contains(toStr, "@") {
		toStr = strings.TrimPrefix(toStr, "+") + "@s.whatsapp.net"
	}
	toJID, err := types.ParseJID(toStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "to inválido")
		return
	}

	var chatState types.ChatPresence
	media := types.ChatPresenceMediaText
	switch state {
	case "composing", "typing":
		chatState = types.ChatPresenceComposing
	case "recording", "audio":
		chatState = types.ChatPresenceComposing
		media = types.ChatPresenceMediaAudio
	case "paused", "pause":
		chatState = types.ChatPresencePaused
	}

	if err := client.SendChatPresence(c.Request.Context(), toJID, chatState, media); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

type uploadMediaRequest struct {
	MediaType  string `json:"media_type" binding:"required"` // image|video|audio|document
	DataBase64 string `json:"data_base64" binding:"required"`
}

func (h *WhatsAppHandler) uploadMedia(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	var req uploadMediaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.DataBase64))
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "base64 inválido")
		return
	}
	var mt whatsmeow.MediaType
	switch strings.ToLower(strings.TrimSpace(req.MediaType)) {
	case "image":
		mt = whatsmeow.MediaImage
	case "video":
		mt = whatsmeow.MediaVideo
	case "audio":
		mt = whatsmeow.MediaAudio
	case "document":
		mt = whatsmeow.MediaDocument
	default:
		response.ErrorWithMessage(c, http.StatusBadRequest, "media_type inválido")
		return
	}
	resp, err := client.Upload(c.Request.Context(), data, mt)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, resp)
}
