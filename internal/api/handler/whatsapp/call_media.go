package whatsapp

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// Media bridge frame kinds: the first byte of every binary WebSocket message.
const (
	mediaAudio byte = 1 // PCM16LE, mono, 16 kHz
	mediaVideo byte = 2 // one Annex-B H.264 access unit
)

// mediaControl is a text message on the bridge. Today only the camera rotation, in
// quarter turns clockwise: sent by apime when the peer's changes, and by the consumer
// to announce its own.
type mediaControl struct {
	Type  string `json:"type"`
	Value int    `json:"value"`
}

// callMedia upgrades to a WebSocket that carries the call's media both ways. Meant for
// the consumer's backend, which holds the instance token: browsers reach it through
// the consumer, never with the token itself.
func (h *Handler) callMedia(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	callID := c.Param("callId")
	if _, err := cm.GetCall(instanceID, callID); err != nil {
		callError(c, err)
		return
	}
	conn, err := websocket.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(4 << 20) // a video keyframe can pass 1 MB
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One writer at a time; a slow consumer drops frames instead of stalling the call.
	var wmu sync.Mutex
	write := func(typ websocket.MessageType, payload []byte, drop bool) {
		if drop {
			if !wmu.TryLock() {
				return
			}
		} else {
			wmu.Lock()
		}
		defer wmu.Unlock()
		wctx, wcancel := context.WithTimeout(ctx, time.Second)
		defer wcancel()
		if err := conn.Write(wctx, typ, payload); err != nil {
			cancel()
		}
	}
	send := func(kind byte, payload []byte) {
		write(websocket.MessageBinary, append([]byte{kind}, payload...), true)
	}
	// Rotation changes are rare and must not be dropped like a media frame.
	orientation := func(o int) {
		b, _ := json.Marshal(mediaControl{Type: "orientation", Value: o})
		write(websocket.MessageText, b, false)
	}
	bridge, err := cm.AttachMedia(instanceID, callID,
		func(pcm []byte) { send(mediaAudio, pcm) },
		func(au []byte) { send(mediaVideo, au) },
		orientation)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	defer bridge.Close()

	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
		if typ == websocket.MessageText {
			var ctl mediaControl
			if json.Unmarshal(msg, &ctl) == nil && ctl.Type == "orientation" {
				_ = bridge.SetOrientation(ctl.Value)
			}
			continue
		}
		if len(msg) < 2 {
			continue
		}
		switch msg[0] {
		case mediaAudio:
			bridge.SendAudio(msg[1:])
		case mediaVideo:
			_ = bridge.SendVideo(msg[1:])
		}
	}
}
