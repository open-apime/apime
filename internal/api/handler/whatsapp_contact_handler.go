package handler

import (
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"

	"github.com/open-apime/apime/internal/pkg/response"
)

// saveContactThrottle espaça mutações de contato (app state) POR INSTÂNCIA. Rajada de app state é
// pegada de automação — mesmo padrão do delay do jid_resolver (validado como anti-ban). Só atrasa
// (não rejeita), pois o lead é legítimo. O gate "já salvo" já corta re-envios do mesmo número.
var (
	saveContactLastMu sync.Mutex
	saveContactLast   = map[string]time.Time{}
)

const saveContactMinInterval = 1 * time.Second

// throttleSaveContact bloqueia até que tenha passado o intervalo mínimo (+ jitter) desde o último
// save desta instância. Retorna imediatamente se já faz tempo suficiente.
func throttleSaveContact(instanceID string) {
	saveContactLastMu.Lock()
	last, ok := saveContactLast[instanceID]
	now := time.Now()
	var wait time.Duration
	if ok {
		if elapsed := now.Sub(last); elapsed < saveContactMinInterval {
			wait = saveContactMinInterval - elapsed
		}
	}
	// Reserva o próximo slot já contando o wait, para chamadas concorrentes se enfileirarem.
	next := now.Add(wait)
	saveContactLast[instanceID] = next
	saveContactLastMu.Unlock()

	wait += time.Duration(rand.Intn(500)) * time.Millisecond // jitter 0-500ms
	if wait > 0 {
		time.Sleep(wait)
	}
}

type saveContactRequest struct {
	JID       string `json:"jid"`       // número ou JID (ex.: "5548999998888" ou "...@s.whatsapp.net")
	FullName  string `json:"fullName" binding:"required"`
	FirstName string `json:"firstName"` // opcional
	// SaveOnPrimaryAddressbook, quando true, pede ao aparelho principal p/ gravar também na agenda
	// física do celular. Default false: salva só na conta WhatsApp (suficiente p/ o vínculo anti-ban).
	SaveOnPrimaryAddressbook bool `json:"saveOnPrimaryAddressbook"`
}

// saveContact salva um contato na lista de contatos da CONTA do WhatsApp (não na agenda do celular,
// salvo saveOnPrimaryAddressbook). Idempotente: se já houver nome salvo, não reenvia (gate local,
// sem rede) — evita rajada de mutações de app state (pegada de automação).
func (h *WhatsAppHandler) saveContact(c *gin.Context) {
	instanceID, ok := h.requireInstanceToken(c)
	if !ok {
		return
	}

	var req saveContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	jidStr := strings.TrimSpace(req.JID)
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
	if client.Store == nil || client.Store.Contacts == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "contacts store não disponível")
		return
	}

	// Índice da mutação usa sempre o PN (número). Se veio um LID, resolve p/ o número.
	var lid types.JID
	if jid.Server == types.HiddenUserServer {
		lid = jid
		if client.Store.LIDs != nil {
			if pn, err := client.Store.LIDs.GetPNForLID(c.Request.Context(), jid); err == nil && !pn.IsEmpty() {
				jid = pn.ToNonAD()
			}
		}
		if jid.Server == types.HiddenUserServer {
			response.ErrorWithMessage(c, http.StatusBadRequest, "não foi possível resolver o número (PN) do contato LID")
			return
		}
	} else if client.Store.LIDs != nil {
		// Entrou PN: tenta descobrir o LID p/ preencher lidJID na mutação (best-effort).
		if l, err := client.Store.LIDs.GetLIDForPN(c.Request.Context(), jid); err == nil && !l.IsEmpty() {
			lid = l
		}
	}

	// Gate "já salvo": leitura local no store (sem rede). Salvo = FullName ou FirstName preenchidos
	// (só PushName = conhecido, mas não salvo).
	if contact, err := client.Store.Contacts.GetContact(c.Request.Context(), jid); err == nil &&
		(contact.FullName != "" || contact.FirstName != "") {
		response.Success(c, http.StatusOK, gin.H{
			"status":    "already_saved",
			"jid":       jid.String(),
			"fullName":  contact.FullName,
			"firstName": contact.FirstName,
		})
		return
	}

	// Espaça mutações de app state por instância (anti-ban). Só depois do gate, para não atrasar
	// respostas already_saved.
	throttleSaveContact(instanceID)

	patch := appstate.BuildContact(jid, req.FullName, req.FirstName, lid, req.SaveOnPrimaryAddressbook)
	if err := client.SendAppState(c.Request.Context(), patch); err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, gin.H{"status": "saved", "jid": jid.String()})
}

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
