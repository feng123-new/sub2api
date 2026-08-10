package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayService_APIKeyPassthrough_ImageIntentPreservesGateAndBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","stream":false,"tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}],"tool_choice":{"type":"image_generation"},"input":"draw"}`)

	t.Run("disabled group rejects before upstream", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{}
		svc := newOpenAIImageGenerationControlTestService(upstream)
		c, recorder := newOpenAIImageGenerationControlTestContext(false, "curl/8.0")
		account := newOpenAIImageGenerationControlTestAccount()
		account.Extra = map[string]any{"openai_passthrough": true}

		result, err := svc.Forward(context.Background(), c, account, body)

		require.Error(t, err)
		require.Nil(t, result)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Equal(t, "permission_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
		require.Nil(t, upstream.lastReq)
	})

	t.Run("allowed group keeps image billing", func(t *testing.T) {
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"output":[{"id":"ig_1","type":"image_generation_call","result":"final-image","size":"2048x1152"}],"usage":{"input_tokens":1,"output_tokens":2}}`,
			)),
		}}
		svc := newOpenAIImageGenerationControlTestService(upstream)
		c, _ := newOpenAIImageGenerationControlTestContext(true, "curl/8.0")
		account := newOpenAIImageGenerationControlTestAccount()
		account.Extra = map[string]any{"openai_passthrough": true}

		result, err := svc.Forward(context.Background(), c, account, body)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, upstream.lastReq)
		require.Equal(t, body, upstream.lastBody)
		require.Equal(t, 1, result.ImageCount)
		require.Equal(t, "gpt-image-2", result.BillingModel)
		require.Equal(t, "2K", result.ImageSize)
		require.Equal(t, "2048x1152", result.ImageInputSize)
	})
}

func TestOpenAIGatewayService_APIKeyPassthrough_DisabledGroupStripsOptionalImageDeclarations(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "top-level image tool",
			body: []byte(`{"model":"gpt-5.5","stream":false,"tools":[{"type":"function","name":"lookup"},{"type":"image_generation"}],"input":"write code","tool_choice":"auto"}`),
		},
		{
			name: "Responses Lite additional tools namespace",
			body: []byte(`{
				"model":"gpt-5.5",
				"stream":false,
				"tools":[{"type":"function","name":"lookup"}],
				"input":[
					{"type":"message","role":"assistant","content":"prior answer"},
					{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]},
					{"type":"message","role":"user","content":"continue"}
				],
				"tool_choice":"auto"
			}`),
		},
		{
			name: "flattened image function",
			body: []byte(`{"model":"gpt-5.5","stream":false,"tools":[{"type":"function","name":"lookup"},{"type":"function","name":"image_gen.imagegen"}],"input":"write code","tool_choice":"auto"}`),
		},
		{
			name: "nested image function",
			body: []byte(`{"model":"gpt-5.5","stream":false,"tools":[{"type":"function","function":{"name":"lookup"}},{"type":"function","function":{"name":"image_gen.imagegen"}}],"input":"write code","tool_choice":"auto"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"resp_passthrough_text","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":2}}`)),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(false, "curl/8.0")
			account := newOpenAIImageGenerationControlTestAccount()
			account.Extra = map[string]any{"openai_passthrough": true}

			result, err := svc.Forward(context.Background(), c, account, tt.body)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.NotNil(t, upstream.lastReq)
			require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="image_generation")`).Exists())
			require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools").tools.#(name=="image_gen")`).Exists())
			require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(name=="image_gen.imagegen")`).Exists())
			require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(function.name=="image_gen.imagegen")`).Exists())
			require.True(t, gjson.GetBytes(upstream.lastBody, `tools.#(name=="lookup")`).Exists() ||
				gjson.GetBytes(upstream.lastBody, `tools.#(function.name=="lookup")`).Exists())
			require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
			if gjson.GetBytes(tt.body, "input").IsArray() {
				require.Equal(t, "prior answer", gjson.GetBytes(upstream.lastBody, "input.0.content").String())
				require.Equal(t, "continue", gjson.GetBytes(upstream.lastBody, "input.1.content").String())
			}
		})
	}
}
