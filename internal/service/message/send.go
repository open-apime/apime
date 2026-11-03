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

	"github.com/open-apime/apime/internal/pkg/instancelock"
	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage/model"
)

type ContactEntry struct {
	DisplayName string `json:"displayName"`
	Vcard       string `json:"vcard"`
}

type SendInput struct {
	InstanceID  string
	To          string
	Type        string
	Text        string
	MediaData   []byte
	MediaType   string
	Caption     string
	FileName    string
	Seconds     int
	PTT         bool
	MessageID   string
	Quoted      string
	Participant string
	QuotedText  string
	// QuotedFromMe: the quoted message was sent by US. The caller cannot build the right
	// Participant, since on 1:1 whatsmeow swaps the target for LID and signs with our LID, so the
	// instance phone number does not work. With this flag ownJIDForChat resolves it here.
	QuotedFromMe      bool
	MentionedJids     []string
	MarkReadMessageID string
	MarkReadSender    string
	DisplayName       string
	Vcard             string
	Contacts          []ContactEntry
	Latitude          float64
	Longitude         float64
	// Status post styling. A text status is an ExtendedTextMessage carrying a background, and the
	// official app always sets one, so these have defaults rather than being optional in practice.
	BackgroundArgb uint32
	TextArgb       uint32
	Font           string
	LocationName   string
	Address        string
	// WhatsAppID is the caller-chosen WhatsApp message id. Lets the caller match the echo of its
	// own send by id; empty means the gateway generates one.
	WhatsAppID string
}

// ownJIDForChat returns the JID our own messages appear under in that chat, which is what
// ContextInfo.Participant needs when quoting our own message.
//
// Mirrors whatsmeow's ownID choice in SendMessage: LID-addressed groups and any 1:1 sign with the
// LID; only legacy PN-mode groups sign with the phone number. Getting it wrong is the same as
// sending no participant: the client fails to match the quoted message.
//
// GetGroupInfo issues an IQ but populates the groupCache SendMessage reads right after, so it is
// not a wasted round-trip. Only called when quoting our own message in a group.
func (s *Service) ownJIDForChat(ctx context.Context, client *whatsmeow.Client, to types.JID) types.JID {
	pn := client.Store.GetJID()
	lid := client.Store.GetLID()
	if lid.IsEmpty() {
		return pn
	}
	if to.Server == types.GroupServer {
		info, err := client.GetGroupInfo(ctx, to)
		if err != nil {
			// Without knowing the group mode, PN is the safe fallback (the legacy behavior).
			s.log.Warn("Falha ao obter info do grupo para resolver o participant do quote",
				zap.String("group", to.String()), zap.Error(err))
			return pn
		}
		if info.AddressingMode == types.AddressingModeLID {
			return lid
		}
		return pn
	}
	return lid
}

// hasChatPresence reports whether the destination accepts typing indicators. A group, a broadcast
// list and a status post have none, so signaling toward them is traffic the real client never
// produces, which is what the anti-ban machinery exists to avoid.
func hasChatPresence(toJID types.JID) bool {
	return toJID.Server == types.DefaultUserServer || toJID.Server == types.HiddenUserServer
}

// closeComposing sends `paused` to close the indicator opened before the send. The cache must
// forget either way, otherwise the next send would trust a "typing" that is no longer open and
// would go out with no signal at all.
func closeComposing(ctx context.Context, client *whatsmeow.Client, input SendInput, toJID types.JID) {
	forgetComposing(input.InstanceID, toJID.String())
	if !hasChatPresence(toJID) {
		return
	}
	_ = client.SendChatPresence(ctx, toJID, types.ChatPresencePaused, presenceMediaType(input.Type))
}

func (s *Service) Send(ctx context.Context, input SendInput) (model.Message, error) {
	if s.sessionMgr == nil {
		return model.Message{}, errors.New("session manager não configurado")
	}

	if input.InstanceID == "" || input.To == "" {
		return model.Message{}, ErrInvalidPayload
	}

	if input.WhatsAppID != "" && !ValidWhatsAppID(input.WhatsAppID) {
		return model.Message{}, ErrInvalidWhatsAppID
	}

	unlock := instancelock.Acquire(input.InstanceID)
	defer unlock()

	// A retried request with an id that already went out replays the stored result: sending it
	// again would make WhatsApp deliver a second copy under the same id.
	if prev, ok := s.alreadySent(ctx, input); ok {
		s.log.Info("envio com whatsappId já enviado, devolvendo resultado anterior",
			zap.String("instance_id", input.InstanceID),
			zap.String("whatsapp_id", input.WhatsAppID))
		return prev, nil
	}

	instance, err := s.instanceRepo.GetByID(ctx, input.InstanceID)
	if err != nil {
		return model.Message{}, fmt.Errorf("instância não encontrada: %w", err)
	}

	client, err := s.sessionMgr.GetClient(input.InstanceID)

	if instance.Status != model.InstanceStatusActive {
		// The stored status only goes back to active on a Connected event, so it can stay stale while
		// the session is live, refusing every send. Trust the live session (memory reads, no network);
		// a temporary ban disconnects the socket, so it still refuses here.
		if err != nil || !client.IsLoggedIn() || !client.IsConnected() {
			return model.Message{}, ErrInstanceNotConnected
		}
		s.log.Warn("status da instância divergia da sessão ativa, corrigido para active",
			zap.String("instance_id", input.InstanceID),
			zap.String("stored_status", string(instance.Status)))
		_ = s.instanceRepo.UpdateStatus(context.Background(), input.InstanceID, model.InstanceStatusActive)
	}

	if err != nil {
		_ = s.instanceRepo.UpdateStatus(context.Background(), input.InstanceID, model.InstanceStatusError)
		return model.Message{}, fmt.Errorf("cliente não encontrado: %w", err)
	}

	if !client.IsLoggedIn() {
		_ = s.instanceRepo.UpdateStatus(context.Background(), input.InstanceID, model.InstanceStatusDisconnected)
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

	// The wait above can take a minute, and a reconnect in the meantime swaps the session's client.
	// Sending through the old one failed with "failed to refresh media connections" (02/11/2026).
	if fresh, ferr := s.sessionMgr.GetClient(input.InstanceID); ferr == nil && fresh != nil {
		client = fresh
	}
	// A socket that is down only for a moment is the retry loop's job, which waits for it.
	if !client.IsLoggedIn() {
		return model.Message{}, ErrInstanceNotConnected
	}

	toJID, err := s.ResolveJID(ctx, client, input.To)
	if err != nil {
		// Propagated without the recipient prefix: the handler picks the status from them, and
		// keeping the phone number out of the text avoids one Sentry issue per number.
		if errors.Is(err, ErrInvalidJID) || errors.Is(err, ErrRecipientLookupUnavailable) {
			return model.Message{}, err
		}
		return model.Message{}, fmt.Errorf("falha ao resolver destinatário %s: %w", input.To, err)
	}

	// Ensure the recipient is in the account's contact list before sending (best-effort, non-blocking).
	s.autoSaveContact(ctx, input.InstanceID, client, toJID, input.DisplayName)

	// Reach-out (463) guard: if this contact already refused via this connection and hasn't
	// replied, skip the send instead of triggering another 463 that feeds the 403-logout. Only
	// individual contacts (groups/broadcast/newsletter have no per-contact tctoken).
	if toJID.Server == types.DefaultUserServer || toJID.Server == types.HiddenUserServer {
		contactKey := normalizeChatKey(toJID.String())
		if reachoutBlocked(input.InstanceID, contactKey) {
			s.log.Warn("envio bloqueado por restrição de reach-out (463) — contato frio não respondeu",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))
			// Terminal failure: mark an already-persisted (queued) message as failed so
			// runStuckRecovery (which re-queues status='queued') doesn't loop. Direct sends have
			// no MessageID and nothing persisted yet, so just return the error.
			if input.MessageID != "" {
				_ = s.repo.Update(ctx, model.Message{
					ID:         input.MessageID,
					InstanceID: input.InstanceID,
					To:         input.To,
					Type:       input.Type,
					Payload:    input.Text,
					Status:     "failed",
				})
			}
			return model.Message{}, ErrContactReachoutLocked
		}
	}

	// Typing simulation wait. Runs in parallel with message preparation and is awaited right before
	// sending; with no presence (group, broadcast) there is nothing to wait for.
	waitPresence := func() {}
	// Any early return between here and the send (invalid payload, upload failure, unsupported
	// type) must stop the simulation: otherwise it keeps signaling "typing" for a message that will
	// never go out. The request ctx alone is not enough — it is only canceled once the handler
	// answers, and by then the contact already saw the indicator.
	stopPresence := func() {}
	// releasePresence always runs: context.WithCancel requires its cancel to be called on every
	// path, including the successful one, or the child context stays registered on the parent.
	releasePresence := func() {}
	defer func() {
		stopPresence()
		releasePresence()
	}()

	if toJID.Server == types.DefaultUserServer || toJID.Server == types.HiddenUserServer {
		hasSession, err := s.sessionMgr.HasSession(input.InstanceID, toJID)

		// 1. Go online — only when the previous mark expired. `available` is client state and
		// holds until replaced, so resending it on every message was a wasted round-trip
		// restating what was already true, and reinforced the always-online pattern.
		if needsAvailable(input.InstanceID) {
			_ = client.SendPresence(ctx, types.PresenceAvailable)
		}

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
			now := time.Now()
			msgIDs := []types.MessageID{types.MessageID(markMsgID)}
			// Send read receipt to sender (blue ticks)
			if markErr := client.MarkRead(ctx, msgIDs, now, toJID, senderJID, types.ReceiptTypeRead); markErr != nil {
				s.log.Error("[markread] erro ao enviar read receipt",
					zap.String("message_id", markMsgID),
					zap.String("chat", toJID.String()),
					zap.Error(markErr))
			}
			// Send read-self receipt to sync read state across own devices (phone)
			if selfErr := client.MarkRead(ctx, msgIDs, now, toJID, senderJID, types.ReceiptTypeReadSelf); selfErr != nil {
				s.log.Error("[markread] erro ao enviar read-self receipt",
					zap.String("message_id", markMsgID),
					zap.String("chat", toJID.String()),
					zap.Error(selfErr))
			} else {
				s.log.Info("[markread] mensagem marcada como lida (read + read-self)",
					zap.String("message_id", markMsgID),
					zap.String("chat", toJID.String()),
					zap.String("sender", senderJID.String()))
			}
		}

		if markMsgID != "" {
			time.Sleep(time.Duration(300+rand.Intn(900)) * time.Millisecond)
		}

		// 3. Send typing/recording presence (audio shows "recording audio...")
		// Reopening an indicator that is already open (messages in sequence in the same chat)
		// changes nothing on the receiving side.
		media := presenceMediaType(input.Type)
		if needsComposing(input.InstanceID, toJID.String()) {
			_ = client.SendChatPresence(ctx, toJID, types.ChatPresenceComposing, media)
		}

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

		// Familiarity with the contact. hasSession alone is heuristic (it checks device 0 via
		// ToNonAD, not the contact's real device — e.g. :80 on LID contacts), so it yields false
		// "new session" positives on active conversations. The tracked inbound is the reliable
		// signal that the conversation is open.
		elapsed, hasInbound := timeSinceLastInbound(input.InstanceID, toJID.String())
		recentInbound := hasInbound && elapsed < 30*time.Minute
		presenceDelay = adjustForFamiliarity(presenceDelay, recentInbound)

		// Brake for the first seconds after connecting: coming back from a reconnect dumping
		// messages is an automation pattern, regardless of who the contact is.
		presenceDelay = time.Duration(float64(presenceDelay) * reconnectFactor(time.Since(connectedAt), 60*time.Second))

		// The time the contact already waited since their message COUNTS as typing. Without this
		// the delay was added on top of a wait that already happened.
		if recentInbound {
			presenceDelay = subtractElapsed(presenceDelay, elapsed)
		}

		s.log.Debug("presence delay calculado",
			zap.String("type", input.Type),
			zap.Duration("delay", presenceDelay),
			zap.Duration("already_waited", elapsed),
			zap.Bool("open_conversation", recentInbound),
			zap.Bool("has_session", err == nil && hasSession))

		// The delay runs in parallel with message preparation (payload assembly, media upload):
		// both occupy the same window. Before, one only started when the other finished, which on
		// media added seconds without changing anything the contact sees.
		presenceCtx, cancelPresence := context.WithCancel(ctx)
		releasePresence = cancelPresence
		presenceDone := make(chan struct{})
		go func() {
			defer close(presenceDone)
			simulatePresenceDelay(presenceCtx, client, toJID, media, presenceDelay)
		}()
		waitPresence = func() { <-presenceDone }
		// On an early return, cancel the simulation and close the indicator: the contact must not
		// keep seeing "typing" for a message that was never sent.
		stopPresence = func() {
			cancelPresence()
			<-presenceDone
			forgetComposing(input.InstanceID, toJID.String())
		}
	}

	var waMessage *waE2E.Message
	var messageType string
	var payload string

	buildContextInfo := func(quotedID string, participant string, mentionedJids []string) *waE2E.ContextInfo {
		ctxInfo := &waE2E.ContextInfo{}
		if quotedID != "" {
			ctxInfo.StanzaID = proto.String(quotedID)

			// ContextInfo has no FromMe field, so Participant is the ONLY authorship signal. When
			// quoting our own message it must be our JID: sending the contact's makes the client
			// look for the quote among their messages, miss it, and fall back (no thumbnail, no
			// jump to the original).
			if input.QuotedFromMe {
				ctxInfo.Participant = proto.String(s.ownJIDForChat(ctx, client, toJID).ToNonAD().String())
			} else if participant != "" {
				if !strings.Contains(participant, "@") {
					participant = participant + "@s.whatsapp.net"
				}
				if pj, perr := types.ParseJID(participant); perr == nil {
					ctxInfo.Participant = proto.String(pj.ToNonAD().String())
				} else {
					ctxInfo.Participant = proto.String(participant)
				}
			} else if toJID.Server == types.DefaultUserServer || toJID.Server == types.HiddenUserServer {
				ctxInfo.Participant = proto.String(toJID.ToNonAD().String())
			}

			if input.QuotedText != "" {
				ctxInfo.QuotedMessage = &waE2E.Message{
					Conversation: proto.String(input.QuotedText),
				}
			}
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
		if toJID == types.StatusBroadcastJID {
			// A text status is never a bare Conversation: the official client always sends an
			// ExtendedTextMessage with a background colour and a font, because the composer makes
			// you pick one. Sending a plain Conversation here is a shape the recipient's client
			// never sees from a real status, and older versions render it as "update WhatsApp to
			// view this message" instead of showing the text.
			waMessage = &waE2E.Message{
				ExtendedTextMessage: buildStatusText(input),
			}
		} else if input.Quoted != "" || len(input.MentionedJids) > 0 {
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

	// "gif" rides the video branch on purpose: on WhatsApp a GIF IS an MP4 video carrying the
	// GifPlayback flag, which is what makes the client loop it with no controls. Uploading a real
	// .gif here would arrive as a still image.
	case "image", "video", "gif":
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
				URL:              &uploadResp.URL,
				DirectPath:       &uploadResp.DirectPath,
				MediaKey:         uploadResp.MediaKey,
				FileEncSHA256:    uploadResp.FileEncSHA256,
				FileSHA256:       uploadResp.FileSHA256,
				FileLength:       &uploadResp.FileLength,
				Mimetype:         proto.String(input.MediaType),
				StreamingSidecar: uploadResp.StreamingSidecar,
			}
			if input.Caption != "" {
				videoMsg.Caption = proto.String(input.Caption)
			}
			if input.Type == "gif" {
				videoMsg.GifPlayback = proto.Bool(true)
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

	case "sticker":
		if len(input.MediaData) == 0 {
			return model.Message{}, ErrInvalidPayload
		}

		// Validated BEFORE the upload: a sticker outside 512x512 WebP under 500 KB is accepted by
		// the server and then fails to render on the recipient's phone, with nothing reporting it.
		// Refusing here turns a silent failure into an error the caller can act on, and saves the
		// round trip.
		sticker, err := inspectSticker(input.MediaData)
		if err != nil {
			return model.Message{}, fmt.Errorf("%w: %s", ErrInvalidPayload, err)
		}

		// MediaImage, not a type of its own: MediaStickerPack exists but is for sticker PACKS, and
		// a lone sticker travels on the image keys.
		uploadResp, err := client.Upload(ctx, input.MediaData, whatsmeow.MediaImage)
		if err != nil {
			return model.Message{}, fmt.Errorf("erro ao fazer upload da figurinha: %w", err)
		}

		stickerMsg := &waE2E.StickerMessage{
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			Mimetype:      proto.String(stickerMimeType),
			Width:         proto.Uint32(sticker.width),
			Height:        proto.Uint32(sticker.height),
			// Read from the WebP header, never guessed: without it an animated sticker arrives
			// frozen on the recipient's phone.
			IsAnimated:        proto.Bool(sticker.animated),
			MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
		}
		// No caption: the protocol has no such field on a sticker, so one would be silently dropped.
		if input.Quoted != "" || len(input.MentionedJids) > 0 {
			stickerMsg.ContextInfo = buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
		}
		waMessage = &waE2E.Message{
			StickerMessage: stickerMsg,
		}
		messageType = "sticker"
		payload = fmt.Sprintf("sticker:%s", stickerMimeType)

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

	case "contact":
		if input.Vcard == "" && len(input.Contacts) == 0 {
			return model.Message{}, ErrInvalidPayload
		}
		for _, c := range input.Contacts {
			if c.Vcard == "" {
				return model.Message{}, fmt.Errorf("%w: campo 'vcard' é obrigatório em cada contato", ErrInvalidPayload)
			}
		}
		var ctxInfo *waE2E.ContextInfo
		if input.Quoted != "" || len(input.MentionedJids) > 0 {
			ctxInfo = buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
		}
		if len(input.Contacts) > 1 {
			contacts := make([]*waE2E.ContactMessage, len(input.Contacts))
			for i, c := range input.Contacts {
				contacts[i] = &waE2E.ContactMessage{
					DisplayName: proto.String(c.DisplayName),
					Vcard:       proto.String(c.Vcard),
				}
			}
			arrMsg := &waE2E.ContactsArrayMessage{
				DisplayName: proto.String(input.DisplayName),
				Contacts:    contacts,
			}
			if ctxInfo != nil {
				arrMsg.ContextInfo = ctxInfo
			}
			waMessage = &waE2E.Message{
				ContactsArrayMessage: arrMsg,
			}
		} else {
			vcard := input.Vcard
			displayName := input.DisplayName
			if len(input.Contacts) == 1 {
				vcard = input.Contacts[0].Vcard
				displayName = input.Contacts[0].DisplayName
			}
			contactMsg := &waE2E.ContactMessage{
				DisplayName: proto.String(displayName),
				Vcard:       proto.String(vcard),
			}
			if ctxInfo != nil {
				contactMsg.ContextInfo = ctxInfo
			}
			waMessage = &waE2E.Message{
				ContactMessage: contactMsg,
			}
		}
		messageType = "contact"
		payload = fmt.Sprintf("contact:%s", input.DisplayName)

	case "location":
		locMsg := &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(input.Latitude),
			DegreesLongitude: proto.Float64(input.Longitude),
		}
		if input.LocationName != "" {
			locMsg.Name = proto.String(input.LocationName)
		}
		if input.Address != "" {
			locMsg.Address = proto.String(input.Address)
		}
		if input.Quoted != "" || len(input.MentionedJids) > 0 {
			locMsg.ContextInfo = buildContextInfo(input.Quoted, input.Participant, input.MentionedJids)
		}
		waMessage = &waE2E.Message{LocationMessage: locMsg}
		messageType = "location"
		payload = fmt.Sprintf("location:%f,%f", input.Latitude, input.Longitude)

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
	reachoutLocked := false
	// Stays true only while every failed attempt was a socket-down error, i.e. nothing ever left the
	// process. Only then is the failure answered as a retryable 503 (the SDK resends on 503).
	onlySocketDown := true
	var waitDeadline time.Time

	// Only here does the wait cost anything: everything that could be prepared already was, while
	// it ran. From this point on the message does go out, so the deferred stop becomes a no-op.
	waitPresence()
	stopPresence = func() {}

	// One id for every attempt: a retry after a send that did reach WhatsApp must not create a
	// second message, and the caller matches the echo by this id.
	sendID := resolveSendID(input.WhatsAppID, client.GenerateMessageID)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			s.log.Info("tentando reenvio de mensagem devido a erro anterior",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", backoff),
				zap.String("to", toJID.String()))
			time.Sleep(backoff)

			// Same gate as the first attempt: a group, a broadcast list or a status post has no
			// chat presence, and typing toward a JID that cannot receive it is exactly the
			// anomalous traffic the anti-ban machinery exists to avoid.
			if hasChatPresence(toJID) {
				_ = client.SendPresence(ctx, types.PresenceAvailable)
				_ = client.SendChatPresence(ctx, toJID, types.ChatPresenceComposing, presenceMediaType(input.Type))

				_, _ = client.GetUserDevices(ctx, []types.JID{toJID})
			}
		}

		resp, err = client.SendMessage(ctx, toJID, waMessage, whatsmeow.SendRequestExtra{ID: sendID})
		if err == nil {
			// An empty ID means WhatsApp did not actually accept the message.
			if resp.ID == "" {
				s.log.Warn("WhatsApp retornou ID vazio - envio pode ter falhado",
					zap.Int("attempt", attempt),
					zap.String("to", toJID.String()))
				err = fmt.Errorf("WhatsApp não confirmou envio (ID vazio)")
				onlySocketDown = false
				continue
			}

			s.log.Info("mensagem enviada com sucesso",
				zap.Int("attempt", attempt),
				zap.String("to", toJID.String()),
				zap.String("server_id", resp.ID),
				zap.Int64("timestamp", resp.Timestamp.Unix()))
			if s.sessionMgr != nil {
				s.sessionMgr.CacheOutgoingMessage(resp.ID, toJID, waMessage)
			}
			break
		}

		s.log.Warn("falha no envio da mensagem",
			zap.Int("attempt", attempt),
			zap.Error(err),
			zap.String("to", toJID.String()))

		if !isSocketDown(err) {
			onlySocketDown = false
		} else if attempt < maxRetries {
			deadline := outages.deadline(input.InstanceID, time.Now())
			if waitDeadline.IsZero() {
				waitDeadline = time.Now().Add(reconnectWaitBudget)
			}
			if waitDeadline.Before(deadline) {
				deadline = waitDeadline
			}
			start := time.Now()
			// A reconnect may hand the session a new client; waiting on the old one never ends.
			live := func() bool {
				if fresh, ferr := s.sessionMgr.GetClient(input.InstanceID); ferr == nil && fresh != nil {
					client = fresh
				}
				return client.IsConnected() && client.IsLoggedIn()
			}
			if !waitUntilConnected(ctx, live, deadline) {
				s.log.Warn("socket did not come back in time, giving up on the send",
					zap.String("instance_id", input.InstanceID),
					zap.String("to", toJID.String()),
					zap.Duration("waited", time.Since(start)))
				break
			}
			outages.clear(input.InstanceID)
			s.log.Info("socket back after a drop, resending",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()),
				zap.Duration("waited", time.Since(start)))
			continue
		}

		// Permanent: the number has no LID, so another attempt only adds discovery traffic.
		if errors.Is(err, whatsmeow.ErrRecipientNotOnWhatsApp) {
			break
		}

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
			// Synchronous 463 is a per-contact reach-out timelock (missing tctoken), NOT an account
			// ban — the account ban arrives via NotifyAccountReachoutTimelock / 403-logout. Block the
			// contact, stop retrying, and emit a contact-scoped event without flagging the connection.
			reachoutLocked = true
			s.log.Warn("restrição de reach-out (463) neste contato, abortando retentativas",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))
			reachoutStore(input.InstanceID, normalizeChatKey(toJID.String()))
			if s.eventLogRepo != nil {
				go func() {
					evtCtx, evtCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer evtCancel()
					payload := `{"message":"Restrição de reach-out (463) neste contato","reason":"server returned error 463","detail":"Contato frio sem tctoken; aguardando o contato iniciar conversa"}`
					_, _ = s.eventLogRepo.Create(evtCtx, model.EventLog{
						InstanceID: input.InstanceID,
						Type:       "contact_reachout_locked",
						Payload:    payload,
					})
				}()
			}
			if s.webhookQueue != nil {
				evt := queue.Event{
					ID:         uuid.NewString(),
					InstanceID: input.InstanceID,
					Type:       "contact_reachout_locked",
					Payload: map[string]interface{}{
						"type":   "contact_reachout_locked",
						"reason": "server returned error 463",
						"detail": "Contato frio sem tctoken; aguardando o contato iniciar conversa",
						"to":     toJID.String(),
						"code":   463,
					},
					CreatedAt: time.Now(),
				}
				if enqErr := s.webhookQueue.Enqueue(ctx, evt); enqErr != nil {
					s.log.Warn("falha ao enfileirar webhook contact_reachout_locked", zap.Error(enqErr))
				}
			}
			break
		}

		if strings.Contains(err.Error(), "not logged in") {
			s.log.Warn("instância não logada, abortando retentativas",
				zap.String("instance_id", input.InstanceID))
			_ = s.instanceRepo.UpdateStatus(context.Background(), input.InstanceID, model.InstanceStatusDisconnected)
			break
		}

		if strings.Contains(err.Error(), "device JID") {
			s.log.Warn("instância desconectada: dispositivo sem sessão ativa",
				zap.String("instance_id", input.InstanceID),
				zap.String("to", toJID.String()))
			_ = s.instanceRepo.UpdateStatus(context.Background(), input.InstanceID, model.InstanceStatusDisconnected)
			break
		}
	}

	closeComposing(ctx, client, input, toJID)

	if err != nil {
		isDisconnectedErr := strings.Contains(err.Error(), "not logged in") ||
			strings.Contains(err.Error(), "device JID")

		// A transport/session failure (dropped socket, no connection, timeout) does NOT prove
		// the number does not exist on WhatsApp — only IsOnWhatsApp proves that. Persisting a
		// negative here poisons the database cache and blocks legitimate resends until the TTL
		// expires. Only record a negative on a non-transient error.
		//
		// Important: a SendMessage failure (temporary ban 463, untrusted identity, missing crypto
		// session, any "server returned error") also does NOT prove the number is absent — the JID
		// was already resolved via IsOnWhatsApp/positive cache before reaching here. We treat these
		// as transient so we don't poison the negative cache.
		errMsg := err.Error()
		isTransientErr := isDisconnectedErr ||
			strings.Contains(errMsg, "connection") ||
			strings.Contains(errMsg, "not connected") ||
			strings.Contains(errMsg, "websocket") ||
			strings.Contains(errMsg, "timeout") ||
			strings.Contains(errMsg, "error 463") ||
			strings.Contains(errMsg, "server returned error") ||
			strings.Contains(errMsg, "untrusted identity") ||
			strings.Contains(errMsg, "no signal session")

		// Only drop the JID from the in-memory cache when the error is NOT transient. On a
		// transient error (463, socket, session), clearing the cache would force the next send to
		// redo IsOnWhatsApp (a discovery query) for an already-known contact — a "prospecting"
		// footprint that worsens the anti-spam heuristic. We keep the positive warm.
		if !isTransientErr {
			jidCache.Delete(input.To)
			s.log.Info("Removido do cache de JID devido a erro não-transitório de envio", zap.String("phone", input.To))
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

		if errors.Is(err, whatsmeow.ErrRecipientNotOnWhatsApp) {
			return msg, fmt.Errorf("%w: número não registrado no WhatsApp", ErrInvalidJID)
		}

		// Reach-out (463)
		if reachoutLocked {
			return msg, ErrContactReachoutLocked
		}

		if onlySocketDown && isSocketDown(err) {
			return msg, fmt.Errorf("%w: %v", ErrSessionUnavailable, err)
		}

		return msg, fmt.Errorf("erro ao enviar mensagem após %d tentativas: %w", maxRetries, err)
	}

	outages.clear(input.InstanceID)
	msg.Status = "sent"
	msg.WhatsAppID = resp.ID
	if err := s.repo.Update(ctx, msg); err != nil {
		s.log.Warn("erro ao atualizar status enviado no banco", zap.Error(err))
	}

	closeComposing(ctx, client, input, toJID)

	return msg, nil
}
