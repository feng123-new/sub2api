package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// OpenAIUpstreamPolicyRejectionErrorType is the stable Ops classification for
// provider policy decisions. It is deliberately separate from provider error
// codes so dashboards do not confuse policy decisions with outages.
const OpenAIUpstreamPolicyRejectionErrorType = "upstream_policy_rejection"

const opsUpstreamErrorClassificationKey = "ops_upstream_error_classification"

type openAIUpstreamPolicyRejection struct {
	ErrorType    string
	Reason       string
	Message      string
	ClientStatus int
}

func SetOpsUpstreamErrorClassification(c *gin.Context, classification string) {
	if c == nil {
		return
	}
	classification = strings.TrimSpace(classification)
	if classification == "" {
		return
	}
	c.Set(opsUpstreamErrorClassificationKey, classification)
}

func GetOpsUpstreamErrorClassification(c *gin.Context) string {
	if c == nil {
		return ""
	}
	classification, _ := c.Get(opsUpstreamErrorClassificationKey)
	value, _ := classification.(string)
	return strings.TrimSpace(value)
}

func classifyOpenAIUpstreamPolicyRejection(upstreamStatus int, upstreamMsg string, body []byte) (openAIUpstreamPolicyRejection, bool) {
	for _, value := range []string{
		gjson.GetBytes(body, "error.code").String(),
		gjson.GetBytes(body, "response.error.code").String(),
		gjson.GetBytes(body, "error.type").String(),
		gjson.GetBytes(body, "response.error.type").String(),
	} {
		if reason := normalizeOpenAIUpstreamPolicyReason(value); reason != "" {
			return newOpenAIUpstreamPolicyRejection(upstreamStatus, reason, upstreamMsg, body), true
		}
	}

	messages := []string{
		strings.TrimSpace(upstreamMsg),
		strings.TrimSpace(gjson.GetBytes(body, "error.message").String()),
		strings.TrimSpace(gjson.GetBytes(body, "response.error.message").String()),
	}
	for _, message := range messages {
		lower := strings.ToLower(message)
		switch {
		case strings.Contains(lower, "invalid prompt") &&
			(strings.Contains(lower, "flagged") || strings.Contains(lower, "usage policy")):
			return newOpenAIUpstreamPolicyRejection(upstreamStatus, "invalid_prompt", message, body), true
		case strings.Contains(lower, "content policy") &&
			(strings.Contains(lower, "blocked") || strings.Contains(lower, "violation") || strings.Contains(lower, "flagged")):
			return newOpenAIUpstreamPolicyRejection(upstreamStatus, "content_policy", message, body), true
		case strings.Contains(lower, "safety") &&
			(strings.Contains(lower, "blocked") || strings.Contains(lower, "violation") || strings.Contains(lower, "flagged")):
			return newOpenAIUpstreamPolicyRejection(upstreamStatus, "safety_error", message, body), true
		}
	}

	return openAIUpstreamPolicyRejection{}, false
}

func normalizeOpenAIUpstreamPolicyReason(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "invalid_prompt":
		return "invalid_prompt"
	case "content_policy", "content_policy_violation", "content_filter", "content_filter_violation":
		return "content_policy"
	case "safety_error":
		return "safety_error"
	case "cyber_policy":
		return "cyber_policy"
	default:
		return ""
	}
}

func newOpenAIUpstreamPolicyRejection(upstreamStatus int, reason, message string, body []byte) openAIUpstreamPolicyRejection {
	message = strings.TrimSpace(message)
	if message == "" {
		for _, path := range []string{"error.message", "response.error.message", "message"} {
			message = strings.TrimSpace(gjson.GetBytes(body, path).String())
			if message != "" {
				break
			}
		}
	}
	if message == "" {
		message = "Request blocked by upstream policy"
	}
	clientStatus := http.StatusForbidden
	if reason == "invalid_prompt" {
		clientStatus = http.StatusBadRequest
	} else if upstreamStatus == http.StatusBadRequest || upstreamStatus == http.StatusForbidden {
		clientStatus = upstreamStatus
	}
	return openAIUpstreamPolicyRejection{
		ErrorType:    OpenAIUpstreamPolicyRejectionErrorType,
		Reason:       reason,
		Message:      message,
		ClientStatus: clientStatus,
	}
}

func recordOpenAIUpstreamPolicyRejection(c *gin.Context, account *Account, rejection openAIUpstreamPolicyRejection, upstreamStatus int, requestID string, body []byte, passthrough bool, detail string) {
	if c == nil {
		return
	}
	SetOpsUpstreamErrorClassification(c, rejection.ErrorType)
	setOpsUpstreamError(c, upstreamStatus, rejection.Message, detail)
	if rejection.Reason == "cyber_policy" {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           rejection.Reason,
			Message:        rejection.Message,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: upstreamStatus,
		})
	}
	event := OpsUpstreamErrorEvent{
		ProxyID:              opsUpstreamProxyID(account),
		ProxyName:            opsUpstreamProxyName(account),
		Platform:             PlatformOpenAI,
		UpstreamStatusCode:   upstreamStatus,
		UpstreamRequestID:    strings.TrimSpace(requestID),
		UpstreamResponseBody: detail,
		Passthrough:          passthrough,
		Kind:                 "policy_rejection",
		Reason:               rejection.Reason,
		Message:              rejection.Message,
		Detail:               detail,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}
