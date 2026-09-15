package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyOpenAIUpstreamPolicyRejection(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantReason string
		wantStatus int
		wantHit    bool
	}{
		{
			name:       "invalid prompt wrapped as bad gateway",
			status:     http.StatusBadGateway,
			body:       `{"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"Your prompt was flagged as potentially violating our usage policy."}}`,
			wantReason: "invalid_prompt",
			wantStatus: http.StatusBadRequest,
			wantHit:    true,
		},
		{
			name:       "content policy wrapped as bad gateway",
			status:     http.StatusBadGateway,
			body:       `{"error":{"type":"content_policy_violation","message":"blocked by content policy"}}`,
			wantReason: "content_policy",
			wantStatus: http.StatusForbidden,
			wantHit:    true,
		},
		{
			name:       "safety error preserves forbidden",
			status:     http.StatusForbidden,
			body:       `{"error":{"type":"safety_error","message":"request blocked"}}`,
			wantReason: "safety_error",
			wantStatus: http.StatusForbidden,
			wantHit:    true,
		},
		{
			name:       "cyber policy wrapped as bad gateway",
			status:     http.StatusBadGateway,
			body:       `{"error":{"code":"cyber_policy","message":"blocked by policy"}}`,
			wantReason: "cyber_policy",
			wantStatus: http.StatusForbidden,
			wantHit:    true,
		},
		{
			name:    "generic policy wording is not enough",
			status:  http.StatusForbidden,
			body:    `{"error":{"type":"permission_error","message":"policy configuration denied access"}}`,
			wantHit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejection, hit := classifyOpenAIUpstreamPolicyRejection(tt.status, "", []byte(tt.body))
			require.Equal(t, tt.wantHit, hit)
			if !tt.wantHit {
				return
			}
			require.Equal(t, tt.wantReason, rejection.Reason)
			require.Equal(t, tt.wantStatus, rejection.ClientStatus)
			require.Equal(t, OpenAIUpstreamPolicyRejectionErrorType, rejection.ErrorType)
		})
	}
}

func TestOpenAIUpstreamPolicyRejectionDoesNotFailoverOrAffectAccountHealth(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"prompt flagged"}}`)
	account := &Account{ID: 901, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}

	require.False(t, shouldFailoverOpenAIPassthroughResponse(account, http.StatusBadGateway, body))
	require.False(t, (&OpenAIGatewayService{}).shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadGateway, "", body))

	repo := &openAIStream403AccountRepo{}
	rates := NewRateLimitService(repo, nil, nil, nil, nil)
	svc := &OpenAIGatewayService{rateLimitService: rates}
	require.False(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, nil, body, "gpt-5.6-sol"))
	require.Zero(t, repo.setErrorCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIContextPreflightUnsupportedShapeIsExplicitlySkipped(t *testing.T) {
	preflight := &openAIContextPreflight{
		mode:           "enforce",
		thresholdBPS:   9000,
		billingService: openAIContextPreflightLimitLookup{"gpt-5.5": {MaxInputTokens: 100}},
	}
	result := preflight.evaluate(openAIContextPreflightInput{
		RequestID:  "req-unsupported-shape",
		Endpoint:   openAIContextPreflightEndpointResponses,
		Body:       []byte(`{"model":"gpt-5.5","input":[{"type":"future_item","text":"hello"}]}`),
		FinalModel: "gpt-5.5",
	})

	require.Equal(t, openAIContextPreflightDecisionSkipped, result.Decision)
	require.Equal(t, openAIContextPreflightSkipUnsupportedShape, result.SkipReason)
	require.False(t, result.Reject)
	require.Nil(t, result.Rejection)
}

func TestEstimateOpenAIInputTokensCompleteResponsesAcceptsNestedToolAndTextShapes(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"instructions":"Follow the tool result.",
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run the tool"}]},
			{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"first result"},{"type":"output_text","text":"second result"}]}
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
