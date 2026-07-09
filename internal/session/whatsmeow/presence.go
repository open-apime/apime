package whatsmeow

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"
)

func (m *Manager) FetchUserDevices(ctx context.Context, instanceID string, jids []string) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return fmt.Errorf("cliente não encontrado")
	}

	parsedJIDs := make([]types.JID, 0, len(jids))
	for _, j := range jids {
		jid, err := types.ParseJID(j)
		if err == nil {
			parsedJIDs = append(parsedJIDs, jid)
		}
	}

	if len(parsedJIDs) == 0 {
		return nil
	}

	devices, err := client.GetUserDevices(ctx, parsedJIDs)
	if err != nil {
		m.log.Error("erro ao buscar dispositivos dos usuários",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return err
	}

	m.log.Debug("lista de dispositivos atualizada com sucesso",
		zap.String("instance_id", instanceID),
		zap.Int("count", len(devices)),
	)
	return nil
}

func (m *Manager) ClearDeviceCache(instanceID string, jidStr string) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return fmt.Errorf("cliente não encontrado")
	}

	if _, err := types.ParseJID(jidStr); err != nil {
		return err
	}

	m.log.Debug("cache de dispositivos limpo",
		zap.String("instance_id", instanceID),
		zap.String("jid", jidStr),
	)
	return nil
}

func (m *Manager) SendChatPresence(ctx context.Context, instanceID, jidStr string, state types.Presence) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil || !client.IsLoggedIn() {
		return fmt.Errorf("cliente não conectado")
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return err
	}

	return client.SendChatPresence(ctx, jid, types.ChatPresence(state), types.ChatPresenceMediaText)
}

func (m *Manager) ResetContactSession(ctx context.Context, instanceID, jidStr string) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil || client.Store == nil ||
		client.Store.Sessions == nil || client.Store.Identities == nil {
		// A session still being restored (staggered boot) has a Store whose sub-stores aren't wired
		// yet — dereferencing them below panics. Bail out as a handled error instead.
		return fmt.Errorf("store não disponível")
	}
	if !client.IsLoggedIn() {
		return fmt.Errorf("instância não logada")
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return err
	}

	// Guard: a group JID (@g.us) has no 1:1 Signal session/identity.
	// Deleting them via the group's SignalAddress() corrupts the sender keys and breaks
	// receiving messages from the other members. A group reset must target the participant.
	if jid.Server == types.GroupServer {
		m.log.Warn("reset de sessão ignorado: JID de grupo não tem sessão Signal 1:1",
			zap.String("instance_id", instanceID),
			zap.String("target_jid", jidStr),
		)
		return nil
	}

	err = client.Store.Sessions.DeleteSession(ctx, jid.SignalAddress().String())
	if err != nil {
		m.log.Warn("aviso: falha ao deletar sessão do contato (pode não existir)",
			zap.String("instance_id", instanceID),
			zap.String("target_jid", jidStr),
			zap.Error(err),
		)
	}

	err = client.Store.Identities.DeleteIdentity(ctx, jid.SignalAddress().String())
	if err != nil {
		m.log.Warn("aviso: falha ao deletar identidade do contato",
			zap.String("instance_id", instanceID),
			zap.String("target_jid", jidStr),
			zap.Error(err),
		)
	}

	m.log.Info("RESET COMPLETO de criptografia realizado para o contato (sessão + identidade)",
		zap.String("instance_id", instanceID),
		zap.String("target_jid", jidStr),
	)
	return nil
}
