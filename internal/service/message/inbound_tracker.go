package message

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

const inboundTTL = 14 * 24 * time.Hour // 14 days

// normalizeChatKey normaliza o chatJID para a forma sem device (ToNonAD) antes de
// montar a chave do tracker. Necessário porque contatos endereçados via LID têm o
// Chat resolvido com sufixo de device (ex.: "554899827309:80@s.whatsapp.net"), enquanto
// o envio busca o JID sem device (ToNonAD). Sem isso, store e lookup nunca batem e o
// auto-markread (ticks azuis) nunca dispara para contatos LID. ParseJID lida com
// s.whatsapp.net/lid/g.us/broadcast; em caso de erro, mantém a string original.
func normalizeChatKey(chatJID string) string {
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return chatJID
	}
	return jid.ToNonAD().String()
}

// inboundEntry tracks the last inbound message per chat for auto MarkRead
type inboundEntry struct {
	messageID string
	senderJID string
	trackedAt time.Time
}

var (
	inboundTracker sync.Map // key: "instanceID:chatJID" → value: inboundEntry
	cleanupOnce    sync.Once
)

// TrackInbound stores the last inbound message ID for a given chat.
// Called by the event handler when a message is received.
// The Send function uses this to auto-mark messages as read before sending.
func TrackInbound(instanceID, chatJID, messageID, senderJID string) {
	cleanupOnce.Do(startCleanupLoop)
	key := instanceID + ":" + normalizeChatKey(chatJID)
	inboundTracker.Store(key, inboundEntry{
		messageID: messageID,
		senderJID: senderJID,
		trackedAt: time.Now(),
	})
}

// popLastInbound returns and removes the last inbound message for a given chat.
// Returns false if no inbound message is tracked or if the entry has expired.
func popLastInbound(instanceID, chatJID string) (inboundEntry, bool) {
	key := instanceID + ":" + normalizeChatKey(chatJID)
	val, ok := inboundTracker.LoadAndDelete(key)
	if !ok {
		return inboundEntry{}, false
	}
	entry := val.(inboundEntry)
	if time.Since(entry.trackedAt) > inboundTTL {
		return inboundEntry{}, false
	}
	return entry, true
}

// hasRecentInbound informa se há um inbound rastreado e válido para o chat, SEM removê-lo
// (diferente de popLastInbound). Serve como prova de "conversa já aberta": se o contato nos
// mandou mensagem recentemente, o envio é uma resposta — não uma nova conversa.
func hasRecentInbound(instanceID, chatJID string) bool {
	key := instanceID + ":" + normalizeChatKey(chatJID)
	val, ok := inboundTracker.Load(key)
	if !ok {
		return false
	}
	entry, ok := val.(inboundEntry)
	if !ok {
		return false
	}
	return time.Since(entry.trackedAt) <= inboundTTL
}

func startCleanupLoop() {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			inboundTracker.Range(func(key, val any) bool {
				if now.Sub(val.(inboundEntry).trackedAt) > inboundTTL {
					inboundTracker.Delete(key)
				}
				return true
			})
		}
	}()
}
