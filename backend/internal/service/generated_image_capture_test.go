package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type generatedImageCaptureRecorder struct {
	owners   []GeneratedImageOwner
	payloads [][]byte
}

func (r *generatedImageCaptureRecorder) Submit(owner GeneratedImageOwner, payload []byte) bool {
	r.owners = append(r.owners, owner)
	r.payloads = append(r.payloads, append([]byte(nil), payload...))
	return true
}

func generatedImageCaptureContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Set("api_key", &APIKey{ID: 12, UserID: 34, GroupID: captureGroupIDPtr(56)})
	return c, rec
}

func captureGroupIDPtr(value int64) *int64 { return &value }

func TestGeneratedImageCaptureWaitsForSuccessfulResponse(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			c, _ := generatedImageCaptureContext("/v1/responses")
			recorder := &generatedImageCaptureRecorder{}
			svc := &OpenAIGatewayService{generatedImageService: recorder}
			item := `{"id":"ig_capture","type":"image_generation_call","status":"completed","result":"aGVsbG8="}`
			terminal := `{"type":"response.failed","response":{"id":"resp_capture","status":"failed","error":{"code":"server_error","message":"generation failed"}}}`
			if success {
				terminal = `{"type":"response.completed","response":{"id":"resp_capture","status":"completed","output":[` + item + `]}}`
			}
			body := "data: {\"type\":\"response.output_item.done\",\"item\":" + item + "}\n\ndata: " + terminal + "\n\n"
			response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			_, _ = svc.handleStreamingResponse(c.Request.Context(), response, c, &Account{ID: 78}, time.Now(), "gpt-6-sol", "gpt-6-sol")
			if success {
				require.Len(t, recorder.payloads, 1)
			} else {
				require.Empty(t, recorder.payloads, "failed responses must not appear as completed gallery images")
			}
		})
	}
}

func TestGeneratedImageCaptureAsyncDefersUntilCommitted(t *testing.T) {
	c, _ := generatedImageCaptureContext("/v1/images/generations")
	recorder := &generatedImageCaptureRecorder{}
	svc := &OpenAIGatewayService{generatedImageService: recorder}
	payload := []byte(`{"data":[{"b64_json":"aGVsbG8="}]}`)
	DeferGeneratedImageCapture(c)
	svc.captureGeneratedImage(c, &Account{ID: 78}, "gpt-image-2", payload, 0)
	require.Empty(t, recorder.payloads)
	svc.CommitDeferredGeneratedImageCapture(c, payload)
	require.Len(t, recorder.payloads, 1)
	require.Equal(t, int64(34), recorder.owners[0].UserID)
	require.Equal(t, payload, recorder.payloads[0])
}

func TestGeneratedImageCaptureOnlyFinalOutputsWithAuthenticatedOwner(t *testing.T) {
	c, _ := generatedImageCaptureContext("/v1/responses")
	recorder := &generatedImageCaptureRecorder{}
	svc := &OpenAIGatewayService{generatedImageService: recorder}
	account := &Account{ID: 78}
	for _, payload := range []string{
		`{"type":"response.image_generation_call.partial_image","partial_image_b64":"aGVsbG8="}`,
		`{"type":"response.failed","response":{"output":[{"type":"image_generation_call","result":"aGVsbG8="}]}}`,
		`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"input_image","image_url":"https://example.com/input.png"}]}]}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","result":"aGVsbG8="}}`,
		`{"data":[{"revised_prompt":"an image"}]}`,
	} {
		svc.captureGeneratedImage(c, account, "gpt-image-2", []byte(payload), 0)
	}
	require.Empty(t, recorder.payloads)
	final := []byte(`{"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aGVsbG8="}}`)
	svc.captureGeneratedImage(c, account, "gpt-image-2", final, 0)
	svc.captureGeneratedImage(c, account, "gpt-image-2", []byte(`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","result":"aGVsbG8="}]}}`), 0)
	require.Len(t, recorder.payloads, 2)
	require.Equal(t, final, recorder.payloads[0])
	for _, owner := range recorder.owners {
		require.Equal(t, int64(34), owner.UserID)
		require.Equal(t, int64(12), owner.APIKeyID)
		require.Equal(t, int64(56), owner.GroupID)
		require.Equal(t, int64(78), owner.AccountID)
		require.Equal(t, "gpt-image-2", owner.Model)
		require.Equal(t, "/v1/responses", owner.Endpoint)
		require.NotEmpty(t, owner.RequestID)
	}
	require.Equal(t, recorder.owners[0].RequestID, recorder.owners[1].RequestID, "done/completed must deduplicate within one request")
	svc.captureGeneratedImage(c, account, "gpt-image-2", final, 1)
	svc.captureGeneratedImage(c, account, "gpt-image-2", final, 2)
	require.Len(t, recorder.owners, 4)
	require.NotEqual(t, recorder.owners[2].RequestID, recorder.owners[3].RequestID, "WS turns must be distinct")
	c.Set("api_key", &APIKey{ID: 12})
	svc.captureGeneratedImage(c, account, "gpt-image-2", final, 3)
	require.Len(t, recorder.owners, 4, "cannot infer an absent user ID")
}

func TestGeneratedImageCaptureImagesJSONPreservesClientPayload(t *testing.T) {
	c, rec := generatedImageCaptureContext("/v1/images/generations")
	recorder := &generatedImageCaptureRecorder{}
	svc := &OpenAIGatewayService{generatedImageService: recorder}
	payload := `{"created":123,"data":[{"b64_json":"aGVsbG8=","revised_prompt":"hi"}]}`
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
	_, count, _, err := svc.handleOpenAIImagesNonStreamingResponse(c.Request.Context(), response, c, &Account{ID: 78}, &OpenAIImagesRequest{Model: "gpt-image-2"})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.JSONEq(t, payload, rec.Body.String())
	require.Len(t, recorder.payloads, 1)
	require.Equal(t, rec.Body.Bytes(), recorder.payloads[0])
}

func TestGeneratedImageCaptureImagesSSEIgnoresPartialAndPreservesFrames(t *testing.T) {
	c, rec := generatedImageCaptureContext("/v1/images/generations")
	recorder := &generatedImageCaptureRecorder{}
	svc := &OpenAIGatewayService{generatedImageService: recorder}
	c.Set(generatedImageAccountIDKey, int64(78))
	c.Set(generatedImageModelKey, "gpt-image-2")
	sse := "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\"}\n\n" +
		"event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"aGVsbG8=\"}\n\n"
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}
	_, count, _, _, err := svc.handleOpenAIImagesStreamingResponse(response, c, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, sse, rec.Body.String())
	require.Len(t, recorder.payloads, 1)
	require.Equal(t, "image_generation.completed", captureJSONField(recorder.payloads[0], "type"))
}

func captureJSONField(payload []byte, field string) string {
	return gjson.GetBytes(payload, field).String()
}

func TestGeneratedImageCaptureOAuthConvertedJSONPreservesClientPayload(t *testing.T) {
	c, rec := generatedImageCaptureContext("/v1/images/edits")
	c.Set(generatedImageAccountIDKey, int64(78))
	recorder := &generatedImageCaptureRecorder{}
	svc := &OpenAIGatewayService{generatedImageService: recorder}
	upstream := "data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}]}}\n\n"
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream))}
	_, count, _, err := svc.handleOpenAIImagesOAuthNonStreamingResponse(response, c, "b64_json", "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, rec.Body.Bytes(), recorder.payloads[0])
	require.Equal(t, "aGVsbG8=", captureJSONField(recorder.payloads[0], "data.0.b64_json"))
	require.Equal(t, int64(78), recorder.owners[0].AccountID)
}

func TestGeneratedImageCaptureGeminiNativeJSONPreservesClientPayload(t *testing.T) {
	c, rec := generatedImageCaptureContext("/v1beta/models/gemini-image:generateContent")
	c.Set(generatedImageModelKey, "gemini-image")
	recorder := &generatedImageCaptureRecorder{}
	svc := &GeminiMessagesCompatService{generatedImageService: recorder}
	payload := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}]}`
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
	_, err := svc.handleNativeNonStreamingResponse(c, response, false, &Account{ID: 78}, "upstream-id")
	require.NoError(t, err)
	require.JSONEq(t, payload, rec.Body.String())
	require.Len(t, recorder.payloads, 1)
	require.Equal(t, rec.Body.Bytes(), recorder.payloads[0])
	require.Equal(t, "gemini-image", recorder.owners[0].Model)
	for _, filtered := range []string{
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/mpeg","data":"aGVsbG8="}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"SAFETY"}]}`,
	} {
		captureGeneratedImageTo(recorder, c, &Account{ID: 78}, "gemini-image", []byte(filtered), 0)
	}
	require.Len(t, recorder.payloads, 1)
}

func TestGeneratedImageCaptureGrokImagesOnlyNotVideo(t *testing.T) {
	c, rec := generatedImageCaptureContext("/v1/images/generations")
	images := &generatedImageCaptureRecorder{}
	upstreamPayload := `{"data":[{"url":"https://images.example.com/generated.png"}]}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstreamPayload)),
	}}
	svc := &OpenAIGatewayService{generatedImageService: images, httpUpstream: upstream}
	account := &Account{ID: 78, Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "upstream-key"}}
	request := []byte(`{"model":"grok-imagine-image","prompt":"draw"}`)
	_, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", request, "application/json")
	require.NoError(t, err)
	require.JSONEq(t, upstreamPayload, rec.Body.String())
	require.Len(t, images.payloads, 1)
	require.Equal(t, rec.Body.Bytes(), images.payloads[0])
	require.Equal(t, int64(78), images.owners[0].AccountID)
	require.Equal(t, "grok-imagine-image", images.owners[0].Model)
}

func TestGeneratedImageCaptureGeminiNativeSSEWaitsForTerminal(t *testing.T) {
	c, rec := generatedImageCaptureContext("/v1beta/models/gemini-image:streamGenerateContent")
	c.Set(generatedImageModelKey, "gemini-image")
	images := &generatedImageCaptureRecorder{}
	svc := &GeminiMessagesCompatService{generatedImageService: images}
	payload := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}}]}`
	stream := "data: " + payload + "\n\ndata: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}
	_, err := svc.handleNativeStreamingResponse(c, response, time.Now(), false, &Account{ID: 78}, "upstream-id")
	require.NoError(t, err)
	require.Equal(t, stream, rec.Body.String())
	require.Len(t, images.payloads, 1)
	require.JSONEq(t, payload, string(images.payloads[0]))

	c2, _ := generatedImageCaptureContext("/v1beta/models/gemini-image:streamGenerateContent")
	failedStream := "data: " + payload + "\n\ndata: {\"error\":{\"code\":400,\"status\":\"INVALID_ARGUMENT\",\"message\":\"blocked\"}}\n\n"
	response.Body = io.NopCloser(strings.NewReader(failedStream))
	_, err = svc.handleNativeStreamingResponse(c2, response, time.Now(), false, &Account{ID: 78}, "upstream-id")
	require.NoError(t, err)
	require.Len(t, images.payloads, 1, "failed stream must not archive previous image chunk")
}
