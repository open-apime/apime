package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/response"
	"github.com/open-apime/apime/internal/storage/model"
)

func (h *InstanceHandler) getInstanceInfo(c *gin.Context) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}

	instance, err := h.service.Get(c.Request.Context(), instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusNotFound, "instância não encontrada")
		return
	}

	if h.sessionManager == nil {
		response.Success(c, http.StatusOK, gin.H{
			"id":        instance.ID,
			"name":      instance.Name,
			"status":    instance.Status,
			"connected": false,
		})
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		if instance.Status == model.InstanceStatusActive {
			ctxUpdate := c.Request.Context()
			if _, updateErr := h.service.UpdateStatus(ctxUpdate, instanceID, model.InstanceStatusError); updateErr != nil {
				h.log.Warn("erro ao atualizar status da instância", zap.String("instance_id", instanceID), zap.Error(updateErr))
			}
			instance.Status = model.InstanceStatusError
		}
		response.Success(c, http.StatusOK, gin.H{
			"id":        instance.ID,
			"name":      instance.Name,
			"status":    instance.Status,
			"connected": false,
		})
		return
	}

	isLoggedIn := client.IsLoggedIn()
	if !isLoggedIn && instance.Status == model.InstanceStatusActive {
		ctxUpdate := c.Request.Context()
		if _, updateErr := h.service.UpdateStatus(ctxUpdate, instanceID, model.InstanceStatusError); updateErr != nil {
			h.log.Warn("erro ao atualizar status da instância", zap.String("instance_id", instanceID), zap.Error(updateErr))
		}
		instance.Status = model.InstanceStatusError
	}

	responseData := gin.H{
		"id":        instance.ID,
		"name":      instance.Name,
		"status":    instance.Status,
		"connected": isLoggedIn,
	}

	if isLoggedIn && client.Store != nil && client.Store.ID != nil {
		instanceJID := client.Store.ID.String()
		responseData["instanceJID"] = instanceJID

		profilePic, err := client.GetProfilePictureInfo(c.Request.Context(), *client.Store.ID, nil)
		if err == nil && profilePic != nil {
			responseData["profilePicture"] = gin.H{
				"url": profilePic.URL,
				"id":  profilePic.ID,
			}
		} else if err != nil {
			h.log.Debug("não foi possível obter foto de perfil da instância",
				zap.String("instance_id", instanceID),
				zap.Error(err))
		}
	}

	response.Success(c, http.StatusOK, responseData)
}

func (h *InstanceHandler) getProfile(c *gin.Context) {
	instanceID := c.Param("id")
	jidStr := c.Param("jid")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}

	if h.sessionManager == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "session manager não configurado")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, fmt.Sprintf("JID inválido: %s", jidStr))
		return
	}

	userInfoMap, err := client.GetUserInfo(c.Request.Context(), []types.JID{jid})
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	userInfo, exists := userInfoMap[jid]
	if !exists {
		response.ErrorWithMessage(c, http.StatusNotFound, "perfil não encontrado")
		return
	}

	response.Success(c, http.StatusOK, userInfo)
}

func (h *InstanceHandler) getBusinessProfile(c *gin.Context) {
	instanceID := c.Param("id")
	jidStr := c.Param("jid")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}

	if h.sessionManager == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "session manager não configurado")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, fmt.Sprintf("JID inválido: %s", jidStr))
		return
	}

	businessProfile, err := client.GetBusinessProfile(c.Request.Context(), jid)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, businessProfile)
}

func (h *InstanceHandler) getProfilePicture(c *gin.Context) {
	instanceID := c.Param("id")
	jidStr := c.Param("jid")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return
	}

	if h.sessionManager == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "session manager não configurado")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, fmt.Sprintf("JID inválido: %s", jidStr))
		return
	}

	pictureInfo, err := client.GetProfilePictureInfo(c.Request.Context(), jid, nil)
	// Benign cases: a contact with no picture or who hid their picture is not a
	// server error — respond 200 with null instead of 500.
	if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) || errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
		response.Success(c, http.StatusOK, nil)
		return
	}
	// An unstable instance (socket down, reconnecting, unanswered IQ) is not a
	// server error — the picture is simply unavailable right now. Respond 200 with
	// null (same handling as "no picture"); the consumer re-fetches on the next cycle
	// once the instance reconnects. Avoids polluting Sentry with 5xx.
	if errors.Is(err, whatsmeow.ErrNotConnected) || errors.Is(err, whatsmeow.ErrNotLoggedIn) ||
		errors.Is(err, whatsmeow.ErrClientIsNil) || errors.Is(err, whatsmeow.ErrIQTimedOut) {
		response.Success(c, http.StatusOK, nil)
		return
	}
	// Transient/protocol errors on the WhatsApp side are also not our fault:
	// rate limit, temporary unavailability, blocked picture (forbidden),
	// removed resource (gone), an incomplete response from Meta (ElementMissing), or
	// a request cancelled/expired by the consumer. The picture is best-effort — respond
	// 200 null and the consumer re-fetches. Avoids polluting Sentry with 5xx.
	var elementMissing *whatsmeow.ElementMissingError
	if errors.Is(err, whatsmeow.ErrIQRateOverLimit) || errors.Is(err, whatsmeow.ErrIQServiceUnavailable) ||
		errors.Is(err, whatsmeow.ErrIQForbidden) || errors.Is(err, whatsmeow.ErrIQGone) ||
		errors.As(err, &elementMissing) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		response.Success(c, http.StatusOK, nil)
		return
	}
	// bad-request (400) means the queried JID is invalid/non-existent from the
	// WhatsApp server's point of view (e.g. malformed number, synthetic JID).
	// It's a caller input error, not a server failure: respond 400 with the
	// message instead of 500. Ideally the caller stops querying invalid JIDs.
	if errors.Is(err, whatsmeow.ErrIQBadRequest) {
		response.Error(c, http.StatusBadRequest, fmt.Errorf("JID inválido para consulta de foto: %s", jidStr))
		return
	}
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	// No error, but the lib may return nil when there is no picture.
	response.Success(c, http.StatusOK, pictureInfo)
}
