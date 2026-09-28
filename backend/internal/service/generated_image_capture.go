package service

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type generatedImageSubmitter interface {
	Submit(owner GeneratedImageOwner, payload []byte) bool
}

const generatedImageRequestIDKey = "generated_image_request_id"
const generatedImageAccountIDKey = "generated_image_account_id"
const generatedImageModelKey = "generated_image_model"
const generatedImageDeferKey = "generated_image_defer"
const generatedImageDeferredOwnerKey = "generated_image_deferred_owner"

// Async task completion must succeed before its original gateway result is archived.
func DeferGeneratedImageCapture(c *gin.Context) {
	c.Set(generatedImageDeferKey, true)
}

func (s *OpenAIGatewayService) CommitDeferredGeneratedImageCapture(c *gin.Context, payload []byte) {
	if s == nil || s.generatedImageService == nil {
		return
	}
	if value, ok := c.Get(generatedImageDeferredOwnerKey); ok {
		if owner, ok := value.(GeneratedImageOwner); ok {
			s.generatedImageService.Submit(owner, payload)
		}
	}
}

func successfulGeneratedImageResponse(payload []byte) bool {
	kind := gjson.GetBytes(payload, "type").String()
	return kind == "response.completed" || kind == "response.done"
}

// captureGeneratedImage receives only finalized output envelopes, never request input.
// turn is zero for HTTP and 1-based for each WebSocket turn.
func (s *OpenAIGatewayService) captureGeneratedImage(c *gin.Context, account *Account, model string, payload []byte, turn int) {
	if s == nil {
		return
	}
	captureGeneratedImageTo(s.generatedImageService, c, account, model, payload, turn)
}

func captureGeneratedImageTo(images generatedImageSubmitter, c *gin.Context, account *Account, model string, payload []byte, turn int) {
	if images == nil || c == nil || c.Request == nil || !hasFinalGeneratedImage(payload) {
		return
	}
	value, ok := c.Get("api_key")
	if !ok {
		return
	}
	key, ok := value.(*APIKey)
	if !ok || key == nil || key.UserID <= 0 || key.ID <= 0 {
		return
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	} else if value, ok := c.Get(generatedImageAccountIDKey); ok {
		accountID, _ = value.(int64)
	}
	if accountID <= 0 {
		return
	}
	owner := GeneratedImageOwner{
		UserID: key.UserID, APIKeyID: key.ID, AccountID: accountID,
		Model: strings.TrimSpace(model), Endpoint: c.Request.URL.Path,
	}
	if value, ok := c.Get(generatedImageModelKey); ok {
		owner.Model, _ = value.(string)
	}
	if key.GroupID != nil {
		owner.GroupID = *key.GroupID
	}
	if owner.Model == "" {
		owner.Model = gjson.GetBytes(payload, "model").String()
		if owner.Model == "" {
			owner.Model = gjson.GetBytes(payload, "response.model").String()
		}
	}
	requestID, ok := c.Get(generatedImageRequestIDKey)
	if !ok {
		requestID = uuid.NewString()
		c.Set(generatedImageRequestIDKey, requestID)
	}
	owner.RequestID, _ = requestID.(string)
	if turn > 0 {
		owner.RequestID = fmt.Sprintf("%s:%d", owner.RequestID, turn)
	}
	if c.GetBool(generatedImageDeferKey) {
		c.Set(generatedImageDeferredOwnerKey, owner)
		return
	}
	images.Submit(owner, payload)
}

func hasFinalGeneratedImage(payload []byte) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	if signal, ok := detectGeminiResponseSignal(payload); ok && signal.Kind != geminiSignalNone {
		return false
	}
	if countGeminiInlineImageOutputs(payload) > 0 {
		return true
	}
	if eventType := root.Get("type").String(); eventType != "" && eventType != "response" && eventType != "response.completed" && eventType != "response.done" && eventType != "response.output_item.done" && eventType != "image_generation.completed" && eventType != "image_edit.completed" {
		return false
	}
	for _, item := range root.Get("data").Array() {
		if item.Get("b64_json").String() != "" || item.Get("url").String() != "" {
			return true
		}
	}
	for _, item := range root.Get("output").Array() {
		if item.Get("type").String() == "image_generation_call" && item.Get("result").String() != "" {
			return true
		}
	}
	switch root.Get("type").String() {
	case "image_generation.completed", "image_edit.completed":
		return root.Get("b64_json").String() != "" || root.Get("url").String() != ""
	case "response.output_item.done":
		item := root.Get("item")
		return item.Get("type").String() == "image_generation_call" && item.Get("result").String() != ""
	case "response.completed", "response.done":
		for _, item := range root.Get("response.output").Array() {
			if item.Get("type").String() == "image_generation_call" && item.Get("result").String() != "" {
				return true
			}
		}
	}
	return false
}
