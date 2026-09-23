package admin

import (
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *AccountHandler) GetCodexTicketMaterial(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	attempt, err := strconv.ParseInt(c.Query("attempt_id"), 10, 64)
	if err != nil || attempt <= 0 {
		response.BadRequest(c, "positive attempt_id is required")
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	result, err := h.codexTicketGateway.CodexTicketMaterial(c.Request.Context(), id, c.Query("model"), attempt)
	if errors.Is(err, service.ErrCodexTicketMaterialMissing) {
		response.Error(c, 404, "此历史记录未保存票据正文，无法恢复")
		return
	}
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) ValidateCodexTicketMaterial(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Model     string `json:"model" binding:"required"`
		AttemptID int64  `json:"attempt_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.AttemptID < 0 {
		response.BadRequest(c, "valid model and attempt_id are required")
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	result, err := h.codexTicketGateway.ValidateCodexTicketMaterial(c.Request.Context(), id, input.Model, input.AttemptID)
	if errors.Is(err, service.ErrCodexTicketMaterialMissing) {
		response.Error(c, 404, "没有可用的票据正文")
		return
	}
	if errors.Is(err, service.ErrCodexTicketMaterialExpired) {
		response.Error(c, 422, "票据已过期，请获取新票据后检测")
		return
	}
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, result)
}
