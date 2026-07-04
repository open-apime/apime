package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/google/uuid"
	messageSvc "github.com/open-apime/apime/internal/service/message"
)

// confirmJIDFromEvent extracts the s.whatsapp.net JID from the event (resolving @lid via Alt)
// and confirms it in the resolver, invalidating the negative cache and persisting the mapping.
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
			// Video note (round video message). In the protocol it's a VideoMessage in the
			// ptvMessage field → downloaded like a video. We flag isPtv so the consumer can tell them apart in the UI.
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

		// Message edit in the new protocol: it arrives encrypted as a secretEncryptedMessage
		// (SecretEncType=MESSAGE_EDIT), no longer as protocolMessage type 14. We decrypt it with the
		// stored message secret and expose the new content + the original message id so the
		// consumer can apply the edit. Without this the edit is lost (the consumer treats it as a sync).
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
		// Inbound message that WhatsApp could not decrypt (desynchronized Signal
		// session, common after re-pairing). The content never arrives; we emit a
		// marker so the backend records it in the conversation instead of the
		// inbound silently disappearing. whatsmeow has already requested a resend.
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
		// View once: the server delivers only a stub (the media isn't sent to linked
		// devices, for privacy — same as WhatsApp Web's "view on your phone"). We tell it
		// apart by UnavailableType so the consumer shows the right notice instead of "unavailable".
		if evt.UnavailableType == events.UnavailableTypeViewOnce {
			result["unavailableType"] = "view_once"
			result["text"] = "🔒 Mensagem de visualização única — abra no celular"
		} else {
			result["text"] = "Mensagem indisponível"
		}
	case *events.ChatPresence:
		// "Typing…" / "recording audio" indicator. Emitted by WhatsApp while the contact
		// is composing. Ephemeral — the consumer decides whether and how long to show it.
		// Chat/Sender usually comes as @lid; we resolve it to the phone number (PN) so the
		// consumer can match it with the contact/conversation.
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
			result["media"] = string(evt.Media) // "" (text) | "audio"
		}
	case *events.Connected:
		result["type"] = "connected"
	case *events.Disconnected:
		result["type"] = "disconnected"
	case *events.LoggedOut:
		result["type"] = "disconnected"
		result["reason"] = evt.Reason.String()
	case *events.NotifyAccountReachoutTimelock:
		// AUTHORITATIVE server notification about the reach-out timelock (the cause of error 463).
		// It's the exact source of the restriction state — no need to guess the duration (it's not "always 7 days"):
		//   IsActive=true  → restriction in effect; TimeEnforcementEnds = when it expires (exact date).
		//   IsActive=false → restriction lifted → the consumer moves the connection back to "connected".
		// Complements the synchronous 463 from the send path (message/service.go), which is only the immediate trigger.
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
			// RFC3339 in the webhook JSON → the consumer does new Date(restrictedUntil).
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
