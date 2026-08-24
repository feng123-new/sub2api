package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const grokHedgeTestRequestBody = `{"model":"grok-4.5","stream":true,"input":"hello"}`

type grokHedgeTestUpstream struct {
	do func(*http.Request, int64) (*http.Response, error)
}

func (u *grokHedgeTestUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	return u.do(req, accountID)
}

func (u *grokHedgeTestUpstream) DoWithTLS(req *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.do(req, accountID)
}

type grokHedgeControlledBody struct {
	ctx        context.Context
	chunks     chan []byte
	closed     chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
	pending    []byte
}

func newGrokHedgeControlledBody(ctx context.Context) *grokHedgeControlledBody {
	return &grokHedgeControlledBody{
		ctx:    ctx,
		chunks: make(chan []byte, 4),
		closed: make(chan struct{}),
	}
}

func (b *grokHedgeControlledBody) Read(dst []byte) (int, error) {
	for len(b.pending) == 0 {
		select {
		case chunk, ok := <-b.chunks:
			if !ok {
				return 0, io.EOF
			}
			b.pending = chunk
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-b.closed:
			return 0, io.ErrClosedPipe
		}
	}
	n := copy(dst, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *grokHedgeControlledBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (b *grokHedgeControlledBody) send(payload string) {
	b.chunks <- []byte(payload)
}

func (b *grokHedgeControlledBody) finish() {
	b.finishOnce.Do(func() { close(b.chunks) })
}

type grokHedgeUsageRepo struct {
	AccountRepository
	mu         sync.Mutex
	updatedIDs []int64
}

func (r *grokHedgeUsageRepo) UpdateExtra(_ context.Context, id int64, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedIDs = append(r.updatedIDs, id)
	return nil
}

func (r *grokHedgeUsageRepo) updates() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.updatedIDs...)
}

type grokHedgeForwardCall struct {
	outcome *OpenAIForwardOutcome
	err     error
}

func grokHedgeTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "grok-test",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key"},
	}
}

func grokHedgeResponse(body io.ReadCloser, requestID string, remaining int) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{requestID},
			"X-Ratelimit-Limit-Requests":     []string{"100"},
			"X-Ratelimit-Remaining-Requests": []string{strconv.Itoa(remaining)},
			"X-Ratelimit-Reset-Requests":     []string{"1893456000"},
		},
		Body: body,
	}
}

func grokHedgeCompleteStream(label string, inputTokens, outputTokens int) string {
	return "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + label + "\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_" + label + "\",\"model\":\"grok-" + label + "\",\"usage\":{\"input_tokens\":" +
		strconv.Itoa(inputTokens) + ",\"output_tokens\":" + strconv.Itoa(outputTokens) + "}}}\n\n"
}

func newGrokHedgeTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(grokHedgeTestRequestBody))
	return c, recorder
}

func TestGrokStreamingHedgePrimaryFastDoesNotAcquireSecondary(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	var secondaryAcquireCalls atomic.Int32
	var primaryReleaseCalls atomic.Int32
	upstream := &grokHedgeTestUpstream{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		require.Equal(t, primary.ID, accountID)
		return grokHedgeResponse(io.NopCloser(bytes.NewBufferString(grokHedgeCompleteStream("primary", 1, 2))), "request-primary", 9), nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokHedgeTestContext()

	outcome, err := svc.ForwardWithOptions(c.Request.Context(), c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
		GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
			Delay:              50 * time.Millisecond,
			PrimaryReleaseFunc: func() { primaryReleaseCalls.Add(1) },
			AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
				secondaryAcquireCalls.Add(1)
				return nil, nil
			},
		},
	})

	require.NoError(t, err)
	require.Same(t, primary, outcome.Account)
	require.Equal(t, 1, outcome.Result.Usage.InputTokens)
	require.Equal(t, 2, outcome.Result.Usage.OutputTokens)
	require.Equal(t, "grok-primary", outcome.Result.UpstreamResponseModel)
	require.False(t, outcome.Result.UpstreamResponseModelConflict)
	require.Zero(t, secondaryAcquireCalls.Load())
	require.EqualValues(t, 1, primaryReleaseCalls.Load())
	require.Contains(t, recorder.Body.String(), "primary")
	require.Equal(t, "request-primary", recorder.Result().Header.Get("X-Request-Id"))
}

func TestGrokStreamingHedgeTerminalOnlyPrimaryDoesNotAcquireSecondary(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	var secondaryAcquireCalls atomic.Int32
	terminalOnly := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_primary\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	upstream := &grokHedgeTestUpstream{do: func(_ *http.Request, _ int64) (*http.Response, error) {
		return grokHedgeResponse(io.NopCloser(bytes.NewBufferString(terminalOnly)), "request-primary", 9), nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokHedgeTestContext()

	outcome, err := svc.ForwardWithOptions(c.Request.Context(), c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
		GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
			Delay: 50 * time.Millisecond,
			AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
				secondaryAcquireCalls.Add(1)
				return nil, nil
			},
		},
	})

	require.NoError(t, err)
	require.Same(t, primary, outcome.Account)
	require.Zero(t, secondaryAcquireCalls.Load())
	require.Equal(t, 2, outcome.Result.Usage.InputTokens)
	require.Equal(t, 1, outcome.Result.Usage.OutputTokens)
	require.Equal(t, 1, bytes.Count(recorder.Body.Bytes(), []byte(`"type":"response.completed"`)))
}

func TestGrokStreamingHedgeSecondaryWinsWithoutLoserEffects(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	secondary := grokHedgeTestAccount(2)
	primaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	primaryRequestCanceled := make(chan struct{})
	var cancelOnce sync.Once
	var primaryReleaseCalls atomic.Int32
	var secondaryReleaseCalls atomic.Int32
	repo := &grokHedgeUsageRepo{}
	cache := &stubGatewayCache{sessionBindings: make(map[string]int64)}
	upstream := &grokHedgeTestUpstream{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		switch accountID {
		case primary.ID:
			body := newGrokHedgeControlledBody(req.Context())
			body.send("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_primary_private\"}}\n\n")
			primaryBodyReady <- body
			go func() {
				<-req.Context().Done()
				cancelOnce.Do(func() { close(primaryRequestCanceled) })
			}()
			return grokHedgeResponse(body, "request-primary-loser", 1), nil
		case secondary.ID:
			return grokHedgeResponse(io.NopCloser(bytes.NewBufferString(grokHedgeCompleteStream("secondary", 3, 4))), "request-secondary-winner", 8), nil
		default:
			return nil, errors.New("unexpected account")
		}
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo, cache: cache}
	c, recorder := newGrokHedgeTestContext()
	groupID := int64(7)

	outcome, err := svc.ForwardWithOptions(c.Request.Context(), c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
		GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
			Delay:              5 * time.Millisecond,
			PrimaryReleaseFunc: func() { primaryReleaseCalls.Add(1) },
			GroupID:            &groupID,
			SessionHash:        "hedged-session",
			AcquireSecondary: func(_ context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
				require.Contains(t, excluded, primary.ID)
				return &AccountSelectionResult{
					Account: secondary, Acquired: true,
					ReleaseFunc: func() { secondaryReleaseCalls.Add(1) },
				}, nil
			},
		},
	})

	require.NoError(t, err)
	require.Same(t, secondary, outcome.Account)
	require.Equal(t, []int64{secondary.ID}, outcome.AttemptedSecondaryAccountIDs)
	require.Equal(t, 3, outcome.Result.Usage.InputTokens)
	require.Equal(t, 4, outcome.Result.Usage.OutputTokens)
	require.Equal(t, "grok-secondary", outcome.Result.UpstreamResponseModel)
	require.False(t, outcome.Result.UpstreamResponseModelConflict)
	require.NotContains(t, recorder.Body.String(), "primary_private")
	require.NotContains(t, recorder.Body.String(), "request-primary-loser")
	require.Contains(t, recorder.Body.String(), "secondary")
	require.Equal(t, 1, bytes.Count(recorder.Body.Bytes(), []byte(`"type":"response.completed"`)))
	require.Equal(t, "request-secondary-winner", recorder.Result().Header.Get("X-Request-Id"))
	require.Equal(t, []int64{secondary.ID}, repo.updates())
	require.Equal(t, secondary.ID, cache.sessionBindings[svc.openAISessionCacheKey("hedged-session")])
	require.EqualValues(t, 1, primaryReleaseCalls.Load())
	require.EqualValues(t, 1, secondaryReleaseCalls.Load())

	primaryBody := <-primaryBodyReady
	select {
	case <-primaryBody.closed:
	case <-time.After(time.Second):
		t.Fatal("primary loser body was not closed")
	}
	select {
	case <-primaryRequestCanceled:
	case <-time.After(time.Second):
		t.Fatal("primary loser request was not canceled")
	}
}

func TestGrokStreamingHedgePrimaryWinsAfterSecondaryStarts(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	secondary := grokHedgeTestAccount(2)
	primaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	secondaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	var primaryReleaseCalls atomic.Int32
	var secondaryReleaseCalls atomic.Int32
	cache := &stubGatewayCache{sessionBindings: make(map[string]int64)}
	upstream := &grokHedgeTestUpstream{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		body := newGrokHedgeControlledBody(req.Context())
		if accountID == primary.ID {
			body.send("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_primary_private\"}}\n\n")
			primaryBodyReady <- body
			return grokHedgeResponse(body, "request-primary-winner", 7), nil
		}
		body.send("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_secondary_private\"}}\n\n")
		secondaryBodyReady <- body
		return grokHedgeResponse(body, "request-secondary-loser", 1), nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, cache: cache}
	c, recorder := newGrokHedgeTestContext()
	groupID := int64(8)
	cache.sessionBindings[svc.openAISessionCacheKey("primary-session")] = primary.ID
	resultCh := make(chan grokHedgeForwardCall, 1)

	go func() {
		outcome, err := svc.ForwardWithOptions(c.Request.Context(), c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
			GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
				Delay:              5 * time.Millisecond,
				PrimaryReleaseFunc: func() { primaryReleaseCalls.Add(1) },
				GroupID:            &groupID,
				SessionHash:        "primary-session",
				AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
					return &AccountSelectionResult{Account: secondary, Acquired: true, ReleaseFunc: func() { secondaryReleaseCalls.Add(1) }}, nil
				},
			},
		})
		resultCh <- grokHedgeForwardCall{outcome: outcome, err: err}
	}()

	primaryBody := <-primaryBodyReady
	secondaryBody := <-secondaryBodyReady
	primaryBody.send(grokHedgeCompleteStream("primary", 5, 6))
	primaryBody.finish()
	call := <-resultCh

	require.NoError(t, call.err)
	require.Same(t, primary, call.outcome.Account)
	require.Equal(t, []int64{secondary.ID}, call.outcome.AttemptedSecondaryAccountIDs)
	require.Contains(t, recorder.Body.String(), "primary")
	require.NotContains(t, recorder.Body.String(), "secondary_private")
	require.Equal(t, 1, bytes.Count(recorder.Body.Bytes(), []byte(`"type":"response.completed"`)))
	require.Equal(t, "request-primary-winner", recorder.Result().Header.Get("X-Request-Id"))
	require.Equal(t, primary.ID, cache.sessionBindings[svc.openAISessionCacheKey("primary-session")])
	require.EqualValues(t, 1, primaryReleaseCalls.Load())
	require.EqualValues(t, 1, secondaryReleaseCalls.Load())
	select {
	case <-secondaryBody.closed:
	case <-time.After(time.Second):
		t.Fatal("secondary loser body was not closed")
	}
}

func TestGrokStreamingHedgeNoSecondaryCapacityContinuesPrimary(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	primaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	secondaryAcquireCalled := make(chan struct{})
	upstream := &grokHedgeTestUpstream{do: func(req *http.Request, _ int64) (*http.Response, error) {
		body := newGrokHedgeControlledBody(req.Context())
		primaryBodyReady <- body
		return grokHedgeResponse(body, "request-primary", 9), nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, recorder := newGrokHedgeTestContext()
	resultCh := make(chan grokHedgeForwardCall, 1)

	go func() {
		outcome, err := svc.ForwardWithOptions(c.Request.Context(), c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
			GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
				Delay: 5 * time.Millisecond,
				AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
					close(secondaryAcquireCalled)
					return nil, nil
				},
			},
		})
		resultCh <- grokHedgeForwardCall{outcome: outcome, err: err}
	}()

	primaryBody := <-primaryBodyReady
	<-secondaryAcquireCalled
	primaryBody.send(grokHedgeCompleteStream("primary", 1, 1))
	primaryBody.finish()
	call := <-resultCh

	require.NoError(t, call.err)
	require.Same(t, primary, call.outcome.Account)
	require.Empty(t, call.outcome.AttemptedSecondaryAccountIDs)
	require.Contains(t, recorder.Body.String(), "primary")
}

func TestGrokStreamingHedgeClientCancellationReleasesBothOnce(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	secondary := grokHedgeTestAccount(2)
	primaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	secondaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	var primaryReleaseCalls atomic.Int32
	var secondaryReleaseCalls atomic.Int32
	upstream := &grokHedgeTestUpstream{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		body := newGrokHedgeControlledBody(req.Context())
		if accountID == primary.ID {
			primaryBodyReady <- body
		} else {
			secondaryBodyReady <- body
		}
		return grokHedgeResponse(body, "request-stalled", 1), nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokHedgeTestContext()
	requestCtx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestCtx)
	resultCh := make(chan grokHedgeForwardCall, 1)

	go func() {
		outcome, err := svc.ForwardWithOptions(requestCtx, c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
			GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
				Delay:              5 * time.Millisecond,
				PrimaryReleaseFunc: func() { primaryReleaseCalls.Add(1) },
				AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
					return &AccountSelectionResult{Account: secondary, Acquired: true, ReleaseFunc: func() { secondaryReleaseCalls.Add(1) }}, nil
				},
			},
		})
		resultCh <- grokHedgeForwardCall{outcome: outcome, err: err}
	}()

	primaryBody := <-primaryBodyReady
	secondaryBody := <-secondaryBodyReady
	cancel()
	call := <-resultCh

	require.ErrorIs(t, call.err, context.Canceled)
	require.Equal(t, []int64{secondary.ID}, call.outcome.AttemptedSecondaryAccountIDs)
	require.EqualValues(t, 1, primaryReleaseCalls.Load())
	require.EqualValues(t, 1, secondaryReleaseCalls.Load())
	for _, body := range []*grokHedgeControlledBody{primaryBody, secondaryBody} {
		select {
		case <-body.closed:
		case <-time.After(time.Second):
			t.Fatal("canceled hedge body was not closed")
		}
	}
}

func TestGrokStreamingHedgeCommittedWinnerCancellationReleasesBothOnce(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	secondary := grokHedgeTestAccount(2)
	primaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	secondaryBodyReady := make(chan *grokHedgeControlledBody, 1)
	var primaryReleaseCalls atomic.Int32
	var secondaryReleaseCalls atomic.Int32
	upstream := &grokHedgeTestUpstream{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		body := newGrokHedgeControlledBody(req.Context())
		if accountID == primary.ID {
			primaryBodyReady <- body
			return grokHedgeResponse(body, "request-primary-winner", 9), nil
		}
		secondaryBodyReady <- body
		return grokHedgeResponse(body, "request-secondary-loser", 1), nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokHedgeTestContext()
	requestCtx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(requestCtx)
	resultCh := make(chan grokHedgeForwardCall, 1)

	go func() {
		outcome, err := svc.ForwardWithOptions(requestCtx, c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
			GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
				Delay:              5 * time.Millisecond,
				PrimaryReleaseFunc: func() { primaryReleaseCalls.Add(1) },
				AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
					return &AccountSelectionResult{Account: secondary, Acquired: true, ReleaseFunc: func() { secondaryReleaseCalls.Add(1) }}, nil
				},
			},
		})
		resultCh <- grokHedgeForwardCall{outcome: outcome, err: err}
	}()

	primaryBody := <-primaryBodyReady
	secondaryBody := <-secondaryBodyReady
	primaryBody.send("data: {\"type\":\"response.output_text.delta\",\"delta\":\"primary\"}\n\n")
	select {
	case <-secondaryBody.closed:
	case <-time.After(time.Second):
		t.Fatal("secondary loser was not closed after primary won")
	}
	require.Zero(t, primaryReleaseCalls.Load())
	require.Eventually(t, func() bool { return secondaryReleaseCalls.Load() == 1 }, time.Second, time.Millisecond)

	cancel()
	require.Eventually(t, func() bool { return primaryReleaseCalls.Load() == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, secondaryReleaseCalls.Load())
	select {
	case <-primaryBody.closed:
		t.Fatal("winner body must remain open to drain terminal usage after client cancellation")
	default:
	}

	primaryBody.send("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_primary\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}}\n\n")
	primaryBody.finish()
	call := <-resultCh
	require.NoError(t, call.err)
	require.Same(t, primary, call.outcome.Account)
	require.Equal(t, 4, call.outcome.Result.Usage.InputTokens)
	require.Equal(t, 2, call.outcome.Result.Usage.OutputTokens)
	require.EqualValues(t, 1, primaryReleaseCalls.Load())
	require.EqualValues(t, 1, secondaryReleaseCalls.Load())
}

func TestGrokStreamingHedgePreparationFailureExcludesSecondary(t *testing.T) {
	primary := grokHedgeTestAccount(1)
	secondary := grokHedgeTestAccount(2)
	secondary.Credentials = nil
	primaryDoMayReturn := make(chan struct{})
	upstream := &grokHedgeTestUpstream{do: func(_ *http.Request, accountID int64) (*http.Response, error) {
		require.Equal(t, primary.ID, accountID)
		<-primaryDoMayReturn
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"primary failed"}}`)),
		}, nil
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := newGrokHedgeTestContext()
	resultCh := make(chan grokHedgeForwardCall, 1)
	secondarySelected := make(chan struct{})
	secondaryReleased := make(chan struct{})

	go func() {
		outcome, err := svc.ForwardWithOptions(c.Request.Context(), c, primary, []byte(grokHedgeTestRequestBody), OpenAIForwardOptions{
			GrokStreamingHedge: &OpenAIGrokStreamingHedgeOptions{
				Delay: 5 * time.Millisecond,
				AcquireSecondary: func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
					close(secondarySelected)
					return &AccountSelectionResult{
						Account: secondary, Acquired: true,
						ReleaseFunc: func() { close(secondaryReleased) },
					}, nil
				},
			},
		})
		resultCh <- grokHedgeForwardCall{outcome: outcome, err: err}
	}()

	<-secondarySelected
	<-secondaryReleased
	close(primaryDoMayReturn)
	call := <-resultCh
	require.Error(t, call.err)
	require.Equal(t, []int64{secondary.ID}, call.outcome.AttemptedSecondaryAccountIDs)
}
