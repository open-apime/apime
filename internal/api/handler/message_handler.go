package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/pkg/response"
	messageSvc "github.com/open-apime/apime/internal/service/message"
)

// parseMentionedJids parses mentionedJids from form data (JSON array string)
func parseMentionedJids(c *gin.Context) []string {
	raw := c.PostForm("mentionedJids")
	if raw == "" {
		return nil
	}
	var jids []string
	if err := json.Unmarshal([]byte(raw), &jids); err != nil {
		return nil
	}
	return jids
}

type MessageHandler struct {
	service *messageSvc.Service
}

func NewMessageHandler(service *messageSvc.Service) *MessageHandler {
	return &MessageHandler{service: service}
}

func (h *MessageHandler) Register(r *gin.RouterGroup) {
	r.POST("/instances/:id/messages", h.enqueue)
	r.POST("/instances/:id/messages/text", h.sendText)
	r.POST("/instances/:id/messages/media", h.sendMedia)
	r.POST("/instances/:id/messages/audio", h.sendAudio)
	r.POST("/instances/:id/messages/document", h.sendDocument)
	r.POST("/instances/:id/messages/contact", h.sendContact)
	r.GET("/instances/:id/messages", h.list)
}

type messageRequest struct {
	To      string `json:"to" binding:"required"`
	Type    string `json:"type" binding:"required"`
	Payload string `json:"payload" binding:"required"`
}

func (h *MessageHandler) enqueue(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	var req messageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	msg, err := h.service.Enqueue(c.Request.Context(), messageSvc.EnqueueInput{
		InstanceID: instanceID,
		To:         req.To,
		Type:       req.Type,
		Payload:    req.Payload,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusAccepted, msg)
}

type sendTextRequest struct {
	To                string   `json:"to" binding:"required"`
	Text              string   `json:"text" binding:"required"`
	Quoted            string   `json:"quoted"`
	QuotedParticipant string   `json:"quotedParticipant"`
	QuotedText        string   `json:"quotedText"`
	MentionedJids     []string `json:"mentionedJids"`
	MarkReadMessageID string   `json:"markReadMessageId"`
	MarkReadSender    string   `json:"markReadSender"`
}

func (h *MessageHandler) sendText(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	var req sendTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	// Pass the raw JID/phone so the service can resolve it dynamically via IsOnWhatsApp.

	msg, err := h.service.Send(c.Request.Context(), messageSvc.SendInput{
		InstanceID:        instanceID,
		To:                req.To,
		Type:              "text",
		Text:              req.Text,
		Quoted:            req.Quoted,
		Participant:       req.QuotedParticipant,
		QuotedText:        req.QuotedText,
		MentionedJids:     req.MentionedJids,
		MarkReadMessageID: req.MarkReadMessageID,
		MarkReadSender:    req.MarkReadSender,
	})
	if err != nil {
		if errors.Is(err, messageSvc.ErrInstanceNotConnected) {
			response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		} else if errors.Is(err, messageSvc.ErrInvalidJID) {
			response.Error(c, http.StatusBadRequest, err)
		} else if errors.Is(err, messageSvc.ErrSessionUnavailable) {
			response.ErrorWithMessage(c, http.StatusServiceUnavailable, "sessão não pronta, tente novamente")
		} else {
			response.Error(c, http.StatusInternalServerError, err)
		}
		return
	}

	response.Success(c, http.StatusOK, msg)
}

func (h *MessageHandler) sendMedia(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	to := c.PostForm("to")
	mediaType := c.PostForm("type") // "image" or "video"
	caption := c.PostForm("caption")

	if to == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "campo 'to' é obrigatório")
		return
	}

	if mediaType != "image" && mediaType != "video" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "tipo deve ser 'image' ou 'video'")
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "arquivo não fornecido")
		return
	}

	src, err := file.Open()
	if err != nil {
		response.ErrorWithMessage(c, http.StatusInternalServerError, "erro ao abrir arquivo")
		return
	}
	defer src.Close()

	fileData, err := io.ReadAll(src)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusInternalServerError, "erro ao ler arquivo")
		return
	}

	msg, err := h.service.Send(c.Request.Context(), messageSvc.SendInput{
		InstanceID:        instanceID,
		To:                to,
		Type:              mediaType,
		MediaData:         fileData,
		MediaType:         file.Header.Get("Content-Type"),
		Caption:           caption,
		Quoted:            c.PostForm("quoted"),
		Participant:       c.PostForm("quotedParticipant"),
		QuotedText:        c.PostForm("quotedText"),
		MentionedJids:     parseMentionedJids(c),
		MarkReadMessageID: c.PostForm("markReadMessageId"),
		MarkReadSender:    c.PostForm("markReadSender"),
	})
	if err != nil {
		if errors.Is(err, messageSvc.ErrInstanceNotConnected) {
			response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		} else if errors.Is(err, messageSvc.ErrInvalidJID) {
			response.Error(c, http.StatusBadRequest, err)
		} else if errors.Is(err, messageSvc.ErrSessionUnavailable) {
			response.ErrorWithMessage(c, http.StatusServiceUnavailable, "sessão não pronta, tente novamente")
		} else {
			response.Error(c, http.StatusInternalServerError, err)
		}
		return
	}

	response.Success(c, http.StatusOK, msg)
}

func (h *MessageHandler) sendAudio(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	to := c.PostForm("to")

	if to == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "campo 'to' é obrigatório")
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "arquivo não fornecido")
		return
	}

	src, err := file.Open()
	if err != nil {
		response.ErrorWithMessage(c, http.StatusInternalServerError, "erro ao abrir arquivo")
		return
	}
	defer src.Close()

	fileData, err := io.ReadAll(src)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusInternalServerError, "erro ao ler arquivo")
		return
	}

	secondsStr := c.PostForm("seconds")
	seconds, _ := strconv.Atoi(secondsStr)

	pttStr := c.PostForm("ptt")
	ptt := pttStr == "true" || pttStr == "1"

	mediaType := file.Header.Get("Content-Type")

	msg, err := h.service.Send(c.Request.Context(), messageSvc.SendInput{
		InstanceID:        instanceID,
		To:                to,
		Type:              "audio",
		MediaData:         fileData,
		MediaType:         mediaType,
		Seconds:           seconds,
		PTT:               ptt,
		Quoted:            c.PostForm("quoted"),
		Participant:       c.PostForm("quotedParticipant"),
		QuotedText:        c.PostForm("quotedText"),
		MentionedJids:     parseMentionedJids(c),
		MarkReadMessageID: c.PostForm("markReadMessageId"),
		MarkReadSender:    c.PostForm("markReadSender"),
	})
	if err != nil {
		if errors.Is(err, messageSvc.ErrInstanceNotConnected) {
			response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		} else if errors.Is(err, messageSvc.ErrInvalidJID) {
			response.Error(c, http.StatusBadRequest, err)
		} else if errors.Is(err, messageSvc.ErrSessionUnavailable) {
			response.ErrorWithMessage(c, http.StatusServiceUnavailable, "sessão não pronta, tente novamente")
		} else {
			response.Error(c, http.StatusInternalServerError, err)
		}
		return
	}

	response.Success(c, http.StatusOK, msg)
}

func (h *MessageHandler) sendDocument(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	to := c.PostForm("to")
	fileName := c.PostForm("filename")
	caption := c.PostForm("caption")

	if to == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "campo 'to' é obrigatório")
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "arquivo não fornecido")
		return
	}

	if fileName == "" {
		fileName = file.Filename
	}

	src, err := file.Open()
	if err != nil {
		response.ErrorWithMessage(c, http.StatusInternalServerError, "erro ao abrir arquivo")
		return
	}
	defer src.Close()

	fileData, err := io.ReadAll(src)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusInternalServerError, "erro ao ler arquivo")
		return
	}

	msg, err := h.service.Send(c.Request.Context(), messageSvc.SendInput{
		InstanceID:        instanceID,
		To:                to,
		Type:              "document",
		MediaData:         fileData,
		MediaType:         file.Header.Get("Content-Type"),
		FileName:          fileName,
		Caption:           caption,
		Quoted:            c.PostForm("quoted"),
		Participant:       c.PostForm("quotedParticipant"),
		QuotedText:        c.PostForm("quotedText"),
		MentionedJids:     parseMentionedJids(c),
		MarkReadMessageID: c.PostForm("markReadMessageId"),
		MarkReadSender:    c.PostForm("markReadSender"),
	})
	if err != nil {
		if errors.Is(err, messageSvc.ErrInstanceNotConnected) {
			response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		} else if errors.Is(err, messageSvc.ErrInvalidJID) {
			response.Error(c, http.StatusBadRequest, err)
		} else if errors.Is(err, messageSvc.ErrSessionUnavailable) {
			response.ErrorWithMessage(c, http.StatusServiceUnavailable, "sessão não pronta, tente novamente")
		} else {
			response.Error(c, http.StatusInternalServerError, err)
		}
		return
	}

	response.Success(c, http.StatusOK, msg)
}

type sendContactRequest struct {
	To                string                    `json:"to" binding:"required"`
	DisplayName       string                    `json:"displayName" binding:"required"`
	Vcard             string                    `json:"vcard"`
	Contacts          []messageSvc.ContactEntry `json:"contacts"`
	Quoted            string                    `json:"quoted"`
	QuotedParticipant string                    `json:"quotedParticipant"`
	QuotedText        string                    `json:"quotedText"`
	MentionedJids     []string                  `json:"mentionedJids"`
}

func (h *MessageHandler) sendContact(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	var req sendContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	msg, err := h.service.Send(c.Request.Context(), messageSvc.SendInput{
		InstanceID:    instanceID,
		To:            req.To,
		Type:          "contact",
		DisplayName:   req.DisplayName,
		Vcard:         req.Vcard,
		Contacts:      req.Contacts,
		Quoted:        req.Quoted,
		Participant:   req.QuotedParticipant,
		QuotedText:    req.QuotedText,
		MentionedJids: req.MentionedJids,
	})
	if err != nil {
		if errors.Is(err, messageSvc.ErrInvalidPayload) {
			response.Error(c, http.StatusBadRequest, err)
		} else if errors.Is(err, messageSvc.ErrInstanceNotConnected) {
			response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		} else if errors.Is(err, messageSvc.ErrInvalidJID) {
			response.Error(c, http.StatusBadRequest, err)
		} else if errors.Is(err, messageSvc.ErrSessionUnavailable) {
			response.ErrorWithMessage(c, http.StatusServiceUnavailable, "sessão não pronta, tente novamente")
		} else {
			response.Error(c, http.StatusInternalServerError, err)
		}
		return
	}

	response.Success(c, http.StatusOK, msg)
}

func (h *MessageHandler) list(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}
	list, err := h.service.List(c.Request.Context(), instanceID)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, list)
}
