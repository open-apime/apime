package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/pkg/response"
	"github.com/open-apime/apime/internal/voip"
)

// callManager is an optional extension of SessionManager, type-asserted like
// appStateResyncer so test doubles do not have to implement calls. Routes answer 501
// when it is absent.
type callManager interface {
	StartCall(ctx context.Context, instanceID, to string, video bool) (voip.Call, error)
	VideoCall(instanceID, callID, action string) error
	AttachMedia(instanceID, callID string, onAudio, onVideo func([]byte), onOrientation func(int)) (voip.MediaBridge, error)
	GetCall(instanceID, callID string) (voip.Call, error)
	ListCalls(instanceID string) ([]voip.Call, error)
	AnswerCall(instanceID, callID string) error
	HangupCall(instanceID, callID string) error
	RejectCall(ctx context.Context, instanceID, callID, from string) error
}

type startCallRequest struct {
	To string `json:"to" binding:"required"`
	// Video starts a video call; the camera stream goes through the media bridge.
	Video bool `json:"video"`
}

type rejectCallRequest struct {
	// From is the rejectFrom of the call.offer webhook; only needed when the
	// instance runs without the call engine.
	From string `json:"from"`
}

func (h *Handler) callsFor(c *gin.Context) (callManager, string, bool) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return nil, "", false
	}
	cm, ok := h.sessionManager.(callManager)
	if !ok {
		response.ErrorWithMessage(c, http.StatusNotImplemented, "chamadas não suportadas")
		return nil, "", false
	}
	return cm, instanceID, true
}

func callError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, voip.ErrCallsDisabled):
		response.ErrorWithMessage(c, http.StatusConflict, err.Error())
	case errors.Is(err, voip.ErrInvalidVideoAction):
		response.ErrorWithMessage(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, voip.ErrCallNotFound):
		response.ErrorWithMessage(c, http.StatusNotFound, err.Error())
	default:
		response.Error(c, http.StatusBadRequest, err)
	}
}

func (h *Handler) startCall(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	var req startCallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	call, err := cm.StartCall(c.Request.Context(), instanceID, strings.TrimSpace(req.To), req.Video)
	if err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusCreated, call)
}

func (h *Handler) listCalls(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	calls, err := cm.ListCalls(instanceID)
	if err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusOK, calls)
}

func (h *Handler) getCall(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	call, err := cm.GetCall(instanceID, c.Param("callId"))
	if err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusOK, call)
}

func (h *Handler) answerCall(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	if err := cm.AnswerCall(instanceID, c.Param("callId")); err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) rejectCall(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	var req rejectCallRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Error(c, http.StatusBadRequest, err)
			return
		}
	}
	if err := cm.RejectCall(c.Request.Context(), instanceID, c.Param("callId"), req.From); err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) hangupCall(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	if err := cm.HangupCall(instanceID, c.Param("callId")); err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}

type videoCallRequest struct {
	Action string `json:"action" binding:"required"`
}

func (h *Handler) videoCall(c *gin.Context) {
	cm, instanceID, ok := h.callsFor(c)
	if !ok {
		return
	}
	var req videoCallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	if err := cm.VideoCall(instanceID, c.Param("callId"), req.Action); err != nil {
		callError(c, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"status": "ok"})
}
