package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/queue"
	messageSvc "github.com/open-apime/apime/internal/service/message"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/media"
)

type InstanceChecker interface {
	HasWebhook(ctx context.Context, instanceID string) bool
}

type JIDConfirmer interface {
	ConfirmJID(ctx context.Context, jid types.JID)
}

type EventHandler struct {
	queue           queue.Queue
	log             *zap.Logger
	mediaStorage    *media.Storage
	messageRepo     storage.MessageRepository
	apiBaseURL      string
	instanceChecker InstanceChecker
	jidConfirmer    JIDConfirmer
}

func NewEventHandler(q queue.Queue, log *zap.Logger, mediaStorage *media.Storage, messageRepo storage.MessageRepository, apiBaseURL string, instanceChecker InstanceChecker, jidConfirmer JIDConfirmer) *EventHandler {
	return &EventHandler{
		queue:           q,
		log:             log,
		mediaStorage:    mediaStorage,
		messageRepo:     messageRepo,
		apiBaseURL:      apiBaseURL,
		instanceChecker: instanceChecker,
		jidConfirmer:    jidConfirmer,
	}
}

// confirmJIDFromEvent extrai o JID s.whatsapp.net do evento (resolvendo @lid via Alt)
// e confirma no resolver, invalidando cache negativo e persistindo o mapeamento.
func (h *EventHandler) confirmJIDFromEvent(ctx context.Context, evt any) {
	if h.jidConfirmer == nil {
		return
	}
	resolve := func(primary, alt types.JID) types.JID {
		if primary.Server == types.DefaultUserServer && primary.User != "" {
			return primary
		}
		if alt.Server == types.DefaultUserServer && alt.User != "" {
			return alt
		}
		return types.EmptyJID
	}
	switch e := evt.(type) {
	case *events.Message:
		if e.Info.IsGroup {
			return
		}
		jid := resolve(e.Info.Sender, e.Info.SenderAlt)
		if jid.IsEmpty() && e.Info.IsFromMe {
			jid = resolve(e.Info.Chat, e.Info.RecipientAlt)
		}
		if !jid.IsEmpty() {
			h.jidConfirmer.ConfirmJID(ctx, jid)
		}
	case *events.Receipt:
		if e.IsGroup {
			return
		}
		jid := resolve(e.Sender, e.MessageSender)
		if !jid.IsEmpty() {
			h.jidConfirmer.ConfirmJID(ctx, jid)
		}
	case *events.Presence:
		jid := resolve(e.From, types.EmptyJID)
		if !jid.IsEmpty() {
			h.jidConfirmer.ConfirmJID(ctx, jid)
		}
	}
}

func (h *EventHandler) Handle(ctx context.Context, instanceID string, instanceJID string, client *whatsmeow.Client, evt any) {
	if h.instanceChecker != nil && !h.instanceChecker.HasWebhook(ctx, instanceID) {
		h.log.Info("[dispatcher] evento ignorado: instância sem webhook configurado", zap.String("instance", instanceID))
		return
	}

	h.log.Debug("[dispatcher] processando evento para webhook", zap.String("instance", instanceID), zap.String("type", fmt.Sprintf("%T", evt)))

	h.confirmJIDFromEvent(ctx, evt)

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

	// Eventos marcados como "ignore" não geram webhook (ex.: mensagem
	// indescriptografável vinda de mim mesmo ou de grupo).
	if t, _ := normalized["type"].(string); t == "ignore" {
		return
	}

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
		if reaction := evt.Message.GetReactionMessage(); reaction != nil {
			result["type"] = "reaction"

			result["reactionEmoji"] = reaction.GetText()
			if key := reaction.GetKey(); key != nil {
				result["reactionMessageId"] = key.GetID()
			}

			senderJID := evt.Info.Sender.String()
			if strings.Contains(senderJID, "@lid") && !evt.Info.SenderAlt.IsEmpty() {
				senderJID = evt.Info.SenderAlt.String()
			}
			result["from"] = senderJID

			chatJID := evt.Info.Chat.String()
			if strings.Contains(chatJID, "@lid") {
				if evt.Info.IsFromMe && !evt.Info.RecipientAlt.IsEmpty() && strings.Contains(evt.Info.RecipientAlt.String(), "@s.whatsapp.net") {
					chatJID = evt.Info.RecipientAlt.String()
				} else if !evt.Info.IsFromMe && !evt.Info.SenderAlt.IsEmpty() && strings.Contains(evt.Info.SenderAlt.String(), "@s.whatsapp.net") {
					chatJID = evt.Info.SenderAlt.String()
				}
			}
			result["chatJID"] = chatJID
			if evt.Info.IsGroup {
				result["participant"] = senderJID
				result["author"] = senderJID
			}

			result["isFromMe"] = evt.Info.IsFromMe
			result["isGroup"] = evt.Info.IsGroup
			result["timestamp"] = evt.Info.Timestamp
			result["pushName"] = evt.Info.PushName
			return result
		}

		result["type"] = "message"

		senderJID := evt.Info.Sender.String()
		if strings.Contains(senderJID, "@lid") && !evt.Info.SenderAlt.IsEmpty() {
			senderJID = evt.Info.SenderAlt.String()
		}
		result["from"] = senderJID

		chatJID := evt.Info.Chat.String()
		if strings.Contains(chatJID, "@lid") {
			if evt.Info.IsFromMe && !evt.Info.RecipientAlt.IsEmpty() && strings.Contains(evt.Info.RecipientAlt.String(), "@s.whatsapp.net") {
				chatJID = evt.Info.RecipientAlt.String()
			} else if !evt.Info.IsFromMe && !evt.Info.SenderAlt.IsEmpty() && strings.Contains(evt.Info.SenderAlt.String(), "@s.whatsapp.net") {
				chatJID = evt.Info.SenderAlt.String()
			}
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

		// Track inbound messages for auto MarkRead before sending
		if !evt.Info.IsFromMe {
			messageSvc.TrackInbound(instanceID, chatJID, evt.Info.ID, senderJID)
			h.log.Info("[markread] inbound tracked",
				zap.String("key", instanceID+":"+chatJID),
				zap.String("msg_id", evt.Info.ID),
				zap.String("sender", senderJID))
		}

		if evt.Message.GetConversation() != "" {
			result["text"] = evt.Message.GetConversation()
		}

		if extText := evt.Message.GetExtendedTextMessage(); extText != nil {
			result["text"] = extText.GetText()
		}

		if img := evt.Message.GetImageMessage(); img != nil {
			h.log.Info("detectada imagem, iniciando processamento", zap.String("msg_id", evt.Info.ID))
			result["mediaType"] = "image"
			if img.GetCaption() != "" {
				result["caption"] = img.GetCaption()
			}
			result["mimetype"] = img.GetMimetype()
			result["fileSize"] = img.GetFileLength()

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

			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, vid, vid.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				} else {
					h.log.Warn("falha ao baixar vídeo, enviando webhook sem URL", zap.String("msg_id", evt.Info.ID))
				}
			}
		} else if ptv := evt.Message.GetPtvMessage(); ptv != nil {
			// Recado de vídeo (video note redondo). No protocolo é um VideoMessage no campo
			// ptvMessage → baixa igual a vídeo. Marcamos isPtv p/ o consumidor diferenciar na UI.
			h.log.Info("detectado vídeo em recado (PTV), iniciando processamento", zap.String("msg_id", evt.Info.ID))
			result["mediaType"] = "video"
			result["isPtv"] = true
			if ptv.GetCaption() != "" {
				result["caption"] = ptv.GetCaption()
			}
			result["mimetype"] = ptv.GetMimetype()
			result["fileSize"] = ptv.GetFileLength()
			result["duration"] = ptv.GetSeconds()

			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, ptv, ptv.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				} else {
					h.log.Warn("falha ao baixar vídeo em recado (PTV), enviando webhook sem URL", zap.String("msg_id", evt.Info.ID))
				}
			}
		} else if doc := evt.Message.GetDocumentMessage(); doc != nil {
			result["mediaType"] = "document"
			if doc.GetTitle() != "" {
				result["fileName"] = doc.GetTitle()
			}
			result["mimetype"] = doc.GetMimetype()
			result["fileSize"] = doc.GetFileLength()

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
			if client != nil && h.mediaStorage != nil {
				if mediaURL := h.downloadAndSaveMedia(ctx, instanceID, evt.Info.ID, client, stk, stk.GetMimetype()); mediaURL != "" {
					result["mediaUrl"] = mediaURL
				}
			}
		}

		var mentionedJids []string
		if extText := evt.Message.GetExtendedTextMessage(); extText != nil && extText.GetContextInfo() != nil {
			mentionedJids = extText.GetContextInfo().GetMentionedJID()
		} else if img := evt.Message.GetImageMessage(); img != nil && img.GetContextInfo() != nil {
			mentionedJids = img.GetContextInfo().GetMentionedJID()
		} else if vid := evt.Message.GetVideoMessage(); vid != nil && vid.GetContextInfo() != nil {
			mentionedJids = vid.GetContextInfo().GetMentionedJID()
		} else if aud := evt.Message.GetAudioMessage(); aud != nil && aud.GetContextInfo() != nil {
			mentionedJids = aud.GetContextInfo().GetMentionedJID()
		} else if doc := evt.Message.GetDocumentMessage(); doc != nil && doc.GetContextInfo() != nil {
			mentionedJids = doc.GetContextInfo().GetMentionedJID()
		}
		if len(mentionedJids) > 0 {
			result["mentionedJids"] = mentionedJids
		}

		// Edição de mensagem no protocolo novo: chega criptografada como secretEncryptedMessage
		// (SecretEncType=MESSAGE_EDIT), não mais como protocolMessage type 14. Decriptamos com o
		// message secret guardado e expomos o conteúdo novo + o id da mensagem original para o
		// consumidor aplicar a edição. Sem isso a edição some (o consumidor a trata como sync).
		if sec := evt.Message.GetSecretEncryptedMessage(); sec != nil &&
			sec.GetSecretEncType() == waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
			if client != nil {
				if decrypted, err := client.DecryptSecretEncryptedMessage(ctx, evt); err == nil {
					newText := decrypted.GetConversation()
					if newText == "" {
						newText = decrypted.GetExtendedTextMessage().GetText()
					}
					if targetKey := sec.GetTargetMessageKey(); targetKey != nil {
						result["editedMessageId"] = targetKey.GetID()
					}
					if newText != "" {
						result["editedText"] = newText
						h.log.Info("edição de mensagem decriptada",
							zap.String("msg_id", evt.Info.ID),
							zap.String("target", result["editedMessageId"].(string)))
					}
				} else {
					h.log.Warn("falha ao decriptar edição (secretEncryptedMessage)",
						zap.String("msg_id", evt.Info.ID), zap.Error(err))
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
	case *events.UndecryptableMessage:
		// Mensagem recebida que o WhatsApp não conseguiu descriptografar
		// (sessão Signal dessincronizada, comum após repareamento). O conteúdo
		// nunca chega; emitimos um marcador para o backend registrar na conversa
		// em vez de o inbound sumir silenciosamente. O whatsmeow já pediu reenvio.
		if evt.Info.IsFromMe || evt.Info.IsGroup {
			result["type"] = "ignore"
			break
		}
		result["type"] = "message"
		result["undecryptable"] = true
		result["messageType"] = "unsupported"

		senderJID := evt.Info.Sender.String()
		if strings.Contains(senderJID, "@lid") && !evt.Info.SenderAlt.IsEmpty() {
			senderJID = evt.Info.SenderAlt.String()
		}
		result["from"] = senderJID

		chatJID := evt.Info.Chat.String()
		if strings.Contains(chatJID, "@lid") && !evt.Info.SenderAlt.IsEmpty() && strings.Contains(evt.Info.SenderAlt.String(), "@s.whatsapp.net") {
			chatJID = evt.Info.SenderAlt.String()
		}
		result["chatJID"] = chatJID
		result["to"] = chatJID
		result["isFromMe"] = false
		result["isGroup"] = false
		result["messageId"] = evt.Info.ID
		result["timestamp"] = evt.Info.Timestamp
		result["pushName"] = evt.Info.PushName
		// Visualização única: o servidor entrega só um stub (a mídia não vem a dispositivos
		// vinculados, por privacidade — igual ao WhatsApp Web "veja no celular"). Diferenciamos
		// pelo UnavailableType para o consumidor mostrar o aviso certo em vez de "indisponível".
		if evt.UnavailableType == events.UnavailableTypeViewOnce {
			result["unavailableType"] = "view_once"
			result["text"] = "🔒 Mensagem de visualização única — abra no celular"
		} else {
			result["text"] = "Mensagem indisponível"
		}
	case *events.ChatPresence:
		// Indicador "digitando…" / "gravando áudio". Emitido pelo WhatsApp quando o contato
		// está compondo. Efêmero — o consumidor decide exibir e por quanto tempo.
		// O Chat/Sender costuma vir como @lid; resolvemos para o número (PN) para o consumidor
		// conseguir casar com o contato/conversa.
		result["type"] = "chat_presence"

		chatJID := evt.Chat
		if chatJID.Server == types.HiddenUserServer && client != nil && client.Store != nil && client.Store.LIDs != nil {
			if pn, err := client.Store.LIDs.GetPNForLID(ctx, chatJID.ToNonAD()); err == nil && !pn.IsEmpty() {
				chatJID = pn
			}
		}
		senderJID := evt.Sender
		if senderJID.Server == types.HiddenUserServer && client != nil && client.Store != nil && client.Store.LIDs != nil {
			if pn, err := client.Store.LIDs.GetPNForLID(ctx, senderJID.ToNonAD()); err == nil && !pn.IsEmpty() {
				senderJID = pn
			}
		}

		result["from"] = senderJID.String()
		result["chatJID"] = chatJID.String()
		result["state"] = string(evt.State) // "composing" | "paused"
		if evt.Media != "" {
			result["media"] = string(evt.Media) // "" (texto) | "audio"
		}
	case *events.Connected:
		result["type"] = "connected"
	case *events.Disconnected:
		result["type"] = "disconnected"
	case *events.LoggedOut:
		result["type"] = "disconnected"
		result["reason"] = evt.Reason.String()
	case *events.NotifyAccountReachoutTimelock:
		// Notificação AUTORITATIVA do servidor sobre a trava de reach-out (a causa do erro 463).
		// É a fonte exata do estado da restrição — dispensa chutar duração (não é "sempre 7 dias"):
		//   IsActive=true  → restrição vigente; TimeEnforcementEnds = quando expira (data exata).
		//   IsActive=false → restrição saiu → o consumidor devolve a conexão para "conectado".
		// Complementa o 463 síncrono do envio (message/service.go), que é só o gatilho imediato.
		if evt.IsActive {
			result["type"] = "temporary_ban"
			result["reason"] = "account reachout timelock"
			result["code"] = 463
		} else {
			result["type"] = "restriction_lifted"
		}
		result["active"] = evt.IsActive
		if evt.EnforcementType != "" {
			result["enforcementType"] = evt.EnforcementType
		}
		if !evt.TimeEnforcementEnds.IsZero() {
			// RFC3339 no JSON do webhook → o consumidor faz new Date(restrictedUntil).
			result["restrictedUntil"] = evt.TimeEnforcementEnds.Time
		}
	default:
		result["type"] = "unknown"
		result["eventType"] = fmt.Sprintf("%T", evt)
	}

	if data, err := json.Marshal(evt); err == nil {
		result["raw"] = json.RawMessage(data)
	}

	return result
}

func (h *EventHandler) downloadAndSaveMedia(ctx context.Context, instanceID string, messageID string, client *whatsmeow.Client, downloadable whatsmeow.DownloadableMessage, mimetype string) string {
	h.log.Info("baixando mídia",
		zap.String("instance_id", instanceID),
		zap.String("message_id", messageID),
		zap.String("mimetype", mimetype),
	)

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

	mediaID, err := h.mediaStorage.Save(ctx, instanceID, messageID, data, mimetype)
	if err != nil {
		h.log.Error("erro ao salvar mídia",
			zap.String("instance_id", instanceID),
			zap.String("message_id", messageID),
			zap.Error(err),
		)
		return ""
	}

	mediaURL := fmt.Sprintf("%s/api/media/%s/%s", h.apiBaseURL, instanceID, mediaID)

	h.log.Info("mídia salva e URL gerada",
		zap.String("instance_id", instanceID),
		zap.String("media_id", mediaID),
		zap.String("media_url", mediaURL),
	)

	return mediaURL
}

func (h *EventHandler) generateEventID() string {
	return uuid.New().String()
}
