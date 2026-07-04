package whatsmeow

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type outgoingMsgEntry struct {
	msg *waE2E.Message
	at  time.Time
}

const outgoingMsgCacheTTL = 30 * time.Minute

// CacheOutgoingMessage stores the raw protobuf of a sent message to allow
// reconstruction on retry receipts (including media, which cannot be rebuilt from the database).
func (m *Manager) CacheOutgoingMessage(id string, msg *waE2E.Message) {
	if id == "" || msg == nil {
		return
	}
	now := time.Now()
	m.outgoingMsgCache.Store(id, outgoingMsgEntry{msg: msg, at: now})
	// Opportunistic cleanup of expired entries.
	m.outgoingMsgCache.Range(func(k, v any) bool {
		if e, ok := v.(outgoingMsgEntry); ok && now.Sub(e.at) > outgoingMsgCacheTTL {
			m.outgoingMsgCache.Delete(k)
		}
		return true
	})
}

func (m *Manager) getMessageForRetryCallback(instanceID string) func(types.JID, types.JID, string) *waE2E.Message {
	return func(chat, sender types.JID, id string) *waE2E.Message {
		m.log.Debug("WhatsMeow solicitou mensagem para retry",
			zap.String("instance_id", instanceID),
			zap.String("msg_id", id),
			zap.String("chat", chat.String()))

		// 1. In-memory cache of the raw protobuf — covers any type, including media.
		if val, ok := m.outgoingMsgCache.Load(id); ok {
			if entry, ok := val.(outgoingMsgEntry); ok && entry.msg != nil && time.Since(entry.at) <= outgoingMsgCacheTTL {
				m.log.Info("mensagem reconstruída para retry via cache de protobuf",
					zap.String("instance_id", instanceID),
					zap.String("msg_id", id))
				return entry.msg
			}
			m.outgoingMsgCache.Delete(id)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		msg, err := m.messageRepo.GetByWhatsAppID(ctx, id)
		if err != nil {
			m.log.Warn("MENSAGEM NÃO ENCONTRADA no banco para retry solicitado pelo WhatsApp",
				zap.String("instance_id", instanceID),
				zap.String("msg_id", id),
				zap.String("chat", chat.String()),
				zap.Error(err))
			return nil
		}

		waMsg := &waE2E.Message{}
		switch msg.Type {
		case "text":
			waMsg.Conversation = proto.String(msg.Payload)
			m.log.Info("mensagem de TEXTO reconstruída para retry com sucesso",
				zap.String("instance_id", instanceID),
				zap.String("msg_id", id))
			return waMsg
		case "image", "video", "audio", "document":
			m.log.Warn("não foi possível reconstruir mensagem de MÍDIA para retry sem Raw protobuf (futura implementação)",
				zap.String("instance_id", instanceID),
				zap.String("msg_id", id),
				zap.String("type", msg.Type))
		default:
			m.log.Warn("tipo de mensagem desconhecido para reconstrução de retry",
				zap.String("type", msg.Type),
				zap.String("msg_id", id))
		}

		return nil
	}
}
