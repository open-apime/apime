package message

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

const inboundTTL = 14 * 24 * time.Hour // 14 days

// normalizeChatKey normalizes the chatJID to its device-less form (ToNonAD) before
// building the tracker key. Needed because contacts addressed via LID have their Chat
// resolved with a device suffix (e.g. "554899827309:80@s.whatsapp.net"), while sending
// looks up the device-less JID (ToNonAD). Without this, store and lookup never match and
// auto-markread (blue ticks) never fires for LID contacts. ParseJID handles
// s.whatsapp.net/lid/g.us/broadcast; on error, it keeps the original string.
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

// hasRecentInbound reports whether there is a tracked, still-valid inbound for the chat,
// WITHOUT removing it (unlike popLastInbound). It serves as proof of an "already open
// conversation": if the contact messaged us recently, an outgoing message is a reply, not
// a new conversation.
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
