package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetCodexTransport reports the persisted HTTP/SSE transport and sidecar
// readiness. The WebSocket implementation remains in the Go gateway.
func (h *SettingHandler) GetCodexTransport(c *gin.Context) {
	response.Success(c, h.settingService.GetCodexTransportStatus(c.Request.Context()))
}

func (h *SettingHandler) UpdateCodexTransport(c *gin.Context) {
	var req struct {
		Mode string `json:"mode" binding:"required,oneof=go rust"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "mode must be go or rust")
		return
	}
	if err := h.settingService.SetCodexTransportMode(c.Request.Context(), req.Mode); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, h.settingService.GetCodexTransportStatus(c.Request.Context()))
}
