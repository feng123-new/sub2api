package service

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTransportTogglePersistsAndChecksSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test needs a Unix socket")
	}
	socket := filepath.Join(t.TempDir(), "transport.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexRustTransport.SocketPath = socket
	repo := &codexPolicyMigrationRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, cfg)
	ctx := context.Background()
	require.Empty(t, svc.GetCodexTransportMode(ctx), "missing setting keeps legacy default")
	require.True(t, svc.GetCodexTransportStatus(ctx).Available)
	require.Error(t, svc.SetCodexTransportMode(ctx, "invalid"))
	require.NoError(t, svc.SetCodexTransportMode(ctx, CodexTransportRust))
	require.Equal(t, CodexTransportRust, repo.values[SettingKeyOpenAICodexTransportMode])
	require.Equal(t, CodexTransportRust, svc.GetCodexTransportMode(ctx))
	require.Equal(t, CodexTransportRust, NewSettingService(repo, cfg).GetCodexTransportMode(ctx), "mode survives service recreation")
	require.NoError(t, svc.SetCodexTransportMode(ctx, CodexTransportGo))
	require.Equal(t, CodexTransportGo, svc.GetCodexTransportStatus(ctx).Mode)
	require.NoError(t, server.Close())
	require.False(t, svc.CodexRustSocketAvailable())
	require.ErrorContains(t, svc.SetCodexTransportMode(ctx, CodexTransportRust), "not reachable")
	require.Equal(t, CodexTransportGo, svc.GetCodexTransportMode(ctx), "failed enable does not change the mode")
}

func TestCodexOAuthMarkerRejectsAPIKeys(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	require.False(t, IsOpenAICodexOAuthRequest(req.Context()))
	require.False(t, IsOpenAICodexOAuthRequest(MarkOpenAICodexOAuthRequest(req, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).Context()))
	require.False(t, IsOpenAICodexOAuthRequest(MarkOpenAICodexOAuthRequest(req, &Account{Platform: PlatformGemini, Type: AccountTypeOAuth}).Context()))
	require.True(t, IsOpenAICodexOAuthRequest(MarkOpenAICodexOAuthRequest(req, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}).Context()))
}
