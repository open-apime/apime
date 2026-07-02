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

// autoSaveContact garante o destinatário presente na lista de contatos da conta (app state
// critical_unblock_low) no momento do envio. O estado vive só na conta/no Store local — não é
// espelhado para sistemas externos.
//
// Fluxo (best-effort, não bloqueia o envio):
//  1. se já há contato com nome no Store.Contacts (leitura local, sem rede) → nada a fazer;
//  2. cache em memória por instância:jid cobre a janela entre o SendAppState e o eco do app state
//     de volta ao Store, evitando reenvio duplicado nesse intervalo;
//  3. throttle por instância (1/s + jitter) para não emitir mutações de app state em rajada.

var (
	// saveContactSeen marca instance:jid já tratados (evita reprocessar antes do eco do app state).
	saveContactSeen sync.Map // key "instanceID:jid" -> expiresAt (time.Time)

	saveContactThrottleMu sync.Mutex
	saveContactLastByInst = map[string]time.Time{}
)

const (
	saveContactSeenTTL      = 10 * time.Minute
	saveContactMinInterval  = 1 * time.Second
	autoSaveContactDisabled = "APIME_AUTO_SAVE_CONTACT" // ="false" desliga (default: ligado)
)

// autoSaveContact tenta salvar o contato antes do envio. displayName é o nome que o consumidor já
// manda no envio; se vazio, cai para o PushName conhecido no store; se nem isso, não salva.
func (s *Service) autoSaveContact(ctx context.Context, instanceID string, client *whatsmeow.Client, toJID types.JID, displayName string) {
	if os.Getenv(autoSaveContactDisabled) == "false" {
		return
	}
	if client == nil || client.Store == nil || client.Store.Contacts == nil {
		return
	}
	// Só contatos individuais (não grupos/broadcast/newsletter).
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
			return // sem PN não dá p/ indexar a mutação
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

	// Gate "já salvo": leitura local. Salvo = FullName/FirstName preenchidos (só PushName = conhecido).
	contact, err := client.Store.Contacts.GetContact(ctx, pn)
	if err == nil && (contact.FullName != "" || contact.FirstName != "") {
		saveContactSeen.Store(seenKey, time.Now().Add(saveContactSeenTTL))
		return
	}

	// Nome: o que veio no envio; senão o PushName que o contato já anunciou. Sem nome → não salva.
	fullName := displayName
	if fullName == "" {
		fullName = contact.PushName
	}
	if fullName == "" {
		return
	}

	// Marca ANTES de enviar (evita corrida entre 2 envios simultâneos ao mesmo número).
	saveContactSeen.Store(seenKey, time.Now().Add(saveContactSeenTTL))
	s.throttleSaveContact(instanceID)

	patch := appstate.BuildContact(pn, fullName, "", lid, false)
	if err := client.SendAppState(ctx, patch); err != nil {
		// Não fatal: limpa a marca p/ tentar de novo num próximo envio.
		saveContactSeen.Delete(seenKey)
		s.log.Warn("auto-save de contato falhou (não crítico)",
			zap.String("instance_id", instanceID), zap.String("jid", pn.String()), zap.Error(err))
		return
	}
	s.log.Info("contato salvo na conta",
		zap.String("instance_id", instanceID), zap.String("jid", pn.String()))
}

// throttleSaveContact espaça mutações de app state por instância (só atrasa, não rejeita).
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
