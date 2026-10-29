package repository

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// Observe only fixed-destination Codex requests. Other providers keep their
// existing logging volume and no URL, credentials, proxy or body is recorded.
func isCodexTransportObservedRequest(req *http.Request, profile service.HTTPUpstreamProfile) bool {
	return req != nil && req.URL != nil &&
		(profile == service.HTTPUpstreamProfileOpenAI || profile == service.HTTPUpstreamProfileOpenAIHarvest) &&
		codexRustTargetURL(req.URL.String())
}

func codexTransportEndpoint(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "other"
	}
	switch req.URL.Path {
	case "/backend-api/codex/models":
		return "models"
	case "/backend-api/codex/responses":
		return "responses"
	case "/backend-api/codex/responses/compact":
		return "compact"
	default:
		return "other"
	}
}

func codexTransportProtocol(resp *http.Response) string {
	if resp != nil {
		switch resp.ProtoMajor {
		case 1:
			return "http1"
		case 2:
			return "h2"
		}
	}
	return "unknown"
}

// Error types are bounded labels; never log the original error text, which
// could contain URLs, proxy credentials or other sensitive request metadata.
func codexTransportErrorType(resp *http.Response, err error) string {
	if err != nil {
		var transportErr *service.PluginTransportError
		if errors.As(err, &transportErr) {
			switch transportErr.Code {
			case "CODEX_RUST_IPC":
				return "rust_ipc"
			case "CODEX_RUST_UPSTREAM":
				const prefix = "Rust upstream transport failed: "
				if strings.HasPrefix(transportErr.Message, prefix) {
					return "rust_" + safeCodexRustErrorCode(strings.TrimPrefix(transportErr.Message, prefix))
				}
				return "rust_upstream"
			default:
				return "plugin_transport"
			}
		}
		if errors.Is(err, context.Canceled) {
			return "canceled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return "timeout"
		}
		return "transport"
	}
	if resp == nil {
		return "missing_response"
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return "http_429"
	case resp.StatusCode >= http.StatusInternalServerError:
		return "http_5xx"
	case resp.StatusCode >= http.StatusBadRequest:
		return "http_4xx"
	default:
		return "none"
	}
}

func codexTransportElapsedMS(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000
}

// Called exactly once per Codex HTTP attempt when headers arrive or a
// transport error occurs. Streaming responses also get a completion event on
// EOF/Close, so header latency is not confused with full SSE stream duration.
func observeCodexTransport(req *http.Request, accountID int64, route string, started time.Time, resp *http.Response, err error) {
	attemptID := uuid.NewString()
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	endpoint := codexTransportEndpoint(req)
	protocol := codexTransportProtocol(resp)
	slog.Info("codex_transport_request",
		"component", "repository.http_upstream",
		logger.OpsSystemLogSkipField, true,
		"attempt_id", attemptID,
		"route", route,
		"account_id", accountID,
		"method", req.Method,
		"endpoint", endpoint,
		"status_code", status,
		"error_type", codexTransportErrorType(resp, err),
		"upstream_protocol", protocol,
		"header_latency_ms", codexTransportElapsedMS(started),
	)
	if err != nil || resp == nil || resp.Body == nil {
		return
	}
	resp.Body = &codexTransportObservedBody{
		ReadCloser: resp.Body,
		finish: func(reason string) {
			endErrorType := codexTransportErrorType(resp, nil)
			if reason == "read_error" || reason == "close_error" {
				endErrorType = "stream_" + reason
			}
			slog.Info("codex_transport_stream_end",
				"component", "repository.http_upstream",
				logger.OpsSystemLogSkipField, true,
				"attempt_id", attemptID,
				"route", route,
				"account_id", accountID,
				"method", req.Method,
				"endpoint", endpoint,
				"status_code", status,
				"error_type", endErrorType,
				"upstream_protocol", protocol,
				"finish_reason", reason,
				"total_duration_ms", codexTransportElapsedMS(started),
			)
		},
	}
}

type codexTransportObservedBody struct {
	io.ReadCloser
	once   sync.Once
	finish func(string)
}

func (b *codexTransportObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		reason := "read_error"
		if errors.Is(err, io.EOF) {
			reason = "eof"
		}
		b.once.Do(func() { b.finish(reason) })
	}
	return n, err
}

func (b *codexTransportObservedBody) Close() error {
	err := b.ReadCloser.Close()
	reason := "closed"
	if err != nil {
		reason = "close_error"
	}
	b.once.Do(func() { b.finish(reason) })
	return err
}
