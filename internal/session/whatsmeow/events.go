package whatsmeow

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage/model"
)

func (m *Manager) handleEvent(instanceID string, evt any) {
	m.mu.RLock()
	callback := m.onStatusChange
	handler := m.eventHandler
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	instanceJID := ""
	if exists && client != nil && client.Store != nil && client.Store.ID != nil {
		instanceJID = client.Store.ID.String()
	}

	if handler != nil {

		switch evt.(type) {
		case *events.Message, *events.Receipt, *events.Presence,
			*events.UndecryptableMessage, *events.ChatPresence:
			// UndecryptableMessage covers view-once (stub without media) and desynced sessions;
			// ChatPresence is the "typing…" indicator. Without forwarding these, the
			// normalizeEvent that handles them would be dead code.
			go handler.Handle(context.Background(), instanceID, instanceJID, client, evt)
		}
	}

	if receipt, ok := evt.(*events.Receipt); ok {
		m.log.Debug("receipt recebido",
			zap.String("instance_id", instanceID),
			zap.String("type", string(receipt.Type)),
			zap.Int("message_count", len(receipt.MessageIDs)))

		if receipt.Type == types.ReceiptTypeRetry {
			m.log.Warn("RECEBIDO RETRY RECEIPT - Possível falha de decriptação no destino. Acionando reset proativo.",
				zap.String("instance_id", instanceID),
				zap.Strings("msg_ids", receipt.MessageIDs),
				zap.String("chat", receipt.Chat.String()),
				zap.String("sender", receipt.Sender.String()))

			go func() {
				ctx := context.Background()

				// In a group, receipt.Chat is the group JID (@g.us), which is NOT a 1:1
				// Signal session: resetting session/identity by the group JID corrupts the
				// group's encryption state (sender keys) and stops receiving messages from
				// the other members. The correct reset target is the participant (receipt.Sender).
				if receipt.Chat.Server == types.GroupServer {
					if receipt.Sender.IsEmpty() {
						m.log.Warn("retry receipt em grupo sem sender identificável - ignorando reset para não corromper sender keys do grupo",
							zap.String("instance_id", instanceID),
							zap.String("chat", receipt.Chat.String()))
						return
					}
					m.log.Info("retry receipt em grupo - acionando reset apenas do participante (não do grupo)",
						zap.String("instance_id", instanceID),
						zap.String("chat", receipt.Chat.String()),
						zap.String("sender", receipt.Sender.String()))
					_ = m.ResetContactSession(ctx, instanceID, receipt.Sender.String())
					return
				}

				m.log.Info("Acionando reset completo (sessão + identidade) devido a retry receipt",
					zap.String("instance_id", instanceID),
					zap.String("chat", receipt.Chat.String()))
				_ = m.ResetContactSession(ctx, instanceID, receipt.Chat.String())

				// If the sender is a linked device different from the main chat, reset it too
				if receipt.Sender.User == receipt.Chat.User && receipt.Sender.Device != receipt.Chat.Device && receipt.Sender.Device != 0 {
					m.log.Info("Sender é device linked com sessão diferente, acionando reset adicional",
						zap.String("instance_id", instanceID),
						zap.String("sender", receipt.Sender.String()),
						zap.Uint16("device", receipt.Sender.Device))
					_ = m.ResetContactSession(ctx, instanceID, receipt.Sender.String())
				}
			}()
		}
	}

	switch v := evt.(type) {
	case *events.Connected:
		m.log.Info("instância conectada - dispositivo liberado, sincronizando dados essenciais em background",
			zap.String("instance_id", instanceID))

		m.mu.Lock()
		if timer, exists := m.disconnectDebounce[instanceID]; exists {
			timer.Stop()
			delete(m.disconnectDebounce, instanceID)
			m.log.Info("reconexão silenciosa detectada (instabilidade de rede recuperada)", zap.String("instance_id", instanceID))
		}
		m.expectedDisconnect[instanceID] = false
		m.connectedAt[instanceID] = time.Now()
		m.mu.Unlock()

		if client != nil {
			jitterHash := fnv.New32a()
			jitterHash.Write([]byte(instanceID))
			client.AutoReconnectErrors = int(jitterHash.Sum32()%15) + 1 // 1-15 → delay 2-30s
		}

		if handler != nil {
			go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
		}

		m.mu.RLock()
		client, exists := m.clients[instanceID]
		m.mu.RUnlock()
		if exists && client != nil {
			go func() {
				for attempt := 1; attempt <= 10; attempt++ {
					time.Sleep(time.Duration(attempt) * time.Second)
					if !client.IsLoggedIn() {
						return
					}
					m.log.Debug("tentando enviar presence de ativação", zap.String("instance_id", instanceID), zap.Int("attempt", attempt))
					if err := client.SendPresence(context.Background(), types.PresenceAvailable); err != nil {
						m.log.Warn("falha ao enviar presence no Connected", zap.String("instance_id", instanceID), zap.Error(err))
						if attempt == 10 {
							m.log.Error("limite de tentativas de presence atingido", zap.String("instance_id", instanceID))
						}
						continue
					}

					m.log.Info("presence enviado com sucesso - aguardando sincronização de app state", zap.String("instance_id", instanceID))

					go func() {
						m.log.Debug("safety timer iniciado: aguardando critical_block", zap.String("instance_id", instanceID))
						time.Sleep(20 * time.Second)

						m.mu.RLock()
						isReady := m.sessionReady[instanceID]
						m.mu.RUnlock()

						if !isReady {
							m.mu.Lock()
							m.sessionReady[instanceID] = true
							m.mu.Unlock()
							m.log.Warn("safety timer trigger: sessão marcada como pronta (critical_block excedeu 20s)",
								zap.String("instance_id", instanceID))
						}
					}()
					return
				}

				time.Sleep(5 * time.Second)
				m.mu.Lock()
				m.sessionReady[instanceID] = true
				m.mu.Unlock()
			}()
		}

		m.logConnectionEvent(instanceID, "connected", `{"message":"Instância conectada ao WhatsApp"}`)

		if callback != nil {
			callback(instanceID, "active")
		}
	case *events.PairSuccess:
		m.log.Info("pareamento concluído",
			zap.String("instance_id", instanceID),
			zap.String("user_jid", v.ID.String()),
		)
		if callback != nil {
			callback(instanceID, "active")
		}
	case *events.Disconnected:
		m.mu.Lock()
		delete(m.connectedAt, instanceID)
		if m.expectedDisconnect[instanceID] {
			m.log.Info("desconexão intencional processada imediatamente", zap.String("instance_id", instanceID))
			m.expectedDisconnect[instanceID] = false
			m.mu.Unlock()

			if handler != nil {
				go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
			}
		} else {
			m.log.Info("instância desconectada, iniciando debounce", zap.String("instance_id", instanceID))
			if t, exists := m.disconnectDebounce[instanceID]; exists {
				t.Stop()
			}

			timer := time.AfterFunc(5*time.Second, func() {
				m.mu.Lock()
				delete(m.disconnectDebounce, instanceID)
				m.mu.Unlock()

				m.log.Warn("debounce expirado, confirmando desconexão", zap.String("instance_id", instanceID))
				m.logConnectionEvent(instanceID, "disconnected", `{"message":"Conexão perdida com o WhatsApp"}`)
				m.updateInstanceStatus(instanceID, model.InstanceStatusError)
				if callback != nil {
					callback(instanceID, "error")
				}

				if handler != nil {
					go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
				}
			})
			m.disconnectDebounce[instanceID] = timer
			m.mu.Unlock()
			return
		}

		m.log.Warn("instância desconectada",
			zap.String("instance_id", instanceID),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.LoggedOut:
		m.mu.Lock()
		if timer, exists := m.disconnectDebounce[instanceID]; exists {
			timer.Stop()
			delete(m.disconnectDebounce, instanceID)
		}
		wasManual := m.expectedDisconnect[instanceID]
		m.mu.Unlock()

		m.log.Warn("instância deslogada",
			zap.String("instance_id", instanceID),
			zap.String("reason", v.Reason.String()),
		)

		if !wasManual {
			m.logConnectionEvent(instanceID, "logged_out", fmt.Sprintf(`{"message":"Dispositivo removido pelo WhatsApp","reason":"%s"}`, v.Reason.String()))
		}

		if handler != nil {
			go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
		}

		m.updateInstanceStatus(instanceID, model.InstanceStatusDisconnected)
		if callback != nil {
			callback(instanceID, "disconnected")
		}

		go func() {
			time.Sleep(1 * time.Second)
			m.DeleteSession(instanceID)
		}()
	case *events.TemporaryBan:
		m.log.Error("instância temporariamente banida pelo WhatsApp",
			zap.String("instance_id", instanceID),
			zap.String("code", v.Code.String()),
			zap.Duration("expire", v.Expire),
		)

		banReason := tempBanReasonPT(v.Code)
		expireStr := ""
		if v.Expire > 0 {
			expireStr = v.Expire.String()
		}
		m.logConnectionEvent(instanceID, "temporary_ban", fmt.Sprintf(
			`{"message":"Ban temporário pelo WhatsApp","reason":"%s","code":%d,"expire":"%s"}`,
			banReason, int(v.Code), expireStr,
		))

		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.ConnectFailure:
		m.log.Error("falha ao conectar instância",
			zap.String("instance_id", instanceID),
			zap.String("reason", v.Reason.String()),
			zap.String("message", v.Message),
		)
		m.logConnectionEvent(instanceID, "connect_failure", fmt.Sprintf(
			`{"message":"Falha ao conectar ao WhatsApp","reason":"%s","detail":"%s"}`,
			v.Reason.String(), v.Message,
		))
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.StreamError:
		m.log.Error("erro de stream na instância",
			zap.String("instance_id", instanceID),
			zap.String("code", v.Code),
		)
		m.logConnectionEvent(instanceID, "stream_error", fmt.Sprintf(
			`{"message":"Erro de comunicação com o WhatsApp","code":"%s"}`,
			v.Code,
		))
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.PairError:
		m.log.Error("erro no pareamento",
			zap.String("instance_id", instanceID),
			zap.Error(v.Error),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.ClientOutdated:
		m.log.Error("cliente WhatsMeow desatualizado",
			zap.String("instance_id", instanceID),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.HistorySync:
		m.log.Info("history sync recebido",
			zap.String("instance_id", instanceID),
			zap.Int("conversations", len(v.Data.GetConversations())),
		)
	case *events.AppStateSyncComplete:
		m.log.Debug("app state sync completo",
			zap.String("instance_id", instanceID),
			zap.String("name", string(v.Name)),
			zap.Uint64("version", v.Version),
		)

		if v.Name == "critical_block" || v.Name == "critical_unblock_low" {
			m.log.Info("sincronização crítica concluída - prekeys E2E prontas",
				zap.String("instance_id", instanceID),
				zap.String("sync_name", string(v.Name)))

			m.mu.Lock()
			m.sessionReady[instanceID] = true
			m.mu.Unlock()
			m.log.Info("sessão pronta para enviar mensagens (app state sync completo)",
				zap.String("instance_id", instanceID))
		}
	case *events.AppStateSyncError:
		m.log.Error("erro no app state sync",
			zap.String("instance_id", instanceID),
			zap.String("name", string(v.Name)),
			zap.Error(v.Error),
		)
	case *events.StreamReplaced:
		m.log.Warn("stream substituído pelo servidor - limpando cache de dispositivos",
			zap.String("instance_id", instanceID))

		if client != nil {

			go func() {
				time.Sleep(2 * time.Second)
				if client.IsLoggedIn() {
					_ = client.SendPresence(context.Background(), types.PresenceAvailable)
					m.log.Debug("presence reenviado após stream replaced", zap.String("instance_id", instanceID))
				}
			}()
		}
	case *events.KeepAliveTimeout:
		m.log.Warn("keepalive timeout detectado - possível reconexão iminente",
			zap.String("instance_id", instanceID),
			zap.Int("error_count", v.ErrorCount))
	case *events.KeepAliveRestored:
		m.log.Info("keepalive restaurado - limpando cache de dispositivos",
			zap.String("instance_id", instanceID))

	default:

		m.log.Debug("evento recebido",
			zap.String("instance_id", instanceID),
			zap.String("event_type", fmt.Sprintf("%T", evt)),
		)
	}
}

func (m *Manager) updateInstanceStatus(instanceID string, status model.InstanceStatus) {
	if m.instanceRepo == nil {
		return
	}

	ctx := context.Background()
	inst, err := m.instanceRepo.GetByID(ctx, instanceID)
	if err != nil {
		m.log.Warn("erro ao buscar instância para atualizar status",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	inst.Status = status
	if _, err := m.instanceRepo.Update(ctx, inst); err != nil {
		m.log.Warn("erro ao atualizar status da instância no banco",
			zap.String("instance_id", instanceID),
			zap.String("status", string(status)),
			zap.Error(err),
		)
	} else {
		m.log.Info("status da instância atualizado no banco",
			zap.String("instance_id", instanceID),
			zap.String("status", string(status)),
		)
	}
}

func (m *Manager) logConnectionEvent(instanceID, eventType, payload string) {
	if m.eventLogRepo == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := m.eventLogRepo.Create(ctx, model.EventLog{
			InstanceID: instanceID,
			Type:       eventType,
			Payload:    payload,
		}); err != nil {
			m.log.Warn("erro ao gravar evento de conexão",
				zap.String("instance_id", instanceID),
				zap.String("event_type", eventType),
				zap.Error(err),
			)
		}
	}()
}
