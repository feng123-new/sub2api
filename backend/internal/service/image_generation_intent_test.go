package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsImageGenerationIntent(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		model    string
		body     []byte
		want     bool
	}{
		{
			name:     "images endpoint",
			endpoint: "/v1/images/generations",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     true,
		},
		{
			name:     "image model",
			endpoint: "/v1/responses",
			model:    "gpt-image-2",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     true,
		},
		{
			name:     "image tool",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation"}]}`),
			want:     true,
		},
		{
			name:     "flattened image_gen function tool",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"image_gen.imagegen"}]}`),
			want:     true,
		},
		{
			name:     "nested image_gen function tool",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"function","function":{"name":"image_gen.imagegen"}}]}`),
			want:     true,
		},
		{
			name:     "image tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tool_choice":{"type":"image_generation"}}`),
			want:     true,
		},
		{
			name:     "namespace image_gen tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tool_choice":{"type":"namespace","name":"image_gen"}}`),
			want:     true,
		},
		{
			name:     "custom imagegen function tool choice is not image intent",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tool_choice":{"function":{"name":"imagegen"}}}`),
			want:     false,
		},
		{
			name:     "flattened image_gen function tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tool_choice":{"type":"function","name":"image_gen.imagegen"}}`),
			want:     true,
		},
		{
			name:     "nested image_gen function tool choice",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tool_choice":{"type":"function","function":{"name":"image_gen.imagegen"}}}`),
			want:     true,
		},
		{
			name:     "required tool choice alone is text",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","tool_choice":"required"}`),
			want:     false,
		},
		{
			name:     "text only gpt 5.4",
			endpoint: "/v1/responses",
			model:    "gpt-5.4",
			body:     []byte(`{"model":"gpt-5.4","input":"write code"}`),
			want:     false,
		},
		{
			name:     "namespace image_gen tool in top-level tools",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]}`),
			want:     true,
		},
		{
			name:     "custom namespace with nested imagegen function is not image intent",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"media_tools","tools":[{"type":"function","name":"imagegen"}]}]}`),
			want:     false,
		},
		{
			name:     "namespace image_gen in input additional_tools (Responses Lite)",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]}]}`),
			want:     true,
		},
		{
			name:     "non-image namespace tool is not flagged",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"namespace","name":"code_tools","tools":[{"type":"function","name":"run"}]}]}`),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsImageGenerationIntent(tt.endpoint, tt.model, tt.body))
		})
	}
}

func TestIsImageGenerationIntentJSONSemantics(t *testing.T) {
	largeInput := strings.Repeat("x", 1<<20)
	tests := []struct {
		name     string
		endpoint string
		body     []byte
		want     bool
	}{
		{
			name:     "chat body image model",
			endpoint: "/v1/chat/completions",
			body:     []byte(`{"model":"gpt-image-2"}`),
			want:     true,
		},
		{
			name:     "large responses input with trailing namespace tool choice",
			endpoint: "/v1/responses",
			body:     []byte(`{"model":"gpt-5.5","input":"` + largeInput + `","tool_choice":{"type":"namespace","name":"image_gen"}}`),
			want:     true,
		},
		{
			name:     "invalid json with image tool",
			endpoint: "/v1/responses",
			body:     []byte(`{"tools":[{"type":"image_generation"}]`),
			want:     false,
		},
		{
			name:     "duplicate model uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"model":"gpt-5.5","model":"gpt-image-2"}`),
			want:     false,
		},
		{
			name:     "duplicate null model still uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"model":null,"model":"gpt-image-2"}`),
			want:     false,
		},
		{
			name:     "duplicate tools uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"tools":[],"tools":[{"type":"image_generation"}]}`),
			want:     false,
		},
		{
			name:     "duplicate input uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"input":[],"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}]}`),
			want:     false,
		},
		{
			name:     "duplicate tool choice uses first value",
			endpoint: "/v1/responses",
			body:     []byte(`{"tool_choice":"required","tool_choice":{"type":"image_generation"}}`),
			want:     false,
		},
		{
			name:     "escaped top level key",
			endpoint: "/v1/responses",
			body:     []byte(`{"tool_\u0063hoice":{"type":"image_generation"}}`),
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsImageGenerationIntent(tt.endpoint, "gpt-5.5", tt.body))
		})
	}
}

func TestIsImageGenerationIntentMap_NamespaceImageGen(t *testing.T) {
	tests := []struct {
		name    string
		reqBody map[string]any
		want    bool
	}{
		{
			name: "top-level namespace image_gen",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{"type": "namespace", "name": "image_gen", "tools": []any{
						map[string]any{"type": "function", "name": "imagegen"},
					}},
				},
			},
			want: true,
		},
		{
			name: "additional_tools in input",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"input": []any{
					map[string]any{
						"type": "additional_tools",
						"tools": []any{
							map[string]any{"type": "namespace", "name": "image_gen"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "flattened image_gen function tool",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{"type": "function", "name": "image_gen.imagegen"},
				},
			},
			want: true,
		},
		{
			name: "nested image_gen function tool",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{
						"type":     "function",
						"function": map[string]any{"name": "image_gen.imagegen"},
					},
				},
			},
			want: true,
		},
		{
			name: "custom namespace with nested imagegen function is not image intent",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{
						"type": "namespace",
						"name": "media_tools",
						"tools": []any{
							map[string]any{"type": "function", "name": "imagegen"},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "namespace image_gen tool choice",
			reqBody: map[string]any{
				"model":       "gpt-5.5",
				"tool_choice": map[string]any{"type": "namespace", "name": "image_gen"},
			},
			want: true,
		},
		{
			name: "custom imagegen function tool choice is not image intent",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tool_choice": map[string]any{
					"function": map[string]any{"name": "imagegen"},
				},
			},
			want: false,
		},
		{
			name: "flattened image_gen function tool choice",
			reqBody: map[string]any{
				"model":       "gpt-5.5",
				"tool_choice": map[string]any{"type": "function", "name": "image_gen.imagegen"},
			},
			want: true,
		},
		{
			name: "nested image_gen function tool choice",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tool_choice": map[string]any{
					"type":     "function",
					"function": map[string]any{"name": "image_gen.imagegen"},
				},
			},
			want: true,
		},
		{
			name: "non-image namespace not flagged",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{"type": "namespace", "name": "code_tools"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsImageGenerationIntentMap("/v1/responses", "gpt-5.5", tt.reqBody))
		})
	}
}

func TestIsRequiredImageGenerationIntent(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		model    string
		body     []byte
		want     bool
	}{
		{name: "image endpoint", endpoint: "/v1/images/generations", model: "gpt-5.5", want: true},
		{name: "image model", endpoint: "/v1/responses", model: "gpt-image-2", want: true},
		{
			name:     "passive native image tool is optional",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`),
		},
		{
			name:     "passive Lite image namespace is optional",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}],"tool_choice":"auto"}`),
		},
		{
			name:     "passive flattened image function is optional",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":"auto"}`),
		},
		{
			name:     "passive nested image function is optional",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"function","function":{"name":"image_gen.imagegen"}}],"tool_choice":"auto"}`),
		},
		{
			name:     "native image choice is required",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tool_choice":{"type":"image_generation"}}`),
			want:     true,
		},
		{
			name:     "namespace image choice is required",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tool_choice":{"type":"namespace","name":"image_gen"}}`),
			want:     true,
		},
		{
			name:     "flattened function choice is required",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tool_choice":{"type":"function","name":"image_gen.imagegen"}}`),
			want:     true,
		},
		{
			name:     "nested function choice is required",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tool_choice":{"type":"function","function":{"name":"image_gen.imagegen"}}}`),
			want:     true,
		},
		{
			name:     "required with only top-level image tools",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"image_generation"},{"type":"namespace","name":"image_gen"}],"tool_choice":"required"}`),
			want:     true,
		},
		{
			name:     "required with only additional image tools",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}],"tool_choice":"required"}`),
			want:     true,
		},
		{
			name:     "required with only flattened image function",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":"required"}`),
			want:     true,
		},
		{
			name:     "required with only nested image function",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"function","function":{"name":"image_gen.imagegen"}}],"tool_choice":"required"}`),
			want:     true,
		},
		{
			name:     "required with mixed top-level tools is optional",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":"required"}`),
		},
		{
			name:     "required with mixed additional tools is optional",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"},{"type":"custom","name":"exec"}]}],"tool_choice":"required"}`),
		},
		{
			name:     "required without available tools is not image intent",
			endpoint: "/v1/responses",
			model:    "gpt-5.5",
			body:     []byte(`{"tool_choice":"required"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsRequiredImageGenerationIntent(tt.endpoint, tt.model, tt.body))
		})
	}
}

func TestIsRequiredImageGenerationIntentForPlatformPreservesGrokSemantics(t *testing.T) {
	passiveNamespace := []byte(`{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":"auto"}`)
	nativeImageTool := []byte(`{"model":"grok-4.5","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)

	require.False(t, IsRequiredImageGenerationIntentForPlatform(openAIResponsesEndpoint, "grok-4.5", passiveNamespace, PlatformGrok))
	require.True(t, IsRequiredImageGenerationIntentForPlatform(openAIResponsesEndpoint, "grok-4.5", nativeImageTool, PlatformGrok))
	require.False(t, IsRequiredImageGenerationIntentForPlatform(openAIResponsesEndpoint, "gpt-5.5", nativeImageTool, PlatformOpenAI))
}

func TestApplyOpenAIImageGenerationPolicyRejectsDuplicateSensitiveRootKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
		body []byte
	}{
		{
			name: "model safe first image second",
			key:  "model",
			body: []byte(`{"model":"gpt-5.5","model":"gpt-image-2","input":"write code"}`),
		},
		{
			name: "model image first safe second",
			key:  "model",
			body: []byte(`{"model":"gpt-image-2","model":"gpt-5.5","input":"write code"}`),
		},
		{
			name: "tools safe first image second",
			key:  "tools",
			body: []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"lookup"}],"tools":[{"type":"function","name":"image_gen.imagegen"}],"input":"write code","tool_choice":"auto"}`),
		},
		{
			name: "tools image first safe second",
			key:  "tools",
			body: []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"image_gen.imagegen"}],"tools":[{"type":"function","name":"lookup"}],"input":"write code","tool_choice":"auto"}`),
		},
		{
			name: "input safe first image second",
			key:  "input",
			body: []byte(`{"model":"gpt-5.5","input":"write code","input":[{"type":"additional_tools","tools":[{"type":"function","name":"image_gen.imagegen"}]}],"tool_choice":"auto"}`),
		},
		{
			name: "input image first safe second",
			key:  "input",
			body: []byte(`{"model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"function","name":"image_gen.imagegen"}]}],"input":"write code","tool_choice":"auto"}`),
		},
		{
			name: "tool choice safe first image second",
			key:  "tool_choice",
			body: []byte(`{"model":"gpt-5.5","input":"write code","tool_choice":"auto","tool_choice":{"type":"function","name":"image_gen.imagegen"}}`),
		},
		{
			name: "tool choice image first safe second",
			key:  "tool_choice",
			body: []byte(`{"model":"gpt-5.5","input":"write code","tool_choice":{"type":"function","name":"image_gen.imagegen"},"tool_choice":"auto"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := applyOpenAIImageGenerationPolicyToRawPayload(
				openAIResponsesEndpoint,
				"gpt-5.5",
				tt.body,
				PlatformOpenAI,
				true,
			)

			require.Error(t, err)
			var duplicateErr interface {
				error
				DuplicateKey() string
			}
			require.ErrorAs(t, err, &duplicateErr)
			require.Equal(t, tt.key, duplicateErr.DuplicateKey())
		})
	}
}

func TestValidateOpenAIImagePolicyPayloadRejectsDuplicateKeysRecursively(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		key  string
		path string
	}{
		{
			name: "arbitrary root field",
			body: []byte(`{"model":"gpt-5.5","metadata":{"safe":true},"metadata":{"safe":false}}`),
			key:  "metadata",
			path: "root",
		},
		{
			name: "nested tools field",
			body: []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup"}],"tools":[{"type":"function","name":"image_gen.imagegen"}]}]}`),
			key:  "tools",
			path: `root["input"][0]`,
		},
		{
			name: "nested input field",
			body: []byte(`{"metadata":{"input":"first","input":"second"}}`),
			key:  "input",
			path: `root["metadata"]`,
		},
		{
			name: "nested tool choice field",
			body: []byte(`{"metadata":{"tool_choice":"auto","tool_choice":"required"}}`),
			key:  "tool_choice",
			path: `root["metadata"]`,
		},
		{
			name: "nested function field",
			body: []byte(`{"tools":[{"type":"function","function":{"name":"lookup"},"function":{"name":"image_gen.imagegen"}}]}`),
			key:  "function",
			path: `root["tools"][0]`,
		},
		{
			name: "tool object",
			body: []byte(`{"tools":[{"type":"function","name":"lookup","name":"image_gen.imagegen"}]}`),
			key:  "name",
			path: `root["tools"][0]`,
		},
		{
			name: "input additional tools object",
			body: []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"code_tools","name":"image_gen"}]}]}`),
			key:  "name",
			path: `root["input"][0]["tools"][0]`,
		},
		{
			name: "tool choice object",
			body: []byte(`{"tool_choice":{"type":"function","name":"lookup","name":"image_gen.imagegen"}}`),
			key:  "name",
			path: `root["tool_choice"]`,
		},
		{
			name: "namespace child",
			body: []byte(`{"tools":[{"type":"namespace","name":"collaboration","children":[{"type":"function","name":"lookup","name":"imagegen"}]}]}`),
			key:  "name",
			path: `root["tools"][0]["children"][0]`,
		},
		{
			name: "function child",
			body: []byte(`{"tools":[{"type":"function","function":{"name":"lookup","name":"image_gen.imagegen"}}]}`),
			key:  "name",
			path: `root["tools"][0]["function"]`,
		},
		{
			name: "escaped ancestor key has safe path",
			body: []byte(`{"metadata\nx":{"value":1,"value":2}}`),
			key:  "value",
			path: `root["metadata\nx"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOpenAIImagePolicyPayload(tt.body)

			require.Error(t, err)
			var duplicateErr OpenAIImagePolicyDuplicateKeyError
			require.ErrorAs(t, err, &duplicateErr)
			require.Equal(t, tt.key, duplicateErr.DuplicateKey())
			require.Equal(t, tt.path, duplicateErr.ContextPath())
			require.NotContains(t, duplicateErr.ContextPath(), "\n")
			if tt.path == "root" {
				var rootErr *OpenAIImagePolicyDuplicateRootKeyError
				require.ErrorAs(t, err, &rootErr)
				require.Contains(t, err.Error(), `duplicate root key "`+tt.key+`"`)
			} else {
				var nestedErr *OpenAIImagePolicyDuplicateNestedKeyError
				require.ErrorAs(t, err, &nestedErr)
			}
		})
	}
}

func TestValidateOpenAIImagePolicyPayloadAllowsSameKeysInSiblingObjects(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"function","name":"lookup","function":{"name":"lookup"}},
			{"type":"function","name":"image_gen.imagegen","function":{"name":"image_gen.imagegen"}}
		],
		"input":[
			{"type":"message","role":"user"},
			{"type":"message","role":"assistant"}
		]
	}`)

	require.NoError(t, ValidateOpenAIImagePolicyPayload(body))
}

func TestValidateOpenAIImagePolicyPayloadAllowsDuplicatePreviousResponseIDForRecovery(t *testing.T) {
	body := []byte(`{"type":"response.create","previous_response_id":"resp_first","input":[],"previous_response_id":"resp_second"}`)

	require.NoError(t, ValidateOpenAIImagePolicyPayload(body))
}

func TestResolveOpenAIResponsesImageBillingConfigUsesCurrentBodyModel(t *testing.T) {
	imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"mapped-image-model","tools":[{"type":"image_generation","size":"1024x1024"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "mapped-image-model", imageModel)
	require.Equal(t, "1K", imageSize)
}

func TestResolveOpenAIResponsesImageBillingConfigToolModelWins(t *testing.T) {
	imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"mapped-text-model","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1536x1024"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", imageModel)
	require.Equal(t, "2K", imageSize)
}

func TestResolveOpenAIResponsesImageBillingConfigFromBodyIgnoresUnrelatedLargeInput(t *testing.T) {
	cfg, err := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(
		[]byte(`{"model":"mapped-text-model","tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}],"input":[{"type":"message","content":[{"type":"input_text","text":"hi","nonce":1e1000000}]}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-2", cfg.Model)
	require.Equal(t, "2K", cfg.SizeTier)
	require.Equal(t, "2048x1152", cfg.InputSize)
}

func TestResolveOpenAIResponsesImageBillingConfigSupportsOfficialAndCustomSizes(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		wantTier string
	}{
		{
			name:     "official 2k landscape",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-2","size":"2048x1152"}]}`),
			wantTier: "2K",
		},
		{
			name:     "official 4k landscape",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-2","size":"3840x2160"}]}`),
			wantTier: "4K",
		},
		{
			name:     "custom valid 2k",
			body:     []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1280x768"}]}`),
			wantTier: "2K",
		},
		{
			name:     "default image tool model supports flexible size",
			body:     []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","size":"2048x1152"}]}`),
			wantTier: "2K",
		},
		{
			name:     "top level image size is moved into billing",
			body:     []byte(`{"model":"gpt-image-2","size":"2048x2048","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`),
			wantTier: "2K",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(tt.body, "requested-model")
			require.NoError(t, err)
			require.NotEmpty(t, imageModel)
			require.Equal(t, tt.wantTier, imageSize)
		})
	}
}

func TestResolveOpenAIResponsesImageBillingConfigDoesNotRejectUnknownSizes(t *testing.T) {
	imageModel, imageSize, err := resolveOpenAIResponsesImageBillingConfigFromBody(
		[]byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","model":"gpt-image-1.5","size":"2048x1152"}]}`),
		"requested-model",
	)
	require.NoError(t, err)
	require.Equal(t, "gpt-image-1.5", imageModel)
	require.Equal(t, "2K", imageSize)
}

func TestOpenAIImageOutputCounterDeduplicatesFinalImages(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData([]byte(`{"type":"response.image_generation_call.partial_image","partial_image_b64":"abc"}`))
	counter.AddSSEData([]byte(`{"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","result":"final-a","size":"1024x1024"}}`))
	counter.AddSSEData([]byte(`{"type":"response.completed","response":{"output":[{"id":"ig_1","type":"image_generation_call","result":"final-a"},{"id":"ig_2","type":"image_generation_call","result":"final-b","size":"3840x2160"}]}}`))
	require.Equal(t, 2, counter.Count())
	require.Equal(t, []string{"1024x1024", "3840x2160"}, counter.Sizes())
}

func TestOpenAIImageOutputCounterCountsImagesAPIStreamShapes(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData([]byte(`{"type":"image_generation.completed","id":"ig_complete","b64_json":"final-a"}`))
	counter.AddSSEData([]byte(`{"type":"response.output_item.done","item":{"id":"ig_item","type":"image_generation_call","result":"final-b"}}`))
	counter.AddSSEData([]byte(`{"type":"response.completed","response":{"output":[{"id":"ig_done","type":"image_generation_call","result":"final-c"}]}}`))
	require.Equal(t, 3, counter.Count())

	dataCounter := newOpenAIImageOutputCounter()
	dataCounter.AddSSEData([]byte(`{"data":[{"b64_json":"a"},{"b64_json":"b"}]}`))
	dataCounter.AddSSEData([]byte(`{"data":[{"b64_json":"a"},{"b64_json":"b"},{"b64_json":"c"}]}`))
	require.Equal(t, 3, dataCounter.Count())
}

func TestOpenAIImageOutputCounterCountsMultilineSSEDataPayload(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEData([]byte("{\"type\":\"image_generation.completed\",\n\"b64_json\":\"final-a\"}"))
	require.Equal(t, 1, counter.Count())
}

func TestOpenAIImageOutputCounterCountsMultilineSSEBodyPayload(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(
		"data: {\"type\":\"image_generation.completed\",\n" +
			"data: \"b64_json\":\"final-a\"}\n\n" +
			"data: [DONE]\n\n",
	)
	require.Equal(t, 1, counter.Count())
}

func TestOpenAIImageOutputCounterFallsBackForInvalidMultilineSSEBody(t *testing.T) {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(
		"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final-a\"}\n" +
			"data: {\"type\":\"image_generation.completed\",\"b64_json\":\"final-b\"}\n\n",
	)
	require.Equal(t, 2, counter.Count())
}

func TestCollectOpenAIResponseImageOutputSizesFromJSONBytes(t *testing.T) {
	body := []byte(`{
		"output": [
			{"id":"ig_1","type":"image_generation_call","result":"final-a","size":"3840x2160"},
			{"id":"ig_2","type":"image_generation_call","result":"final-b","size":"1024x1024"}
		]
	}`)

	require.Equal(t, 2, countOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"3840x2160", "1024x1024"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}

func TestCollectOpenAIResponseImageOutputSizesFromImagesAPIData(t *testing.T) {
	body := []byte(`{
		"data": [
			{"b64_json":"final-a","size":"2048x1152"},
			{"b64_json":"final-b","size":"2048x1152"}
		]
	}`)

	require.Equal(t, 2, countOpenAIResponseImageOutputsFromJSONBytes(body))
	require.Equal(t, []string{"2048x1152", "2048x1152"}, collectOpenAIResponseImageOutputSizesFromJSONBytes(body))
}

func TestCollectOpenAIImageOutputSizesFromSSEBody(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\",\"size\":\"3840x2160\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"ig_1\",\"type\":\"image_generation_call\",\"result\":\"final-a\"},{\"id\":\"ig_2\",\"type\":\"image_generation_call\",\"result\":\"final-b\",\"size\":\"1024x1024\"}]}}\n\n" +
		"data: [DONE]\n\n"

	require.Equal(t, 2, countOpenAIImageOutputsFromSSEBody(body))
	require.Equal(t, []string{"3840x2160", "1024x1024"}, collectOpenAIImageOutputSizesFromSSEBody(body))
}
