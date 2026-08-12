package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayService_WSDisabledGroupStripsOptionalImageDeclarationsAcrossTurns(t *testing.T) {
	modes := []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var readDelays []time.Duration
			if mode == OpenAIWSIngressModePassthrough {
				readDelays = []time.Duration{0, 200 * time.Millisecond}
			}
			session := startOpenAIImagePolicyWSTestSession(t, mode, [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_image_policy_1","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
				[]byte(`{"type":"response.completed","response":{"id":"resp_image_policy_2","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			}, readDelays)

			session.write(t, `{
				"type":"response.create",
				"model":"gpt-5.5",
				"stream":false,
				"tools":[
					{"type":"function","name":"lookup"},
					{"type":"image_generation"},
					{"type":"function","name":"image_gen.imagegen"},
					{"type":"function","function":{"name":"image_gen.imagegen"}}
				],
				"input":"write code",
				"tool_choice":"auto"
			}`)
			first := session.read(t)
			require.Equal(t, "resp_image_policy_1", gjson.GetBytes(first, "response.id").String())

			session.write(t, `{
				"type":"response.create",
				"stream":false,
				"previous_response_id":"resp_image_policy_1",
				"input":[
					{"type":"message","role":"assistant","content":"prior answer"},
					{"type":"additional_tools","role":"developer","tools":[
						{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]},
						{"type":"function","name":"image_gen.imagegen"},
						{"type":"function","function":{"name":"image_gen.imagegen"}},
						{"type":"custom","name":"exec"}
					]},
					{"type":"message","role":"user","content":"continue"}
				],
				"tool_choice":"required"
			}`)
			second := session.read(t)
			require.Equal(t, "resp_image_policy_2", gjson.GetBytes(second, "response.id").String())

			session.closeNormal(t)
			session.requireNormalServerEnd(t)
			writes := session.upstreamWrites()
			require.Len(t, writes, 2)

			firstUpstream := requestToJSONString(writes[0])
			require.False(t, gjson.Get(firstUpstream, `tools.#(type=="image_generation")`).Exists())
			require.False(t, gjson.Get(firstUpstream, `tools.#(name=="image_gen.imagegen")`).Exists())
			require.False(t, gjson.Get(firstUpstream, `tools.#(function.name=="image_gen.imagegen")`).Exists())
			require.True(t, gjson.Get(firstUpstream, `tools.#(name=="lookup")`).Exists())
			require.Equal(t, "auto", gjson.Get(firstUpstream, "tool_choice").String())

			secondUpstream := requestToJSONString(writes[1])
			require.False(t, gjson.Get(secondUpstream, `input.#(type=="additional_tools").tools.#(name=="image_gen")`).Exists())
			require.False(t, gjson.Get(secondUpstream, `input.#(type=="additional_tools").tools.#(name=="image_gen.imagegen")`).Exists())
			require.False(t, gjson.Get(secondUpstream, `input.#(type=="additional_tools").tools.#(function.name=="image_gen.imagegen")`).Exists())
			require.True(t, gjson.Get(secondUpstream, `input.#(type=="additional_tools").tools.#(name=="exec")`).Exists())
			require.Equal(t, "prior answer", gjson.Get(secondUpstream, "input.0.content").String())
			require.Equal(t, "continue", gjson.Get(secondUpstream, "input.2.content").String())
			require.Equal(t, "required", gjson.Get(secondUpstream, "tool_choice").String())
		})
	}
}

func TestOpenAIGatewayService_WSDisabledGroupRejectsRequiredImageGeneration(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		first       string
		followup    string
		wantWrites  int
		firstEvents [][]byte
		readDelays  []time.Duration
	}{
		{
			name:       "pooled first frame image-only required",
			mode:       OpenAIWSIngressModeCtxPool,
			first:      `{"type":"response.create","model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}],"tool_choice":"required"}`,
			wantWrites: 0,
		},
		{
			name:       "passthrough first frame image-only required",
			mode:       OpenAIWSIngressModePassthrough,
			first:      `{"type":"response.create","model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}],"tool_choice":"required"}`,
			wantWrites: 0,
		},
		{
			name:       "pooled follow-up explicit image function choice",
			mode:       OpenAIWSIngressModeCtxPool,
			first:      `{"type":"response.create","model":"gpt-5.5","input":"write code"}`,
			followup:   `{"type":"response.create","previous_response_id":"resp_image_policy_text","tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":{"type":"function","name":"image_gen.imagegen"},"input":"draw"}`,
			wantWrites: 1,
			firstEvents: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_image_policy_text","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			},
		},
		{
			name:       "passthrough follow-up explicit image function choice",
			mode:       OpenAIWSIngressModePassthrough,
			first:      `{"type":"response.create","model":"gpt-5.5","input":"write code"}`,
			followup:   `{"type":"response.create","previous_response_id":"resp_image_policy_text","tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":{"type":"function","name":"image_gen.imagegen"},"input":"draw"}`,
			wantWrites: 1,
			firstEvents: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_image_policy_text","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
				[]byte(`{"type":"response.completed","response":{"id":"unused_after_policy_close","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			},
			readDelays: []time.Duration{0, 2 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := startOpenAIImagePolicyWSTestSession(t, tt.mode, tt.firstEvents, tt.readDelays)
			session.write(t, tt.first)
			if tt.followup != "" {
				first := session.read(t)
				require.Equal(t, "resp_image_policy_text", gjson.GetBytes(first, "response.id").String())
				session.write(t, tt.followup)
			}

			readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
			_, _, readErr := session.client.Read(readCtx)
			cancelRead()
			require.Error(t, readErr)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(readErr))
			require.Contains(t, readErr.Error(), ImageGenerationPermissionMessage())

			serverErr := session.waitServer(t)
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, serverErr, &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
			require.Equal(t, ImageGenerationPermissionMessage(), closeErr.Reason())
			require.Len(t, session.upstreamWrites(), tt.wantWrites)
		})
	}
}

func TestOpenAIGatewayService_WSRejectsDuplicateImagePolicyKeys(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		first       string
		followup    string
		wantWrites  int
		firstEvents [][]byte
		readDelays  []time.Duration
	}{
		{
			name:  "pooled duplicate model",
			mode:  OpenAIWSIngressModeCtxPool,
			first: `{"type":"response.create","model":"gpt-image-2","model":"gpt-5.5","input":"write code"}`,
		},
		{
			name:  "passthrough duplicate tools",
			mode:  OpenAIWSIngressModePassthrough,
			first: `{"type":"response.create","model":"gpt-5.5","tools":[{"type":"image_generation"}],"tools":[{"type":"function","name":"lookup"}],"input":"write code","tool_choice":"auto"}`,
		},
		{
			name:  "pooled duplicate nested tool name",
			mode:  OpenAIWSIngressModeCtxPool,
			first: `{"type":"response.create","model":"gpt-5.5","tools":[{"type":"function","name":"lookup","name":"image_gen.imagegen"}],"input":"write code"}`,
		},
		{
			name:  "passthrough duplicate nested additional tool name",
			mode:  OpenAIWSIngressModePassthrough,
			first: `{"type":"response.create","model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"code_tools","name":"image_gen"}]}]}`,
		},
		{
			name:       "pooled followup duplicate tool choice name",
			mode:       OpenAIWSIngressModeCtxPool,
			first:      `{"type":"response.create","model":"gpt-5.5","input":"write code"}`,
			followup:   `{"type":"response.create","previous_response_id":"resp_duplicate_policy","tool_choice":{"type":"function","name":"lookup","name":"image_gen.imagegen"},"input":"continue"}`,
			wantWrites: 1,
			firstEvents: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_duplicate_policy","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			},
		},
		{
			name:       "passthrough followup duplicate namespace child name",
			mode:       OpenAIWSIngressModePassthrough,
			first:      `{"type":"response.create","model":"gpt-5.5","input":"write code"}`,
			followup:   `{"type":"response.create","previous_response_id":"resp_duplicate_policy","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"lookup","name":"imagegen"}]}],"input":"continue"}`,
			wantWrites: 1,
			firstEvents: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_duplicate_policy","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
				[]byte(`{"type":"response.completed","response":{"id":"unused_after_policy_close","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			},
			readDelays: []time.Duration{0, 2 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := startOpenAIImagePolicyWSTestSession(t, tt.mode, tt.firstEvents, tt.readDelays)
			session.write(t, tt.first)
			if tt.followup != "" {
				first := session.read(t)
				require.Equal(t, "resp_duplicate_policy", gjson.GetBytes(first, "response.id").String())
				session.write(t, tt.followup)
			}

			readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
			_, _, readErr := session.client.Read(readCtx)
			cancelRead()
			require.Error(t, readErr)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(readErr))

			serverErr := session.waitServer(t)
			var closeErr *OpenAIWSClientCloseError
			require.ErrorAs(t, serverErr, &closeErr)
			require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
			require.Equal(t, "invalid websocket request payload", closeErr.Reason())
			require.Len(t, session.upstreamWrites(), tt.wantWrites)
		})
	}
}

type openAIImagePolicyWSTestSession struct {
	client      *coderws.Conn
	server      *httptest.Server
	captureConn *openAIWSCaptureConn
	serverErrCh chan error
}

func startOpenAIImagePolicyWSTestSession(t *testing.T, mode string, events [][]byte, readDelays []time.Duration) *openAIImagePolicyWSTestSession {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{events: events, readDelays: readDelays}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
	accountExtra := map[string]any{"responses_websockets_v2_enabled": true}
	if mode == OpenAIWSIngressModePassthrough {
		cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
		accountExtra = map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}
		svc.openaiWSPassthroughDialer = captureDialer
	} else {
		pool := newOpenAIWSConnPool(cfg)
		pool.setClientDialerForTest(captureDialer)
		svc.openaiWSPool = pool
	}

	groupID := int64(7021)
	apiKey := &APIKey{
		ID:      7022,
		UserID:  7023,
		GroupID: &groupID,
		Group: &Group{
			ID:                   groupID,
			Platform:             PlatformOpenAI,
			AllowImageGeneration: false,
		},
	}
	account := &Account{
		ID:          7024,
		Name:        "openai-image-policy-ws",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       accountExtra,
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		ginCtx.Set("api_key", apiKey)

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		proxyErr := svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
		var closeErr *OpenAIWSClientCloseError
		if errors.As(proxyErr, &closeErr) {
			_ = conn.Close(closeErr.StatusCode(), closeErr.Reason())
		}
		serverErrCh <- proxyErr
	}))

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)

	session := &openAIImagePolicyWSTestSession{
		client:      clientConn,
		server:      wsServer,
		captureConn: captureConn,
		serverErrCh: serverErrCh,
	}
	t.Cleanup(func() {
		_ = clientConn.CloseNow()
		wsServer.Close()
	})
	return session
}

func (s *openAIImagePolicyWSTestSession) write(t *testing.T, payload string) {
	t.Helper()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWrite()
	require.NoError(t, s.client.Write(writeCtx, coderws.MessageText, []byte(payload)))
}

func (s *openAIImagePolicyWSTestSession) read(t *testing.T) []byte {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRead()
	msgType, payload, err := s.client.Read(readCtx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	return payload
}

func (s *openAIImagePolicyWSTestSession) closeNormal(t *testing.T) {
	t.Helper()
	_ = s.client.Close(coderws.StatusNormalClosure, "done")
}

func (s *openAIImagePolicyWSTestSession) waitServer(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.serverErrCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for image-policy websocket server")
		return nil
	}
}

func (s *openAIImagePolicyWSTestSession) requireNormalServerEnd(t *testing.T) {
	t.Helper()
	serverErr := s.waitServer(t)
	if serverErr != nil {
		require.Equal(t, coderws.StatusNormalClosure, coderws.CloseStatus(serverErr), serverErr)
	}
}

func (s *openAIImagePolicyWSTestSession) upstreamWrites() []map[string]any {
	s.captureConn.mu.Lock()
	defer s.captureConn.mu.Unlock()
	writes := make([]map[string]any, len(s.captureConn.writes))
	for index, write := range s.captureConn.writes {
		writes[index] = cloneMapStringAny(write)
	}
	return writes
}
