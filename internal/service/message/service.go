package message

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"mime"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"sync"

	"github.com/open-apime/apime/internal/config"
	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

var (
	jidCache sync.Map
)

type jidCacheEntry struct {
	jid       types.JID
	expiresAt time.Time
}

var (
	ErrInvalidPayload       = errors.New("payload inválido")
	ErrInstanceNotConnected = errors.New("instância não conectada")
	ErrInvalidJID           = errors.New("JID inválido")
	ErrUnsupportedMediaType = errors.New("tipo de mídia não suportado")
	ErrSessionUnavailable   = errors.New("sessão indisponível")
)

type Service struct {
	repo         storage.MessageRepository
	sessionMgr   SessionManager
	instanceRepo storage.InstanceRepository
	contactRepo  storage.ContactRepository
	queue        queue.Queue
	cfg          config.WhatsAppConfig
	log          *zap.Logger
}

type SessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
	IsSessionReady(instanceID string) bool
	HasSession(instanceID string, jid types.JID) (bool, error)
	GetPreKeyCount(instanceID string) (int, error)
	GetConnectedAt(instanceID string) time.Time
}

func NewService(repo storage.MessageRepository, q queue.Queue, cfg config.WhatsAppConfig, log *zap.Logger) *Service {
	return &Service{
		repo:  repo,
		queue: q,
		cfg:   cfg,
		log:   log,
	}
}

func NewServiceWithSession(repo storage.MessageRepository, sessionMgr SessionManager, instanceRepo storage.InstanceRepository, contactRepo storage.ContactRepository, q queue.Queue, cfg config.WhatsAppConfig, log *zap.Logger) *Service {
	return &Service{
		repo:         repo,
		sessionMgr:   sessionMgr,
		instanceRepo: instanceRepo,
		contactRepo:  contactRepo,
		queue:        q,
		cfg:          cfg,
		log:          log,
	}
}

type EnqueueInput struct {
	InstanceID string
	To         string
	Type       string
	Payload    string
}

func (s *Service) Enqueue(ctx context.Context, input EnqueueInput) (model.Message, error) {
	if input.InstanceID == "" || input.To == "" || input.Payload == "" {
		return model.Message{}, ErrInvalidPayload
	}
	message := model.Message{
		ID:         uuid.NewString(),
		InstanceID: input.InstanceID,
		To:         input.To,
		Type:       input.Type,
		Payload:    input.Payload,
		Status:     "queued",
	}
	msg, err := s.repo.Create(ctx, message)
	if err != nil {
		return msg, err
	}

	if s.queue != nil {
		event := queue.Event{
			ID:         msg.ID,
			InstanceID: msg.InstanceID,
			Type:       msg.Type,
			Payload: map[string]interface{}{
				"to":   msg.To,
				"text": msg.Payload,
			},
			CreatedAt: msg.CreatedAt,
		}
		if err := s.queue.Enqueue(ctx, event); err != nil {
			s.log.Error("erro ao enfileirar mensagem para o worker", zap.Error(err))
		}
	}

	return msg, nil
}

type SendInput struct {
	InstanceID string
	To         string
	Type       string
	Text       string
	MediaData  []byte
	MediaType  string
	Caption    string
	FileName   string
	Seconds    int
	PTT        bool
	MessageID  string
	Quoted        string
	Participant   string
	MentionedJids []string
}

func (s *Service) Send(ctx context.Context, input SendInput) (model.Message, error) {
	if s.sessionMgr == nil {
		return model.Message{}, errors.New("session manager não configurado")
	}

	if input.InstanceID == "" || input.To == "" {
		return model.Message{}, ErrInvalidPayload
	}

	instance, err := s.instanceRepo.GetByID(ctx, input.InstanceID)
	if err != nil {
		return model.Message{}, fmt.Errorf("instância não encontrada: %w", err)
	}

	if instance.Status != model.InstanceStatusActive {
		return model.Message{}, ErrInstanceNotConnected
	}

	client, err := s.sessionMgr.GetClient(input.InstanceID)
	if err != nil {
		ctxUpdate := context.Background()
		if instToUpdate, fetchErr := s.instanceRepo.GetByID(ctxUpdate, input.InstanceID); fetchErr == nil {
			instToUpdate.Status = model.InstanceStatusError
			_, _ = s.instanceRepo.Update(ctxUpdate, instToUpdate)
		}
		return model.Message{}, fmt.Errorf("cliente não encontrado: %w", err)
	}

	if !client.IsLoggedIn() {
		ctxUpdate := context.Background()
		if instToUpdate, fetchErr := s.instanceRepo.GetByID(ctxUpdate, input.InstanceID); fetchErr == nil {
			instToUpdate.Status = model.InstanceStatusDisconnected
			_, _ = s.instanceRepo.Update(ctxUpdate, instToUpdate)
		}
		return model.Message{}, ErrInstanceNotConnected
	}

	readyStart := time.Now()
	isReady := false
	poked := false

	connectedAt := s.sessionMgr.GetConnectedAt(input.InstanceID)
	isColdStart := time.Since(connectedAt) < 60*time.Second
	minPreKeys := 5
	if isColdStart {
		minPreKeys = 20
		s.log.Debug("Sessão em Cold Start detectada, aguardando estabilização maior",
			zap.String("instance_id", input.InstanceID),
			zap.Duration("since_connection", time.Since(connectedAt)))
	}

	for time.Since(readyStart) < 30*time.Second {
		preKeyCount, _ := s.sessionMgr.GetPreKeyCount(input.InstanceID)

		if s.sessionMgr.IsSessionReady(input.InstanceID) && preKeyCount >= minPreKeys {
			isReady = true
			break
		}

		if time.Since(readyStart) > 2*time.Second && !poked {
			s.log.Info("Sessão ainda não pronta, enviando presence de ativação...", zap.String("instance_id", input.InstanceID), zap.Int("prekeys", preKeyCount))
			_ = client.SendPresence(ctx, types.PresenceAvailable)
			poked = true
		}

		time.Sleep(1 * time.Second)
	}

	if !isReady {
		return model.Message{}, fmt.Errorf("%w: criptografia não pronta (pode levar alguns instantes após conectar)", ErrSessionUnavailable)
	}

	if isColdStart {
		timeSinceConnect := time.Since(connectedAt)
		safeThreshold := 60 * time.Second

		if timeSinceConnect < safeThreshold {
			remainingWait := safeThreshold - timeSinceConnect
			jitter := time.Duration(rand.Intn(5000)) * time.Millisecond
			finalWait := remainingWait + jitter

			s.log.Info("Estabilizando sessão...",
				zap.String("instance_id", input.InstanceID),
				zap.Duration("connected_for", timeSinceConnect),
				zap.Duration("wait_time", finalWait))

			time.Sleep(finalWait)
		}
	}

	time.Sleep(1500 * time.Millisecond)

	toJID, err := s.ResolveJID(ctx, client, input.To)
	if err != nil {
		if errors.Is(err, ErrInvalidJID) {
			return model.Message{}, err
		}
		return model.Message{}, fmt.Errorf("falha ao resolver destinatário %s: %w", input.To, err)
	}

	if toJID.Server == types.DefaultUserServer || toJID.Server == types.HiddenUserServer {
		hasSession, err := s.sessionMgr.HasSession(input.InstanceID, toJID)

		_ = client.SendPresence(ctx, types.PresenceAvailable)

		_ = client.SendChatPresence(ctx, toJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)

		s.log.Debug("buscando dispositivos do destinatário antes do envio",
			zap.String("instance_id", input.InstanceID),
			zap.String("to", toJID.String()))

		devices, devErr := client.GetUserDevices(ctx, []types.JID{toJID})
		if devErr != nil {
			s.log.Warn("erro ao buscar dispositivos do destinatário",
				zap.String("to", toJID.String()),
				zap.Error(devErr))
		} else {
			s.log.Debug("dispositivos do destinatário atualizados",
				zap.String("to", toJID.String()),
				zap.Int("device_count", len(devices)))

			time.Sleep(800 * time.Millisecond)
		}

		if err == nil && !hasSession {
			s.log.Info("Nova sessão detectada...",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))

			wait := 1500 + rand.Intn(1500)
			time.Sleep(time.Duration(wait) * time.Millisecond)
		} else {
			wait := 500 + rand.Intn(700)
			time.Sleep(time.Duration(wait) * time.Millisecond)
		}
	}

	var waMessage *waE2E.Message
	var messageType string
	var payload string

	buildContextInfo := func(quotedID string, participant string, mentionedJids []string) *waE2E.ContextInfo {
		ctxInfo := &waE2E.ContextInfo{}
		if quotedID != "" {
			ctxInfo.StanzaID = proto.String(quotedID)
		}
		if participant != "" {
			if !strings.Contains(participant, "@") {
				participant = participant + "@s.whatsapp.net"
			}
			ctxInfo.Participant = proto.String(participant)
		}
		if len(mentionedJids) > 0 {
			normalized := make([]string, len(mentionedJids))
			for i, jid := range mentionedJids {
				if !strings.Contains(jid, "@") {
					normalized[i] = jid + "@s.whatsapp.net"
				} else {
					normalized[i] = jid
				}
			}
			ctxInfo.MentionedJID = normalized
		}
		return ctxInfo
	}

	switch input.Type {
	case "text":
		if input.Text == "" {
			return model.Message{}, ErrInvalidPayload
		}
		if input.Quoted != "" || len(input.MentionedJids) > 0 {
			waMessage = &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text:        proto.String(input.Text),
					ContextInfo: buildContextInfo(input.Quoted, input.Participant, input.MentionedJids),
				},
			}
		} else {
			waMessage = &waE2E.Message{
				Conversation: proto.String(input.Text),
			}
		}
		messageType = "text"
		payload = input.Text

	case "image", "video":
		if len(input.MediaData) == 0 {
			return model.Message{}, ErrInvalidPayload
		}

		var mediaType whatsmeow.MediaType
		if input.Type == "image" {
			mediaType = whatsmeow.MediaImage
		} else {
			mediaType = whatsmeow.MediaVideo
		}

		uploadResp, err := client.Upload(ctx, input.MediaData, mediaType)
		if err != nil {
			return model.Message{}, fmt.Errorf("erro ao fazer upload da mídia: %w", err)
		}

		if input.Type == "image" {
			imageMsg := &waE2E.ImageMessage{
				URL:           &uploadResp.URL,
				DirectPath:    &uploadResp.DirectPath,
				MediaKey:      uploadResp.MediaKey,
				FileEncSHA256: uploadResp.FileEncSHA256,
				FileSHA256:    uploadResp.FileSHA256,
				FileLength:    &uploadResp.FileLength,
				Mimetype:      proto.String(input.MediaType),
			}
			if input.Caption != "" {
				imageMsg.Caption = proto.String(input.Caption)
			}
			if input.Quoted != "" || len(input.MentionedJids) > 0 {
				imageMsg.ContextInfo = buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
			}
			waMessage = &waE2E.Message{
				ImageMessage: imageMsg,
			}
		} else {
			videoMsg := &waE2E.VideoMessage{
				URL:           &uploadResp.URL,
				DirectPath:    &uploadResp.DirectPath,
				MediaKey:      uploadResp.MediaKey,
				FileEncSHA256: uploadResp.FileEncSHA256,
				FileSHA256:    uploadResp.FileSHA256,
				FileLength:    &uploadResp.FileLength,
				Mimetype:      proto.String(input.MediaType),
			}
			if input.Caption != "" {
				videoMsg.Caption = proto.String(input.Caption)
			}
			if input.Quoted != "" || len(input.MentionedJids) > 0 {
				videoMsg.ContextInfo = buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
			}
			waMessage = &waE2E.Message{
				VideoMessage: videoMsg,
			}
		}
		messageType = input.Type
		payload = fmt.Sprintf("media:%s", input.MediaType)

	case "audio":
		if len(input.MediaData) == 0 {
			return model.Message{}, ErrInvalidPayload
		}

		uploadResp, err := client.Upload(ctx, input.MediaData, whatsmeow.MediaAudio)
		if err != nil {
			return model.Message{}, fmt.Errorf("erro ao fazer upload do áudio: %w", err)
		}
		isPTT := input.PTT

		var waveform []byte
		var sidecar []byte

		if isPTT {
			waveform = generatePTTWaveform(input.Seconds)
			sidecar = make([]byte, 16)
		}

		finalMimeType := input.MediaType
		if isPTT && strings.Contains(input.MediaType, "audio/ogg") {
			finalMimeType = "audio/ogg; codecs=opus"
		}

		audioMsg := &waE2E.AudioMessage{
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			Mimetype:      proto.String(finalMimeType),
			ContextInfo: &waE2E.ContextInfo{
				Expiration: proto.Uint32(0),
			},
			PTT:               proto.Bool(isPTT),
			Seconds:           proto.Uint32(uint32(input.Seconds)),
			Waveform:          waveform,
			StreamingSidecar:  sidecar,
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		}
		if input.Quoted != "" || len(input.MentionedJids) > 0 {
			ctxInfo := buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
			audioMsg.ContextInfo.StanzaID = ctxInfo.StanzaID
			audioMsg.ContextInfo.Participant = ctxInfo.Participant
			audioMsg.ContextInfo.MentionedJID = ctxInfo.MentionedJID
		}
		waMessage = &waE2E.Message{
			AudioMessage: audioMsg,
		}
		messageType = "audio"
		payload = fmt.Sprintf("audio:%s", input.MediaType)

	case "document":
		if len(input.MediaData) == 0 {
			return model.Message{}, ErrInvalidPayload
		}

		uploadResp, err := client.Upload(ctx, input.MediaData, whatsmeow.MediaDocument)
		if err != nil {
			return model.Message{}, fmt.Errorf("erro ao fazer upload do documento: %w", err)
		}

		fileName := input.FileName
		if fileName == "" {
			exts, _ := mime.ExtensionsByType(input.MediaType)
			if len(exts) > 0 {
				fileName = "document" + exts[0]
			} else {
				fileName = "document"
			}
		}

		docMsg := &waE2E.DocumentMessage{
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			Mimetype:      proto.String(input.MediaType),
			FileName:      proto.String(fileName),
		}
		if input.Caption != "" {
			docMsg.Caption = proto.String(input.Caption)
		}
		if input.Quoted != "" || len(input.MentionedJids) > 0 {
			docMsg.ContextInfo = buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
		}
		waMessage = &waE2E.Message{
			DocumentMessage: docMsg,
		}
		messageType = "document"
		payload = fmt.Sprintf("document:%s:%s", fileName, input.MediaType)

	default:
		return model.Message{}, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, input.Type)
	}

	var msg model.Message
	if input.MessageID != "" {
		msg.ID = input.MessageID
		msg.InstanceID = input.InstanceID
		msg.To = input.To
		msg.Type = messageType
		msg.Payload = payload
		msg.Status = "sending"

		if err := s.repo.Update(ctx, msg); err != nil {
			s.log.Warn("erro ao atualizar status da mensagem existente, tentando criar nova", zap.Error(err))
			msg, err = s.repo.Create(ctx, msg)
			if err != nil {
				return model.Message{}, fmt.Errorf("erro ao salvar mensagem: %w", err)
			}
		}
	} else {
		message := model.Message{
			ID:         uuid.NewString(),
			InstanceID: input.InstanceID,
			To:         input.To,
			Type:       messageType,
			Payload:    payload,
			Status:     "sending",
		}
		msg, err = s.repo.Create(ctx, message)
		if err != nil {
			return model.Message{}, fmt.Errorf("erro ao salvar mensagem: %w", err)
		}
	}

	var resp whatsmeow.SendResponse
	maxRetries := 3

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			s.log.Info("tentando reenvio de mensagem devido a erro anterior",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", backoff),
				zap.String("to", toJID.String()))
			time.Sleep(backoff)

			_ = client.SendPresence(ctx, types.PresenceAvailable)
			_ = client.SendChatPresence(ctx, toJID, types.ChatPresenceComposing, types.ChatPresenceMediaText)

			_, _ = client.GetUserDevices(ctx, []types.JID{toJID})
		}

		resp, err = client.SendMessage(ctx, toJID, waMessage)
		if err == nil {
			// Validar se WhatsApp realmente aceitou
			if resp.ID == "" {
				s.log.Warn("WhatsApp retornou ID vazio - envio pode ter falhado",
					zap.Int("attempt", attempt),
					zap.String("to", toJID.String()))
				err = fmt.Errorf("WhatsApp não confirmou envio (ID vazio)")
				continue // Tenta retry
			}

			s.log.Info("mensagem enviada com sucesso",
				zap.Int("attempt", attempt),
				zap.String("to", toJID.String()),
				zap.String("server_id", resp.ID),
				zap.Int64("timestamp", resp.Timestamp.Unix()))
			break
		}

		s.log.Warn("falha no envio da mensagem",
			zap.Int("attempt", attempt),
			zap.Error(err),
			zap.String("to", toJID.String()))

		if strings.Contains(err.Error(), "untrusted identity") {
			s.log.Warn("erro de identidade não confiável detectado, limpando identidade e tentando novamente",
				zap.String("to", toJID.String()))
			client.Store.Identities.DeleteIdentity(ctx, toJID.SignalAddress().String())
			continue
		}

		if strings.Contains(err.Error(), "no signal session") {
			s.log.Warn("sessão de criptografia não estabelecida (cold start), tentando warmup e reenvio",
				zap.String("to", toJID.String()))
			_, _ = client.GetUserDevices(ctx, []types.JID{toJID})
			continue
		}

		if strings.Contains(err.Error(), "not logged in") {
			s.log.Warn("instância não logada, abortando retentativas",
				zap.String("instance_id", input.InstanceID))
			ctxUpdate := context.Background()
			if instToUpdate, fetchErr := s.instanceRepo.GetByID(ctxUpdate, input.InstanceID); fetchErr == nil {
				instToUpdate.Status = model.InstanceStatusDisconnected
				_, _ = s.instanceRepo.Update(ctxUpdate, instToUpdate)
			}
			break
		}

		if strings.Contains(err.Error(), "device JID") {
			s.log.Warn("instância desconectada: dispositivo sem sessão ativa",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))
			ctxUpdate := context.Background()
			if instToUpdate, fetchErr := s.instanceRepo.GetByID(ctxUpdate, input.InstanceID); fetchErr == nil {
				instToUpdate.Status = model.InstanceStatusDisconnected
				_, _ = s.instanceRepo.Update(ctxUpdate, instToUpdate)
			}
			break
		}
	}

	_ = client.SendChatPresence(ctx, toJID, types.ChatPresencePaused, types.ChatPresenceMediaText)

	if err != nil {
		jidCache.Delete(input.To)
		s.log.Info("Removido do cache de JID devido a erro de envio", zap.String("phone", input.To))

		isDisconnectedErr := strings.Contains(err.Error(), "not logged in") ||
			strings.Contains(err.Error(), "device JID")

		if !isDisconnectedErr && !strings.Contains(err.Error(), "connection") {
			if s.contactRepo != nil {
				_ = s.contactRepo.Upsert(ctx, model.Contact{
					Phone: input.To,
					JID:   "",
				})
			}
		}

		msg.Status = "failed"
		_ = s.repo.Update(ctx, msg)

		if strings.Contains(err.Error(), "device JID") || strings.Contains(err.Error(), "not logged in") {
			return msg, fmt.Errorf("Desconectado")
		}

		return msg, fmt.Errorf("erro ao enviar mensagem após %d tentativas: %w", maxRetries, err)
	}

	msg.Status = "sent"
	msg.WhatsAppID = resp.ID
	if err := s.repo.Update(ctx, msg); err != nil {
		s.log.Warn("erro ao atualizar status enviado no banco", zap.Error(err))
	}

	_ = client.SendChatPresence(ctx, toJID, types.ChatPresencePaused, types.ChatPresenceMediaText)

	return msg, nil
}

func (s *Service) List(ctx context.Context, instanceID string) ([]model.Message, error) {
	return s.repo.ListByInstance(ctx, instanceID)
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
			return entry.jid, nil
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
					s.log.Debug("JID resolvido via banco de dados", zap.String("phone", phone), zap.String("jid", jid.String()))
					positiveTTL := time.Duration(s.cfg.JIDCachePositiveTTLHours) * time.Hour
					jidCache.Store(phone, jidCacheEntry{jid: jid, expiresAt: time.Now().Add(positiveTTL)})
					return jid, nil
				}
			}
		}
	}

	if !strings.HasPrefix(phone, "55") {
		return types.ParseJID(phone + "@s.whatsapp.net")
	}

	delay := 1000 + rand.Intn(2000)
	s.log.Debug("Aplicando delay de segurança antes de IsOnWhatsApp", zap.Int("ms", delay))
	time.Sleep(time.Duration(delay) * time.Millisecond)

	candidates := []string{phone}

	if len(phone) == 13 {
		optionWithout9 := phone[:4] + phone[5:]
		candidates = append(candidates, optionWithout9)
	} else if len(phone) == 12 {
		optionWith9 := phone[:4] + "9" + phone[4:]
		candidates = append(candidates, optionWith9)
	}

	s.log.Debug("Candidatos gerados para validação", zap.String("original", phone), zap.Strings("candidates", candidates))

	resp, err := client.IsOnWhatsApp(ctx, candidates)
	if err != nil {
		s.log.Error("falha ao consultar IsOnWhatsApp - abortando envio", zap.String("phone", phone), zap.Error(err))
		return types.EmptyJID, fmt.Errorf("falha ao validar número no WhatsApp: %w", err)
	}

	resolvedJID := types.EmptyJID
	for _, item := range resp {
		if item.JID.User != "" {
			resolvedJID = item.JID
			break
		}
	}

	if resolvedJID.IsEmpty() {
		negativeTTL := time.Duration(s.cfg.JIDCacheNegativeTTLDays) * 24 * time.Hour
		s.log.Warn("WhatsApp não encontrado - registrando em cache negativo", zap.String("original_phone", phone), zap.Any("candidates", candidates), zap.Duration("ttl", negativeTTL))
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

func generatePTTWaveform(seconds int) []byte {
	const size = 64
	waveform := make([]byte, size)

	numSegments := 2 + rand.Intn(3)
	if seconds > 10 {
		numSegments = 3 + rand.Intn(3)
	}

	type segment struct {
		center    float64
		width     float64
		intensity float64
	}

	segments := make([]segment, numSegments)
	for i := range segments {
		basePos := (float64(i) + 0.5) / float64(numSegments)
		segments[i] = segment{
			center:    basePos*float64(size) + float64(rand.Intn(6)-3),
			width:     float64(4 + rand.Intn(10)),
			intensity: 30.0 + float64(rand.Intn(70)), // 30-100
		}
	}

	mainPeak := rand.Intn(numSegments)
	segments[mainPeak].intensity = 70.0 + float64(rand.Intn(50)) // 70-120
	segments[mainPeak].width = float64(6 + rand.Intn(12))

	for i := 0; i < size; i++ {
		val := float64(rand.Intn(8))

		for _, seg := range segments {
			dist := math.Abs(float64(i) - seg.center)
			if dist < seg.width {
				normalized := dist / seg.width
				contribution := seg.intensity * math.Exp(-3.0*normalized*normalized)
				contribution *= 0.7 + 0.3*math.Sin(float64(i)*0.8+float64(rand.Intn(10)))
				val += contribution
			}
		}

		if val > 10 {
			val += float64(rand.Intn(int(val/5) + 1))
		}

		if val > 255 {
			val = 255
		}
		waveform[i] = byte(val)
	}

	for i := 0; i < 3; i++ {
		factor := float64(i+1) / 4.0
		waveform[i] = byte(float64(waveform[i]) * factor)
		waveform[size-1-i] = byte(float64(waveform[size-1-i]) * factor)
	}

	return waveform
}
