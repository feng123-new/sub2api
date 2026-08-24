package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIContextPreflightLimitLookup map[string]ModelContextLimits

func (l openAIContextPreflightLimitLookup) GetExactModelContextLimits(model string) (ModelContextLimits, bool) {
	limits, ok := l[model]
	return limits, ok
}

func TestOpenAIContextPreflightThresholdBoundaries(t *testing.T) {
	preflight := &openAIContextPreflight{
		mode:           "enforce",
		thresholdBPS:   9000,
		billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
	}
	body := []byte(`{"model":"gpt-5.5","input":"hello"}`)

	preflight.estimate = func([]byte, openAIContextPreflightEndpoint, string) (int, bool, openAIContextPreflightSkipReason) {
		return 89, true, ""
	}
	allowed := preflight.evaluate(openAIContextPreflightInput{
		Endpoint:   openAIContextPreflightEndpointResponses,
		Body:       body,
		FinalModel: "gpt-5.5",
	})
	require.Equal(t, openAIContextPreflightDecisionAllowed, allowed.Decision)
	require.False(t, allowed.Reject)
	require.Equal(t, 90, allowed.ThresholdTokens)

	preflight.estimate = func([]byte, openAIContextPreflightEndpoint, string) (int, bool, openAIContextPreflightSkipReason) {
		return 90, true, ""
	}
	rejected := preflight.evaluate(openAIContextPreflightInput{
		Endpoint:   openAIContextPreflightEndpointResponses,
		Body:       body,
		FinalModel: "gpt-5.5",
	})
	require.Equal(t, openAIContextPreflightDecisionRejected, rejected.Decision)
	require.True(t, rejected.Reject)
	require.Equal(t, "context_length_exceeded", rejected.Rejection.Code)
	require.Equal(t, 90, rejected.Rejection.EstimatedInputTokens)
}

func TestOpenAIContextPreflightFailOpenDecisions(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hello"}`)
	tests := []struct {
		name       string
		preflight  *openAIContextPreflight
		body       []byte
		finalModel string
		wantReason openAIContextPreflightSkipReason
	}{
		{
			name: "model excluded by allowlist",
			preflight: &openAIContextPreflight{
				mode: "enforce", thresholdBPS: 9000, models: map[string]struct{}{"o3": {}},
			},
			body: body, finalModel: "gpt-5.5", wantReason: openAIContextPreflightSkipUnknownModel,
		},
		{
			name: "missing exact model limit",
			preflight: &openAIContextPreflight{
				mode: "enforce", thresholdBPS: 9000, billingService: openAIContextPreflightLimitLookup{},
			},
			body: body, finalModel: "gpt-5.5", wantReason: openAIContextPreflightSkipMissingModelLimit,
		},
		{
			name: "mapped model differs from request body",
			preflight: &openAIContextPreflight{
				mode: "enforce", thresholdBPS: 9000,
				billingService: openAIContextPreflightLimitLookup{"o3": {MaxInputTokens: 100}},
			},
			body: body, finalModel: "o3", wantReason: openAIContextPreflightSkipModelMappingUnresolved,
		},
		{
			name: "malformed request body",
			preflight: &openAIContextPreflight{
				mode: "enforce", thresholdBPS: 9000,
				billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
			},
			body: []byte(`{"model":`), finalModel: "gpt-5.5", wantReason: openAIContextPreflightSkipCountFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.preflight.evaluate(openAIContextPreflightInput{
				Endpoint:   openAIContextPreflightEndpointResponses,
				Body:       test.body,
				FinalModel: test.finalModel,
			})
			require.Equal(t, openAIContextPreflightDecisionSkipped, result.Decision)
			require.Equal(t, test.wantReason, result.SkipReason)
			require.False(t, result.Reject)
			require.Nil(t, result.Rejection)
		})
	}
}

func TestOpenAIContextPreflightShadowNeverRejects(t *testing.T) {
	preflight := &openAIContextPreflight{
		mode:           "shadow",
		thresholdBPS:   9000,
		billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
		estimate: func([]byte, openAIContextPreflightEndpoint, string) (int, bool, openAIContextPreflightSkipReason) {
			return 90, true, ""
		},
	}

	result := preflight.evaluate(openAIContextPreflightInput{
		Endpoint:   openAIContextPreflightEndpointResponses,
		Body:       []byte(`{"model":"gpt-5.5","input":"hello"}`),
		FinalModel: "gpt-5.5",
	})

	require.Equal(t, openAIContextPreflightDecisionWouldReject, result.Decision)
	require.False(t, result.Reject)
	require.NotNil(t, result.Rejection)
}

func TestEstimateOpenAIInputTokensCompleteResponses(t *testing.T) {
	estimated, complete, reason := estimateOpenAIInputTokensComplete(
		[]byte(`{"model":"gpt-5.5","instructions":"Be concise.","input":"Hello world"}`),
		openAIContextPreflightEndpointResponses,
		"gpt-5.5",
	)

	require.True(t, complete)
	require.Empty(t, reason)
	require.Positive(t, estimated)
}

func TestEstimateOpenAIInputTokensCompleteResponsesCountsAdditionalToolsCatalog(t *testing.T) {
	message := map[string]any{"type": "message", "role": "user", "content": "hello"}
	baseBody, err := json.Marshal(map[string]any{
		"model": "gpt-5.5",
		"input": []any{message},
	})
	require.NoError(t, err)
	withCatalogBody, err := json.Marshal(map[string]any{
		"model": "gpt-5.5",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{
					"type":        "custom",
					"name":        "exec",
					"description": strings.Repeat("catalog token ", 40),
				},
			}},
			message,
		},
	})
	require.NoError(t, err)

	baseTokens, baseComplete, baseReason := estimateOpenAIInputTokensComplete(
		baseBody,
		openAIContextPreflightEndpointResponses,
		"gpt-5.5",
	)
	withCatalogTokens, withCatalogComplete, withCatalogReason := estimateOpenAIInputTokensComplete(
		withCatalogBody,
		openAIContextPreflightEndpointResponses,
		"gpt-5.5",
	)

	require.True(t, baseComplete)
	require.Empty(t, baseReason)
	require.True(t, withCatalogComplete)
	require.Empty(t, withCatalogReason)
	require.Greater(t, withCatalogTokens-baseTokens, 20, "the canonical additional_tools catalog must contribute to the estimate")
}

func TestEstimateOpenAIInputTokensCompleteResponsesAcceptsFlattenedNamespaceHistory(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}]},
			{"type":"function_call","call_id":"call_1","name":"collaboration__spawn_agent","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","name":"collaboration__spawn_agent","output":"ok"}
		]
	}`)

	estimated, complete, reason := estimateOpenAIInputTokensComplete(
		body,
		openAIContextPreflightEndpointResponses,
		"gpt-5.5",
	)

	require.True(t, complete)
	require.Empty(t, reason)
	require.Positive(t, estimated)
}

func TestEstimateOpenAIInputTokensCompleteResponsesRejectsMalformedAdditionalTools(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"gpt-5.5","input":[{"type":"additional_tools","role":"developer","tools":"not-an-array"}]}`),
		[]byte(`{"model":"gpt-5.5","input":[{"type":"additional_tools","role":42,"tools":[]}]}`),
	} {
		_, complete, reason := estimateOpenAIInputTokensComplete(
			body,
			openAIContextPreflightEndpointResponses,
			"gpt-5.5",
		)
		require.False(t, complete)
		require.Equal(t, openAIContextPreflightSkipUnsupportedShape, reason)
	}
}

func TestOpenAIContextPreflightRejectsOversizedNormalizedResponsesLiteHistory(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.5",
		"tools": []any{map[string]any{
			"type":  "namespace",
			"name":  "collaboration",
			"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
		}},
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "spawn_agent", "namespace": "collaboration", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "name": "spawn_agent", "namespace": "collaboration", "output": "ok"},
			map[string]any{"type": "message", "role": "assistant", "content": strings.Repeat("history ", 240)},
			map[string]any{"type": "message", "role": "user", "content": "continue"},
		},
	})
	require.NoError(t, err)
	normalized, changed, err := normalizeOpenAIResponsesLitePayload(nil, body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(normalized), `"additional_tools"`)

	preflight := &openAIContextPreflight{
		mode:           "enforce",
		thresholdBPS:   9000,
		billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
	}
	result := preflight.evaluate(openAIContextPreflightInput{
		Endpoint:   openAIContextPreflightEndpointResponses,
		Body:       normalized,
		FinalModel: "gpt-5.5",
	})

	require.Equal(t, openAIContextPreflightDecisionRejected, result.Decision)
	require.True(t, result.Reject)
	require.Empty(t, result.SkipReason)
	require.NotNil(t, result.Rejection)
	require.Equal(t, "context_length_exceeded", result.Rejection.Code)
	require.GreaterOrEqual(t, result.EstimatedInputTokens, result.ThresholdTokens)
}

func TestOpenAIContextPreflightRerunsAfterProactiveNamespaceStripAndHTTPRetryMutation(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":2048,"input":[{"type":"custom_tool_call","name":"second","namespace":"remove","input":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	svc := newOpenAIRejectedFieldTestService(upstream)
	estimates := 0
	svc.contextPreflight = &openAIContextPreflight{
		mode:           "enforce",
		thresholdBPS:   9000,
		billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
		estimate: func([]byte, openAIContextPreflightEndpoint, string) (int, bool, openAIContextPreflightSkipReason) {
			estimates++
			return 1, true, ""
		},
	}

	result, err := svc.Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, 2, estimates)
	for _, forwardedBody := range upstream.bodies {
		require.False(t, gjson.GetBytes(forwardedBody, "input.0.namespace").Exists())
	}
	require.Equal(t, int64(2048), gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Int())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
}
