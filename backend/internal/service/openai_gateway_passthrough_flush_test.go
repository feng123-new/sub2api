package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type passthroughFlushTestWriter struct {
	gin.ResponseWriter
	recorder         *httptest.ResponseRecorder
	failAfterWrites  int
	successfulWrites int
	failedWrites     int
	flushBodyLengths []int
}

func (w *passthroughFlushTestWriter) Write(data []byte) (int, error) {
	if w.failAfterWrites >= 0 && w.successfulWrites >= w.failAfterWrites {
		w.failedWrites++
		return 0, errors.New("client disconnected")
	}
	n, err := w.ResponseWriter.Write(data)
	if err == nil {
		w.successfulWrites++
	}
	return n, err
}

func (w *passthroughFlushTestWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *passthroughFlushTestWriter) Flush() {
	w.ResponseWriter.Flush()
	w.flushBodyLengths = append(w.flushBodyLengths, w.recorder.Body.Len())
}

type passthroughFlushTestErrorBody struct {
	payload []byte
	err     error
	sent    bool
}

func (r *passthroughFlushTestErrorBody) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.payload), nil
	}
	return 0, r.err
}

func (r *passthroughFlushTestErrorBody) Close() error { return nil }

// The upstream reader may return a complete SSE terminal frame and a non-EOF
// error in the same Read. Scanner still delivers the buffered event to service.
type passthroughTerminalOutcomeTestBody struct {
	payload       []byte
	sent          bool
	outcome       string
	closedOutcome string
	closes        int
}

func (r *passthroughTerminalOutcomeTestBody) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, r.payload), io.ErrUnexpectedEOF
}

func (r *passthroughTerminalOutcomeTestBody) Close() error {
	r.closes++
	r.closedOutcome = r.outcome
	return nil
}

func (r *passthroughTerminalOutcomeTestBody) SetCodexTransportStreamOutcome(outcome string) {
	r.outcome = outcome
}

type passthroughObservedTransformSource struct {
	*strings.Reader
	outcome string
	closes  int
}

func (r *passthroughObservedTransformSource) Close() error {
	r.closes++
	return nil
}

func (r *passthroughObservedTransformSource) SetCodexTransportStreamOutcome(outcome string) {
	r.outcome = outcome
}

func runPassthroughFlushTest(
	t *testing.T,
	body io.ReadCloser,
	failAfterWrites int,
	setups ...func(*gin.Context),
) (*openaiStreamingResultPassthrough, *httptest.ResponseRecorder, *passthroughFlushTestWriter, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	writer := &passthroughFlushTestWriter{
		ResponseWriter:  c.Writer,
		recorder:        recorder,
		failAfterWrites: failAfterWrites,
	}
	c.Writer = writer
	for _, setup := range setups {
		setup(c)
	}

	svc := &OpenAIGatewayService{cfg: &config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
	result, err := svc.handleStreamingResponsePassthrough(
		context.Background(),
		resp,
		c,
		&Account{ID: 1, Platform: PlatformOpenAI, Name: "flush-test"},
		time.Now(),
		"",
		"",
	)
	return result, recorder, writer, err
}

func TestOpenAIStreamingPassthroughFlushesAtCompleteEventBoundaries(t *testing.T) {
	firstEvent := "event: response.output_text.delta\n" +
		"id: event-1\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n"
	heartbeat := ": keepalive\n\n"
	terminalEvent := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_flush","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}` + "\n\n"
	upstream := firstEvent + heartbeat + terminalEvent

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{
		len(firstEvent),
		len(firstEvent) + len(heartbeat),
		len(upstream),
	}, writer.flushBodyLengths)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughReportsAcceptedTerminalDespiteReadError(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			payload := `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"` + terminal + `","response":{"usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"
			body := &passthroughTerminalOutcomeTestBody{payload: []byte(payload)}
			result, _, _, err := runPassthroughFlushTest(t, body, -1)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, body.sent)
			require.Equal(t, terminal, body.outcome)
		})
	}
}

func TestOpenAIStreamingPassthroughReportsFailedTerminalBeforeFailover(t *testing.T) {
	payload := `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"account_disabled","message":"disabled"}}}` + "\n\n"
	body := &passthroughTerminalOutcomeTestBody{payload: []byte(payload)}
	_, _, _, err := runPassthroughFlushTest(t, body, -1)
	require.Error(t, err)
	require.Equal(t, "response.failed", body.outcome)
}

func TestOpenAINativeStreamingReportsTerminalBeforeClosingBody(t *testing.T) {
	for _, tt := range []struct {
		name      string
		keepalive int
	}{
		{name: "sync"},
		{name: "async", keepalive: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, terminal := range []string{"response.completed", "response.incomplete"} {
				t.Run(terminal, func(t *testing.T) {
					payload := `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
						`data: {"type":"` + terminal + `","response":{"usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"
					body := &passthroughTerminalOutcomeTestBody{payload: []byte(payload)}
					gin.SetMode(gin.TestMode)
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					svc := &OpenAIGatewayService{cfg: &config.Config{
						Gateway: config.GatewayConfig{
							MaxLineSize:             defaultMaxLineSize,
							StreamKeepaliveInterval: tt.keepalive,
						},
					}}
					resp := &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}}
					result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c,
						&Account{ID: 1, Platform: PlatformOpenAI, Name: "native-terminal-test"}, time.Now(), "model", "model")
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 2, result.usage.InputTokens)
					require.Equal(t, 1, result.usage.OutputTokens)
					require.Contains(t, recorder.Body.String(), terminal)
					if tt.keepalive == 0 {
						require.Zero(t, body.closes, "sync caller owns the body close")
						require.NoError(t, body.Close())
					}
					require.Equal(t, terminal, body.outcome)
					require.Equal(t, terminal, body.closedOutcome, "terminal must be recorded before Close")
					require.Equal(t, 1, body.closes)
				})
			}
		})
	}
}

func TestOpenAINativeStreamingReportsHandlerErrorWithoutTerminal(t *testing.T) {
	body := &passthroughTerminalOutcomeTestBody{payload: []byte(
		`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n")}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{
		Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
	}}
	resp := &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}}
	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c,
		&Account{ID: 1, Platform: PlatformOpenAI, Name: "native-error-test"}, time.Now(), "model", "model")
	require.Error(t, err)
	require.Equal(t, "handler_error", body.outcome)
	require.NoError(t, body.Close())
	require.Equal(t, "handler_error", body.closedOutcome)
}

func TestCodexObservedClientToolTransformKeepsSourceOpenUntilOutcome(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"
	source := &passthroughObservedTransformSource{Reader: strings.NewReader(stream)}
	body := newResponsesClientToolStreamBody(source,
		apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"example": true}}, defaultMaxLineSize)
	result, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Contains(t, string(result), "response.completed")
	require.Zero(t, source.closes, "transform must not close before service reports the terminal outcome")
	reporter, ok := body.(interface{ SetCodexTransportStreamOutcome(string) })
	require.True(t, ok)
	reporter.SetCodexTransportStreamOutcome("response.completed")
	require.NoError(t, body.Close())
	require.Equal(t, "response.completed", source.outcome)
	require.Equal(t, 1, source.closes)
}

func TestOpenAIStreamingPassthroughKeepsPreamblePendingUntilFirstOutputBoundary(t *testing.T) {
	preamble := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_pending"}}` + "\n\n" +
		": waiting\n\n"
	firstOutput := `data: {"type":"response.output_text.delta","delta":"ready"}` + "\n\n"
	terminalEvent := `data: {"type":"response.completed","response":{"id":"resp_pending","usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}` + "\n\n"
	upstream := preamble + firstOutput + terminalEvent

	_, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.NoError(t, err)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{
		len(preamble) + len(firstOutput),
		len(upstream),
	}, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughFlushesTerminalEventAtEOFWithoutBlankLine(t *testing.T) {
	upstream := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_eof","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`
	wantBody := upstream + "\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, wantBody, recorder.Body.String())
	require.Equal(t, []int{len(wantBody)}, writer.flushBodyLengths)
	require.Equal(t, 5, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughFailedBeforeOutputCanStillFailOverWithoutFlush(t *testing.T) {
	upstream := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_failover"}}` + "\n\n" +
		"event: response.failed\n" +
		`data: {"type":"response.failed","error":{"code":"server_error","message":"upstream processing failed"}}` + "\n\n"

	_, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, recorder.Body.String())
	require.Empty(t, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughNonRetryableFailedBeforeOutputFlushesAtBoundary(t *testing.T) {
	upstream := "event: response.failed\n" +
		`data: {"type":"response.failed","error":{"code":"content_policy","message":"request blocked by policy"},"usage":{"input_tokens":6,"output_tokens":0,"total_tokens":6}}` + "\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{len(upstream)}, writer.flushBodyLengths)
	require.Equal(t, 6, result.usage.InputTokens)
	require.Zero(t, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughBareErrorTerminatesBeforeDone(t *testing.T) {
	errorEvent := "event: error\n" +
		`data: {"type":"error","error":{"code":"content_policy","message":"request blocked by policy"},"usage":{"input_tokens":6,"output_tokens":0,"total_tokens":6}}` + "\n\n"
	upstream := errorEvent + "data: [DONE]\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	body := recorder.Body.String()
	require.NotContains(t, body, `"type":"error"`)
	require.Equal(t, 1, strings.Count(body, `"type":"response.failed"`))
	require.Contains(t, body, `"status":"failed"`)
	require.NotContains(t, body, "[DONE]")
	require.Equal(t, []int{len(body)}, writer.flushBodyLengths)
	require.Equal(t, 6, result.usage.InputTokens)
	require.Zero(t, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughBareErrorDrainsAuthoritativeFailedUsage(t *testing.T) {
	errorEvent := "event: error\n" +
		`data: {"type":"error","error":{"code":"content_policy","message":"request blocked by policy"}}` + "\n\n"
	failedEvent := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_failed","usage":{"input_tokens":9,"output_tokens":2},"error":{"code":"content_policy","message":"request blocked by policy"}}}` + "\n\n"
	upstream := errorEvent + failedEvent + "data: [DONE]\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	require.NotNil(t, result)
	body := recorder.Body.String()
	require.NotContains(t, body, `"type":"error"`)
	require.Equal(t, 1, strings.Count(body, `"type":"response.failed"`))
	require.Contains(t, body, `"id":"resp_failed"`)
	require.NotContains(t, body, "[DONE]")
	require.Equal(t, []int{len(body)}, writer.flushBodyLengths)
	require.Equal(t, 9, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughFailedAfterOutputFlushesAtBoundaryAndKeepsUsage(t *testing.T) {
	firstOutput := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	failedEvent := "event: response.failed\n" +
		`data: {"type":"response.failed","error":{"code":"server_error","message":"upstream processing failed"},"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}` + "\n\n"
	upstream := firstOutput + failedEvent

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{len(firstOutput), len(upstream)}, writer.flushBodyLengths)
	require.Equal(t, 7, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughClientDisconnectStillDrainsTerminalUsage(t *testing.T) {
	firstOutput := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	terminalEvent := `data: {"type":"response.completed","response":{"id":"resp_drain","usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}}}` + "\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(
		t,
		io.NopCloser(strings.NewReader(firstOutput+terminalEvent)),
		2,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, firstOutput, recorder.Body.String())
	require.Equal(t, []int{len(firstOutput)}, writer.flushBodyLengths)
	require.Equal(t, 1, writer.failedWrites)
	require.Equal(t, 11, result.usage.InputTokens)
	require.Equal(t, 4, result.usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughScannerErrorFlushesWrittenResidual(t *testing.T) {
	upstream := []byte(`data: {"type":"response.output_text.delta","delta":"partial"}`)
	readErr := errors.New("upstream read failed")

	_, recorder, writer, err := runPassthroughFlushTest(t, &passthroughFlushTestErrorBody{
		payload: upstream,
		err:     readErr,
	}, -1)

	require.ErrorIs(t, err, readErr)
	wantBody := string(upstream) + "\n"
	require.Equal(t, wantBody, recorder.Body.String())
	require.Equal(t, []int{len(wantBody)}, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughNamespaceTrailingJSONErrorFlushesWrittenResidualOnce(t *testing.T) {
	writtenPrefix := `data: {"type":"response.output_text.delta","delta":"prefix"}` + "\n"
	trailingData := `data: {"type":"response.output_text.delta","delta":"not-written"} {"extra":true}`

	_, recorder, writer, err := runPassthroughFlushTest(
		t,
		io.NopCloser(strings.NewReader(writtenPrefix+trailingData)),
		-1,
		func(c *gin.Context) {
			setOpenAIResponsesNamespaceNames(c, map[string]apicompat.ResponsesNamespaceName{
				"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
			})
		},
	)

	require.ErrorContains(t, err, "restore OpenAI passthrough namespace response")
	require.Equal(t, writtenPrefix, recorder.Body.String())
	require.Equal(t, []int{len(writtenPrefix)}, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughBlankWriteFailureDoesNotFlushAndStillDrainsUsage(t *testing.T) {
	writtenDataLine := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n"
	terminalEvent := `data: {"type":"response.completed","response":{"id":"resp_blank_failure","usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}}` + "\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(
		t,
		io.NopCloser(strings.NewReader(writtenDataLine+"\n"+terminalEvent)),
		1,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, writtenDataLine, recorder.Body.String())
	require.Empty(t, writer.flushBodyLengths)
	require.Equal(t, 1, writer.successfulWrites)
	require.Equal(t, 1, writer.failedWrites)
	require.Equal(t, 13, result.usage.InputTokens)
	require.Equal(t, 5, result.usage.OutputTokens)
}
