package dashboard

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/service/user"
)

func (h *Handler) usersPage(c *gin.Context) {
	q := c.Query("q")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	users, total, err := h.users.List(c.Request.Context(), q, limit, offset)
	if err != nil {
		h.renderError(c, err)
		return
	}

	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}

	adminCount := 0
	for _, u := range users {
		if u.Role == "admin" {
			adminCount++
		}
	}

	data := map[string]any{
		"Users":         users,
		"Total":         total,
		"CurrentPage":   page,
		"TotalPages":    totalPages,
		"Query":         q,
		"Limit":         limit,
		"NewUserToken":  c.Query("newUserToken"),
		"NewUserEmail":  c.Query("newUserEmail"),
		"CurrentUserID": c.GetString("userID"),
		"AdminCount":    adminCount,
	}
	dataPage := h.pageData(c, "", "users_content", data)
	c.HTML(http.StatusOK, "layout", dataPage)
}

func (h *Handler) createUser(c *gin.Context) {
	input := user.CreateInput{
		Email:    strings.TrimSpace(c.PostForm("email")),
		Password: c.PostForm("password"),
		Role:     strings.TrimSpace(c.PostForm("role")),
	}
	user, token, err := h.users.Create(c.Request.Context(), input)
	if err != nil {
		h.logger.Warn("erro ao criar usuário", zap.Error(err))
		redirectWithMessage(c, "/dashboard/users", "error", "Não foi possível criar usuário.")
		return
	}

	values := url.Values{}
	values.Set("success", "Usuário criado com sucesso! Copie o token de acesso abaixo.")
	values.Set("newUserToken", token)
	values.Set("newUserEmail", user.Email)
	c.Redirect(http.StatusSeeOther, "/dashboard/users?"+values.Encode())
}

func (h *Handler) updateUserPassword(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard/users", "error", "Usuário inválido.")
		return
	}
	if err := h.users.UpdatePassword(c.Request.Context(), id, c.PostForm("password")); err != nil {
		h.logger.Warn("erro ao atualizar senha", zap.Error(err))
		redirectWithMessage(c, "/dashboard/users", "error", "Falha ao atualizar senha.")
		return
	}
	redirectWithMessage(c, "/dashboard/users", "success", "Senha atualizada.")
}

func (h *Handler) deleteUser(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		redirectWithMessage(c, "/dashboard/users", "error", "Usuário inválido.")
		return
	}
	if current := c.GetString("userID"); current == id {
		redirectWithMessage(c, "/dashboard/users", "error", "Você não pode remover o próprio usuário logado.")
		return
	}

	if err := h.users.Delete(c.Request.Context(), id); err != nil {
		h.logger.Warn("erro ao remover usuário", zap.Error(err))
		if err.Error() == "não é possível remover o último administrador" {
			redirectWithMessage(c, "/dashboard/users", "error", "Não é possível remover o único administrador do sistema.")
			return
		}
		redirectWithMessage(c, "/dashboard/users", "error", "Falha ao remover usuário.")
		return
	}
	redirectWithMessage(c, "/dashboard/users", "success", "Usuário removido.")
}
