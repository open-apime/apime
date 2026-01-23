package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/media"
)

type InstanceChecker interface {
	HasWebhook(ctx context.Context, instanceID string) bool
}

type EventHandler struct {
	queue           queue.Queue
	log             *zap.Logger
	mediaStorage    *media.Storage
	messageRepo     storage.MessageRepository
	apiBaseURL      string
	instanceChecker InstanceChecker
}

func NewEventHandler(q queue.Queue, log *zap.Logger, mediaStorage *media.Storage, messageRepo storage.MessageRepository, apiBaseURL string, instanceChecker InstanceChecker) *EventHandler {
	return &EventHandler{
		queue:           q,
		log:             log,
		mediaStorage:    mediaStorage,
		messageRepo:     messageRepo,
		apiBaseURL:      apiBaseURL,
		instanceChecker: instanceChecker,
	}
}

func (h *EventHandler) Handle(ctx context.Context, instanceID string, instanceJID string, client *whatsmeow.Client, evt any) {
	if h.instanceChecker != nil && !h.instanceChecker.HasWebhook(ctx, instanceID) {
		h.log.Info("[dispatcher] evento ignorado: instância sem webhook configurado", zap.String("instance", instanceID))
		return
	}

	h.log.Debug("[dispatcher] processando evento para webhook", zap.String("instance", instanceID), zap.String("type", fmt.Sprintf("%T", evt)))

	if receipt, ok := evt.(*events.Receipt); ok {
		status := string(receipt.Type)
		if receipt.Type == types.ReceiptTypeRetry {
			h.log.Warn("[dispatcher] RECEBIDO RETRY RECEIPT - Destinatário não conseguiu decriptar a mensagem",
				zap.Strings("msg_ids", receipt.MessageIDs),
				zap.String("chat", receipt.Chat.String()))
		}

		for _, msgID := range receipt.MessageIDs {
			if err := h.messageRepo.UpdateStatusByWhatsAppID(ctx, msgID, status); err != nil {
				h.log.Warn("[dispatcher] erro ao atualizar status da mensagem via receipt",
					zap.String("msg_id", msgID),
					zap.String("status", status),
					zap.Error(err))
			} else {
				h.log.Info("[dispatcher] status da mensagem atualizado via receipt",
					zap.String("msg_id", msgID),
					zap.String("status", status))
			}
		}
	}

	normalized := h.normalizeEvent(ctx, instanceID, client, evt)

	if instanceJID != "" {
		normalized["instanceJID"] = instanceJID
	}

	event := queue.Event{
		ID:         h.generateEventID(),
		InstanceID: instanceID,
		Type:       normalized["type"].(string),
		Payload:    normalized,
		CreatedAt:  time.Now(),
	}

	if err := h.queue.Enqueue(ctx, event); err != nil {
		h.log.Error("[dispatcher] event handler: erro ao enfileirar", zap.Error(err))
		return
	}

	h.log.Info("[dispatcher] evento enfileirado", zap.String("type", event.Type), zap.String("instance", instanceID))

	payloadInfo, _ := json.Marshal(event.Payload)
	h.log.Debug("[dispatcher] event handler: detalhes do evento",
		zap.String("type", event.Type),
		zap.String("instance", instanceID),
		zap.String("instanceJID", instanceJID),
		zap.String("payload", string(payloadInfo)),
	)
}

func (h *EventHandler) normalizeEvent(ctx context.Context, instanceID string, client *whatsmeow.Client, evt any) map[string]interface{} {
	result := make(map[string]interface{})

	switch evt := evt.(type) {
	case *events.Message:
		result["type"] = "message"

		// Determinar o JID correto do remetente
		// Se o Sender for um LID (@lid), usar o SenderAlt que contém o número real
		senderJID := evt.Info.Sender.String()
		if strings.Contains(senderJID, "@lid") && !evt.Info.SenderAlt.IsEmpty() {
			senderJID = evt.Info.SenderAlt.String()
		}
		result["from"] = senderJID

		// Determinar o Chat JID correto (identificador ESTÁVEL da conversa)
		// Se Chat for um LID, precisamos buscar o número real em outros campos
		chatJID := evt.Info.Chat.String()
		if strings.Contains(chatJID, "@lid") {
			// Para mensagens ENVIADAS (isFromMe=true), o número real do DESTINATÁRIO está em RecipientAlt
			// Para mensagens RECEBIDAS (isFromMe=false), o número real do REMETENTE está em SenderAlt
			if evt.Info.IsFromMe && !evt.Info.RecipientAlt.IsEmpty() && strings.Contains(evt.Info.RecipientAlt.String(), "@s.whatsapp.net") {
				chatJID = evt.Info.RecipientAlt.String()
			} else if !evt.Info.IsFromMe && !evt.Info.SenderAlt.IsEmpty() && strings.Contains(evt.Info.SenderAlt.String(), "@s.whatsapp.net") {
				chatJID = evt.Info.SenderAlt.String()
			}
			// Se ainda for LID, logar para debug
			if strings.Contains(chatJID, "@lid") {
				h.log.Warn("Chat ainda é LID após resolução",
					zap.String("original_chat", evt.Info.Chat.String()),
					zap.String("resolved_chat", chatJID),
					zap.String("recipientAlt", evt.Info.RecipientAlt.String()),
					zap.String("senderAlt", evt.Info.SenderAlt.String()),
					zap.Bool("isFromMe", evt.Info.IsFromMe))
			}
		}
		result["chatJID"] = chatJID
		result["to"] = chatJID
		result["isFromMe"] = evt.Info.IsFromMe
		result["isGroup"] = evt.Info.IsGroup
		result["messageId"] = evt.Info.ID
		result["timestamp"] = evt.Info.Timestamp
		result["pushName"] = evt.Info.PushName

		// Texto da mensagem
		if evt.Message.GetConversation() != "" {
			result["text"] = evt.Message.GetConversation()
		}

		// Extended text message (citações, links, etc)
		if extText := evt.Message.GetExtendedTextMessage(); extText != nil {
			result["text"] = extText.GetText()
		}

		// Mídia (imagem, vídeo, documento, áudio) - agora com download
		if img := evt.Message.GetImageMessage(); img != nil {
			h.log.Info("detectada imagem, iniciando processamento", zap.String("msg_id", evt.Info.ID))
			result["mediaType"] = "image"
			if img.GetCaption() != "" {
				result["caption"] = img.GetCaption()
			}
			result["mimetype"] = img.GetMimetype()
			result["fileSize"] = img.GetFileLength()

			// Baixar mídia localmente
			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, img, img.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				} else {
					h.log.Warn("falha ao baixar imagem, enviando webhook sem URL", zap.String("msg_id", evt.Info.ID))
				}
			}
		} else if vid := evt.Message.GetVideoMessage(); vid != nil {
			h.log.Info("detectado vídeo, iniciando processamento", zap.String("msg_id", evt.Info.ID))
			result["mediaType"] = "video"
			if vid.GetCaption() != "" {
				result["caption"] = vid.GetCaption()
			}
			result["mimetype"] = vid.GetMimetype()
			result["fileSize"] = vid.GetFileLength()
			result["duration"] = vid.GetSeconds()

			// Baixar mídia localmente
			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, vid, vid.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				} else {
					h.log.Warn("falha ao baixar vídeo, enviando webhook sem URL", zap.String("msg_id", evt.Info.ID))
				}
			}
		} else if doc := evt.Message.GetDocumentMessage(); doc != nil {
			result["mediaType"] = "document"
			if doc.GetTitle() != "" {
				result["fileName"] = doc.GetTitle()
			}
			result["mimetype"] = doc.GetMimetype()
			result["fileSize"] = doc.GetFileLength()

			// Baixar mídia localmente
			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, doc, doc.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				}
			}
		} else if aud := evt.Message.GetAudioMessage(); aud != nil {
			result["mediaType"] = "audio"
			result["mimetype"] = aud.GetMimetype()
			result["fileSize"] = aud.GetFileLength()
			result["duration"] = aud.GetSeconds()
			result["ptt"] = aud.GetPTT() // Push-to-Talk

			// Baixar mídia localmente
			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, aud, aud.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				}
			}
		} else if loc := evt.Message.GetLocationMessage(); loc != nil {
			result["mediaType"] = "location"
			result["latitude"] = loc.GetDegreesLatitude()
			result["longitude"] = loc.GetDegreesLongitude()
			if loc.GetAddress() != "" {
				result["address"] = loc.GetAddress()
			}
		} else if con := evt.Message.GetContactMessage(); con != nil {
			result["mediaType"] = "contact"
			result["contactName"] = con.GetDisplayName()
			result["contactNumber"] = con.GetVcard()
		} else if stk := evt.Message.GetStickerMessage(); stk != nil {
			result["mediaType"] = "sticker"
			// Baixar mídia localmente
			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, stk, stk.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				}
			}
		}
	case *events.Receipt:
		result["type"] = "receipt"
		result["messageIds"] = evt.MessageIDs
		result["timestamp"] = evt.Timestamp
		result["chat"] = evt.Chat.String()
		result["isGroup"] = evt.IsGroup
		if !evt.Sender.IsEmpty() {
			result["from"] = evt.Sender.String()
		}
		if !evt.MessageSender.IsEmpty() {
			result["messageSender"] = evt.MessageSender.String()
		}
		result["status"] = string(evt.Type)
	case *events.Presence:
		result["type"] = "presence"
		result["from"] = evt.From.String()
		result["unavailable"] = evt.Unavailable
		if !evt.LastSeen.IsZero() {
			result["lastSeen"] = evt.LastSeen
		}
	case *events.Connected:
		result["type"] = "connected"
	case *events.Disconnected:
		result["type"] = "disconnected"
	case *events.LoggedOut:
		result["type"] = "disconnected"
		result["reason"] = evt.Reason.String()
	default:
		result["type"] = "unknown"
		result["eventType"] = fmt.Sprintf("%T", evt)
	}

	// Adicionar dados brutos do evento (serializado)
	if data, err := json.Marshal(evt); err == nil {
		result["raw"] = json.RawMessage(data)
	}

	return result
}

// downloadAndSaveMedia baixa mídia usando o cliente WhatsMeow e salva localmente.
// Retorna a URL para acessar a mídia via API.
func (h *EventHandler) downloadAndSaveMedia(ctx context.Context, instanceID string, messageID string, client *whatsmeow.Client, downloadable whatsmeow.DownloadableMessage, mimetype string) string {
	h.log.Info("baixando mídia",
		zap.String("instance_id", instanceID),
		zap.String("message_id", messageID),
		zap.String("mimetype", mimetype),
	)

	// Baixar mídia do WhatsApp
	data, err := client.Download(ctx, downloadable)
	if err != nil {
		h.log.Error("erro ao baixar mídia",
			zap.String("instance_id", instanceID),
			zap.String("message_id", messageID),
			zap.Error(err),
		)
		return ""
	}

	h.log.Info("mídia baixada com sucesso",
		zap.String("instance_id", instanceID),
		zap.String("message_id", messageID),
		zap.Int("size", len(data)),
	)

	// Salvar no storage
	mediaID, err := h.mediaStorage.Save(ctx, instanceID, messageID, data, mimetype)
	if err != nil {
		h.log.Error("erro ao salvar mídia",
			zap.String("instance_id", instanceID),
			zap.String("message_id", messageID),
			zap.Error(err),
		)
		return ""
	}

	// Construir URL para acesso via API
	mediaURL := fmt.Sprintf("%s/api/media/%s/%s", h.apiBaseURL, instanceID, mediaID)

	h.log.Info("mídia salva e URL gerada",
		zap.String("instance_id", instanceID),
		zap.String("media_id", mediaID),
		zap.String("media_url", mediaURL),
	)

	return mediaURL
}

// generateEventID gera um ID único para o evento.
func (h *EventHandler) generateEventID() string {
	return uuid.New().String()
}
