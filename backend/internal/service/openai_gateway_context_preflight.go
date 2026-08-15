package service

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type openAIContextPreflightEndpoint string

const (
	openAIContextPreflightEndpointResponses       openAIContextPreflightEndpoint = "responses"
	openAIContextPreflightEndpointChatCompletions openAIContextPreflightEndpoint = "chat_completions"
)

type openAIContextPreflightDecision string

const (
	openAIContextPreflightDecisionOff         openAIContextPreflightDecision = "off"
	openAIContextPreflightDecisionAllowed     openAIContextPreflightDecision = "allowed"
	openAIContextPreflightDecisionSkipped     openAIContextPreflightDecision = "skipped"
	openAIContextPreflightDecisionWouldReject openAIContextPreflightDecision = "would_reject"
	openAIContextPreflightDecisionRejected    openAIContextPreflightDecision = "rejected"
)

type openAIContextPreflightSkipReason string

const (
	openAIContextPreflightSkipUnknownModel           openAIContextPreflightSkipReason = "unknown_model"
	openAIContextPreflightSkipMissingModelLimit      openAIContextPreflightSkipReason = "missing_model_limit"
	openAIContextPreflightSkipModelMappingUnresolved openAIContextPreflightSkipReason = "model_mapping_unresolved"
	openAIContextPreflightSkipUnsupportedEncoding    openAIContextPreflightSkipReason = "unsupported_encoding"
	openAIContextPreflightSkipUnsupportedShape       openAIContextPreflightSkipReason = "unsupported_shape"
	openAIContextPreflightSkipCountFailed            openAIContextPreflightSkipReason = "count_failed"
)

type openAIContextPreflightEstimator func(
	body []byte,
	endpoint openAIContextPreflightEndpoint,
	model string,
) (tokens int, complete bool, reason openAIContextPreflightSkipReason)

const openAIContextPreflightWorkBudget = 50 * time.Millisecond

type openAIContextPreflightInput struct {
	RequestID  string
	Endpoint   openAIContextPreflightEndpoint
	Body       []byte
	FinalModel string
}

type openAIContextPreflightRejection struct {
	StatusCode           int    `json:"-"`
	Type                 string `json:"type"`
	Code                 string `json:"code"`
	Param                string `json:"param"`
	Message              string `json:"message"`
	Model                string `json:"model"`
	EstimatedInputTokens int    `json:"estimated_input_tokens"`
	ThresholdTokens      int    `json:"threshold_tokens"`
}

func (r *openAIContextPreflightRejection) Error() string {
	if r == nil {
		return "openai context preflight rejected"
	}
	return "openai context preflight rejected: " + r.Message
}

type openAIContextPreflightResult struct {
	Decision             openAIContextPreflightDecision
	Reject               bool
	SkipReason           openAIContextPreflightSkipReason
	Model                string
	EstimatedInputTokens int
	ModelInputLimit      int
	ThresholdBPS         int
	ThresholdTokens      int
	Rejection            *openAIContextPreflightRejection
}

type openAIContextLimitLookup interface {
	GetExactModelContextLimits(string) (ModelContextLimits, bool)
}

type openAIContextPreflight struct {
	mode           string
	thresholdBPS   int
	models         map[string]struct{}
	billingService openAIContextLimitLookup
	estimate       openAIContextPreflightEstimator
	now            func() time.Time
}

type openAIContextPreflightEvaluationOptions struct {
	emitRolloutEvent bool
}

func newOpenAIContextPreflight(cfg *config.Config, billingService *BillingService) *openAIContextPreflight {
	mode := "off"
	threshold := 0.90
	var configuredModels []string
	if cfg != nil {
		configured := cfg.Gateway.ContextPreflight
		if normalizedMode := strings.ToLower(strings.TrimSpace(configured.Mode)); normalizedMode != "" {
			mode = normalizedMode
		}
		if configured.Threshold > 0 {
			threshold = configured.Threshold
		}
		configuredModels = configured.Models
	}
	if mode != "shadow" && mode != "enforce" {
		mode = "off"
	}

	models := make(map[string]struct{}, len(configuredModels))
	for _, model := range configuredModels {
		normalized := normalizeOpenAIContextPreflightModel(model)
		if normalized != "" {
			models[normalized] = struct{}{}
		}
	}
	if mode != "off" {
		warmOpenAIContextPreflightCodecs(models)
	}

	return &openAIContextPreflight{
		mode:           mode,
		thresholdBPS:   int(math.Round(threshold * 10000)),
		models:         models,
		billingService: billingService,
		now:            time.Now,
	}
}

func (p *openAIContextPreflight) evaluate(input openAIContextPreflightInput) openAIContextPreflightResult {
	return p.evaluateWithOptions(input, openAIContextPreflightEvaluationOptions{emitRolloutEvent: true})
}

func (p *openAIContextPreflight) evaluateWithOptions(
	input openAIContextPreflightInput,
	options openAIContextPreflightEvaluationOptions,
) openAIContextPreflightResult {
	result := openAIContextPreflightResult{Decision: openAIContextPreflightDecisionOff}
	if p == nil || p.mode == "off" {
		return result
	}

	startedAt := time.Now()
	result.ThresholdBPS = p.thresholdBPS
	result.Model = normalizeOpenAIContextPreflightModel(input.FinalModel)
	if len(p.models) > 0 {
		if _, allowed := p.models[result.Model]; !allowed {
			return p.skipped(input, result, openAIContextPreflightSkipUnknownModel, startedAt, options)
		}
	}
	if p.billingService == nil {
		return p.skipped(input, result, openAIContextPreflightSkipMissingModelLimit, startedAt, options)
	}
	limits, found := p.billingService.GetExactModelContextLimits(result.Model)
	if !found {
		return p.skipped(input, result, openAIContextPreflightSkipMissingModelLimit, startedAt, options)
	}
	result.ModelInputLimit = limits.MaxInputTokens
	if len(input.Body) > openAICompleteRequestBodyMaxBytes {
		return p.skipped(input, result, openAIContextPreflightSkipCountFailed, startedAt, options)
	}

	now := p.now
	if now == nil {
		now = time.Now
	}
	workBudget := &openAICompleteWorkBudget{now: now, deadline: now().Add(openAIContextPreflightWorkBudget)}
	request, bodyModel, bodyValid, bodyMalformed, err := readOpenAIContextPreflightBodyModel(input.Body, workBudget)
	if err != nil || bodyMalformed {
		return p.skipped(input, result, openAIContextPreflightSkipCountFailed, startedAt, options)
	}
	if result.Model == "" || !bodyValid || normalizeOpenAIContextPreflightModel(bodyModel) != result.Model {
		return p.skipped(input, result, openAIContextPreflightSkipModelMappingUnresolved, startedAt, options)
	}

	var estimated int
	var complete bool
	var reason openAIContextPreflightSkipReason
	if p.estimate != nil {
		if workBudget.check() != nil {
			return p.skipped(input, result, openAIContextPreflightSkipCountFailed, startedAt, options)
		}
		estimated, complete, reason = p.estimate(input.Body, input.Endpoint, result.Model)
		if workBudget.check() != nil {
			return p.skipped(input, result, openAIContextPreflightSkipCountFailed, startedAt, options)
		}
	} else {
		estimated, complete, reason = estimateOpenAIInputTokensCompleteBudgeted(request, input.Endpoint, result.Model, workBudget)
	}
	if !complete || estimated < 0 {
		if estimated < 0 {
			reason = openAIContextPreflightSkipCountFailed
		}
		return p.skipped(input, result, boundedOpenAIContextPreflightSkipReason(reason), startedAt, options)
	}

	result.EstimatedInputTokens = estimated
	result.ThresholdTokens = (result.ModelInputLimit/10000)*result.ThresholdBPS +
		((result.ModelInputLimit%10000)*result.ThresholdBPS)/10000
	if estimated < result.ThresholdTokens {
		result.Decision = openAIContextPreflightDecisionAllowed
		if p.mode == "shadow" && options.emitRolloutEvent {
			p.logThresholdDecision("context_preflight_allowed", input, result, startedAt)
		}
		return result
	}

	result.Rejection = &openAIContextPreflightRejection{
		StatusCode:           http.StatusBadRequest,
		Type:                 "invalid_request_error",
		Code:                 "context_length_exceeded",
		Param:                "input",
		Message:              "The gateway estimate of the input is at or above this gateway's configured context threshold. Shorten the conversation history or start a new conversation.",
		Model:                result.Model,
		EstimatedInputTokens: result.EstimatedInputTokens,
		ThresholdTokens:      result.ThresholdTokens,
	}
	if p.mode == "shadow" {
		result.Decision = openAIContextPreflightDecisionWouldReject
		if options.emitRolloutEvent {
			p.logThresholdDecision("context_preflight_shadow_reject", input, result, startedAt)
		}
		return result
	}
	result.Decision = openAIContextPreflightDecisionRejected
	result.Reject = true
	p.logThresholdDecision("context_preflight_rejected", input, result, startedAt)
	return result
}

func (s *OpenAIGatewayService) runOpenAIContextPreflight(
	ctx context.Context,
	c *gin.Context,
	endpoint openAIContextPreflightEndpoint,
	body []byte,
	finalModel string,
) error {
	return s.runOpenAIContextPreflightWithOptions(ctx, c, endpoint, body, finalModel, openAIContextPreflightEvaluationOptions{emitRolloutEvent: true})
}

func (s *OpenAIGatewayService) runOpenAIContextPreflightSilently(
	ctx context.Context,
	c *gin.Context,
	endpoint openAIContextPreflightEndpoint,
	body []byte,
	finalModel string,
) error {
	return s.runOpenAIContextPreflightWithOptions(ctx, c, endpoint, body, finalModel, openAIContextPreflightEvaluationOptions{})
}

func (s *OpenAIGatewayService) runOpenAIWSContextPreflight(
	ctx context.Context,
	c *gin.Context,
	body []byte,
	finalModel string,
) error {
	if s == nil || s.contextPreflight == nil {
		return nil
	}
	result := s.contextPreflight.evaluate(openAIContextPreflightInput{
		RequestID:  openAIContextPreflightRequestID(ctx, c),
		Endpoint:   openAIContextPreflightEndpointResponses,
		Body:       body,
		FinalModel: finalModel,
	})
	if !result.Reject || result.Rejection == nil {
		return nil
	}
	return NewOpenAIWSClientCloseError(
		coderws.StatusPolicyViolation,
		result.Rejection.Message,
		result.Rejection,
	)
}

func (s *OpenAIGatewayService) runOpenAIContextPreflightWithOptions(
	ctx context.Context,
	c *gin.Context,
	endpoint openAIContextPreflightEndpoint,
	body []byte,
	finalModel string,
	options openAIContextPreflightEvaluationOptions,
) error {
	if s == nil || s.contextPreflight == nil {
		return nil
	}
	result := s.contextPreflight.evaluateWithOptions(openAIContextPreflightInput{
		RequestID:  openAIContextPreflightRequestID(ctx, c),
		Endpoint:   endpoint,
		Body:       body,
		FinalModel: finalModel,
	}, options)
	if !result.Reject || result.Rejection == nil {
		return nil
	}
	if c != nil && !c.Writer.Written() {
		c.JSON(http.StatusBadRequest, gin.H{"error": result.Rejection})
	}
	return result.Rejection
}

func openAIContextPreflightRequestID(ctx context.Context, c *gin.Context) string {
	if requestID, _ := ctx.Value(ctxkey.RequestID).(string); strings.TrimSpace(requestID) != "" {
		return strings.TrimSpace(requestID)
	}
	if c != nil && c.Request != nil {
		if requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string); strings.TrimSpace(requestID) != "" {
			return strings.TrimSpace(requestID)
		}
		return strings.TrimSpace(c.GetHeader("X-Request-ID"))
	}
	return ""
}

func (p *openAIContextPreflight) skipped(input openAIContextPreflightInput, result openAIContextPreflightResult, reason openAIContextPreflightSkipReason, startedAt time.Time, options openAIContextPreflightEvaluationOptions) openAIContextPreflightResult {
	result.Decision = openAIContextPreflightDecisionSkipped
	result.SkipReason = boundedOpenAIContextPreflightSkipReason(reason)
	if !options.emitRolloutEvent {
		return result
	}
	fields := []zap.Field{
		zap.String("request_id", strings.TrimSpace(input.RequestID)),
		zap.String("mode", p.mode),
		zap.String("decision", string(result.Decision)),
		zap.String("skip_reason", string(result.SkipReason)),
		zap.String("endpoint", string(input.Endpoint)),
		zap.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	}
	if result.ModelInputLimit > 0 {
		fields = append(fields, zap.String("model", result.Model))
	}
	logger.L().Named("service.openai_gateway").Info("context_preflight_skipped", fields...)
	return result
}

func (p *openAIContextPreflight) logThresholdDecision(event string, input openAIContextPreflightInput, result openAIContextPreflightResult, startedAt time.Time) {
	encoding, _ := strictOpenAIInputTokensEncodingForModel(result.Model)
	logger.L().Named("service.openai_gateway").Info(event,
		zap.String("request_id", strings.TrimSpace(input.RequestID)),
		zap.String("model", result.Model),
		zap.String("mode", p.mode),
		zap.String("decision", string(result.Decision)),
		zap.String("encoding", string(encoding)),
		zap.Int("estimated_input_tokens", result.EstimatedInputTokens),
		zap.Int("model_input_limit", result.ModelInputLimit),
		zap.Int("threshold_bps", result.ThresholdBPS),
		zap.Int("threshold_tokens", result.ThresholdTokens),
		zap.String("endpoint", string(input.Endpoint)),
		zap.Int64("duration_ms", time.Since(startedAt).Milliseconds()),
	)
}

func normalizeOpenAIContextPreflightModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func readOpenAIContextPreflightBodyModel(body []byte, workBudget *openAICompleteWorkBudget) (request map[string]json.RawMessage, model string, valid bool, malformed bool, err error) {
	if unmarshalErr := json.Unmarshal(body, &request); unmarshalErr != nil {
		if budgetErr := workBudget.check(); budgetErr != nil {
			return nil, "", false, false, budgetErr
		}
		return nil, "", false, true, nil
	}
	if budgetErr := workBudget.check(); budgetErr != nil {
		return nil, "", false, false, budgetErr
	}
	if request == nil {
		return nil, "", false, false, nil
	}
	raw, ok := request["model"]
	if !ok {
		return request, "", false, false, nil
	}
	if unmarshalErr := json.Unmarshal(raw, &model); unmarshalErr != nil || strings.TrimSpace(model) == "" {
		if budgetErr := workBudget.check(); budgetErr != nil {
			return nil, "", false, false, budgetErr
		}
		return request, "", false, false, nil
	}
	if budgetErr := workBudget.check(); budgetErr != nil {
		return nil, "", false, false, budgetErr
	}
	return request, model, true, false, nil
}

func boundedOpenAIContextPreflightSkipReason(reason openAIContextPreflightSkipReason) openAIContextPreflightSkipReason {
	switch reason {
	case openAIContextPreflightSkipUnknownModel,
		openAIContextPreflightSkipMissingModelLimit,
		openAIContextPreflightSkipModelMappingUnresolved,
		openAIContextPreflightSkipUnsupportedEncoding,
		openAIContextPreflightSkipUnsupportedShape,
		openAIContextPreflightSkipCountFailed:
		return reason
	default:
		return openAIContextPreflightSkipCountFailed
	}
}
