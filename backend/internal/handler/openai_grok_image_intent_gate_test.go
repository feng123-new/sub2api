package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayHandlerResponses_GrokPassiveImageToolDeclarationBypassesPermissionGate(t *testing.T) {
	body := `{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto","input":"write code"}`
	rec := runOpenAIResponsesImagePermissionGateTest(t, service.PlatformGrok, body)

	require.NotEqual(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}

func TestOpenAIGatewayHandlerResponses_GrokResponsesLiteImageToolDeclarationBypassesPermissionGate(t *testing.T) {
	body := `{"model":"grok-4.5","tool_choice":"auto","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]},{"type":"message","role":"user","content":"write code"}]}`
	rec := runOpenAIResponsesImagePermissionGateTest(t, service.PlatformGrok, body)

	require.NotEqual(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
}

func TestOpenAIGatewayHandlerResponses_OpenAIOptionalImageDeclarationsBypassPermissionGate(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "passive top-level image namespace",
			body: `{"model":"gpt-5.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto","input":"write code"}`,
		},
		{
			name: "Responses Lite additional tools namespace",
			body: `{"model":"gpt-5.5","tool_choice":"auto","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}]},{"type":"message","role":"user","content":"write code"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := runOpenAIResponsesImagePermissionGateTest(t, service.PlatformOpenAI, tt.body)

			require.NotEqual(t, http.StatusForbidden, rec.Code)
			require.NotContains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
		})
	}
}

func TestOpenAIGatewayHandlerResponses_ImagePermissionHardSignalsStillRejected(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		body     string
	}{
		{
			name:     "Grok native image_generation declaration",
			platform: service.PlatformGrok,
			body:     `{"model":"grok-4.5","tools":[{"type":"image_generation"}],"input":"draw"}`,
		},
		{
			name:     "Grok explicit image_gen tool choice",
			platform: service.PlatformGrok,
			body:     `{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen"}],"tool_choice":{"type":"namespace","name":"image_gen"},"input":"draw"}`,
		},
		{
			name:     "OpenAI native image_generation tool",
			platform: service.PlatformOpenAI,
			body:     `{"model":"gpt-5.5","tools":[{"type":"image_generation","model":"gpt-image-2"},{"type":"function","name":"lookup"}],"tool_choice":"auto","input":"draw a cat"}`,
		},
		{
			name:     "OpenAI explicit image tool choice",
			platform: service.PlatformOpenAI,
			body:     `{"model":"gpt-5.5","tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"},"input":"draw"}`,
		},
		{
			name:     "OpenAI image-only required choice",
			platform: service.PlatformOpenAI,
			body:     `{"model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"image_gen"}]}],"tool_choice":"required"}`,
		},
		{
			name:     "OpenAI image model",
			platform: service.PlatformOpenAI,
			body:     `{"model":"gpt-image-2","input":"draw a cat"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := runOpenAIResponsesImagePermissionGateTest(t, tt.platform, tt.body)

			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Contains(t, rec.Body.String(), service.ImageGenerationPermissionMessage())
		})
	}
}

func TestOpenAIGatewayHandlerResponses_RejectsDuplicateImagePolicyRootKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
		body string
	}{
		{name: "model safe first image second", key: "model", body: `{"model":"gpt-5.5","model":"gpt-image-2","input":"write code"}`},
		{name: "model image first safe second", key: "model", body: `{"model":"gpt-image-2","model":"gpt-5.5","input":"write code"}`},
		{name: "tools safe first image second", key: "tools", body: `{"model":"gpt-5.5","tools":[{"type":"function","name":"lookup"}],"tools":[{"type":"function","name":"image_gen.imagegen"}],"input":"write code","tool_choice":"auto"}`},
		{name: "tools image first safe second", key: "tools", body: `{"model":"gpt-5.5","tools":[{"type":"function","name":"image_gen.imagegen"}],"tools":[{"type":"function","name":"lookup"}],"input":"write code","tool_choice":"auto"}`},
		{name: "input safe first image second", key: "input", body: `{"model":"gpt-5.5","input":"write code","input":[{"type":"additional_tools","tools":[{"type":"function","name":"image_gen.imagegen"}]}],"tool_choice":"auto"}`},
		{name: "input image first safe second", key: "input", body: `{"model":"gpt-5.5","input":[{"type":"additional_tools","tools":[{"type":"function","name":"image_gen.imagegen"}]}],"input":"write code","tool_choice":"auto"}`},
		{name: "tool choice safe first image second", key: "tool_choice", body: `{"model":"gpt-5.5","input":"write code","tool_choice":"auto","tool_choice":{"type":"function","name":"image_gen.imagegen"}}`},
		{name: "tool choice image first safe second", key: "tool_choice", body: `{"model":"gpt-5.5","input":"write code","tool_choice":{"type":"function","name":"image_gen.imagegen"},"tool_choice":"auto"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := runOpenAIResponsesImagePermissionGateTest(t, service.PlatformOpenAI, tt.body)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), `duplicate root key \"`+tt.key+`\"`)
		})
	}
}

func runOpenAIResponsesImagePermissionGateTest(t *testing.T, platform string, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(6301)
	userID := int64(6302)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      6303,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                   groupID,
			Platform:             platform,
			AllowImageGeneration: false,
		},
		User: &service.User{ID: userID, Status: service.StatusActive},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID, Concurrency: 1})

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil),
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper: &ConcurrencyHelper{concurrencyService: service.NewConcurrencyService(
			&helperConcurrencyCacheStub{userSeq: []bool{true}},
		)},
		cfg:          &config.Config{},
		imageLimiter: &imageConcurrencyLimiter{},
	}

	h.Responses(c)
	return rec
}
