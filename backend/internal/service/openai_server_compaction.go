package service

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIServerCompactionDecision string

const (
	openAIServerCompactionDecisionOff               openAIServerCompactionDecision = "off"
	openAIServerCompactionDecisionNoThreshold       openAIServerCompactionDecision = "no_threshold"
	openAIServerCompactionDecisionWouldInject       openAIServerCompactionDecision = "would_inject"
	openAIServerCompactionDecisionInjected          openAIServerCompactionDecision = "injected"
	openAIServerCompactionDecisionClientConfigured  openAIServerCompactionDecision = "client_configured"
	openAIServerCompactionDecisionCompactionTrigger openAIServerCompactionDecision = "compaction_trigger"
	openAIServerCompactionDecisionCompactRequest    openAIServerCompactionDecision = "compact_request"
	openAIServerCompactionDecisionResponsesLite     openAIServerCompactionDecision = "responses_lite"
)

var openAIServerCompactionDecisionLogs sync.Map

func applyOpenAIServerCompaction(
	body []byte,
	model string,
	compactRequest bool,
	responsesLite bool,
	cfg config.GatewayOpenAIServerCompactionConfig,
) ([]byte, openAIServerCompactionDecision, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" || mode == "off" {
		return body, openAIServerCompactionDecisionOff, nil
	}
	threshold := openAIServerCompactionThreshold(cfg, model)
	if threshold <= 0 {
		return body, openAIServerCompactionDecisionNoThreshold, nil
	}
	if responsesLite {
		return body, openAIServerCompactionDecisionResponsesLite, nil
	}
	if compactRequest {
		return body, openAIServerCompactionDecisionCompactRequest, nil
	}
	if HasCompactionTriggerInInput(body) {
		return body, openAIServerCompactionDecisionCompactionTrigger, nil
	}
	if gjson.GetBytes(body, "context_management").Exists() {
		return body, openAIServerCompactionDecisionClientConfigured, nil
	}
	if mode == "shadow" {
		return body, openAIServerCompactionDecisionWouldInject, nil
	}
	if mode != "enforce" {
		return body, openAIServerCompactionDecisionOff, nil
	}
	injected, err := sjson.SetBytes(body, "context_management", []map[string]any{{
		"type":              "compaction",
		"compact_threshold": threshold,
	}})
	if err != nil {
		return nil, openAIServerCompactionDecisionOff, fmt.Errorf("inject OpenAI server compaction: %w", err)
	}
	return injected, openAIServerCompactionDecisionInjected, nil
}

func openAIServerCompactionThreshold(cfg config.GatewayOpenAIServerCompactionConfig, model string) int64 {
	model = strings.ToLower(strings.TrimSpace(model))
	if threshold, ok := cfg.ModelThresholds[model]; ok {
		return threshold
	}
	return cfg.DefaultThreshold
}

func logOpenAIServerCompactionDecisionOnce(
	decision openAIServerCompactionDecision,
	model string,
	threshold int64,
) {
	if decision != openAIServerCompactionDecisionInjected && decision != openAIServerCompactionDecisionWouldInject {
		return
	}
	key := fmt.Sprintf("%s:%s:%d", decision, strings.ToLower(strings.TrimSpace(model)), threshold)
	if _, loaded := openAIServerCompactionDecisionLogs.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	logger.LegacyPrintf(
		"service.openai_gateway",
		"[OpenAI] Server compaction decision=%s model=%s compact_threshold=%d",
		decision,
		model,
		threshold,
	)
}

func removeRejectedInjectedOpenAIServerCompaction(statusCode int, body, responseBody []byte) ([]byte, bool, error) {
	if statusCode != http.StatusBadRequest || !gjson.GetBytes(body, "context_management").Exists() {
		return body, false, nil
	}
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(responseBody)))
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
	param := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.param").String()))
	if param == "" && strings.Contains(message, "context_management") {
		param = "context_management"
	}
	explicitFieldRejection := isExplicitOpenAIResponsesFieldRejection(code, message) && param == "context_management"
	unsupportedServerCompaction := code == "unsupported_value" &&
		param == "compact_threshold" &&
		strings.Contains(message, "does not support server-side compaction")
	if !explicitFieldRejection && !unsupportedServerCompaction {
		return body, false, nil
	}
	retryBody, err := sjson.DeleteBytes(body, "context_management")
	if err != nil {
		return nil, false, fmt.Errorf("delete rejected injected context_management: %w", err)
	}
	return retryBody, true, nil
}
