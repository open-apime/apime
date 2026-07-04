package message

import (
	"context"
	"math/rand"
	"os"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"
)

// autoSaveContact ensures the recipient is present in the account's contact list (app state
// critical_unblock_low) at send time. The state lives only in the account/local Store — it is not
// mirrored to external systems.
//
// Flow (best-effort, does not block the send):
//  1. if a named contact already exists in Store.Contacts (local read, no network) → nothing to do;
//  2. an in-memory instance:jid cache covers the window between SendAppState and the app state echo
//     coming back to the Store, avoiding a duplicate resend in that interval;
//  3. per-instance throttle (1/s + jitter) to avoid emitting app state mutations in bursts.

var (
	// saveContactSeen marks instance:jid pairs already handled (avoids reprocessing before the app state echo).
	saveContactSeen sync.Map // key "instanceID:jid" -> expiresAt (time.Time)

	saveContactThrottleMu sync.Mutex
	saveContactLastByInst = map[string]time.Time{}
)

const (
	saveContactSeenTTL      = 10 * time.Minute
	saveContactMinInterval  = 1 * time.Second
	autoSaveContactDisabled = "APIME_AUTO_SAVE_CONTACT" // ="false" turns it off (default: on)
)

// autoSaveContact tries to save the contact before the send. displayName is the name the consumer
// already sends with the message; if empty, it falls back to the PushName known in the store; if
// neither is available, it does not save.
func (s *Service) autoSaveContact(ctx context.Context, instanceID string, client *whatsmeow.Client, toJID types.JID, displayName string) {
	if os.Getenv(autoSaveContactDisabled) == "false" {
		return
	}
	if client == nil || client.Store == nil || client.Store.Contacts == nil {
		return
	}
	// Individual contacts only (not groups/broadcast/newsletter).
	if toJID.Server != types.DefaultUserServer && toJID.Server != types.HiddenUserServer {
		return
	}

	pn := toJID.ToNonAD()
	var lid types.JID
	if pn.Server == types.HiddenUserServer {
		lid = pn
		if client.Store.LIDs != nil {
			if resolved, err := client.Store.LIDs.GetPNForLID(ctx, pn); err == nil && !resolved.IsEmpty() {
				pn = resolved.ToNonAD()
			}
		}
		if pn.Server == types.HiddenUserServer {
			return // without a PN the mutation cannot be indexed
		}
	} else if client.Store.LIDs != nil {
		if l, err := client.Store.LIDs.GetLIDForPN(ctx, pn); err == nil && !l.IsEmpty() {
			lid = l
		}
	}

	seenKey := instanceID + ":" + pn.String()
	if exp, ok := saveContactSeen.Load(seenKey); ok {
		if t, _ := exp.(time.Time); time.Now().Before(t) {
			return
		}
	}

	// "Already saved" gate: local read. Saved = FullName/FirstName set (PushName only = just known).
	contact, err := client.Store.Contacts.GetContact(ctx, pn)
	if err == nil && (contact.FullName != "" || contact.FirstName != "") {
		saveContactSeen.Store(seenKey, time.Now().Add(saveContactSeenTTL))
		return
	}

	// Name: whatever came with the send; otherwise the PushName the contact already advertised. No name → don't save.
	fullName := displayName
	if fullName == "" {
		fullName = contact.PushName
	}
	if fullName == "" {
		return
	}

	// Mark BEFORE sending (avoids a race between 2 simultaneous sends to the same number).
	saveContactSeen.Store(seenKey, time.Now().Add(saveContactSeenTTL))
	s.throttleSaveContact(instanceID)

	patch := appstate.BuildContact(pn, fullName, "", lid, false)
	if err := client.SendAppState(ctx, patch); err != nil {
		// Not fatal: clear the mark to retry on a future send.
		saveContactSeen.Delete(seenKey)
		s.log.Warn("auto-save de contato falhou (não crítico)",
			zap.String("instance_id", instanceID), zap.String("jid", pn.String()), zap.Error(err))
		return
	}
	s.log.Info("contato salvo na conta",
		zap.String("instance_id", instanceID), zap.String("jid", pn.String()))
}

// throttleSaveContact spaces out app state mutations per instance (only delays, never rejects).
func (s *Service) throttleSaveContact(instanceID string) {
	saveContactThrottleMu.Lock()
	now := time.Now()
	var wait time.Duration
	if last, ok := saveContactLastByInst[instanceID]; ok {
		if elapsed := now.Sub(last); elapsed < saveContactMinInterval {
			wait = saveContactMinInterval - elapsed
		}
	}
	next := now.Add(wait)
	saveContactLastByInst[instanceID] = next
	saveContactThrottleMu.Unlock()

	wait += time.Duration(rand.Intn(500)) * time.Millisecond
	if wait > 0 {
		time.Sleep(wait)
	}
}
