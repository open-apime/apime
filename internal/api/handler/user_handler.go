package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/open-apime/apime/internal/api/middleware"
	"github.com/open-apime/apime/internal/pkg/response"
	userSvc "github.com/open-apime/apime/internal/service/user"
)

// UserHandler exposes user management endpoints.
type UserHandler struct {
	service *userSvc.Service
}

// NewUserHandler creates a new user handler.
func NewUserHandler(service *userSvc.Service) *UserHandler {
	return &UserHandler{service: service}
}

// Register registers the protected endpoints.
func (h *UserHandler) Register(r *gin.RouterGroup) {
	admin := r.Group("")
	admin.Use(middleware.RequireAdmin(h.service))

	admin.GET("/users", h.list)
	admin.POST("/users", h.create)
	admin.PATCH("/users/:id/password", h.updatePassword)
	admin.POST("/users/:id/token", h.rotateAPIToken)
	admin.DELETE("/users/:id", h.delete)
}

func (h *UserHandler) list(c *gin.Context) {
	users, _, err := h.service.List(c.Request.Context(), "", 0, 0)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err)
		return
	}

	response.Success(c, http.StatusOK, users)
}

type createUserRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
	Role     string `json:"role"`
}

func (h *UserHandler) create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	user, token, err := h.service.Create(c.Request.Context(), userSvc.CreateInput{
		Email:    req.Email,
		Password: req.Password,
		Role:     req.Role,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	response.Success(c, http.StatusCreated, gin.H{
		"user":     user,
		"apiToken": token,
	})
}

type updatePasswordRequest struct {
	Password string `json:"password" binding:"required,min=8"`
}

func (h *UserHandler) updatePassword(c *gin.Context) {
	var req updatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	if err := h.service.UpdatePassword(c.Request.Context(), c.Param("id"), req.Password); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"message": "senha atualizada"})
}

func (h *UserHandler) delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("id")); err != nil {
		response.Error(c, http.StatusBadRequest, err)
		return
	}
	response.Success(c, http.StatusOK, gin.H{"message": "usuário removido"})
}

func (h *UserHandler) rotateAPIToken(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		response.ErrorWithMessage(c, http.StatusBadRequest, "usuário inválido")
		return
	}

	token, err := h.service.RotateAPIToken(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, userSvc.ErrTokenServiceUnavailable) {
			response.ErrorWithMessage(c, http.StatusServiceUnavailable, "serviço de tokens indisponível")
			return
		}
		response.Error(c, http.StatusBadRequest, err)
		return
	}

	response.Success(c, http.StatusOK, gin.H{
		"token": token,
	})
}
