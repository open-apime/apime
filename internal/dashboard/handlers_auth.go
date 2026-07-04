package dashboard

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/api/middleware"
)

func (h *Handler) showLogin(c *gin.Context) {
	data := h.pageData(c, "", "login_content", nil)
	c.HTML(http.StatusOK, "layout", data)
}

func (h *Handler) handleLogin(c *gin.Context) {
	email := strings.TrimSpace(c.PostForm("email"))
	password := c.PostForm("password")

	token, err := h.auth.Login(c.Request.Context(), email, password)
	if err != nil {
		redirectWithMessage(c, "/dashboard/login", "error", "Credenciais inválidas")
		return
	}

	middleware.SetDashboardCookie(c, token)

	redirectWithMessage(c, "/dashboard", "success", "Bem-vindo!")
}

func (h *Handler) logout(c *gin.Context) {
	middleware.ClearDashboardCookie(c)
	flash := url.Values{}
	flash.Set("success", "Sessão encerrada com sucesso.")
	c.Redirect(http.StatusSeeOther, "/dashboard/login?"+flash.Encode())
}
