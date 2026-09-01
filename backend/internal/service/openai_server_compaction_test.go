package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAIServerCompaction(t *testing.T) {
	baseBody := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	enforce := config.GatewayOpenAIServerCompactionConfig{
		Mode: "enforce",
		ModelThresholds: map[string]int64{
			"gpt-5.6-sol": 700000,
		},
	}

	tests := []struct {
		name          string
		cfg           config.GatewayOpenAIServerCompactionConfig
		body          []byte
		model         string
		compactPath   bool
		responsesLite bool
		want          openAIServerCompactionDecision
		wantChanged   bool
		wantLimit     int64
	}{
		{name: "off", cfg: config.GatewayOpenAIServerCompactionConfig{Mode: "off"}, body: baseBody, model: "gpt-5.6-sol", want: openAIServerCompactionDecisionOff},
		{name: "model not configured", cfg: enforce, body: baseBody, model: "gpt-5.6-luna", want: openAIServerCompactionDecisionNoThreshold},
		{name: "shadow", cfg: config.GatewayOpenAIServerCompactionConfig{Mode: "shadow", ModelThresholds: enforce.ModelThresholds}, body: baseBody, model: "gpt-5.6-sol", want: openAIServerCompactionDecisionWouldInject},
		{name: "inject model threshold", cfg: enforce, body: baseBody, model: " GPT-5.6-SOL ", want: openAIServerCompactionDecisionInjected, wantChanged: true, wantLimit: 700000},
		{name: "inject default threshold", cfg: config.GatewayOpenAIServerCompactionConfig{Mode: "enforce", DefaultThreshold: 800000}, body: baseBody, model: "gpt-5.6-luna", want: openAIServerCompactionDecisionInjected, wantChanged: true, wantLimit: 800000},
		{name: "preserve client config", cfg: enforce, body: []byte(`{"model":"gpt-5.6-sol","context_management":[{"type":"compaction","compact_threshold":123456}],"input":"hello"}`), model: "gpt-5.6-sol", want: openAIServerCompactionDecisionClientConfigured, wantLimit: 123456},
		{name: "skip compaction trigger", cfg: enforce, body: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`), model: "gpt-5.6-sol", want: openAIServerCompactionDecisionCompactionTrigger},
		{name: "skip compact path", cfg: enforce, body: baseBody, model: "gpt-5.6-sol", compactPath: true, want: openAIServerCompactionDecisionCompactRequest},
		{name: "skip responses lite", cfg: enforce, body: baseBody, model: "gpt-5.6-sol", responsesLite: true, want: openAIServerCompactionDecisionResponsesLite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, decision, err := applyOpenAIServerCompaction(tt.body, tt.model, tt.compactPath, tt.responsesLite, tt.cfg)
			require.NoError(t, err)
			require.Equal(t, tt.want, decision)
			if tt.wantChanged {
				require.NotEqual(t, string(tt.body), string(got))
			} else {
				require.Equal(t, string(tt.body), string(got))
			}
			if tt.wantLimit > 0 {
				require.Equal(t, tt.wantLimit, gjson.GetBytes(got, "context_management.0.compact_threshold").Int())
			}
		})
	}
}

func TestOpenAIGatewayServiceForwardInjectsServerCompaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"hello"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg: &config.Config{
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
			Gateway: config.GatewayConfig{OpenAIServerCompaction: config.GatewayOpenAIServerCompactionConfig{
				Mode: "enforce",
				ModelThresholds: map[string]int64{
					"gpt-5.6-sol": 700000,
				},
			}},
		},
	}
	account := &Account{
		ID: 7, Name: "openai-apikey", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"},
		Status:      StatusActive, Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.EqualValues(t, 700000, gjson.GetBytes(upstream.lastBody, "context_management.0.compact_threshold").Int())
}

func TestOpenAIGatewayServiceForwardOAuthHTTPInjectsServerCompaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := newOpenAIServerCompactionForwardTestService(upstream)
	account := &Account{
		ID: 17, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Status:      StatusActive, Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.EqualValues(t, 700000, gjson.GetBytes(upstream.lastBody, "context_management.0.compact_threshold").Int())
}

func TestOpenAIGatewayServiceForwardResponsesLiteSkipsServerCompaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"hello"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set(responsesLiteHeader, "true")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_lite","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}

	result, err := newOpenAIServerCompactionForwardTestService(upstream).Forward(
		context.Background(), c, newOpenAIServerCompactionForwardTestAccount(), body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "context_management").Exists())
}

func TestOpenAIGatewayServiceForwardRetriesInjectedServerCompactionRejectionWithoutField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"hello"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_value","message":"Invalid value: 'compact_threshold'. X-OpenAI-Internal-Codex-Responses-Lite does not support server-side compaction.","param":"compact_threshold","type":"invalid_request_error"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_2","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}}
	svc := newOpenAIServerCompactionForwardTestService(upstream)
	account := newOpenAIServerCompactionForwardTestAccount()

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "context_management").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "context_management").Exists())
}

func TestOpenAIGatewayServiceForwardDoesNotRetryClientServerCompactionRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"context_management":[{"type":"compaction","compact_threshold":123456}],"input":"hello"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: context_management","param":"context_management"}}`)),
	}}
	svc := newOpenAIServerCompactionForwardTestService(upstream)
	account := newOpenAIServerCompactionForwardTestAccount()

	result, err := svc.Forward(context.Background(), c, account, body)
	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.EqualValues(t, 123456, gjson.GetBytes(upstream.bodies[0], "context_management.0.compact_threshold").Int())
}

func newOpenAIServerCompactionForwardTestService(upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg: &config.Config{
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
			Gateway: config.GatewayConfig{OpenAIServerCompaction: config.GatewayOpenAIServerCompactionConfig{
				Mode: "enforce",
				ModelThresholds: map[string]int64{
					"gpt-5.6-sol": 700000,
				},
			}},
		},
	}
}

func newOpenAIServerCompactionForwardTestAccount() *Account {
	return &Account{
		ID: 7, Name: "openai-apikey", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"},
		Status:      StatusActive, Schedulable: true,
	}
}
