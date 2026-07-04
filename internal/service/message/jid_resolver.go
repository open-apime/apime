package message

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage/model"
)

var jidCache sync.Map

type jidCacheEntry struct {
	jid       types.JID
	expiresAt time.Time
}

func (s *Service) ResolveJID(ctx context.Context, client *whatsmeow.Client, phone string) (types.JID, error) {
	phone = strings.TrimSpace(phone)

	if phone == "" {
		return types.EmptyJID, errors.New("telefone vazio")
	}

	if strings.Contains(phone, "@g.us") || strings.Contains(phone, "@broadcast") {
		return types.ParseJID(phone)
	}

	if !strings.Contains(phone, "@") {
		phone = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, phone)
	}

	phone = strings.TrimSuffix(phone, "@s.whatsapp.net")

	if val, ok := jidCache.Load(phone); ok {
		entry := val.(jidCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			if entry.jid.IsEmpty() {
				s.log.Debug("JID negativo (não está no WhatsApp) resolvido via cache", zap.String("phone", phone))
				return types.EmptyJID, fmt.Errorf("%w: número não registrado no WhatsApp (cache)", ErrInvalidJID)
			}
			s.log.Debug("JID resolvido via cache em memória", zap.String("phone", phone), zap.String("jid", entry.jid.String()))
			return entry.jid.ToNonAD(), nil
		}
		jidCache.Delete(phone)
	}

	if s.contactRepo != nil {
		if contact, err := s.contactRepo.GetByPhone(ctx, phone); err == nil {
			if contact.JID == "" {
				negativeTTL := time.Duration(s.cfg.JIDCacheNegativeTTLDays) * 24 * time.Hour
				if time.Since(contact.UpdatedAt) < negativeTTL {
					s.log.Debug("JID negativo (não está no WhatsApp) resolvido via banco de dados", zap.String("phone", phone))
					jidCache.Store(phone, jidCacheEntry{jid: types.EmptyJID, expiresAt: contact.UpdatedAt.Add(negativeTTL)})
					return types.EmptyJID, fmt.Errorf("%w: número não registrado no WhatsApp (DB cache)", ErrInvalidJID)
				}
			} else {
				jid, jerr := types.ParseJID(contact.JID)
				if jerr == nil {
					jid = jid.ToNonAD()
					positiveDBTTL := time.Duration(s.cfg.JIDCachePositiveDBTTLDays) * 24 * time.Hour
					if time.Since(contact.UpdatedAt) < positiveDBTTL {
						s.log.Debug("JID resolvido via banco de dados", zap.String("phone", phone), zap.String("jid", jid.String()))
						positiveTTL := time.Duration(s.cfg.JIDCachePositiveTTLHours) * time.Hour
						jidCache.Store(phone, jidCacheEntry{jid: jid, expiresAt: time.Now().Add(positiveTTL)})
						return jid, nil
					}
					s.log.Info("JID positivo expirado no banco, revalidando via IsOnWhatsApp", zap.String("phone", phone), zap.String("oldJid", jid.String()), zap.Duration("age", time.Since(contact.UpdatedAt)))
				}
			}
		}
	}

	if !strings.HasPrefix(phone, "55") {
		return types.ParseJID(phone + "@s.whatsapp.net")
	}

	// Query sequentially, one number at a time, to mimic human behavior:
	// if the original is not found and a variant exists, retry it after a delay.

	delay := 1000 + rand.Intn(2000)
	s.log.Debug("Aplicando delay de segurança antes de IsOnWhatsApp", zap.Int("ms", delay))
	time.Sleep(time.Duration(delay) * time.Millisecond)

	s.log.Debug("Consultando IsOnWhatsApp", zap.String("phone", phone))
	resp, err := client.IsOnWhatsApp(ctx, []string{phone})
	if err != nil {
		s.log.Error("falha ao consultar IsOnWhatsApp - abortando envio", zap.String("phone", phone), zap.Error(err))
		return types.EmptyJID, fmt.Errorf("falha ao validar número no WhatsApp: %w", err)
	}

	resolvedJID := types.EmptyJID
	for _, item := range resp {
		if item.IsIn && item.JID.User != "" {
			resolvedJID = item.JID
			break
		}
	}

	// If not found, try a variant (Brazil only, one number at a time).
	if resolvedJID.IsEmpty() {
		var variant string

		if len(phone) == 13 && strings.HasPrefix(phone, "55") {
			// 13 digits: try without the extra leading 9.
			variant = phone[:4] + phone[5:]
		} else if len(phone) == 12 && strings.HasPrefix(phone, "55") {
			afterDDD := phone[4:]
			// 12 digits: add the 9 only for mobile numbers (prefix 6-9).
			if len(afterDDD) > 0 && afterDDD[0] >= '6' && afterDDD[0] <= '9' {
				variant = phone[:4] + "9" + afterDDD
			}
		}

		if variant != "" {
			variantDelay := 1000 + rand.Intn(2000)
			s.log.Debug("Tentando variante com delay", zap.String("variant", variant), zap.Int("ms", variantDelay))
			time.Sleep(time.Duration(variantDelay) * time.Millisecond)

			resp2, err2 := client.IsOnWhatsApp(ctx, []string{variant})
			if err2 != nil {
				s.log.Warn("falha ao consultar variante", zap.String("variant", variant), zap.Error(err2))
			} else {
				for _, item := range resp2 {
					if item.IsIn && item.JID.User != "" {
						resolvedJID = item.JID
						break
					}
				}
			}
		}
	}

	if resolvedJID.IsEmpty() {
		negativeTTL := time.Duration(s.cfg.JIDCacheNegativeTTLDays) * 24 * time.Hour
		s.log.Warn("WhatsApp não encontrado - registrando em cache negativo", zap.String("phone", phone), zap.Duration("ttl", negativeTTL))
		jidCache.Store(phone, jidCacheEntry{jid: types.EmptyJID, expiresAt: time.Now().Add(negativeTTL)})

		if s.contactRepo != nil {
			_ = s.contactRepo.Upsert(ctx, model.Contact{
				Phone: phone,
				JID:   "",
			})
		}

		return types.EmptyJID, fmt.Errorf("%w: número não registrado no WhatsApp", ErrInvalidJID)
	}

	positiveTTL := time.Duration(s.cfg.JIDCachePositiveTTLHours) * time.Hour
	jidCache.Store(phone, jidCacheEntry{jid: resolvedJID, expiresAt: time.Now().Add(positiveTTL)})
	if s.contactRepo != nil {
		_ = s.contactRepo.Upsert(ctx, model.Contact{
			Phone: phone,
			JID:   resolvedJID.String(),
		})
	}

	return resolvedJID, nil
}

// ConfirmJID invalidates the negative cache and persists the positive mapping
// based on a real event (message, receipt, presence) that proves the number is on WhatsApp.
// It only accepts JIDs from the s.whatsapp.net server.
func (s *Service) ConfirmJID(ctx context.Context, jid types.JID) {
	if jid.Server != types.DefaultUserServer || jid.User == "" {
		return
	}
	// Normalize to a user JID (device 0): events carry a device part
	// (e.g. 554788359190:48@s.whatsapp.net) and SendMessage requires a recipient without a device.
	jid = jid.ToNonAD()
	phone := jid.User
	jidStr := jid.String()

	// Fast path: a valid positive cache entry with the same JID means nothing to do, no DB access.
	if val, loaded := jidCache.Load(phone); loaded {
		if entry, ok := val.(jidCacheEntry); ok && !entry.jid.IsEmpty() && entry.jid.String() == jidStr && time.Now().Before(entry.expiresAt) {
			return
		}
		if entry, ok := val.(jidCacheEntry); ok && entry.jid.IsEmpty() {
			s.log.Info("cache negativo invalidado por evento", zap.String("phone", phone))
		}
		jidCache.Delete(phone)
	}

	positiveTTL := time.Duration(s.cfg.JIDCachePositiveTTLHours) * time.Hour
	jidCache.Store(phone, jidCacheEntry{jid: jid, expiresAt: time.Now().Add(positiveTTL)})

	if s.contactRepo == nil {
		return
	}
	if err := s.contactRepo.Upsert(ctx, model.Contact{Phone: phone, JID: jidStr}); err != nil {
		s.log.Warn("falha ao persistir JID confirmado por evento", zap.String("phone", phone), zap.Error(err))
	}
}
