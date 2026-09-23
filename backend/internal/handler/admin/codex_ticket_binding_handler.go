package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) GetCodexTicketBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	result, err := h.codexTicketGateway.CodexTicketBinding(c.Request.Context(), id, c.Query("model"))
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) ConfirmCodexTicketBinding(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Model     string `json:"model" binding:"required"`
		AttemptID int64  `json:"attempt_id" binding:"required,gt=0"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "model and positive attempt_id are required")
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	result, err := h.codexTicketGateway.ConfirmCodexTicketMaterial(c.Request.Context(), id, input.Model, input.AttemptID)
	if err != nil {
		response.Error(c, 422, err.Error())
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) DeleteCodexTicketBinding(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	if err := h.codexTicketGateway.UnpinCodexTicket(c.Request.Context(), id, c.Query("model")); err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"unlocked": true})
}
