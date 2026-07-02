package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/types"

	"github.com/open-apime/apime/internal/pkg/response"
)

func (h *WhatsAppHandler) listContacts(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	if client.Store == nil || client.Store.Contacts == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "contacts store não disponível")
		return
	}
	contacts, err := client.Store.Contacts.GetAllContacts(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"contacts": contacts})
}

func (h *WhatsAppHandler) getContact(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	jidStr := c.Param("jid")
	jidStr = strings.TrimSpace(jidStr)
	if !strings.Contains(jidStr, "@") {
		jidStr = strings.TrimPrefix(jidStr, "+") + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	// Resolução LID → PN: tenta mapear JID oculto para o número real
	// antes de buscar o contato. O mapping é populado automaticamente
	// pelo whatsmeow (SenderAlt/RecipientAlt e GetUserInfo).
	var inputJID *types.JID
	if jid.Server == types.HiddenUserServer {
		copy := jid
		inputJID = &copy

		// 1. Lookup local (rápido, sem rede)
		if client.Store != nil && client.Store.LIDs != nil {
			if pn, err := client.Store.LIDs.GetPNForLID(c.Request.Context(), jid); err == nil && !pn.IsEmpty() {
				jid = pn.ToNonAD()
			}
		}

		// 2. Se ainda é LID, força população via rede e tenta de novo
		if jid.Server == types.HiddenUserServer {
			if infoMap, err := client.GetUserInfo(c.Request.Context(), []types.JID{jid}); err == nil {
				if info, ok := infoMap[jid]; ok && !info.LID.IsEmpty() {
					if client.Store != nil && client.Store.LIDs != nil {
						if pn, err := client.Store.LIDs.GetPNForLID(c.Request.Context(), jid); err == nil && !pn.IsEmpty() {
							jid = pn.ToNonAD()
						}
					}
				}
			}
		}
	}

	if client.Store == nil || client.Store.Contacts == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "contacts store não disponível")
		return
	}
	contact, err := client.Store.Contacts.GetContact(c.Request.Context(), jid)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	resp := gin.H{"jid": jid.String(), "contact": contact}
	if inputJID != nil {
		resp["inputJID"] = inputJID.String()
	}
	response.Success(c, http.StatusOK, resp)
}

func (h *WhatsAppHandler) getUserInfo(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	jidStr := strings.TrimSpace(c.Param("jid"))
	if !strings.Contains(jidStr, "@") {
		jidStr = strings.TrimPrefix(jidStr, "+") + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "jid inválido")
		return
	}

	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}

	infoMap, err := client.GetUserInfo(c.Request.Context(), []types.JID{jid})
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	info, exists := infoMap[jid]
	if !exists {
		response.ErrorWithMessage(c, http.StatusNotFound, "usuário não encontrado")
		return
	}
	response.Success(c, http.StatusOK, info)
}

type getContactQRLinkRequest struct {
	Revoke bool `json:"revoke"`
}

func (h *WhatsAppHandler) getContactQRLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	revoke := false
	if strings.EqualFold(strings.TrimSpace(c.Query("revoke")), "true") {
		revoke = true
	} else {
		var req getContactQRLinkRequest
		_ = c.ShouldBindJSON(&req)
		revoke = req.Revoke
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	code, err := client.GetContactQRLink(c.Request.Context(), revoke)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"code": code})
}

type resolveContactQRLinkRequest struct {
	Code string `json:"code" binding:"required"`
}

func (h *WhatsAppHandler) resolveContactQRLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req resolveContactQRLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	target, err := client.ResolveContactQRLink(c.Request.Context(), strings.TrimSpace(req.Code))
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, target)
}

type resolveBusinessMessageLinkRequest struct {
	Code string `json:"code" binding:"required"`
}

func (h *WhatsAppHandler) resolveBusinessMessageLink(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}
	var req resolveBusinessMessageLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	client, err := h.sessionManager.GetClient(instanceID)
	if err != nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "instância não conectada")
		return
	}
	target, err := client.ResolveBusinessMessageLink(c.Request.Context(), strings.TrimSpace(req.Code))
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, target)
}
