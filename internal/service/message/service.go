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

	"github.com/open-apime/apime/internal/config"
	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

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
	eventLogRepo storage.EventLogRepository
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

func NewServiceWithSession(repo storage.MessageRepository, sessionMgr SessionManager, instanceRepo storage.InstanceRepository, contactRepo storage.ContactRepository, eventLogRepo storage.EventLogRepository, q queue.Queue, cfg config.WhatsAppConfig, log *zap.Logger) *Service {
	return &Service{
		repo:         repo,
		sessionMgr:   sessionMgr,
		instanceRepo: instanceRepo,
		contactRepo:  contactRepo,
		eventLogRepo: eventLogRepo,
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
	MarkReadMessageID string
	MarkReadSender    string
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

		// 1. Go online
		_ = client.SendPresence(ctx, types.PresenceAvailable)

		// 2. Mark as read — auto-detect from inbound tracker or use explicit param
		markMsgID := input.MarkReadMessageID
		markSender := input.MarkReadSender
		if markMsgID == "" {
			trackerKey := input.InstanceID + ":" + toJID.String()
			if entry, ok := popLastInbound(input.InstanceID, toJID.String()); ok {
				markMsgID = entry.messageID
				markSender = entry.senderJID
				s.log.Info("[markread] inbound tracker hit",
					zap.String("key", trackerKey),
					zap.String("message_id", markMsgID),
					zap.String("sender", markSender))
			} else {
				s.log.Info("[markread] inbound tracker miss — nenhuma mensagem pendente",
					zap.String("key", trackerKey))
			}
		}
		if markMsgID != "" {
			senderJID := toJID
			if markSender != "" {
				if parsed, parseErr := types.ParseJID(markSender); parseErr == nil {
					senderJID = parsed
				}
			}
			if markErr := client.MarkRead(ctx, []types.MessageID{types.MessageID(markMsgID)}, time.Now(), toJID, senderJID, types.ReceiptTypeRead); markErr != nil {
				s.log.Error("[markread] erro ao marcar como lida",
					zap.String("message_id", markMsgID),
					zap.String("chat", toJID.String()),
					zap.String("sender", senderJID.String()),
					zap.Error(markErr))
			} else {
				s.log.Info("[markread] mensagem marcada como lida antes do envio",
					zap.String("message_id", markMsgID),
					zap.String("chat", toJID.String()),
					zap.String("sender", senderJID.String()))
			}
		}

		// 3. Send typing/recording presence (audio shows "recording audio...")
		media := presenceMediaType(input.Type)
		_ = client.SendChatPresence(ctx, toJID, types.ChatPresenceComposing, media)

		// 4. Fetch devices for crypto warmup
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
		}

		// 5. Content-based dynamic delay
		presenceDelay := calculatePresenceDelay(input)
		if err == nil && !hasSession {
			s.log.Info("Nova sessão detectada...",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))
			presenceDelay += time.Duration(1500+rand.Intn(1000)) * time.Millisecond
		}
		s.log.Debug("presence delay calculado",
			zap.String("type", input.Type),
			zap.Duration("delay", presenceDelay))
		simulatePresenceDelay(ctx, client, toJID, media, presenceDelay)
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
			_ = client.SendChatPresence(ctx, toJID, types.ChatPresenceComposing, presenceMediaType(input.Type))

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

		if strings.Contains(err.Error(), "error 463") {
			s.log.Warn("WhatsApp restrito (error 463), abortando retentativas",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))
			// Registrar evento no log da conexão
			if s.eventLogRepo != nil {
				go func() {
					evtCtx, evtCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer evtCancel()
					payload := `{"message":"WhatsApp restrito (error 463)","reason":"server returned error 463","detail":"Conta possivelmente restrita ou banida pelo WhatsApp"}`
					_, _ = s.eventLogRepo.Create(evtCtx, model.EventLog{
						InstanceID: input.InstanceID,
						Type:       "temporary_ban",
						Payload:    payload,
					})
				}()
			}
			break
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

	_ = client.SendChatPresence(ctx, toJID, types.ChatPresencePaused, presenceMediaType(input.Type))

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

	_ = client.SendChatPresence(ctx, toJID, types.ChatPresencePaused, presenceMediaType(input.Type))

	return msg, nil
}

func (s *Service) List(ctx context.Context, instanceID string) ([]model.Message, error) {
	return s.repo.ListByInstance(ctx, instanceID)
}
