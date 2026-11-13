package whatsmeow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/voip"
)

// callEngine is the VoIP engine of one instance plus the calls it is tracking.
type callEngine struct {
	client *meowcaller.Client
	wa     *whatsmeow.Client
	mu     sync.Mutex
	calls  map[string]*trackedCall
	// endHints holds what whatsmeow told us about how a call ended before the engine
	// closes it (the engine only sees "rejected" for a decline made on the phone).
	endHints map[string]string
	offers   map[string]offerMeta
}

type offerMeta struct {
	video bool
	group string
}

type trackedCall struct {
	call      *meowcaller.Call
	direction string
	startedAt time.Time
	isVideo   bool
	groupJID  string

	mu        sync.Mutex
	active    bool
	localEnd  bool // this side hung up or declined through the API
	ended     bool
	lastVideo string
}

// attachCalls installs the engine on a client that has not connected yet. It must run
// before Connect: meowcaller hooks whatsmeow's <ack> handler, which is only safe before
// the receive loop starts. Instances without calls_enabled are left untouched, since the
// engine preaccepts every incoming call it sees.
func (m *Manager) attachCalls(ctx context.Context, instanceID string, client *whatsmeow.Client) {
	m.detachCalls(instanceID)
	if m.instanceRepo == nil {
		return
	}
	inst, err := m.instanceRepo.GetByID(ctx, instanceID)
	if err != nil || !inst.CallsEnabled {
		return
	}

	logger := zerolog.New(zerologToZap{log: m.log, instanceID: instanceID}).Level(zerolog.InfoLevel).With().Timestamp().Logger()
	eng := &callEngine{
		client: meowcaller.NewClient(client, meowcaller.WithLogger(logger)),
		wa:     client,
		calls:  make(map[string]*trackedCall),

		endHints: make(map[string]string),
		offers:   make(map[string]offerMeta),
	}
	eng.client.OnIncomingCall(func(call *meowcaller.Call) {
		m.trackCall(instanceID, eng, call, voip.DirectionIncoming)
	})
	// Runs ahead of the engine's own handler, which is added after this one.
	client.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.CallOffer:
			eng.noteOffer(e)
		case *events.CallReject:
			if own := client.Store.GetLID(); !own.IsEmpty() && e.From.User == own.User {
				eng.hint(e.CallID, voip.ReasonRejectedElsewhere)
			}
		}
	})

	m.voipMu.Lock()
	m.voip[instanceID] = eng
	m.voipMu.Unlock()
	m.log.Info("motor de chamadas instalado", zap.String("instance_id", instanceID))
}

func (m *Manager) detachCalls(instanceID string) {
	m.voipMu.Lock()
	eng := m.voip[instanceID]
	delete(m.voip, instanceID)
	m.voipMu.Unlock()
	if eng == nil {
		return
	}
	// Hang up outside the lock: OnEnd fires back into eng.mu.
	eng.mu.Lock()
	active := make([]*trackedCall, 0, len(eng.calls))
	for _, tc := range eng.calls {
		active = append(active, tc)
	}
	eng.mu.Unlock()
	for _, tc := range active {
		_ = tc.call.Hangup()
	}
}

func (m *Manager) callEngineFor(instanceID string) (*callEngine, error) {
	m.voipMu.Lock()
	defer m.voipMu.Unlock()
	eng := m.voip[instanceID]
	if eng == nil {
		return nil, voip.ErrCallsDisabled
	}
	return eng, nil
}

func (m *Manager) trackCall(instanceID string, eng *callEngine, call *meowcaller.Call, direction string) *trackedCall {
	tc := &trackedCall{call: call, direction: direction, startedAt: time.Now()}
	eng.mu.Lock()
	eng.calls[call.ID()] = tc
	eng.mu.Unlock()

	tc.isVideo, tc.groupJID = eng.offerInfo(call.ID())
	call.OnVideoState(func(v meowcaller.VideoState) {
		if v.Active {
			tc.mu.Lock()
			tc.isVideo = true
			tc.mu.Unlock()
		}
		m.emitVideoState(instanceID, eng, tc, v)
	})
	call.OnStateChange(func(phase meowcaller.CallPhase) {
		// The end is reported once, by OnEnd, with its reason.
		if phase == meowcaller.CallPhaseEnded {
			return
		}
		m.emitCallState(instanceID, eng, tc, voip.KindState, phaseName(phase), "")
		if phase == meowcaller.CallPhaseActive {
			tc.mu.Lock()
			first := !tc.active
			tc.active = true
			tc.mu.Unlock()
			if first {
				m.emitCallState(instanceID, eng, tc, voip.KindAccepted, "active", "")
			}
		}
	})
	call.OnEnd(func(reason string) {
		tc.mu.Lock()
		if tc.ended {
			tc.mu.Unlock()
			return
		}
		tc.ended = true
		tc.mu.Unlock()
		eng.mu.Lock()
		delete(eng.calls, call.ID())
		hint := eng.endHints[call.ID()]
		delete(eng.endHints, call.ID())
		delete(eng.offers, call.ID())
		eng.mu.Unlock()
		reason = endReason(tc, reason, hint)
		m.emitCallState(instanceID, eng, tc, voip.KindState, "ended", reason)
		m.emitCallState(instanceID, eng, tc, voip.KindEnded, "ended", reason)
	})
	m.emitCallState(instanceID, eng, tc, voip.KindState, phaseName(call.State()), "")
	return tc
}

func (m *Manager) emitCallState(instanceID string, eng *callEngine, tc *trackedCall, kind, state, reason string) {
	m.mu.RLock()
	handler := m.eventHandler
	m.mu.RUnlock()
	if handler == nil {
		return
	}
	jid := ""
	if eng.wa.Store != nil && eng.wa.Store.ID != nil {
		jid = eng.wa.Store.ID.String()
	}
	evt := &voip.StateEvent{Call: snapshot(tc, state), Kind: kind, Reason: reason}
	go handler.Handle(context.Background(), instanceID, jid, eng.wa, evt)
}

func snapshot(tc *trackedCall, state string) voip.Call {
	return voip.Call{
		ID:        tc.call.ID(),
		Peer:      tc.call.Peer().String(),
		Direction: tc.direction,
		State:     state,
		StartedAt: tc.startedAt,
		IsVideo:   tc.isVideo,
		GroupJID:  tc.groupJID,
	}
}

func phaseName(p meowcaller.CallPhase) string {
	switch p {
	case meowcaller.CallPhaseCalling:
		return "calling"
	case meowcaller.CallPhaseRinging:
		return "ringing"
	case meowcaller.CallPhaseConnecting:
		return "connecting"
	case meowcaller.CallPhaseActive:
		return "active"
	case meowcaller.CallPhaseEnded:
		return "ended"
	default:
		return "idle"
	}
}

func (m *Manager) StartCall(ctx context.Context, instanceID, to string, video bool) (voip.Call, error) {
	eng, err := m.callEngineFor(instanceID)
	if err != nil {
		return voip.Call{}, err
	}
	call, err := eng.client.CallWithOptions(ctx, to, meowcaller.CallOptions{Video: video})
	if err != nil {
		return voip.Call{}, err
	}
	eng.mu.Lock()
	eng.offers[call.ID()] = offerMeta{video: video}
	eng.mu.Unlock()
	m.trackCall(instanceID, eng, call, voip.DirectionOutgoing)
	return m.GetCall(instanceID, call.ID())
}

func (m *Manager) GetCall(instanceID, callID string) (voip.Call, error) {
	tc, err := m.lookupCall(instanceID, callID)
	if err != nil {
		return voip.Call{}, err
	}
	return snapshot(tc, phaseName(tc.call.State())), nil
}

func (m *Manager) ListCalls(instanceID string) ([]voip.Call, error) {
	eng, err := m.callEngineFor(instanceID)
	if err != nil {
		return nil, err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	out := make([]voip.Call, 0, len(eng.calls))
	for _, tc := range eng.calls {
		out = append(out, snapshot(tc, phaseName(tc.call.State())))
	}
	return out, nil
}

func (m *Manager) AnswerCall(instanceID, callID string) error {
	tc, err := m.lookupCall(instanceID, callID)
	if err != nil {
		return err
	}
	if err := tc.call.Answer(); err != nil {
		return err
	}
	// Answered counts from the accept: the engine only turns a call active on the
	// caller's first audio, which may never come.
	tc.mu.Lock()
	first := !tc.active
	tc.active = true
	tc.mu.Unlock()
	if eng, err := m.callEngineFor(instanceID); err == nil && first {
		m.emitCallState(instanceID, eng, tc, voip.KindAccepted, "active", "")
	}
	return nil
}

func (m *Manager) HangupCall(instanceID, callID string) error {
	tc, err := m.lookupCall(instanceID, callID)
	if err != nil {
		return err
	}
	tc.markLocalEnd()
	return tc.call.Hangup()
}

// RejectCall declines an incoming call. It works without the engine too, through
// whatsmeow's own reject, which is what an auto-reject on a calls-disabled instance uses;
// that path needs the caller JID from the call.offer webhook.
func (m *Manager) RejectCall(ctx context.Context, instanceID, callID, from string) error {
	if tc, err := m.lookupCall(instanceID, callID); err == nil {
		tc.markLocalEnd()
		return tc.call.Reject()
	}
	if strings.TrimSpace(from) == "" {
		return voip.ErrCallNotFound
	}
	jid, err := types.ParseJID(from)
	if err != nil {
		return fmt.Errorf("from inválido: %w", err)
	}
	client, err := m.GetClient(instanceID)
	if err != nil {
		return err
	}
	return client.RejectCall(ctx, jid, callID)
}

func (m *Manager) lookupCall(instanceID, callID string) (*trackedCall, error) {
	eng, err := m.callEngineFor(instanceID)
	if err != nil {
		return nil, err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	tc := eng.calls[callID]
	if tc == nil {
		return nil, voip.ErrCallNotFound
	}
	return tc, nil
}

// ReloadClient swaps the instance's client for a fresh one, so a calls_enabled change
// takes effect: the engine can only be installed before Connect. The old client's
// handlers are removed first, so its Disconnected never reaches the debounce and the
// consumer does not see the connection drop.
func (m *Manager) ReloadClient(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	old, ok := m.clients[instanceID]
	if ok {
		delete(m.clients, instanceID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}

	old.RemoveEventHandlers()
	old.Disconnect()
	m.detachCalls(instanceID)

	_, err := m.restoreSessionIfExists(ctx, instanceID)
	return err
}

// zerologToZap forwards meowcaller's zerolog lines into the apime zap log.
type zerologToZap struct {
	log        *zap.Logger
	instanceID string
}

func (w zerologToZap) Write(p []byte) (int, error) {
	w.log.Info("meowcaller", zap.String("instance_id", w.instanceID), zap.ByteString("event", p))
	return len(p), nil
}

func (tc *trackedCall) markLocalEnd() {
	tc.mu.Lock()
	tc.localEnd = true
	tc.mu.Unlock()
}

func (e *callEngine) hint(callID, reason string) {
	e.mu.Lock()
	e.endHints[callID] = reason
	e.mu.Unlock()
}

func (e *callEngine) noteOffer(ev *events.CallOffer) {
	meta := offerMeta{group: ev.GroupJID.String()}
	if ev.GroupJID.IsEmpty() {
		meta.group = ""
	}
	if ev.Data != nil {
		_, meta.video = ev.Data.GetOptionalChildByTag("video")
		if meta.group == "" {
			if g := ev.Data.AttrGetter().OptionalJIDOrEmpty("group-jid"); !g.IsEmpty() {
				meta.group = g.String()
			}
		}
	}
	e.mu.Lock()
	e.offers[ev.CallID] = meta
	e.mu.Unlock()
}

func (e *callEngine) offerInfo(callID string) (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	meta := e.offers[callID]
	return meta.video, meta.group
}

// endReason maps how WhatsApp and the engine ended a call to the reasons consumers get.
// An empty reason means a hangup after an answer, or a call nobody answered: cancelled
// when this side gave up, missed otherwise.
func endReason(tc *trackedCall, raw, hint string) string {
	if hint != "" {
		return hint
	}
	switch raw {
	case voip.ReasonAcceptedElsewhere, voip.ReasonRejectedElsewhere, voip.ReasonGroupCallEnded, voip.ReasonRejected:
		return raw
	}
	tc.mu.Lock()
	active, local := tc.active, tc.localEnd
	tc.mu.Unlock()
	switch {
	case active:
		return voip.ReasonHangup
	case local:
		return voip.ReasonCancelled
	}
	return voip.ReasonMissed
}

// engineEndsCall reports whether a whatsmeow call event is an end the engine already
// turns into call.ended.
func (m *Manager) engineEndsCall(instanceID string, evt any) bool {
	m.voipMu.Lock()
	eng := m.voip[instanceID]
	m.voipMu.Unlock()
	if eng == nil {
		return false
	}
	var callID string
	switch e := evt.(type) {
	case *events.CallTerminate:
		callID = e.CallID
	case *events.CallReject:
		callID = e.CallID
	case *events.CallOffer:
		if e.Data == nil {
			return false
		}
		ag := e.Data.AttrGetter()
		if ag.OptionalString("is_call_ended") != "1" && ag.OptionalString("terminate_reason") == "" {
			return false
		}
		callID = e.CallID
	default:
		return false
	}
	// A call the engine never tracked (e.g. its key failed to decrypt) gets no OnEnd, so the
	// whatsmeow end is the only one there is: suppressing it leaves the call ringing forever.
	eng.mu.Lock()
	_, tracked := eng.calls[callID]
	eng.mu.Unlock()
	return tracked
}

// emitVideoState reports the peer's camera: on, off, or asking to turn an audio call
// into a video one (answered with VideoCall "accept").
func (m *Manager) emitVideoState(instanceID string, eng *callEngine, tc *trackedCall, v meowcaller.VideoState) {
	state := "off"
	switch {
	case v.Upgrade:
		state = "upgrade_requested"
	case v.Active:
		state = "on"
	}
	// The peer repeats its state on every transition; only changes are worth a webhook.
	tc.mu.Lock()
	same := tc.lastVideo == state
	tc.lastVideo = state
	tc.mu.Unlock()
	if same {
		return
	}
	m.emitCallState(instanceID, eng, tc, voip.KindVideo, phaseName(tc.call.State()), state)
}

// VideoCall changes this side's video: "start" asks to turn an audio call into video,
// "accept" takes the peer's request, "stop" ends this side's video, "pause" and
// "resume" mute the camera without leaving video.
func (m *Manager) VideoCall(instanceID, callID, action string) error {
	tc, err := m.lookupCall(instanceID, callID)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		err = tc.call.StartVideo()
	case "accept":
		err = tc.call.AcceptVideo()
	case "stop":
		err = tc.call.StopVideo()
	case "pause":
		err = tc.call.SetVideoEnabled(false)
	case "resume":
		err = tc.call.SetVideoEnabled(true)
	default:
		return voip.ErrInvalidVideoAction
	}
	if err == nil && (action == "start" || action == "accept") {
		tc.mu.Lock()
		tc.isVideo = true
		tc.mu.Unlock()
	}
	return err
}
