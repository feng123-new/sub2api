package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexTransportSettingRepo struct {
	service.SettingRepository
	mode string
}

func (r *codexTransportSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != service.SettingKeyOpenAICodexTransportMode || r.mode == "" {
		return "", service.ErrSettingNotFound
	}
	return r.mode, nil
}

func (r *codexTransportSettingRepo) Set(_ context.Context, key, value string) error {
	if key == service.SettingKeyOpenAICodexTransportMode {
		r.mode = value
	}
	return nil
}

func TestCodexTransportAdminRouteSwitchValidation(t *testing.T) {
	repo := &codexTransportSettingRepo{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexRustTransport.SocketPath = "/nonexistent/sub2api-codex-rust.sock"
	h := &SettingHandler{settingService: service.NewSettingService(repo, cfg)}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/admin/settings/codex-transport", h.GetCodexTransport)
	router.PUT("/admin/settings/codex-transport", h.UpdateCodexTransport)

	request := func(method, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/admin/settings/codex-transport", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		return recorder
	}

	initial := request(http.MethodGet, "")
	require.Equal(t, http.StatusOK, initial.Code)
	require.Contains(t, initial.Body.String(), `"mode":"go"`)
	require.Contains(t, initial.Body.String(), `"available":false`)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPut, `{"mode":"invalid"}`).Code)
	require.Equal(t, http.StatusBadRequest, request(http.MethodPut, `{"mode":"rust"}`).Code, "unreachable sidecar must block enable")
	require.Empty(t, repo.mode)
	require.Equal(t, http.StatusOK, request(http.MethodPut, `{"mode":"go"}`).Code)
	require.Equal(t, service.CodexTransportGo, repo.mode)
}
