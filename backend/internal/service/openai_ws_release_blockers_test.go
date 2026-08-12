package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIWSContextPreflightObservation struct {
	body       []byte
	finalModel string
}

func newOpenAIWSPooledContextTestService(
	t *testing.T,
	mode string,
	events [][]byte,
	estimate openAIContextPreflightEstimator,
) (*OpenAIGatewayService, *openAIWSCaptureConn, *Account) {
	t.Helper()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{events: events}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)
	t.Cleanup(pool.Close)

	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
	svc.cfg = cfg
	svc.httpUpstream = &httpUpstreamRecorder{}
	svc.cache = &stubGatewayCache{}
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	svc.toolCorrector = NewCodexToolCorrector()
	svc.openaiWSPool = pool
	svc.contextPreflight = &openAIContextPreflight{
		mode:           mode,
		thresholdBPS:   9000,
		billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
		estimate:       estimate,
	}

	account := &Account{
		ID:          9701,
		Name:        "openai-ws-context-preflight",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
			"model_mapping": map[string]any{
				"client-model": "gpt-5.5",
			},
		},
		Extra: map[string]any{"responses_websockets_v2_enabled": true},
	}
	return svc, captureConn, account
}

func startOpenAIWSReleaseBlockerServer(
	t *testing.T,
	svc *OpenAIGatewayService,
	account *Account,
	hooks *OpenAIWSIngressHooks,
) (string, <-chan error) {
	t.Helper()

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Request = r.Clone(r.Context())
		proxyErr := svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
		var closeErr *OpenAIWSClientCloseError
		if errors.As(proxyErr, &closeErr) {
			reason := closeErr.Reason()
			if len(reason) > 120 {
				reason = reason[:120]
			}
			_ = conn.Close(closeErr.StatusCode(), reason)
		}
		serverErrCh <- proxyErr
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), serverErrCh
}

func dialOpenAIWSReleaseBlockerClient(t *testing.T, wsURL string) *coderws.Conn {
	t.Helper()
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	conn, _, err := coderws.Dial(dialCtx, wsURL, nil)
	cancelDial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func writeOpenAIWSReleaseBlockerFrame(t *testing.T, conn *coderws.Conn, msgType coderws.MessageType, payload string) {
	t.Helper()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err := conn.Write(writeCtx, msgType, []byte(payload))
	cancelWrite()
	require.NoError(t, err)
}

func readOpenAIWSReleaseBlockerFrame(conn *coderws.Conn) (coderws.MessageType, []byte, error) {
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRead()
	return conn.Read(readCtx)
}

func openAIWSCaptureWriteCount(conn *openAIWSCaptureConn) int {
	if conn == nil {
		return 0
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return len(conn.writes)
}

func TestOpenAIPooledWSContextPreflight_FirstTurnUsesFinalPayloadAndThresholdMode(t *testing.T) {
	testCases := []struct {
		name        string
		mode        string
		estimate    int
		wantAllowed bool
	}{
		{name: "enforce below threshold", mode: "enforce", estimate: 89, wantAllowed: true},
		{name: "shadow above threshold", mode: "shadow", estimate: 90, wantAllowed: true},
		{name: "enforce at threshold", mode: "enforce", estimate: 90, wantAllowed: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			observedCh := make(chan openAIWSContextPreflightObservation, 1)
			svc, upstreamConn, account := newOpenAIWSPooledContextTestService(
				t,
				testCase.mode,
				[][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_context_first","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)},
				func(body []byte, _ openAIContextPreflightEndpoint, finalModel string) (int, bool, openAIContextPreflightSkipReason) {
					observedCh <- openAIWSContextPreflightObservation{body: append([]byte(nil), body...), finalModel: finalModel}
					return testCase.estimate, true, ""
				},
			)
			wsURL, serverErrCh := startOpenAIWSReleaseBlockerServer(t, svc, account, nil)
			clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

			writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","service_tier":"priority","input":"first turn"}`)
			_, payload, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
			if testCase.wantAllowed {
				require.NoError(t, readErr)
				require.Equal(t, "resp_context_first", gjson.GetBytes(payload, "response.id").String())
				require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
			} else {
				if readErr == nil {
					_ = clientConn.Close(coderws.StatusNormalClosure, "unexpected upstream response")
				}
				var closeErr coderws.CloseError
				require.ErrorAs(t, readErr, &closeErr)
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
				require.Contains(t, closeErr.Reason, "gateway estimate")
			}

			select {
			case observation := <-observedCh:
				require.Equal(t, "gpt-5.5", observation.finalModel)
				require.Equal(t, "gpt-5.5", gjson.GetBytes(observation.body, "model").String())
				require.False(t, gjson.GetBytes(observation.body, "service_tier").Exists(), "preflight must run after fast-policy filtering")
			case <-time.After(time.Second):
				t.Fatal("context preflight did not inspect the first pooled websocket turn")
			}

			select {
			case proxyErr := <-serverErrCh:
				if testCase.wantAllowed {
					require.NoError(t, proxyErr)
				} else {
					var closeErr *OpenAIWSClientCloseError
					require.ErrorAs(t, proxyErr, &closeErr)
					require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for pooled websocket proxy exit")
			}

			if testCase.wantAllowed {
				require.Equal(t, 1, openAIWSCaptureWriteCount(upstreamConn))
			} else {
				require.Zero(t, openAIWSCaptureWriteCount(upstreamConn), "enforced context rejection must happen before an upstream write")
			}
		})
	}
}

func TestOpenAIPooledWSContextPreflight_FollowupOmittedModelUsesFinalReplayPayload(t *testing.T) {
	observedCh := make(chan openAIWSContextPreflightObservation, 2)
	svc, upstreamConn, account := newOpenAIWSPooledContextTestService(
		t,
		"enforce",
		[][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_context_turn_1","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_context_turn_2","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
		func(body []byte, _ openAIContextPreflightEndpoint, finalModel string) (int, bool, openAIContextPreflightSkipReason) {
			observedCh <- openAIWSContextPreflightObservation{body: append([]byte(nil), body...), finalModel: finalModel}
			if gjson.GetBytes(body, "input.#").Int() == 2 {
				return 90, true, ""
			}
			return 1, true, ""
		},
	)
	wsURL, serverErrCh := startOpenAIWSReleaseBlockerServer(t, svc, account, nil)
	clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","store":false,"service_tier":"priority","metadata":{"turn":1},"input":"small"}`)
	_, firstEvent, err := readOpenAIWSReleaseBlockerFrame(clientConn)
	require.NoError(t, err)
	require.Equal(t, "resp_context_turn_1", gjson.GetBytes(firstEvent, "response.id").String())

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","store":false,"service_tier":"priority","metadata":{"turn":2},"previous_response_id":"resp_context_turn_1","input":"large"}`)
	_, unexpectedPayload, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
	if readErr == nil {
		_ = clientConn.Close(coderws.StatusNormalClosure, "unexpected upstream response")
		t.Fatalf("follow-up context rejection reached upstream: %s", string(unexpectedPayload))
	}
	var clientClose coderws.CloseError
	require.ErrorAs(t, readErr, &clientClose)
	require.Equal(t, coderws.StatusPolicyViolation, clientClose.Code)
	require.Contains(t, clientClose.Reason, "gateway estimate")

	firstObservation := <-observedCh
	secondObservation := <-observedCh
	require.Equal(t, "gpt-5.5", firstObservation.finalModel)
	require.Equal(t, "gpt-5.5", secondObservation.finalModel)
	require.Equal(t, "gpt-5.5", gjson.GetBytes(secondObservation.body, "model").String(), "omitted follow-up model must be restored and mapped before preflight")
	require.False(t, gjson.GetBytes(secondObservation.body, "service_tier").Exists(), "preflight must see the post-policy payload")
	require.False(t, gjson.GetBytes(secondObservation.body, "previous_response_id").Exists(), "strict replay normalization must finish before preflight")
	require.Equal(t, int64(2), gjson.GetBytes(secondObservation.body, "input.#").Int())
	require.Equal(t, "small", gjson.GetBytes(secondObservation.body, "input.0").String())
	require.Equal(t, "large", gjson.GetBytes(secondObservation.body, "input.1").String())

	select {
	case proxyErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, proxyErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pooled websocket follow-up rejection")
	}
	require.Equal(t, 1, openAIWSCaptureWriteCount(upstreamConn), "rejected follow-up must not be written upstream")
}

type openAIWSReleaseTestFrame struct {
	msgType coderws.MessageType
	payload []byte
}

type openAIWSReleaseGatedConn struct {
	mu          sync.Mutex
	events      []openAIWSReleaseTestFrame
	nextEvent   int
	writes      []openAIWSReleaseTestFrame
	writeSignal chan struct{}
	terminalErr error
	closed      bool
}

func newOpenAIWSReleaseGatedConn(events []openAIWSReleaseTestFrame) *openAIWSReleaseGatedConn {
	return &openAIWSReleaseGatedConn{
		events:      events,
		writeSignal: make(chan struct{}, len(events)+4),
	}
}

func (c *openAIWSReleaseGatedConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

func (c *openAIWSReleaseGatedConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

func (c *openAIWSReleaseGatedConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	if c.nextEvent >= len(c.events) {
		terminalErr := c.terminalErr
		c.terminalErr = nil
		c.mu.Unlock()
		if terminalErr != nil {
			return coderws.MessageText, nil, terminalErr
		}
		return coderws.MessageText, nil, io.EOF
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case <-c.writeSignal:
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nextEvent >= len(c.events) {
		return coderws.MessageText, nil, io.EOF
	}
	event := c.events[c.nextEvent]
	c.nextEvent++
	return event.msgType, append([]byte(nil), event.payload...), nil
}

func (c *openAIWSReleaseGatedConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errOpenAIWSConnClosed
	}
	c.writes = append(c.writes, openAIWSReleaseTestFrame{msgType: msgType, payload: append([]byte(nil), payload...)})
	c.mu.Unlock()
	select {
	case c.writeSignal <- struct{}{}:
	default:
	}
	return nil
}

func (c *openAIWSReleaseGatedConn) Ping(context.Context) error {
	return nil
}

func (c *openAIWSReleaseGatedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *openAIWSReleaseGatedConn) Writes() []openAIWSReleaseTestFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	writes := make([]openAIWSReleaseTestFrame, len(c.writes))
	copy(writes, c.writes)
	return writes
}

type openAIWSReleaseDialer struct {
	mu        sync.Mutex
	conn      openAIWSClientConn
	dialCount int
}

func (d *openAIWSReleaseDialer) Dial(context.Context, string, http.Header, string) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	d.dialCount++
	d.mu.Unlock()
	return d.conn, http.StatusSwitchingProtocols, http.Header{"X-Request-ID": []string{"req_release_blocker"}}, nil
}

func (d *openAIWSReleaseDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func newOpenAIWSPassthroughContextTestService(
	t *testing.T,
	mode string,
	events []openAIWSReleaseTestFrame,
	estimate openAIContextPreflightEstimator,
) (*OpenAIGatewayService, *openAIWSReleaseGatedConn, *openAIWSReleaseDialer, *Account) {
	t.Helper()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := newOpenAIWSReleaseGatedConn(events)
	dialer := &openAIWSReleaseDialer{conn: upstreamConn}
	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
	svc.cfg = cfg
	svc.httpUpstream = &httpUpstreamRecorder{}
	svc.cache = &stubGatewayCache{}
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	svc.toolCorrector = NewCodexToolCorrector()
	svc.openaiWSPassthroughDialer = dialer
	if mode != "" {
		svc.contextPreflight = &openAIContextPreflight{
			mode:           mode,
			thresholdBPS:   9000,
			billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
			estimate:       estimate,
		}
	}

	account := &Account{
		ID:          9702,
		Name:        "openai-ws-passthrough-release-blockers",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
			"model_mapping": map[string]any{
				"client-model": "gpt-5.5",
			},
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		},
	}
	return svc, upstreamConn, dialer, account
}

type openAIWSReleaseServerResult struct {
	proxyErr  error
	ginWrote  bool
	ginStatus int
}

func startOpenAIWSPassthroughReleaseBlockerServer(
	t *testing.T,
	svc *OpenAIGatewayService,
	account *Account,
	hooks *OpenAIWSIngressHooks,
) (string, <-chan openAIWSReleaseServerResult) {
	t.Helper()

	resultCh := make(chan openAIWSReleaseServerResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			resultCh <- openAIWSReleaseServerResult{proxyErr: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			resultCh <- openAIWSReleaseServerResult{proxyErr: err}
			return
		}

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		ginCtx.Request = r.Clone(r.Context())
		groupID := int64(9702)
		ginCtx.Set("api_key", &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, AllowImageGeneration: true}})
		SetOpenAIWSClientFirstMessageType(ginCtx, msgType)
		proxyErr := svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
		var closeErr *OpenAIWSClientCloseError
		if errors.As(proxyErr, &closeErr) {
			reason := closeErr.Reason()
			if len(reason) > 120 {
				reason = reason[:120]
			}
			_ = conn.Close(closeErr.StatusCode(), reason)
		}
		resultCh <- openAIWSReleaseServerResult{
			proxyErr:  proxyErr,
			ginWrote:  ginCtx.Writer.Written(),
			ginStatus: ginCtx.Writer.Status(),
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), resultCh
}

func TestOpenAIPassthroughWSContextPreflight_FirstTurnUsesMappedNormalizedPayload(t *testing.T) {
	testCases := []struct {
		name        string
		mode        string
		estimate    int
		wantAllowed bool
	}{
		{name: "enforce below threshold", mode: "enforce", estimate: 89, wantAllowed: true},
		{name: "shadow above threshold", mode: "shadow", estimate: 90, wantAllowed: true},
		{name: "enforce at threshold", mode: "enforce", estimate: 90, wantAllowed: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			observedCh := make(chan openAIWSContextPreflightObservation, 1)
			svc, upstreamConn, dialer, account := newOpenAIWSPassthroughContextTestService(
				t,
				testCase.mode,
				[]openAIWSReleaseTestFrame{{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_passthrough_context","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)}},
				func(body []byte, _ openAIContextPreflightEndpoint, finalModel string) (int, bool, openAIContextPreflightSkipReason) {
					observedCh <- openAIWSContextPreflightObservation{body: append([]byte(nil), body...), finalModel: finalModel}
					return testCase.estimate, true, ""
				},
			)
			wsURL, serverResultCh := startOpenAIWSPassthroughReleaseBlockerServer(t, svc, account, nil)
			clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

			writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","service_tier":"priority","input":"first passthrough turn"}`)
			_, payload, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
			if testCase.wantAllowed {
				require.NoError(t, readErr)
				require.Equal(t, "resp_passthrough_context", gjson.GetBytes(payload, "response.id").String())
				_ = clientConn.Close(coderws.StatusNormalClosure, "done")
			} else {
				if readErr == nil {
					_ = clientConn.Close(coderws.StatusNormalClosure, "unexpected upstream response")
				}
				var closeErr coderws.CloseError
				require.ErrorAs(t, readErr, &closeErr)
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
				require.Contains(t, closeErr.Reason, "gateway estimate")
			}

			select {
			case observation := <-observedCh:
				require.Equal(t, "gpt-5.5", observation.finalModel)
				require.Equal(t, "gpt-5.5", gjson.GetBytes(observation.body, "model").String())
				require.False(t, gjson.GetBytes(observation.body, "service_tier").Exists())
			case <-time.After(time.Second):
				t.Fatal("context preflight did not inspect the first passthrough turn")
			}

			select {
			case serverResult := <-serverResultCh:
				require.False(t, serverResult.ginWrote, "websocket context rejection must not write an HTTP response after upgrade")
				if testCase.wantAllowed {
					require.NoError(t, serverResult.proxyErr)
				} else {
					var closeErr *OpenAIWSClientCloseError
					require.ErrorAs(t, serverResult.proxyErr, &closeErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for passthrough websocket proxy exit")
			}

			if testCase.wantAllowed {
				require.Equal(t, 1, dialer.DialCount())
				require.Len(t, upstreamConn.Writes(), 1)
			} else {
				require.Zero(t, dialer.DialCount(), "enforced first-turn rejection must happen before upstream dial/write")
				require.Empty(t, upstreamConn.Writes())
			}
		})
	}
}

func TestOpenAIPassthroughWSContextPreflight_FollowupOmittedModelRejectedBeforeWrite(t *testing.T) {
	observedCh := make(chan openAIWSContextPreflightObservation, 2)
	svc, upstreamConn, _, account := newOpenAIWSPassthroughContextTestService(
		t,
		"enforce",
		[]openAIWSReleaseTestFrame{
			{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_passthrough_context_1","usage":{"input_tokens":1,"output_tokens":1}}}`)},
			{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_passthrough_context_2","usage":{"input_tokens":1,"output_tokens":1}}}`)},
		},
		func(body []byte, _ openAIContextPreflightEndpoint, finalModel string) (int, bool, openAIContextPreflightSkipReason) {
			observedCh <- openAIWSContextPreflightObservation{body: append([]byte(nil), body...), finalModel: finalModel}
			if gjson.GetBytes(body, "input").String() == "large" {
				return 90, true, ""
			}
			return 1, true, ""
		},
	)
	wsURL, serverResultCh := startOpenAIWSPassthroughReleaseBlockerServer(t, svc, account, nil)
	clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","service_tier":"priority","input":"small"}`)
	_, firstEvent, err := readOpenAIWSReleaseBlockerFrame(clientConn)
	require.NoError(t, err)
	require.Equal(t, "resp_passthrough_context_1", gjson.GetBytes(firstEvent, "response.id").String())

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","service_tier":"priority","input":"large"}`)
	_, unexpectedPayload, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
	if readErr == nil {
		_ = clientConn.Close(coderws.StatusNormalClosure, "unexpected upstream response")
		t.Fatalf("follow-up passthrough context rejection reached upstream: %s", string(unexpectedPayload))
	}
	var clientClose coderws.CloseError
	require.ErrorAs(t, readErr, &clientClose)
	require.Equal(t, coderws.StatusPolicyViolation, clientClose.Code)

	firstObservation := <-observedCh
	secondObservation := <-observedCh
	require.Equal(t, "gpt-5.5", firstObservation.finalModel)
	require.Equal(t, "gpt-5.5", secondObservation.finalModel)
	require.Equal(t, "gpt-5.5", gjson.GetBytes(secondObservation.body, "model").String())
	require.False(t, gjson.GetBytes(secondObservation.body, "service_tier").Exists())

	select {
	case serverResult := <-serverResultCh:
		require.False(t, serverResult.ginWrote)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverResult.proxyErr, &closeErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for passthrough follow-up context rejection")
	}
	require.Len(t, upstreamConn.Writes(), 1, "rejected follow-up must not be written upstream")
}

type openAIWSReleaseSingleFrameConn struct {
	frame openAIWSReleaseTestFrame
	read  bool
}

func (c *openAIWSReleaseSingleFrameConn) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	if c.read {
		return coderws.MessageText, nil, errors.New("client frame stream stopped")
	}
	c.read = true
	return c.frame.msgType, append([]byte(nil), c.frame.payload...), nil
}

func (c *openAIWSReleaseSingleFrameConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return nil
}

func (c *openAIWSReleaseSingleFrameConn) Close() error {
	return nil
}

type openAIWSReleaseWriteOnlyFrameConn struct {
	mu     sync.Mutex
	writes []openAIWSReleaseTestFrame
}

func (c *openAIWSReleaseWriteOnlyFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	<-ctx.Done()
	return coderws.MessageText, nil, ctx.Err()
}

func (c *openAIWSReleaseWriteOnlyFrameConn) WriteFrame(_ context.Context, msgType coderws.MessageType, payload []byte) error {
	c.mu.Lock()
	c.writes = append(c.writes, openAIWSReleaseTestFrame{msgType: msgType, payload: append([]byte(nil), payload...)})
	c.mu.Unlock()
	return nil
}

func (c *openAIWSReleaseWriteOnlyFrameConn) Close() error {
	return nil
}

func (c *openAIWSReleaseWriteOnlyFrameConn) WriteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

func TestOpenAIPassthroughWSBinaryApplicationFramesRejectedBeforeUpstreamWrite(t *testing.T) {
	testCases := []struct {
		name    string
		payload string
	}{
		{name: "blocked model attempt", payload: `{"type":"response.create","model":"blocked-model","service_tier":"priority"}`},
		{name: "image attempt", payload: `{"type":"response.create","model":"gpt-5.5","tools":[{"type":"image_generation"}]}`},
		{name: "duplicate payload attempt", payload: `{"type":"response.create","model":"gpt-5.5","model":"blocked-model"}`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			inner := &openAIWSReleaseSingleFrameConn{frame: openAIWSReleaseTestFrame{msgType: coderws.MessageBinary, payload: []byte(testCase.payload)}}
			filterCalled := false
			clientConn := &openAIWSPolicyEnforcingFrameConn{
				inner: inner,
				filter: func(msgType coderws.MessageType, payload []byte) ([]byte, *OpenAIFastBlockedError, error) {
					filterCalled = true
					if msgType != coderws.MessageText {
						return payload, nil, nil
					}
					return payload, nil, nil
				},
			}
			upstreamConn := &openAIWSReleaseWriteOnlyFrameConn{}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			_, relayExit := openaiwsv2.RunEntry(openaiwsv2.EntryInput{
				Ctx:                ctx,
				ClientConn:         clientConn,
				UpstreamConn:       upstreamConn,
				FirstClientMessage: []byte(`{"type":"response.create","model":"gpt-5.5"}`),
				Options: openaiwsv2.RelayOptions{
					FirstMessageSent: true,
				},
			})

			require.NotNil(t, relayExit)
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, relayExit.Err, &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
			require.Contains(t, closeErr.Reason(), "binary")
			require.False(t, filterCalled, "binary application frames must be rejected before JSON/model/image policy evaluation")
			require.Zero(t, upstreamConn.WriteCount(), "binary application frame must never reach upstream")
		})
	}
}

func TestOpenAIPassthroughWSBinaryFirstFrameRejectedBeforeDial(t *testing.T) {
	svc, upstreamConn, dialer, account := newOpenAIWSPassthroughContextTestService(
		t,
		"",
		[]openAIWSReleaseTestFrame{{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_binary_first","usage":{"input_tokens":1,"output_tokens":1}}}`)}},
		nil,
	)
	wsURL, serverResultCh := startOpenAIWSPassthroughReleaseBlockerServer(t, svc, account, nil)
	clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageBinary, `{"type":"response.create","model":"client-model","input":"binary"}`)
	_, unexpectedPayload, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
	if readErr == nil {
		_ = clientConn.Close(coderws.StatusNormalClosure, "unexpected upstream response")
		t.Fatalf("binary first frame reached upstream: %s", string(unexpectedPayload))
	}
	var clientClose coderws.CloseError
	require.ErrorAs(t, readErr, &clientClose)
	require.Equal(t, coderws.StatusPolicyViolation, clientClose.Code)
	require.Contains(t, clientClose.Reason, "binary")

	select {
	case serverResult := <-serverResultCh:
		require.False(t, serverResult.ginWrote)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverResult.proxyErr, &closeErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for binary first-frame rejection")
	}
	require.Zero(t, dialer.DialCount())
	require.Empty(t, upstreamConn.Writes())
}

func TestOpenAIPassthroughWSImageBillingMetadataFromObservedOutput(t *testing.T) {
	testCases := []struct {
		name              string
		responseID        string
		output            string
		imageTokens       int
		wantCount         int
		wantSize          string
		wantInputSize     string
		wantBillingModel  string
		wantFallbackFinal bool
		requestPayload    string
	}{
		{
			name:             "per-turn two generated images",
			responseID:       "resp_image_turn",
			output:           `[{"id":"ig_1","type":"image_generation_call","result":"image-one"},{"id":"ig_2","type":"image_generation_call","result":"image-two"}]`,
			imageTokens:      7,
			wantCount:        2,
			wantSize:         ImageBillingSize1K,
			wantInputSize:    "1024x1024",
			wantBillingModel: "gpt-image-2",
		},
		{
			name:              "final fallback result",
			output:            `[{"id":"ig_final","type":"image_generation_call","result":"image-final"}]`,
			imageTokens:       5,
			wantCount:         1,
			wantSize:          ImageBillingSize1K,
			wantInputSize:     "1024x1024",
			wantBillingModel:  "gpt-image-2",
			wantFallbackFinal: true,
		},
		{
			name:          "intent without generated output",
			responseID:    "resp_image_none",
			output:        `[]`,
			imageTokens:   3,
			wantCount:     0,
			wantSize:      "",
			wantInputSize: "",
		},
		{
			name:           "observed output without inferred intent",
			responseID:     "resp_image_observed",
			output:         `[{"id":"ig_observed","type":"image_generation_call","result":"image-observed"}]`,
			imageTokens:    2,
			wantCount:      1,
			requestPayload: `{"type":"response.create","model":"client-model","input":"plain text request"}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			terminal := `{"type":"response.completed","response":{"id":"` + testCase.responseID + `","output":` + testCase.output + `,"usage":{"input_tokens":2,"output_tokens":4,"output_tokens_details":{"image_tokens":` + strconv.Itoa(testCase.imageTokens) + `}}}}`
			svc, _, _, account := newOpenAIWSPassthroughContextTestService(
				t,
				"",
				[]openAIWSReleaseTestFrame{{msgType: coderws.MessageText, payload: []byte(terminal)}},
				nil,
			)
			resultCh := make(chan *OpenAIForwardResult, 2)
			hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
				if turnErr == nil && result != nil {
					resultCh <- result
				}
			}}
			wsURL, serverResultCh := startOpenAIWSPassthroughReleaseBlockerServer(t, svc, account, hooks)
			clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

			requestPayload := testCase.requestPayload
			if requestPayload == "" {
				requestPayload = `{"type":"response.create","model":"client-model","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`
			}
			writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, requestPayload)
			_, event, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
			require.NoError(t, readErr)
			require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
			_ = clientConn.Close(coderws.StatusNormalClosure, "done")

			var result *OpenAIForwardResult
			select {
			case result = <-resultCh:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for passthrough image billing result")
			}
			require.Equal(t, testCase.wantCount, result.ImageCount)
			require.Equal(t, testCase.wantSize, result.ImageSize)
			require.Equal(t, testCase.wantInputSize, result.ImageInputSize)
			require.Equal(t, testCase.wantBillingModel, result.BillingModel)
			require.Equal(t, testCase.imageTokens, result.Usage.ImageOutputTokens)
			if testCase.wantCount == 0 {
				require.Empty(t, result.ImageOutputSizes)
			}

			select {
			case serverResult := <-serverResultCh:
				if serverResult.proxyErr != nil {
					var closeErr *OpenAIWSClientCloseError
					require.ErrorAs(t, serverResult.proxyErr, &closeErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for passthrough image session exit")
			}
		})
	}
}

func TestOpenAIPassthroughWSFollowupTurnRunsLifecycleHooks(t *testing.T) {
	svc, _, _, account := newOpenAIWSPassthroughContextTestService(
		t,
		"",
		[]openAIWSReleaseTestFrame{
			{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","usage":{"input_tokens":1,"output_tokens":1}}}`)},
			{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_2","usage":{"input_tokens":2,"output_tokens":1}}}`)},
		},
		nil,
	)
	var hooksMu sync.Mutex
	beforeTurns := make([]int, 0, 1)
	afterTurns := make([]int, 0, 2)
	hooks := &OpenAIWSIngressHooks{
		BeforeTurn: func(turn int) error {
			hooksMu.Lock()
			beforeTurns = append(beforeTurns, turn)
			hooksMu.Unlock()
			return nil
		},
		AfterTurn: func(turn int, _ *OpenAIForwardResult, _ error) {
			hooksMu.Lock()
			afterTurns = append(afterTurns, turn)
			hooksMu.Unlock()
		},
	}
	wsURL, serverResultCh := startOpenAIWSPassthroughReleaseBlockerServer(t, svc, account, hooks)
	clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","input":"first"}`)
	_, firstEvent, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
	require.NoError(t, readErr)
	require.Equal(t, "resp_turn_1", gjson.GetBytes(firstEvent, "response.id").String())

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","previous_response_id":"resp_turn_1","input":"second"}`)
	_, secondEvent, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
	require.NoError(t, readErr)
	require.Equal(t, "resp_turn_2", gjson.GetBytes(secondEvent, "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverResult := <-serverResultCh:
		if serverResult.proxyErr != nil {
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, serverResult.proxyErr, &closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for passthrough lifecycle session exit")
	}
	hooksMu.Lock()
	defer hooksMu.Unlock()
	require.Equal(t, []int{2}, beforeTurns)
	require.Equal(t, []int{1, 2}, afterTurns)
}

func TestOpenAIPassthroughWSRelayErrorReturnsCurrentTurnImageResult(t *testing.T) {
	svc, upstreamConn, _, account := newOpenAIWSPassthroughContextTestService(
		t,
		"",
		[]openAIWSReleaseTestFrame{{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.output_item.done","item":{"id":"ig_partial","type":"image_generation_call","result":"image-before-error","size":"1024x1024"}}`),
		}},
		nil,
	)
	upstreamConn.terminalErr = errors.New("synthetic upstream read failure")
	type turnOutcome struct {
		turn   int
		result *OpenAIForwardResult
		err    error
	}
	outcomeCh := make(chan turnOutcome, 1)
	hooks := &OpenAIWSIngressHooks{AfterTurn: func(turn int, result *OpenAIForwardResult, turnErr error) {
		outcomeCh <- turnOutcome{turn: turn, result: result, err: turnErr}
	}}
	wsURL, serverResultCh := startOpenAIWSPassthroughReleaseBlockerServer(t, svc, account, hooks)
	clientConn := dialOpenAIWSReleaseBlockerClient(t, wsURL)

	writeOpenAIWSReleaseBlockerFrame(t, clientConn, coderws.MessageText, `{"type":"response.create","model":"client-model","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}]}`)
	_, imageEvent, readErr := readOpenAIWSReleaseBlockerFrame(clientConn)
	require.NoError(t, readErr)
	require.Equal(t, "response.output_item.done", gjson.GetBytes(imageEvent, "type").String())
	_, _, readErr = readOpenAIWSReleaseBlockerFrame(clientConn)
	require.Error(t, readErr)

	select {
	case outcome := <-outcomeCh:
		require.Equal(t, 1, outcome.turn)
		require.Error(t, outcome.err)
		require.NotNil(t, outcome.result)
		require.Equal(t, 1, outcome.result.ImageCount)
		require.Equal(t, []string{"1024x1024"}, outcome.result.ImageOutputSizes)
		require.Equal(t, ImageBillingSize1K, outcome.result.ImageSize)
		require.Equal(t, "1024x1024", outcome.result.ImageInputSize)
		require.Equal(t, "gpt-image-2", outcome.result.BillingModel)
		require.Equal(t, OpenAIUsage{}, outcome.result.Usage)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for partial image turn outcome")
	}

	select {
	case serverResult := <-serverResultCh:
		require.Error(t, serverResult.proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for partial image relay failure")
	}
}
