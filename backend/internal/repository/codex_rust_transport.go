package repository

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultCodexRustSocket = "/run/codex-rust/transport.sock"

// codexRustTransport forwards selected requests over a private Unix socket.
// TLS, HTTP/2 and the upstream proxy connection are established by Rust.
// This transport intentionally does not retry POST requests or follow redirects.
type codexRustTransport struct {
	client     *http.Client
	accountIDs map[int64]struct{}
}

func newCodexRustTransport(cfg *config.Config, allowAdminToggle bool) *codexRustTransport {
	if cfg == nil || (!cfg.Gateway.OpenAICodexRustTransport.Enabled && !allowAdminToggle) {
		return nil
	}
	settings := cfg.Gateway.OpenAICodexRustTransport
	accounts := make(map[int64]struct{})
	for _, part := range strings.Split(settings.AccountIDs, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 {
			accounts[id] = struct{}{}
		}
	}
	// An explicit account allowlist is mandatory. Enabling the flag alone
	// must never redirect the entire production workload.
	if len(accounts) == 0 && !allowAdminToggle {
		return nil
	}
	socket := strings.TrimSpace(settings.SocketPath)
	if socket == "" {
		socket = defaultCodexRustSocket
	}
	if !strings.HasPrefix(socket, "/") || strings.Contains(socket, "\x00") {
		return nil
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	return &codexRustTransport{
		client:     &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		accountIDs: accounts,
	}
}

func (s *httpUpstreamService) shouldUseCodexRust(req *http.Request, accountID int64, profile service.HTTPUpstreamProfile) bool {
	if s == nil || s.codexRust == nil || req == nil || req.URL == nil || profile != service.HTTPUpstreamProfileOpenAI {
		return false
	}
	if !codexRustTargetURL(req.URL.String()) {
		return false
	}
	if s.codexRustSettings != nil {
		switch s.codexRustSettings.GetCodexTransportMode(req.Context()) {
		case service.CodexTransportGo:
			return false
		case service.CodexTransportRust:
			return accountID > 0 && service.IsOpenAICodexOAuthRequest(req.Context())
		}
	}
	if _, ok := s.codexRust.accountIDs[accountID]; !ok {
		return false
	}
	return true
}

// UseCodexRustFor lets the OAuth service skip its optional outbound plugin for
// explicitly opted-in accounts. Do still enforces the same routing decision.
func (s *httpUpstreamService) UseCodexRustFor(req *http.Request, accountID int64) bool {
	if req == nil {
		return false
	}
	return s.shouldUseCodexRust(req, accountID, service.HTTPUpstreamProfileFromContext(req.Context()))
}

func (s *httpUpstreamService) doCodexRust(req *http.Request, proxyURL string, accountID int64) (*http.Response, error) {
	if req == nil || req.URL == nil || s.codexRust == nil {
		return nil, fmt.Errorf("codex rust transport is unavailable")
	}
	ctx, cancel := context.WithCancel(req.Context())
	localReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://codex-rust.internal/forward", req.Body)
	if err != nil {
		cancel()
		return nil, err
	}
	localReq.ContentLength = req.ContentLength
	localReq.Header = req.Header.Clone()
	// RFC 9110: Connection may nominate additional hop-by-hop header names.
	for _, connection := range localReq.Header.Values("Connection") {
		for _, token := range strings.Split(connection, ",") {
			localReq.Header.Del(strings.TrimSpace(token))
		}
	}
	for key := range localReq.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-codex-rust-") || isCodexRustHopHeader(key) {
			localReq.Header.Del(key)
		}
	}
	localReq.Header.Set("X-Codex-Rust-Target", req.URL.String())
	localReq.Header.Set("X-Codex-Rust-Method", req.Method)
	localReq.Header.Set("X-Codex-Rust-Account", strconv.FormatInt(accountID, 10))
	if req.Method == http.MethodPost && req.ContentLength >= 0 {
		localReq.Header.Set("X-Codex-Rust-Body-Length", strconv.FormatInt(req.ContentLength, 10))
	}
	if proxyURL != "" {
		localReq.Header.Set("X-Codex-Rust-Proxy", proxyURL)
	}
	resp, err := s.codexRust.client.Do(localReq)
	if err != nil {
		cancel()
		// Conservative RequestSent=true prevents replay if the helper failed
		// after forwarding the upstream request but before returning headers.
		return nil, &service.PluginTransportError{Code: "CODEX_RUST_IPC", Message: "Rust transport unavailable", RequestSent: true}
	}
	if code := resp.Header.Get("X-Codex-Rust-Error"); code != "" {
		_ = resp.Body.Close()
		cancel()
		return nil, &service.PluginTransportError{Code: "CODEX_RUST_UPSTREAM", Message: "Rust upstream transport failed: " + safeCodexRustErrorCode(code), RequestSent: true}
	}
	if upstreamProto := resp.Header.Get("X-Codex-Rust-Upstream-Proto"); upstreamProto != "" {
		switch upstreamProto {
		case "h2":
			resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/2.0", 2, 0
		case "http1":
			resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
		}
	}
	resp.Header.Del("X-Codex-Rust-Upstream-Proto")
	decompressResponseBody(resp)
	resp.Request = req
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func isCodexRustHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "host", "connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade", "te", "trailer", "proxy-authorization":
		return true
	default:
		return false
	}
}

func safeCodexRustErrorCode(code string) string {
	if len(code) > 64 {
		code = code[:64]
	}
	for _, r := range code {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return "unknown"
		}
	}
	return code
}

// Validate the destination in the Go gateway before handing the URL to Rust.
// Rust repeats the fixed-host check to protect the socket boundary.
func codexRustTargetURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "chatgpt.com") &&
		(u.Port() == "" || u.Port() == "443") && strings.HasPrefix(u.EscapedPath(), "/backend-api/codex/") &&
		u.User == nil && u.Fragment == ""
}
