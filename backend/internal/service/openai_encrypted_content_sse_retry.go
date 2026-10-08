package service

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIEncryptedContentSSERetryKey = "openai_encrypted_content_sse_retry"

// Forward owns the retry budget and request body. Response handlers may signal
// recovery only before semantic output and while that attempt can be repaired.
type openAIEncryptedContentSSERetry struct {
	payload   []byte
	eventType string
}

func (e *openAIEncryptedContentSSERetry) Error() string {
	return "upstream rejected encrypted content before output"
}

func newOpenAIEncryptedContentSSERetry(c *gin.Context, payload []byte, eventType string, outputStarted bool) error {
	if c == nil || !c.GetBool(openAIEncryptedContentSSERetryKey) || outputStarted ||
		openAIStreamClientOutputStarted(c, false) ||
		(c.Request != nil && c.Request.Context().Err() != nil) ||
		len(gjson.GetBytes(payload, "response.output").Array()) > 0 ||
		len(gjson.GetBytes(payload, "output").Array()) > 0 {
		return nil
	}
	code := gjson.GetBytes(payload, "response.error.code").String()
	switch eventType {
	case "response.failed":
	case "error":
		// A bare error is eligible only once the caller has observed EOF.
		code = gjson.GetBytes(payload, "error.code").String()
		if code == "" {
			code = gjson.GetBytes(payload, "code").String()
		}
	default:
		return nil
	}
	if code != "invalid_encrypted_content" {
		return nil
	}
	return &openAIEncryptedContentSSERetry{payload: append([]byte(nil), payload...), eventType: eventType}
}

// If preparation cannot change the request, replay the withheld failure through
// ordinary response handling rather than swallowing it or exposing the signal.
func (e *openAIEncryptedContentSSERetry) restoreResponse(resp *http.Response) {
	body := append([]byte("event: "+e.eventType+"\ndata: "), e.payload...)
	body = append(body, '\n', '\n')
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Body = io.NopCloser(bytes.NewReader(body))
}

func openAIEncryptedRecoveryBodyHasOutput(body string) bool {
	hasOutput := false
	forEachOpenAISSEFrame(body, func(eventType string, payload []byte) {
		if eventType != "error" && !openAIStreamEventTypeIsTerminal(eventType) &&
			openAIStreamDataStartsClientOutput(string(payload), eventType) {
			hasOutput = true
		}
	})
	return hasOutput
}
