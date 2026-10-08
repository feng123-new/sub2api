package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type encryptedRetryUpstream struct {
	responses []string
	statuses  []int
	bodies    [][]byte
	closed    int
}

type encryptedRetryBody struct {
	io.Reader
	closed *int
}

func (r *encryptedRetryBody) Close() error { *r.closed++; return nil }

func (u *encryptedRetryUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	i := len(u.bodies) - 1
	if i >= len(u.responses) {
		return nil, fmt.Errorf("unexpected attempt %d", i+1)
	}
	status := http.StatusOK
	contentType := "text/event-stream"
	if len(u.statuses) > i && u.statuses[i] != 0 {
		status = u.statuses[i]
		contentType = "application/json"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: &encryptedRetryBody{Reader: strings.NewReader(u.responses[i]), closed: &u.closed}}, nil
}

func (u *encryptedRetryUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

const encryptedRetryFailed = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"Encrypted content could not be decrypted or parsed.\",\"type\":\"invalid_request_error\"}}}\n\n"
const encryptedRetryCreated = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_failed\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
const encryptedRetryDelta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"OK\"}\n\n"
const encryptedRetryCompleted = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"

func encryptedRetryRequest(t *testing.T, stream bool, itemType string) []byte {
	t.Helper()
	input := []any{}
	if itemType != "" {
		item := map[string]any{"type": itemType, "encrypted_content": "rejected-ciphertext"}
		if itemType == "reasoning" {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": "Preserve this visible summary"}}
		}
		input = append(input, item)
	}
	input = append(input,
		map[string]any{"type": "custom_tool_call", "call_id": "ctc_pair", "name": "check", "input": "number"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "ctc_pair", "output": "9007199254740993"},
		map[string]any{"role": "user", "content": "Reply OK"})
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": stream, "instructions": "test", "input": input, "metadata": map[string]any{"large": json.Number("9007199254740993")}})
	require.NoError(t, err)
	return body
}

func runEncryptedRetryForward(t *testing.T, stream bool, itemType string, u *encryptedRetryUpstream) (*httptest.ResponseRecorder, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := encryptedRetryRequest(t, stream, itemType)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: u}
	account := &Account{ID: 1, Name: "encrypted-retry", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}
	_, err := svc.Forward(context.Background(), c, account, body)
	return rec, err
}

func TestEncryptedContentSSERecoveryBeforeOutput(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for _, item := range []string{"reasoning", "compaction", "compaction_summary"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, item), func(t *testing.T) {
				u := &encryptedRetryUpstream{responses: []string{encryptedRetryCreated + encryptedRetryFailed, encryptedRetryDelta + encryptedRetryCompleted}}
				rec, err := runEncryptedRetryForward(t, stream, item, u)
				require.NoError(t, err)
				require.Len(t, u.bodies, 2)
				require.GreaterOrEqual(t, u.closed, 2)
				require.NotContains(t, rec.Body.String(), "invalid_encrypted_content")
				require.NotContains(t, rec.Body.String(), "resp_failed")
				require.Contains(t, rec.Body.String(), "OK")
				require.Contains(t, string(u.bodies[0]), "rejected-ciphertext")
				require.NotContains(t, string(u.bodies[1]), "rejected-ciphertext")
				first, last := gjson.GetBytes(u.bodies[0], "input").Array(), gjson.GetBytes(u.bodies[1], "input").Array()
				require.Len(t, first, 4)
				if item == "reasoning" {
					require.Len(t, last, 4)
					require.JSONEq(t, first[0].Get("summary").Raw, last[0].Get("summary").Raw)
					require.False(t, last[0].Get("encrypted_content").Exists())
					last = last[1:]
				}
				require.Len(t, last, 3)
				for i := range last {
					require.JSONEq(t, first[i+1].Raw, last[i].Raw)
				}
				require.Equal(t, gjson.GetBytes(u.bodies[0], "model").String(), gjson.GetBytes(u.bodies[1], "model").String())
				if gjson.GetBytes(u.bodies[0], "metadata.large").Exists() {
					require.Equal(t, "9007199254740993", gjson.GetBytes(u.bodies[1], "metadata.large").Raw)
				}
			})
		}
	}
}

func TestEncryptedContentSSERecoveryBoundaries(t *testing.T) {
	cases := []struct {
		name, item, first, second string
		attempts                  int
		wantError                 bool
	}{
		{"retry only once", "reasoning", encryptedRetryFailed, encryptedRetryFailed, 2, true},
		{"no encrypted input", "", encryptedRetryFailed, "", 1, true},
		{"different error", "reasoning", strings.ReplaceAll(encryptedRetryFailed, "invalid_encrypted_content", "invalid_request_error"), "", 1, true},
		{"output already started", "reasoning", encryptedRetryDelta + encryptedRetryFailed, "", 1, true},
		{"terminal contains output", "reasoning", strings.Replace(encryptedRetryFailed, `"output":[]`, `"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]`, 1), "", 1, true},
		{"successful encrypted request", "reasoning", encryptedRetryDelta + encryptedRetryCompleted, "", 1, false},
		{"bare error then success", "reasoning", "event: error\ndata: {\"type\":\"error\",\"code\":\"invalid_encrypted_content\",\"message\":\"transient\"}\n\n" + encryptedRetryCompleted, "", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := &encryptedRetryUpstream{responses: []string{tc.first, tc.second}}
			rec, err := runEncryptedRetryForward(t, true, tc.item, u)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, u.bodies, tc.attempts)
			if tc.wantError {
				require.Contains(t, rec.Body.String(), "response.failed")
			}
		})
	}
}

func TestEncryptedContentHTTP400RecoveryRemainsWorking(t *testing.T) {
	u := &encryptedRetryUpstream{statuses: []int{400, 0}, responses: []string{`{"error":{"code":"invalid_encrypted_content","message":"bad ciphertext"}}`, encryptedRetryDelta + encryptedRetryCompleted}}
	rec, err := runEncryptedRetryForward(t, true, "reasoning", u)
	require.NoError(t, err)
	require.Len(t, u.bodies, 2)
	require.Contains(t, rec.Body.String(), "OK")
}

func TestEncryptedContentRecoverySharesHTTPAndSSEBudget(t *testing.T) {
	failureJSON := `{"error":{"code":"invalid_encrypted_content","message":"bad ciphertext"}}`
	for _, firstHTTP := range []bool{false, true} {
		t.Run(fmt.Sprint(firstHTTP), func(t *testing.T) {
			u := &encryptedRetryUpstream{responses: []string{encryptedRetryFailed, failureJSON}, statuses: []int{0, 400}}
			if firstHTTP {
				u.responses = []string{failureJSON, encryptedRetryFailed}
				u.statuses = []int{400, 0}
			}
			rec, err := runEncryptedRetryForward(t, true, "reasoning", u)
			require.Error(t, err)
			require.Len(t, u.bodies, 2)
			require.NotEmpty(t, rec.Body.String())
		})
	}
}

func TestEncryptedContentRecoveryDoesNotReplayBufferedNonStreamOutput(t *testing.T) {
	u := &encryptedRetryUpstream{responses: []string{encryptedRetryDelta + encryptedRetryFailed}}
	_, err := runEncryptedRetryForward(t, false, "reasoning", u)
	require.Error(t, err)
	require.Len(t, u.bodies, 1)
}

func TestEncryptedContentBareErrorEOFRecovery(t *testing.T) {
	bare := "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"Encrypted content could not be decrypted or parsed.\",\"type\":\"invalid_request_error\"}}\n\n"
	for _, stream := range []bool{true, false} {
		for _, item := range []string{"reasoning", "compaction", "compaction_summary"} {
			for _, unterminated := range []bool{true, false} {
				t.Run(fmt.Sprintf("stream=%v/%s/unterminated=%v", stream, item, unterminated), func(t *testing.T) {
					failed := bare
					if unterminated {
						failed = strings.TrimRight(failed, "\n")
					}
					u := &encryptedRetryUpstream{responses: []string{failed, encryptedRetryDelta + encryptedRetryCompleted}}
					rec, err := runEncryptedRetryForward(t, stream, item, u)
					require.NoError(t, err)
					require.Len(t, u.bodies, 2)
					require.NotContains(t, rec.Body.String(), "invalid_encrypted_content")
					require.Contains(t, rec.Body.String(), "OK")
				})
			}
		}
	}
}

func TestEncryptedContentBareErrorEOFRecoveryBoundaries(t *testing.T) {
	bare := "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"bad ciphertext\"}}\n\n"
	for _, stream := range []bool{true, false} {
		for _, tc := range []struct {
			name, item, body string
			success          bool
		}{
			{"no encrypted input", "", bare, false},
			{"after semantic output", "reasoning", encryptedRetryDelta + bare, false},
			{"later success wins", "reasoning", bare + encryptedRetryCompleted, true},
		} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, tc.name), func(t *testing.T) {
				u := &encryptedRetryUpstream{responses: []string{tc.body}}
				_, err := runEncryptedRetryForward(t, stream, tc.item, u)
				if tc.success {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				require.Len(t, u.bodies, 1)
			})
		}
	}
}

func TestEncryptedContentBareErrorTopLevelCode(t *testing.T) {
	bare := "event: error\ndata: {\"type\":\"error\",\"code\":\"invalid_encrypted_content\",\"message\":\"bad ciphertext\"}\n\n"
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			u := &encryptedRetryUpstream{responses: []string{bare, encryptedRetryDelta + encryptedRetryCompleted}}
			rec, err := runEncryptedRetryForward(t, stream, "reasoning", u)
			require.NoError(t, err)
			require.Len(t, u.bodies, 2)
			require.Contains(t, rec.Body.String(), "OK")
		})
	}
}

func TestEncryptedContentEOFDoesNotTreatTimersAsEOF(t *testing.T) {
	for _, firstOutputTimer := range []bool{true, false} {
		t.Run(fmt.Sprint(firstOutputTimer), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Set(openAIEncryptedContentSSERetryKey, true)
			cfg := &config.Config{}
			if firstOutputTimer {
				cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
			} else {
				cfg.Gateway.StreamDataIntervalTimeout = 1
			}
			svc := &OpenAIGatewayService{cfg: cfg}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			expiry := time.AfterFunc(5*time.Second, func() { _ = writer.Close() })
			defer expiry.Stop()
			go func() {
				_, _ = io.WriteString(writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"bad ciphertext\"}}\n\n")
			}()
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
			_, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, time.Now(), "gpt-6-astra", "gpt-6-astra")
			require.Error(t, err)
			var retry *openAIEncryptedContentSSERetry
			require.NotErrorAs(t, err, &retry)
			require.Contains(t, rec.Body.String(), "response.failed")
		})
	}
}

func TestEncryptedContentTerminalRetryRejectsCanceledAndOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(openAIEncryptedContentSSERetryKey, true)
	payload := []byte(`{"type":"error","error":{"code":"invalid_encrypted_content"},"response":{"output":[{"type":"message"}]}}`)
	require.Nil(t, newOpenAIEncryptedContentSSERetry(c, payload, "error", false))
	ctx, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(ctx)
	require.Nil(t, newOpenAIEncryptedContentSSERetry(c, []byte(`{"type":"error","error":{"code":"invalid_encrypted_content"}}`), "error", false))
}
