package repository

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func captureCodexTransportLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

func parseCodexTransportLogs(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buffer.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("parse observability event: %v", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestCodexTransportLogRecordsActualRouteProtocolAndDuration(t *testing.T) {
	logs := captureCodexTransportLogs(t)
	ctx := service.WithHTTPUpstreamProfile(context.Background(), service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses?private_key=do-not-log",
		strings.NewReader("private body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer do-not-log")
	if !isCodexTransportObservedRequest(req, service.HTTPUpstreamProfileOpenAI) {
		t.Fatal("expected valid Codex destination to be observed")
	}
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, ProtoMajor: 2, Body: io.NopCloser(strings.NewReader("data: event\n\n"))}
	observeCodexTransport(req, 42, service.CodexTransportRust, time.Now().Add(-12*time.Millisecond), resp, nil)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	records := parseCodexTransportLogs(t, logs)
	if len(records) != 2 {
		t.Fatalf("expected one header and one stream event, got %d: %#v", len(records), records)
	}
	first := records[0]
	for key, want := range map[string]any{
		"msg": "codex_transport_request", "route": "rust", "account_id": float64(42),
		"endpoint": "responses", "method": "POST", "upstream_protocol": "h2",
		"status_code": float64(429), "error_type": "http_429", "ops_system_log_skip": true,
	} {
		if first[key] != want {
			t.Errorf("header event %s = %#v; want %#v", key, first[key], want)
		}
	}
	if first["header_latency_ms"].(float64) < 12 {
		t.Fatalf("header latency missing: %#v", first)
	}
	last := records[1]
	if last["msg"] != "codex_transport_stream_end" || last["finish_reason"] != "eof" ||
		last["route"] != "rust" || last["upstream_protocol"] != "h2" ||
		last["error_type"] != "http_429" || last["ops_system_log_skip"] != true {
		t.Fatalf("unexpected stream event: %#v", last)
	}
	if first["attempt_id"] == "" || first["attempt_id"] != last["attempt_id"] {
		t.Fatalf("events not correlated by attempt_id: %#v", records)
	}
	if last["total_duration_ms"].(float64) < 12 {
		t.Fatalf("full duration missing: %#v", last)
	}
	if strings.Contains(logs.String(), "do-not-log") || strings.Contains(logs.String(), "private body") {
		t.Fatalf("request secret was included in observability logs")
	}
}

func TestCodexTransportStreamReadErrorIsClassified(t *testing.T) {
	logs := captureCodexTransportLogs(t)
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusOK, ProtoMajor: 1, Body: io.NopCloser(&failingCodexReader{})}
	observeCodexTransport(req, 7, service.CodexTransportGo, time.Now(), resp, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	records := parseCodexTransportLogs(t, logs)
	if len(records) != 2 || records[1]["finish_reason"] != "read_error" ||
		records[1]["error_type"] != "stream_read_error" || records[0]["attempt_id"] != records[1]["attempt_id"] {
		t.Fatalf("stream read failure missing from transport logs: %#v", records)
	}
}

type failingCodexReader struct{}

func (*failingCodexReader) Read([]byte) (int, error) { return 0, errors.New("private read error") }

func TestCodexTransportObservationClassifiesErrorsWithoutRawMessages(t *testing.T) {
	tests := []struct {
		name string
		resp *http.Response
		err  error
		want string
	}{
		{"success", &http.Response{StatusCode: 200}, nil, "none"},
		{"upstream_bad_request", &http.Response{StatusCode: 401}, nil, "http_4xx"},
		{"upstream_server_error", &http.Response{StatusCode: 502}, nil, "http_5xx"},
		{"ipc", nil, &service.PluginTransportError{Code: "CODEX_RUST_IPC", Message: "contains-secret"}, "rust_ipc"},
		{"rust_connect", nil, &service.PluginTransportError{Code: "CODEX_RUST_UPSTREAM", Message: "Rust upstream transport failed: connect_failed"}, "rust_connect_failed"},
		{"rust_timeout", nil, &service.PluginTransportError{Code: "CODEX_RUST_UPSTREAM", Message: "Rust upstream transport failed: timeout"}, "rust_timeout"},
		{"cancel", nil, context.Canceled, "canceled"},
		{"deadline", nil, context.DeadlineExceeded, "timeout"},
		{"transport", nil, errors.New("do-not-log"), "transport"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexTransportErrorType(tt.resp, tt.err); got != tt.want {
				t.Fatalf("error type: got %q, want %q", got, tt.want)
			}
		})
	}
	if got := codexTransportProtocol(&http.Response{ProtoMajor: 1}); got != "http1" {
		t.Fatalf("Go HTTP/1 protocol = %s", got)
	}
	if got := codexTransportProtocol(nil); got != "unknown" {
		t.Fatalf("failed request protocol = %s", got)
	}
	if isCodexTransportObservedRequest(&http.Request{}, service.HTTPUpstreamProfileOpenAI) {
		t.Fatal("request without URL must not be observed")
	}
	unrelated, err := http.NewRequest(http.MethodGet, "https://example.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if isCodexTransportObservedRequest(unrelated, service.HTTPUpstreamProfileOpenAI) {
		t.Fatal("unrelated upstream must not be observed")
	}
}

func TestCodexTransportGoRouteLoggedForFailedRequest(t *testing.T) {
	logs := captureCodexTransportLogs(t)
	svc := NewHTTPUpstream(&config.Config{}).(*httpUpstreamService)
	req := testCodexRustRequest(t)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	resp, err := svc.Do(req, "", 42, 5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled outbound attempt, got response=%v error=%v", resp, err)
	}
	records := parseCodexTransportLogs(t, logs)
	if len(records) != 1 || records[0]["route"] != "go" ||
		records[0]["error_type"] != "canceled" ||
		records[0]["upstream_protocol"] != "unknown" ||
		records[0]["status_code"] != float64(0) {
		t.Fatalf("Go route observability missing: %#v", records)
	}
}
