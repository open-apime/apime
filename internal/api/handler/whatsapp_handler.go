package handler

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/open-apime/apime/internal/pkg/response"
)

type WhatsAppHandler struct {
	sessionManager WhatsAppSessionManager
}

type WhatsAppSessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
}

func NewWhatsAppHandler(sessionManager WhatsAppSessionManager) *WhatsAppHandler {
	return &WhatsAppHandler{sessionManager: sessionManager}
}

func (h *WhatsAppHandler) Register(r *gin.RouterGroup) {
	r.POST("/instances/:id/whatsapp/check", h.checkIsWhatsApp)
	r.POST("/instances/:id/whatsapp/presence", h.setPresence)
	r.POST("/instances/:id/whatsapp/messages/read", h.markRead)
	r.POST("/instances/:id/whatsapp/messages/delete", h.deleteForEveryone)
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

	resp, err := client.IsOnWhatsApp(c.Request.Context(), []string{phone})
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	// Retornar resposta crua do whatsmeow (evita acoplamento com struct interno)
	response.Success(c, http.StatusOK, gin.H{"results": resp})
}

type setPresenceRequest struct {
	// Para PresenceAvailable/Unavailable, o WhatsMeow só precisa do estado.
	// Para Composing/Recording/Paused, pode ser necessário enviar para um chat específico.
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
		// handled below
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

type createGroupRequest struct {
	Name         string   `json:"name" binding:"required"`
	Participants []string `json:"participants"`
}

func (h *WhatsAppHandler) createGroup(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req createGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	participants := make([]types.JID, 0, len(req.Participants))
	for _, p := range req.Participants {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "@") {
			p = strings.TrimPrefix(p, "+") + "@s.whatsapp.net"
		}
		jid, err := types.ParseJID(p)
		if err != nil {
			response.ErrorWithMessage(c, http.StatusBadRequest, "participant inválido")
			return
		}
		participants = append(participants, jid)
	}

	info, err := client.CreateGroup(c.Request.Context(), whatsmeow.ReqCreateGroup{
		Name:         strings.TrimSpace(req.Name),
		Participants: participants,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, info)
}

type updateGroupParticipantsRequest struct {
	Action       string   `json:"action" binding:"required"` // add/remove/promote/demote
	Participants []string `json:"participants" binding:"required"`
}

func (h *WhatsAppHandler) updateGroupParticipants(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	groupStr := strings.TrimSpace(c.Param("group"))
	if !strings.Contains(groupStr, "@") {
		groupStr = groupStr + "@g.us"
	}
	groupJID, err := types.ParseJID(groupStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "group inválido")
		return
	}

	var req updateGroupParticipantsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	var action whatsmeow.ParticipantChange
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "add":
		action = whatsmeow.ParticipantChangeAdd
	case "remove":
		action = whatsmeow.ParticipantChangeRemove
	case "promote":
		action = whatsmeow.ParticipantChangePromote
	case "demote":
		action = whatsmeow.ParticipantChangeDemote
	default:
		response.ErrorWithMessage(c, http.StatusBadRequest, "action inválida")
		return
	}

	participants := make([]types.JID, 0, len(req.Participants))
	for _, p := range req.Participants {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "@") {
			p = strings.TrimPrefix(p, "+") + "@s.whatsapp.net"
		}
		jid, err := types.ParseJID(p)
		if err != nil {
			response.ErrorWithMessage(c, http.StatusBadRequest, "participant inválido")
			return
		}
		participants = append(participants, jid)
	}

	res, err := client.UpdateGroupParticipants(c.Request.Context(), groupJID, participants, action)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"participants": res})
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

func (h *WhatsAppHandler) newsletterSubscribeLiveUpdates(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	jidStr := strings.TrimSpace(c.Param("jid"))
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}
	dur, err := client.NewsletterSubscribeLiveUpdates(c.Request.Context(), jid)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"duration_seconds": int64(dur.Seconds())})
}

type newsletterMarkViewedRequest struct {
	ServerIDs []string `json:"server_ids" binding:"required"`
}

func (h *WhatsAppHandler) newsletterMarkViewed(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	jidStr := strings.TrimSpace(c.Param("jid"))
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}
	var req newsletterMarkViewedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	ids := make([]types.MessageServerID, 0, len(req.ServerIDs))
	for _, s := range req.ServerIDs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		idInt, err := strconv.Atoi(s)
		if err != nil {
			response.ErrorWithMessage(c, http.StatusBadRequest, "server_id inválido")
			return
		}
		ids = append(ids, types.MessageServerID(idInt))
	}
	if err := client.NewsletterMarkViewed(c.Request.Context(), jid, ids); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

type newsletterSendReactionRequest struct {
	ServerID  string `json:"server_id" binding:"required"`
	Reaction  string `json:"reaction"`
	MessageID string `json:"message_id"`
}

func (h *WhatsAppHandler) newsletterSendReaction(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	jidStr := strings.TrimSpace(c.Param("jid"))
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}
	var req newsletterSendReactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	serverIDStr := strings.TrimSpace(req.ServerID)
	if serverIDStr == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "server_id inválido")
		return
	}
	serverIDInt, err := strconv.Atoi(serverIDStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "server_id inválido")
		return
	}
	if err := client.NewsletterSendReaction(
		c.Request.Context(),
		jid,
		types.MessageServerID(serverIDInt),
		strings.TrimSpace(req.Reaction),
		types.MessageID(strings.TrimSpace(req.MessageID)),
	); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

func (h *WhatsAppHandler) getNewsletterMessageUpdates(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	jidStr := strings.TrimSpace(c.Param("jid"))
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}
	// A struct GetNewsletterUpdatesParams é interna do fork; manteremos nil por enquanto.
	updates, err := client.GetNewsletterMessageUpdates(c.Request.Context(), jid, nil)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"updates": updates})
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

func (h *WhatsAppHandler) listGroupJoinRequests(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	groupStr := strings.TrimSpace(c.Param("group"))
	if !strings.Contains(groupStr, "@") {
		groupStr = groupStr + "@g.us"
	}
	groupJID, err := types.ParseJID(groupStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "group inválido")
		return
	}

	list, err := client.GetGroupRequestParticipants(c.Request.Context(), groupJID)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"requests": list})
}

type updateGroupJoinRequestsRequest struct {
	Action       string   `json:"action" binding:"required"` // approve/reject
	Participants []string `json:"participants" binding:"required"`
}

func (h *WhatsAppHandler) updateGroupJoinRequests(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	groupStr := strings.TrimSpace(c.Param("group"))
	if !strings.Contains(groupStr, "@") {
		groupStr = groupStr + "@g.us"
	}
	groupJID, err := types.ParseJID(groupStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "group inválido")
		return
	}

	var req updateGroupJoinRequestsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	var action whatsmeow.ParticipantRequestChange
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "approve":
		action = whatsmeow.ParticipantChangeApprove
	case "reject":
		action = whatsmeow.ParticipantChangeReject
	default:
		response.ErrorWithMessage(c, http.StatusBadRequest, "action inválida")
		return
	}

	participants := make([]types.JID, 0, len(req.Participants))
	for _, p := range req.Participants {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "@") {
			p = strings.TrimPrefix(p, "+") + "@s.whatsapp.net"
		}
		jid, err := types.ParseJID(p)
		if err != nil {
			response.ErrorWithMessage(c, http.StatusBadRequest, "participant inválido")
			return
		}
		participants = append(participants, jid)
	}

	res, err := client.UpdateGroupRequestParticipants(c.Request.Context(), groupJID, participants, action)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"participants": res})
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

	// Revoke: por padrão, revoga mensagens "from me". Se sender for informado e diferente, permite revoke admin.
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
		// fallback: usar BuildRevoke para suportar revoke admin quando sender for informado
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

func (h *WhatsAppHandler) listContacts(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if client.Store == nil || client.Store.Contacts == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "contacts store não disponível")
		return
	}
	contacts, err := client.Store.Contacts.GetAllContacts(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"contacts": contacts})
}

func (h *WhatsAppHandler) getContact(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	jidStr := c.Param("jid")
	jidStr = strings.TrimSpace(jidStr)
	if !strings.Contains(jidStr, "@") {
		jidStr = strings.TrimPrefix(jidStr, "+") + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if client.Store == nil || client.Store.Contacts == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "contacts store não disponível")
		return
	}
	contact, err := client.Store.Contacts.GetContact(c.Request.Context(), jid)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"jid": jid.String(), "contact": contact})
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

type getContactQRLinkRequest struct {
	Revoke bool `json:"revoke"`
}

func (h *WhatsAppHandler) getContactQRLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	revoke := false
	if strings.EqualFold(strings.TrimSpace(c.Query("revoke")), "true") {
		revoke = true
	} else {
		// Compat: permitir body JSON (mesmo em GET) para clientes antigos
		var req getContactQRLinkRequest
		_ = c.ShouldBindJSON(&req)
		revoke = req.Revoke
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	code, err := client.GetContactQRLink(c.Request.Context(), revoke)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"code": code})
}

type resolveContactQRLinkRequest struct {
	Code string `json:"code" binding:"required"`
}

func (h *WhatsAppHandler) resolveContactQRLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req resolveContactQRLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	target, err := client.ResolveContactQRLink(c.Request.Context(), strings.TrimSpace(req.Code))
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, target)
}

func (h *WhatsAppHandler) getGroupInviteLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	groupStr := strings.TrimSpace(c.Param("group"))
	if !strings.Contains(groupStr, "@") {
		groupStr = groupStr + "@g.us"
	}
	groupJID, err := types.ParseJID(groupStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "group inválido")
		return
	}

	reset := strings.EqualFold(strings.TrimSpace(c.Query("reset")), "true")
	link, err := client.GetGroupInviteLink(c.Request.Context(), groupJID, reset)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"link": link})
}

type getGroupInfoFromLinkRequest struct {
	Link string `json:"link" binding:"required"`
}

func (h *WhatsAppHandler) getGroupInfoFromLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req getGroupInfoFromLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	info, err := client.GetGroupInfoFromLink(c.Request.Context(), strings.TrimSpace(req.Link))
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, info)
}

type joinGroupWithLinkRequest struct {
	Link string `json:"link" binding:"required"`
}

func (h *WhatsAppHandler) joinGroupWithLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req joinGroupWithLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	jid, err := client.JoinGroupWithLink(c.Request.Context(), strings.TrimSpace(req.Link))
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"group": jid.String()})
}

func (h *WhatsAppHandler) leaveGroup(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	groupStr := strings.TrimSpace(c.Param("group"))
	if !strings.Contains(groupStr, "@") {
		groupStr = groupStr + "@g.us"
	}
	groupJID, err := types.ParseJID(groupStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "group inválido")
		return
	}

	if err := client.LeaveGroup(c.Request.Context(), groupJID); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

type resolveBusinessMessageLinkRequest struct {
	Code string `json:"code" binding:"required"`
}

func (h *WhatsAppHandler) resolveBusinessMessageLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req resolveBusinessMessageLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	target, err := client.ResolveBusinessMessageLink(c.Request.Context(), strings.TrimSpace(req.Code))
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, target)
}

func (h *WhatsAppHandler) getUserInfo(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	jidStr := strings.TrimSpace(c.Param("jid"))
	if !strings.Contains(jidStr, "@") {
		jidStr = strings.TrimPrefix(jidStr, "+") + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	infoMap, err := client.GetUserInfo(c.Request.Context(), []types.JID{jid})
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	info, exists := infoMap[jid]
	if !exists {
		response.ErrorWithMessage(c, http.StatusNotFound, "usuário não encontrado")
		return
	}
	response.Success(c, http.StatusOK, info)
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
