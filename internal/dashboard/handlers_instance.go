package dashboard

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/service/instance"
	"github.com/open-apime/apime/internal/storage/model"
)

func (h *Handler) overview(c *gin.Context) {
	ctx := c.Request.Context()

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	q := c.Query("q")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	var instances []model.Instance
	var total int
	var err error

	if userRole == "admin" {
		instances, total, err = h.instances.List(ctx, q, limit, offset)
	} else {
		instances, total, err = h.instances.ListByUser(ctx, userID, userRole, q, limit, offset)
	}

	if err != nil {
		h.renderError(c, err)
		return
	}

	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}

	data := map[string]any{
		"Instances":                  instances,
		"Total":                      total,
		"CurrentPage":                page,
		"TotalPages":                 totalPages,
		"Query":                      q,
		"Limit":                      limit,
		"NewInstanceToken":           c.Query("newInstanceToken"),
		"NewInstanceTokenInstanceID": c.Query("newInstanceTokenInstanceID"),
		"UserRole":                   userRole,
	}

	dataPage := h.pageData(c, "", "instances_content", data)
	c.HTML(http.StatusOK, "layout", dataPage)
}

func (h *Handler) createInstance(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		redirectWithMessage(c, "/dashboard", "error", "Nome é obrigatório.")
		return
	}

	userID := c.GetString("userID")

	_, err := h.instances.Create(c.Request.Context(), instance.CreateInput{
		Name:          name,
		WebhookURL:    strings.TrimSpace(c.PostForm("webhook_url")),
		WebhookSecret: strings.TrimSpace(c.PostForm("webhook_secret")),
		OwnerUserID:   userID,
	})
	if err != nil {
		h.logger.Warn("erro ao criar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao criar instância.")
		return
	}

	redirectWithMessage(c, "/dashboard", "success", "Instância criada com sucesso.")
}

func (h *Handler) updateInstance(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard", "error", "Instância inválida.")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		redirectWithMessage(c, "/dashboard", "error", "Nome é obrigatório.")
		return
	}
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	callerID := userID
	if userRole == "admin" {
		callerID = "admin"
	}

	_, err := h.instances.UpdateByUser(c.Request.Context(), id, instance.UpdateInput{
		Name:          name,
		WebhookURL:    strings.TrimSpace(c.PostForm("webhook_url")),
		WebhookSecret: strings.TrimSpace(c.PostForm("webhook_secret")),
		OwnerUserID:   callerID,
	})
	if err != nil {
		h.logger.Warn("erro ao atualizar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao atualizar instância.")
		return
	}
	redirectWithMessage(c, "/dashboard", "success", "Instância atualizada.")
}

func (h *Handler) rotateInstanceToken(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard", "error", "Instância inválida.")
		return
	}
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	plain, err := h.instances.RotateTokenByUser(c.Request.Context(), id, userID, userRole)
	if err != nil {
		h.logger.Warn("erro ao rotacionar token da instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao gerar token da instância.")
		return
	}
	values := url.Values{}
	values.Set("success", "Token da instância gerado. Copie o valor exibido abaixo.")
	values.Set("newInstanceToken", plain)
	values.Set("newInstanceTokenInstanceID", id)
	c.Redirect(http.StatusSeeOther, "/dashboard?"+values.Encode())
}

func (h *Handler) showInstanceQR(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard", "error", "Instância inválida.")
		return
	}

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	code, err := h.instances.GetQRByUser(ctx, id, userID, userRole)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			h.logger.Info("request cancelado pelo cliente durante geração de QR", zap.String("instance_id", id))
			redirectWithMessage(c, "/dashboard", "error", "Requisição cancelada.")
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.logger.Warn("timeout ao gerar QR", zap.String("instance_id", id))
			redirectWithMessage(c, "/dashboard", "error", "Timeout ao gerar QR code.")
			return
		}

		h.logger.Warn("erro ao gerar QR", zap.String("instance_id", id), zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Falha ao obter QR code: "+err.Error())
		return
	}

	if code == "" {
		redirectWithMessage(c, "/dashboard", "error", "QR code não disponível (já conectado?).")
		return
	}

	png, err := qrcode.Encode(code, qrcode.Medium, 256)
	if err != nil {
		h.logger.Warn("erro ao construir PNG do QR", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "QR code inválido.")
		return
	}

	data := map[string]any{
		"InstanceID": id,
		"QRCode":     base64.StdEncoding.EncodeToString(png),
		"Raw":        code,
	}
	page := h.pageData(c, "", "instance_qr_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) getQRImage(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	code, err := h.instances.GetQRByUser(ctx, id, userID, userRole)
	if err != nil || code == "" {
		c.Status(http.StatusNotFound)
		return
	}

	png, err := qrcode.Encode(code, qrcode.Medium, 256)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	c.Data(http.StatusOK, "image/png", png)
}

func (h *Handler) getInstanceQRStatus(c *gin.Context) {
	id := c.Param("id")

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	instance, err := h.instances.GetByUser(c.Request.Context(), id, userID, userRole)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "instância não encontrada"})
		return
	}

	hasQR := false
	qrCode := ""
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	code, err := h.instances.GetQRByUser(ctx, id, userID, userRole)
	if err == nil && code != "" {
		hasQR = true
		qrCode = code
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    instance.Status,
		"hasQR":     hasQR,
		"qrCode":    qrCode,
		"connected": instance.Status == model.InstanceStatusActive,
	})
}

func (h *Handler) instanceDiagnostics(c *gin.Context) {
	instanceID := c.Param("id")
	ctx := c.Request.Context()

	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	inst, err := h.instances.GetByUser(ctx, instanceID, userID, userRole)
	if err != nil {
		h.renderError(c, err)
		return
	}

	var diagnostics interface{}
	if h.sessionManager != nil {
		diagnostics = h.sessionManager.GetDiagnostics(instanceID)
	}

	data := map[string]any{
		"Instance":    inst,
		"Diagnostics": diagnostics,
	}

	page := h.pageData(c, "", "instance_diagnostics_content", data)
	c.HTML(http.StatusOK, "layout", page)
}

func (h *Handler) disconnectInstance(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	if err := h.instances.DisconnectByUser(c.Request.Context(), id, userID, userRole); err != nil {
		h.logger.Warn("erro ao desconectar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Não foi possível desconectar.")
		return
	}
	redirectWithMessage(c, "/dashboard", "success", "Instância desconectada.")
}

func (h *Handler) deleteInstance(c *gin.Context) {
	id := c.Param("id")
	userID := c.GetString("userID")
	userRole := c.GetString("userRole")

	if err := h.instances.DeleteByUser(c.Request.Context(), id, userID, userRole); err != nil {
		h.logger.Warn("erro ao deletar instância", zap.Error(err))
		redirectWithMessage(c, "/dashboard", "error", "Não foi possível deletar instância.")
		return
	}
	redirectWithMessage(c, "/dashboard", "success", "Instância deletada com sucesso.")
}
