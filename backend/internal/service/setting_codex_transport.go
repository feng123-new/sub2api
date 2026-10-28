package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	CodexTransportGo                = "go"
	CodexTransportRust              = "rust"
	defaultCodexRustTransportSocket = "/run/codex-rust/transport.sock"
	codexTransportCacheTTL          = 2 * time.Second
)

type cachedCodexTransportMode struct {
	mode      string
	expiresAt time.Time
}

type CodexTransportStatus struct {
	Mode           string `json:"mode"`
	Available      bool   `json:"available"`
	LegacyCanary   bool   `json:"legacy_canary"`
	Scope          string `json:"scope"`
	WebSocketRoute string `json:"websocket_route"`
}

// GetCodexTransportMode is safe to use on the gateway hot path. An absent DB key
// preserves the original environment-controlled canary; a DB error fails closed
// to Go. The short TTL also picks up changes made by another application replica.
func (s *SettingService) GetCodexTransportMode(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return CodexTransportGo
	}
	if value, ok := s.openAICodexTransportModeCache.Load().(*cachedCodexTransportMode); ok && time.Now().Before(value.expiresAt) {
		return value.mode
	}
	s.openAICodexTransportModeMu.Lock()
	defer s.openAICodexTransportModeMu.Unlock()
	if value, ok := s.openAICodexTransportModeCache.Load().(*cachedCodexTransportMode); ok && time.Now().Before(value.expiresAt) {
		return value.mode
	}
	dbCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAICodexTransportMode)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return CodexTransportGo
	}
	mode := strings.TrimSpace(raw)
	if errors.Is(err, ErrSettingNotFound) {
		mode = ""
	} else if mode != CodexTransportRust && mode != CodexTransportGo {
		mode = CodexTransportGo
	}
	s.openAICodexTransportModeCache.Store(&cachedCodexTransportMode{mode: mode, expiresAt: time.Now().Add(codexTransportCacheTTL)})
	return mode
}

// SetCodexTransportMode takes effect for new requests without a restart.
// Existing SSE streams remain on the transport they started with.
func (s *SettingService) SetCodexTransportMode(ctx context.Context, mode string) error {
	if mode != CodexTransportGo && mode != CodexTransportRust {
		return fmt.Errorf("unsupported Codex transport mode")
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("settings storage unavailable")
	}
	if mode == CodexTransportRust && !s.CodexRustSocketAvailable() {
		return fmt.Errorf("Rust transport sidecar is not reachable")
	}
	s.openAICodexTransportModeMu.Lock()
	defer s.openAICodexTransportModeMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAICodexTransportMode, mode); err != nil {
		return err
	}
	s.openAICodexTransportModeCache.Store(&cachedCodexTransportMode{mode: mode, expiresAt: time.Now().Add(codexTransportCacheTTL)})
	return nil
}

func (s *SettingService) codexRustSocketPath() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	path := strings.TrimSpace(s.cfg.Gateway.OpenAICodexRustTransport.SocketPath)
	if path == "" {
		path = defaultCodexRustTransportSocket
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return ""
	}
	return path
}

func (s *SettingService) CodexRustSocketAvailable() bool {
	path := s.codexRustSocketPath()
	if path == "" {
		return false
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 600 * time.Millisecond}
	resp, err := client.Get("http://codex-rust.internal/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (s *SettingService) GetCodexTransportStatus(ctx context.Context) CodexTransportStatus {
	mode := s.GetCodexTransportMode(ctx)
	legacyCanary := false
	if s != nil && s.cfg != nil {
		cfg := s.cfg.Gateway.OpenAICodexRustTransport
		legacyCanary = mode == "" && cfg.Enabled && strings.TrimSpace(cfg.AccountIDs) != ""
	}
	if mode == "" {
		mode = CodexTransportGo
	}
	return CodexTransportStatus{
		Mode:           mode,
		Available:      s.CodexRustSocketAvailable(),
		LegacyCanary:   legacyCanary,
		Scope:          "openai_oauth_http_sse",
		WebSocketRoute: "go",
	}
}
