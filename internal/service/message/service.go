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
)

type Service struct {
	repo         storage.MessageRepository
	sessionMgr   SessionManager
	instanceRepo storage.InstanceRepository
	contactRepo  storage.ContactRepository
	queue        queue.Queue
	log          *zap.Logger
}

type SessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
	IsSessionReady(instanceID string) bool
	HasSession(instanceID string, jid types.JID) (bool, error)
	GetPreKeyCount(instanceID string) (int, error)
	GetConnectedAt(instanceID string) time.Time
}

func NewService(repo storage.MessageRepository, q queue.Queue, log *zap.Logger) *Service {
	return &Service{
		repo:  repo,
		queue: q,
		log:   log,
	}
}

func NewServiceWithSession(repo storage.MessageRepository, sessionMgr SessionManager, instanceRepo storage.InstanceRepository, contactRepo storage.ContactRepository, q queue.Queue, log *zap.Logger) *Service {
	return &Service{
		repo:         repo,
		sessionMgr:   sessionMgr,
		instanceRepo: instanceRepo,
		contactRepo:  contactRepo,
		queue:        q,
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

	// Enfileirar para processamento assíncrono
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
			instToUpdate.Status = model.InstanceStatusError
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
		return model.Message{}, fmt.Errorf("sessão indisponível para criptografia (pode levar alguns instantes após conectar), tente novamente")
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

	toJID, err := s.resolveJID(ctx, client, input.To)
	if err != nil {
		return model.Message{}, fmt.Errorf("%w: %s", ErrInvalidJID, input.To)
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

	switch input.Type {
	case "text":
		if input.Text == "" {
			return model.Message{}, ErrInvalidPayload
		}
		waMessage = &waE2E.Message{
			Conversation: proto.String(input.Text),
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
			waveform = make([]byte, 64)
			for i := 0; i < 64; i++ {
				distFromCenter := math.Abs(float64(i) - 31.5)
				normalizedDist := distFromCenter / 32.0

				envelope := 1.0 - (normalizedDist * normalizedDist)
				if envelope < 0 {
					envelope = 0
				}

				baseHeight := envelope * 50.0
				noise := float64(rand.Intn(40))

				finalVal := baseHeight + noise
				if finalVal > 255 {
					finalVal = 255
				}

				waveform[i] = byte(finalVal)
			}

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
		// Fluxo síncrono original
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

			// Recalcular dispositivos no retry caso tenha sido um erro de criptografia
			_, _ = client.GetUserDevices(ctx, []types.JID{toJID})
		}

		resp, err = client.SendMessage(ctx, toJID, waMessage)
		if err == nil {
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

		// Tratar Cold Start de Criptografia: no signal session
		if strings.Contains(err.Error(), "no signal session") {
			s.log.Warn("sessão de criptografia não estabelecida (cold start), tentando warmup e reenvio",
				zap.String("to", toJID.String()))
			_, _ = client.GetUserDevices(ctx, []types.JID{toJID})
			continue
		}

		if strings.Contains(err.Error(), "not logged in") {
			break
		}
	}

	// Limpar o status "digitando" após o envio (sucesso ou falha final)
	_ = client.SendChatPresence(ctx, toJID, types.ChatPresencePaused, types.ChatPresenceMediaText)

	if err != nil {
		jidCache.Delete(input.To)
		s.log.Info("Removido do cache de JID devido a erro de envio", zap.String("phone", input.To))

		msg.Status = "failed"
		_ = s.repo.Update(ctx, msg)
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

func (s *Service) resolveJID(ctx context.Context, client *whatsmeow.Client, phone string) (types.JID, error) {
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
			s.log.Debug("JID resolvido via cache em memória", zap.String("phone", phone), zap.String("jid", entry.jid.String()))
			return entry.jid, nil
		}
		jidCache.Delete(phone)
	}

	if s.contactRepo != nil {
		if contact, err := s.contactRepo.GetByPhone(ctx, phone); err == nil {
			jid, jerr := types.ParseJID(contact.JID)
			if jerr == nil {
				s.log.Debug("JID resolvido via banco de dados", zap.String("phone", phone), zap.String("jid", jid.String()))
				jidCache.Store(phone, jidCacheEntry{jid: jid, expiresAt: time.Now().Add(24 * time.Hour)})
				return jid, nil
			}
		}
	}

	if !strings.HasPrefix(phone, "55") {
		return types.ParseJID(phone + "@s.whatsapp.net")
	}

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
		s.log.Warn("falha ao consultar IsOnWhatsApp, enviando original", zap.String("phone", phone), zap.Error(err))
		return types.ParseJID(phone + "@s.whatsapp.net")
	}

	resolvedJID := types.EmptyJID
	for _, item := range resp {
		if item.JID.User != "" {
			resolvedJID = item.JID
			break
		}
	}

	if resolvedJID.IsEmpty() {
		s.log.Warn("WhatsApp não encontrado", zap.String("original_phone", phone), zap.Any("candidates", candidates))
		return types.EmptyJID, fmt.Errorf("%w: número não registrado no WhatsApp", ErrInvalidJID)
	}

	jidCache.Store(phone, jidCacheEntry{jid: resolvedJID, expiresAt: time.Now().Add(24 * time.Hour)})
	if s.contactRepo != nil {
		_ = s.contactRepo.Upsert(ctx, model.Contact{
			Phone: phone,
			JID:   resolvedJID.String(),
		})
	}

	return resolvedJID, nil
}
