package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/open-apime/apime/internal/pkg/response"
	messageSvc "github.com/open-apime/apime/internal/service/message"
)

type WhatsAppHandler struct {
	sessionManager WhatsAppSessionManager
	messageService *messageSvc.Service
}

type WhatsAppSessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
}

func NewWhatsAppHandler(sessionManager WhatsAppSessionManager, messageService *messageSvc.Service) *WhatsAppHandler {
	return &WhatsAppHandler{
		sessionManager: sessionManager,
		messageService: messageService,
	}
}

func (h *WhatsAppHandler) Register(r *gin.RouterGroup) {
	r.POST("/instances/:id/whatsapp/check", h.checkIsWhatsApp)
	r.POST("/instances/:id/whatsapp/presence", h.setPresence)
	r.POST("/instances/:id/whatsapp/messages/read", h.markRead)
	r.POST("/instances/:id/whatsapp/messages/delete", h.deleteForEveryone)
	r.POST("/instances/:id/whatsapp/messages/edit", h.editMessage)
	r.POST("/instances/:id/whatsapp/messages/react", h.sendReaction)
	r.GET("/instances/:id/whatsapp/contacts", h.listContacts)
	r.GET("/instances/:id/whatsapp/contacts/:jid", h.getContact)
	r.GET("/instances/:id/whatsapp/userinfo/:jid", h.getUserInfo)
	r.GET("/instances/:id/whatsapp/privacy", h.getPrivacySettings)
	r.POST("/instances/:id/whatsapp/privacy", h.setPrivacySetting)
	r.GET("/instances/:id/whatsapp/chat-settings/:chat", h.getChatSettings)
	r.POST("/instances/:id/whatsapp/chat-settings/:chat", h.setChatSettings)
	r.POST("/instances/:id/whatsapp/status", h.setStatusMessage)
	r.POST("/instances/:id/whatsapp/disappearing-timer", h.setDefaultDisappearingTimer)
	r.GET("/instances/:id/whatsapp/qr/contact", h.getContactQRLink)
	r.POST("/instances/:id/whatsapp/qr/contact/resolve", h.resolveContactQRLink)
	r.POST("/instances/:id/whatsapp/qr/business-message/resolve", h.resolveBusinessMessageLink)
	r.GET("/instances/:id/whatsapp/groups/:group/invite-link", h.getGroupInviteLink)
	r.POST("/instances/:id/whatsapp/groups/resolve-invite", h.getGroupInfoFromLink)
	r.POST("/instances/:id/whatsapp/groups/join", h.joinGroupWithLink)
	r.POST("/instances/:id/whatsapp/groups/:group/leave", h.leaveGroup)
	r.POST("/instances/:id/whatsapp/groups", h.createGroup)
	r.POST("/instances/:id/whatsapp/groups/:group/participants", h.updateGroupParticipants)
	r.GET("/instances/:id/whatsapp/groups/:group/requests", h.listGroupJoinRequests)
	r.POST("/instances/:id/whatsapp/groups/:group/requests", h.updateGroupJoinRequests)
	r.GET("/instances/:id/whatsapp/status-privacy", h.getStatusPrivacy)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/live-updates", h.newsletterSubscribeLiveUpdates)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/mark-viewed", h.newsletterMarkViewed)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/reaction", h.newsletterSendReaction)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/message-updates", h.getNewsletterMessageUpdates)
	r.GET("/instances/:id/whatsapp/groups", h.listGroups)
	r.GET("/instances/:id/whatsapp/groups/:group", h.getGroupInfo)
	r.POST("/instances/:id/whatsapp/upload", h.uploadMedia)
}

func (h *WhatsAppHandler) requireInstanceToken(c *gin.Context) (string, bool) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return "", false
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return "", false
	}
	if h.sessionManager == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "session manager não configurado")
		return "", false
	}
	return instanceID, true
}

type checkIsWhatsAppRequest struct {
	Phone string `json:"phone" binding:"required"`
}

func (h *WhatsAppHandler) checkIsWhatsApp(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req checkIsWhatsAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	phone := strings.TrimSpace(req.Phone)
	if phone == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "phone inválido")
		return
	}
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	jid, err := h.messageService.ResolveJID(c.Request.Context(), client, phone)

	result := types.IsOnWhatsAppResponse{
		Query: phone,
	}

	if err == nil {
		result.JID = jid
		result.IsIn = true
	} else if errors.Is(err, messageSvc.ErrInvalidJID) {
		result.IsIn = false
	} else {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, gin.H{"results": []types.IsOnWhatsAppResponse{result}})
}

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

type markReadRequest struct {
	Chat      string `json:"chat" binding:"required"`
	MessageID string `json:"message_id" binding:"required"`
	Sender    string `json:"sender"`
	Played    bool   `json:"played"`
}

func (h *WhatsAppHandler) markRead(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req markReadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	chatStr := strings.TrimSpace(req.Chat)
	if !strings.Contains(chatStr, "@") {
		chatStr = strings.TrimPrefix(chatStr, "+") + "@s.whatsapp.net"
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	senderJID := types.EmptyJID
	if strings.TrimSpace(req.Sender) != "" {
		senderStr := strings.TrimSpace(req.Sender)
		if !strings.Contains(senderStr, "@") {
			senderStr = strings.TrimPrefix(senderStr, "+") + "@s.whatsapp.net"
		}
		senderJID, err = types.ParseJID(senderStr)
		if err != nil {
			response.ErrorWithMessage(c, http.StatusBadRequest, "sender inválido")
			return
		}
	}

	receiptType := types.ReceiptTypeRead
	if req.Played {
		receiptType = types.ReceiptTypePlayed
	}

	if err := client.MarkRead(c.Request.Context(), []types.MessageID{types.MessageID(req.MessageID)}, time.Now(), chatJID, senderJID, receiptType); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

type deleteForEveryoneRequest struct {
	Chat      string `json:"chat" binding:"required"`
	MessageID string `json:"message_id" binding:"required"`
	Sender    string `json:"sender"`
}

func (h *WhatsAppHandler) deleteForEveryone(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req deleteForEveryoneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	chatStr := strings.TrimSpace(req.Chat)
	if !strings.Contains(chatStr, "@") {
		chatStr = strings.TrimPrefix(chatStr, "+") + "@s.whatsapp.net"
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	senderJID := types.EmptyJID
	if strings.TrimSpace(req.Sender) != "" {
		senderStr := strings.TrimSpace(req.Sender)
		if !strings.Contains(senderStr, "@") {
			senderStr = strings.TrimPrefix(senderStr, "+") + "@s.whatsapp.net"
		}
		senderJID, err = types.ParseJID(senderStr)
		if err != nil {
			response.ErrorWithMessage(c, http.StatusBadRequest, "sender inválido")
			return
		}
	}

	resp, err := client.RevokeMessage(c.Request.Context(), chatJID, types.MessageID(req.MessageID))
	if err != nil {
		msg := client.BuildRevoke(chatJID, senderJID, types.MessageID(req.MessageID))
		resp, err = client.SendMessage(c.Request.Context(), chatJID, msg)
		if err != nil {
			response.Error(c, http.StatusInternalServerError, err)
			return
		}
	}

	response.Success(c, http.StatusOK, gin.H{
		"status":    "ok",
		"timestamp": resp.Timestamp,
		"id":        resp.ID,
	})
}

type editMessageRequest struct {
	Chat      string `json:"chat" binding:"required"`
	MessageID string `json:"message_id" binding:"required"`
	Text      string `json:"text" binding:"required"`
}

func (h *WhatsAppHandler) editMessage(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req editMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	chatStr := strings.TrimSpace(req.Chat)
	if !strings.Contains(chatStr, "@") {
		chatStr = strings.TrimPrefix(chatStr, "+") + "@s.whatsapp.net"
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	editMsg := client.BuildEdit(chatJID, types.MessageID(req.MessageID), &waE2E.Message{
		Conversation: proto.String(req.Text),
	})

	resp, err := client.SendMessage(c.Request.Context(), chatJID, editMsg)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, gin.H{
		"status":    "ok",
		"timestamp": resp.Timestamp,
		"id":        resp.ID,
	})
}

type sendReactionRequest struct {
	Chat      string `json:"chat" binding:"required"`
	MessageID string `json:"message_id" binding:"required"`
	Emoji     string `json:"emoji"`
	Sender    string `json:"sender"`
	FromMe    *bool  `json:"from_me"`
}

func (h *WhatsAppHandler) sendReaction(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req sendReactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	chatStr := strings.TrimSpace(req.Chat)
	if !strings.Contains(chatStr, "@") {
		chatStr = strings.TrimPrefix(chatStr, "+") + "@s.whatsapp.net"
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	fromMe := false
	if req.FromMe != nil {
		fromMe = *req.FromMe
	}

	msgKey := &waCommon.MessageKey{
		RemoteJID: proto.String(chatJID.String()),
		FromMe:    proto.Bool(fromMe),
		ID:        proto.String(req.MessageID),
	}

	if strings.TrimSpace(req.Sender) != "" {
		senderStr := strings.TrimSpace(req.Sender)
		if !strings.Contains(senderStr, "@") {
			senderStr = senderStr + "@s.whatsapp.net"
		}
		msgKey.Participant = proto.String(senderStr)
	}

	waMessage := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key:               msgKey,
			Text:              proto.String(strings.TrimSpace(req.Emoji)),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	}

	resp, err := client.SendMessage(c.Request.Context(), chatJID, waMessage)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, gin.H{
		"status":    "ok",
		"timestamp": resp.Timestamp,
		"id":        resp.ID,
	})
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
