package whatsapp

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"

	"github.com/open-apime/apime/internal/pkg/response"
	messageSvc "github.com/open-apime/apime/internal/service/message"
)

type Handler struct {
	sessionManager SessionManager
	messageService *messageSvc.Service
}

type SessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
}

// appStateResyncer is an optional extension of SessionManager: the real session manager implements
// it, and the route type-asserts for it instead of widening the interface that every caller (tests
// included) would then have to satisfy. The route answers 501 when it is absent.
type appStateResyncer interface {
	ResyncAppState(ctx context.Context, instanceID string, collections []string) (resynced, skipped []string, err error)
}

func NewHandler(sessionManager SessionManager, messageService *messageSvc.Service) *Handler {
	return &Handler{
		sessionManager: sessionManager,
		messageService: messageService,
	}
}

func (h *Handler) Register(r *gin.RouterGroup) {
	r.POST("/instances/:id/whatsapp/check", h.checkIsWhatsApp)
	r.POST("/instances/:id/whatsapp/presence", h.setPresence)
	r.POST("/instances/:id/whatsapp/messages/read", h.markRead)
	r.POST("/instances/:id/whatsapp/messages/delete", h.deleteForEveryone)
	r.POST("/instances/:id/whatsapp/messages/edit", h.editMessage)
	r.POST("/instances/:id/whatsapp/messages/react", h.sendReaction)
	r.GET("/instances/:id/whatsapp/contacts", h.listContacts)
	r.GET("/instances/:id/whatsapp/contacts/:jid", h.getContact)
	r.GET("/instances/:id/whatsapp/userinfo/:jid", h.getUserInfo)
	r.GET("/instances/:id/whatsapp/privacy", h.getPrivacySettings)
	r.POST("/instances/:id/whatsapp/privacy", h.setPrivacySetting)
	r.GET("/instances/:id/whatsapp/chat-settings/:chat", h.getChatSettings)
	r.POST("/instances/:id/whatsapp/chat-settings/:chat", h.setChatSettings)
	r.POST("/instances/:id/whatsapp/status", h.setStatusMessage)
	r.POST("/instances/:id/whatsapp/disappearing-timer", h.setDefaultDisappearingTimer)
	r.GET("/instances/:id/whatsapp/qr/contact", h.getContactQRLink)
	r.POST("/instances/:id/whatsapp/qr/contact/resolve", h.resolveContactQRLink)
	r.POST("/instances/:id/whatsapp/qr/business-message/resolve", h.resolveBusinessMessageLink)
	r.GET("/instances/:id/whatsapp/groups/:group/invite-link", h.getGroupInviteLink)
	r.POST("/instances/:id/whatsapp/groups/resolve-invite", h.getGroupInfoFromLink)
	r.POST("/instances/:id/whatsapp/groups/join", h.joinGroupWithLink)
	r.POST("/instances/:id/whatsapp/groups/:group/leave", h.leaveGroup)
	r.POST("/instances/:id/whatsapp/groups", h.createGroup)
	r.POST("/instances/:id/whatsapp/groups/:group/participants", h.updateGroupParticipants)
	r.GET("/instances/:id/whatsapp/groups/:group/requests", h.listGroupJoinRequests)
	r.POST("/instances/:id/whatsapp/groups/:group/requests", h.updateGroupJoinRequests)
	r.GET("/instances/:id/whatsapp/status-privacy", h.getStatusPrivacy)
	r.POST("/instances/:id/whatsapp/appstate/resync", h.resyncAppState)
	r.GET("/instances/:id/whatsapp/broadcast-lists", h.listBroadcastLists)
	r.GET("/instances/:id/whatsapp/broadcast-lists/:jid", h.getBroadcastList)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/live-updates", h.newsletterSubscribeLiveUpdates)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/mark-viewed", h.newsletterMarkViewed)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/reaction", h.newsletterSendReaction)
	r.POST("/instances/:id/whatsapp/newsletters/:jid/message-updates", h.getNewsletterMessageUpdates)
	r.GET("/instances/:id/whatsapp/groups", h.listGroups)
	r.GET("/instances/:id/whatsapp/groups/:group", h.getGroupInfo)
	r.POST("/instances/:id/whatsapp/upload", h.uploadMedia)
	r.POST("/instances/:id/calls", h.startCall)
	r.GET("/instances/:id/calls", h.listCalls)
	r.GET("/instances/:id/calls/:callId", h.getCall)
	r.POST("/instances/:id/calls/:callId/answer", h.answerCall)
	r.POST("/instances/:id/calls/:callId/reject", h.rejectCall)
	r.DELETE("/instances/:id/calls/:callId", h.hangupCall)
	r.POST("/instances/:id/calls/:callId/video", h.videoCall)
	r.GET("/instances/:id/calls/:callId/media", h.callMedia)
}

func (h *Handler) requireInstanceToken(c *gin.Context) (string, bool) {
	instanceID := c.Param("id")
	if c.GetString("authType") != "instance_token" {
		response.ErrorWithMessage(c, http.StatusForbidden, "endpoint disponível apenas com token de instância")
		return "", false
	}
	if c.GetString("instanceID") != instanceID {
		response.ErrorWithMessage(c, http.StatusForbidden, "token inválido para esta instância")
		return "", false
	}
	if h.sessionManager == nil {
		response.ErrorWithMessage(c, http.StatusBadRequest, "session manager não configurado")
		return "", false
	}
	return instanceID, true
}
