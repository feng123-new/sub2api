package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func testCodexRustConfig(socket, accountIDs string) *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{
		OpenAICodexRustTransport: config.GatewayOpenAICodexRustTransportConfig{
			Enabled: true, SocketPath: socket, AccountIDs: accountIDs,
		},
	}}
}

func testCodexRustRequest(t *testing.T) *http.Request {
	t.Helper()
	ctx := service.WithHTTPUpstreamProfile(context.Background(), service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer fake-token")
	return req
}

func TestCodexRustTransportStrictCanaryRouting(t *testing.T) {
	svc := NewHTTPUpstream(testCodexRustConfig("/tmp/transport.sock", "42,invalid,-3")).(*httpUpstreamService)
	req := testCodexRustRequest(t)
	if !svc.shouldUseCodexRust(req, 42, service.HTTPUpstreamProfileOpenAI) {
		t.Fatal("expected allowlisted Codex account to use Rust")
	}
	if svc.shouldUseCodexRust(req, 43, service.HTTPUpstreamProfileOpenAI) {
		t.Fatal("other accounts must remain on Go")
	}
	if svc.shouldUseCodexRust(req, 42, service.HTTPUpstreamProfileOpenAIHarvest) {
		t.Fatal("ticket harvesting must remain on its dedicated Go transport")
	}
	for _, target := range []string{
		"https://api.openai.com/v1/responses",
		"http://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com.evil.example/backend-api/codex/responses",
		"https://user@chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/other",
	} {
		if codexRustTargetURL(target) {
			t.Fatalf("unsafe target accepted: %q", target)
		}
	}
	if got := NewHTTPUpstream(&config.Config{}).(*httpUpstreamService).codexRust; got != nil {
		t.Fatal("Rust transport must be off by default")
	}
	if got := NewHTTPUpstream(testCodexRustConfig("/tmp/socket", "")).(*httpUpstreamService).codexRust; got != nil {
		t.Fatal("empty account allowlist must route nobody")
	}
}

func TestCodexRustTransportUnixStreaming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket integration requires Unix")
	}
	socket := filepath.Join(t.TempDir(), "transport.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Codex-Rust-Target") != "https://chatgpt.com/backend-api/codex/responses" ||
			r.Header.Get("X-Codex-Rust-Method") != "POST" ||
			r.Header.Get("X-Codex-Rust-Account") != "42" ||
			r.Header.Get("X-Codex-Rust-Proxy") != "http://proxy.example:8080" ||
			r.Header.Get("Authorization") != "Bearer fake-token" {
			t.Errorf("internal metadata was not preserved correctly")
		}
		if got, want := r.Header.Get("X-Codex-Rust-Body-Length"), strconv.Itoa(len(`{"model":"test"}`)); got != want {
			t.Errorf("known JSON body length not preserved: got %q, want %q", got, want)
		}
		if r.Header.Get("X-Codex-Rust-Bogus") != "" {
			t.Errorf("untrusted control header leaked to Rust")
		}
		if r.Header.Get("X-Hop-Secret") != "" || r.Header.Get("Connection") != "" {
			t.Errorf("hop-by-hop request header leaked to Rust")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"model":"test"}` {
			t.Errorf("request streaming failed: %s, %v", body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Codex-Rust-Upstream-Proto", "h2")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	svc := NewHTTPUpstream(testCodexRustConfig(socket, "42")).(*httpUpstreamService)
	req := testCodexRustRequest(t)
	req.Header.Set("X-Codex-Rust-Bogus", "untrusted")
	req.Header.Set("Connection", "X-Hop-Secret")
	req.Header.Set("X-Hop-Secret", "must-not-forward")
	resp, err := svc.Do(req, "http://proxy.example:8080", 42, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data: first\n\ndata: second\n\n" || resp.ProtoMajor != 2 || resp.Header.Get("X-Codex-Rust-Upstream-Proto") != "" {
		t.Fatalf("unexpected response: protocol=%s body=%q", resp.Proto, data)
	}
}

func TestCodexRustTransportClosesStreamingSocketOnCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket integration requires Unix")
	}
	socket := filepath.Join(t.TempDir(), "transport.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	clientDisconnected := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(clientDisconnected)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	svc := NewHTTPUpstream(testCodexRustConfig(socket, "42")).(*httpUpstreamService)
	resp, err := svc.Do(testCodexRustRequest(t), "", 42, 5)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 13)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-clientDisconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the SSE response did not cancel the Unix socket request")
	}
}

func TestCodexRustTransportFailsClosedAfterPossibleSend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket integration requires Unix")
	}
	socket := filepath.Join(t.TempDir(), "transport.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Codex-Rust-Error", "send_failed")
		w.WriteHeader(http.StatusBadGateway)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	svc := NewHTTPUpstream(testCodexRustConfig(socket, "42")).(*httpUpstreamService)
	_, err = svc.Do(testCodexRustRequest(t), "", 42, 5)
	var transportErr *service.PluginTransportError
	if !errors.As(err, &transportErr) || !transportErr.RequestSent {
		t.Fatalf("possible upstream send must be non-replayable, got %v", err)
	}
}
