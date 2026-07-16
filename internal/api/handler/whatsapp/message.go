package whatsapp

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/open-apime/apime/internal/pkg/instancelock"
	"github.com/open-apime/apime/internal/pkg/response"
	messageSvc "github.com/open-apime/apime/internal/service/message"
)

type checkIsWhatsAppRequest struct {
	Phone string `json:"phone" binding:"required"`
}

func (h *Handler) checkIsWhatsApp(c *gin.Context) {
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

	unlock := instancelock.Acquire(instanceID)
	defer unlock()

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

type markReadRequest struct {
	Chat      string `json:"chat" binding:"required"`
	MessageID string `json:"message_id" binding:"required"`
	Sender    string `json:"sender"`
	Played    bool   `json:"played"`
}

func (h *Handler) markRead(c *gin.Context) {
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

func (h *Handler) deleteForEveryone(c *gin.Context) {
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

	unlock := instancelock.Acquire(instanceID)
	defer unlock()

	// Resolve o destino via ResolveJID (mesmo caminho do texto) — ver nota em sendReaction.
	chatJID, err := h.messageService.ResolveJID(c.Request.Context(), client, strings.TrimSpace(req.Chat))
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

	humanPause()

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

func (h *Handler) editMessage(c *gin.Context) {
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

	unlock := instancelock.Acquire(instanceID)
	defer unlock()

	// Resolve o destino via ResolveJID (mesmo caminho do texto) — ver nota em sendReaction.
	chatJID, err := h.messageService.ResolveJID(c.Request.Context(), client, strings.TrimSpace(req.Chat))
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "chat inválido")
		return
	}

	humanPause()

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

func (h *Handler) sendReaction(c *gin.Context) {
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

	unlock := instancelock.Acquire(instanceID)
	defer unlock()

	// Resolve o destino pelo mesmo caminho do envio de texto (ResolveJID popula o
	// mapeamento PN->LID no store da lib). Sem isso, SendMessage força PN->LID e falha
	// com "no LID found ... from server" quando o LID não está em cache. Trata @lid/@g.us.
	// Dentro do lock, como o envio de texto, para serializar o IsOnWhatsApp por instância.
	chatJID, err := h.messageService.ResolveJID(c.Request.Context(), client, strings.TrimSpace(req.Chat))
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

	humanPause()

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
