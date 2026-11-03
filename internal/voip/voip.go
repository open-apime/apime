// Package voip holds the call types shared by the session manager (which drives the
// engine) and the webhook layer (which turns them into call.* events), so neither
// has to import the other.
package voip

import (
	"errors"
	"time"
)

var (
	ErrCallsDisabled = errors.New("chamadas desativadas nesta instância")
	ErrCallNotFound  = errors.New("chamada não encontrada")

	ErrMediaClosed        = errors.New("ponte de mídia encerrada")
	ErrInvalidVideoAction = errors.New("ação de vídeo inválida: use start, accept, stop, pause ou resume")
)

const (
	DirectionIncoming = "incoming"
	DirectionOutgoing = "outgoing"
)

// Call is the public snapshot of a call the engine is tracking.
type Call struct {
	ID        string    `json:"callId"`
	Peer      string    `json:"peer"`
	Direction string    `json:"direction"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
	IsVideo   bool      `json:"isVideo"`
	// GroupJID is set when the call was started from a group chat.
	GroupJID string `json:"groupJid,omitempty"`
}

// Kinds of StateEvent: a phase change, the single end of a call, and the moment an
// outgoing call is answered.
const (
	KindState    = "state"
	KindEnded    = "ended"
	KindAccepted = "accepted"
	// KindVideo carries the peer's camera state in Reason: on, off, upgrade_requested.
	KindVideo = "video"
)

// Normalized end reasons, so consumers do not parse WhatsApp's own strings.
const (
	ReasonMissed            = "missed"
	ReasonRejected          = "rejected"
	ReasonRejectedElsewhere = "rejected_elsewhere"
	ReasonAcceptedElsewhere = "accepted_elsewhere"
	ReasonCancelled         = "cancelled"
	ReasonHangup            = "hangup"
	ReasonGroupCallEnded    = "group_call_ended"
)

// StateEvent is what the engine reports about a tracked call. On instances with the
// engine it is the only source of call.state, call.accepted and call.ended, each end
// emitted once per call.
type StateEvent struct {
	Call
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// MediaBridge carries one call's media to a consumer (see the /calls/:callId/media route).
type MediaBridge interface {
	// SendAudio queues PCM16LE mono 16 kHz audio, in any chunk size.
	SendAudio(pcm []byte)
	// SendVideo sends one Annex-B H.264 access unit.
	SendVideo(accessUnit []byte) error
	// SetOrientation announces this side's camera rotation, in quarter turns clockwise.
	SetOrientation(quarterTurns int) error
	Close()
}
