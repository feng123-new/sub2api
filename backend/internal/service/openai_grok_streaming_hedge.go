package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type grokStreamingHedgeAttempt struct {
	service           *OpenAIGatewayService
	account           *Account
	requestContext    context.Context
	cancel            context.CancelFunc
	requestBody       []byte
	originalModel     string
	upstreamModel     string
	cacheIdentity     string
	token             string
	proxyURL          string
	openAIBeta        string
	clientToolMapping apicompat.ResponsesClientToolMapping
	startTime         time.Time
	bodyMu            sync.Mutex
	currentBody       io.Closer
	canceled          bool
}

type grokStreamingHedgeAcquisition struct {
	response         *http.Response
	requestBody      []byte
	upstreamDuration time.Duration
	semanticOutput   bool
	stageErr         error
	err              error
}

type grokStreamingHedgeClientRequestError struct {
	err error
}

func (e *grokStreamingHedgeClientRequestError) Error() string {
	return e.err.Error()
}

func (e *grokStreamingHedgeClientRequestError) Unwrap() error {
	return e.err
}

type grokStreamingHedgeReplayBody struct {
	io.Reader
	source io.Closer
}

func (b *grokStreamingHedgeReplayBody) Close() error {
	return b.source.Close()
}

func (s *OpenAIGatewayService) forwardGrokResponsesHedged(
	ctx context.Context,
	c *gin.Context,
	primaryAccount *Account,
	body []byte,
	originalModel string,
	startTime time.Time,
	options *OpenAIGrokStreamingHedgeOptions,
) (*OpenAIForwardResult, *Account, []int64, error) {
	primaryRelease := onceOpenAIHedgeRelease(ctx, options.PrimaryReleaseFunc)
	defer primaryRelease()

	primaryAttempt, err := s.prepareGrokStreamingHedgeAttempt(ctx, c, primaryAccount, body, originalModel, startTime)
	if err != nil {
		s.writeGrokStreamingHedgePreparationError(c, err)
		return nil, primaryAccount, nil, err
	}
	defer primaryAttempt.abort()
	primaryResult := primaryAttempt.start()

	delay := options.Delay
	if delay <= 0 {
		delay = time.Nanosecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case acquisition := <-primaryResult:
		result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
		return result, primaryAccount, nil, commitErr
	case <-ctx.Done():
		return nil, primaryAccount, nil, ctx.Err()
	case <-timer.C:
	}

	if options.AcquireSecondary == nil {
		acquisition, waitErr := waitForGrokStreamingHedgeAttempt(ctx, primaryResult)
		if waitErr != nil {
			return nil, primaryAccount, nil, waitErr
		}
		result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
		return result, primaryAccount, nil, commitErr
	}

	excludedIDs := map[int64]struct{}{primaryAccount.ID: {}}
	selection, acquireErr := options.AcquireSecondary(ctx, excludedIDs)
	if acquireErr != nil || selection == nil || selection.Account == nil || !selection.Acquired || selection.Account.ID == primaryAccount.ID {
		if selection != nil && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		if ctx.Err() != nil {
			return nil, primaryAccount, nil, ctx.Err()
		}
		acquisition, waitErr := waitForGrokStreamingHedgeAttempt(ctx, primaryResult)
		if waitErr != nil {
			return nil, primaryAccount, nil, waitErr
		}
		result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
		return result, primaryAccount, nil, commitErr
	}

	secondaryAccount := selection.Account
	secondaryRelease := onceOpenAIHedgeRelease(ctx, selection.ReleaseFunc)
	defer secondaryRelease()

	select {
	case acquisition := <-primaryResult:
		secondaryRelease()
		result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
		return result, primaryAccount, nil, commitErr
	default:
	}

	secondaryAttempt, err := s.prepareGrokStreamingHedgeAttempt(ctx, c, secondaryAccount, body, originalModel, startTime)
	if err != nil {
		secondaryRelease()
		acquisition, waitErr := waitForGrokStreamingHedgeAttempt(ctx, primaryResult)
		if waitErr != nil {
			return nil, primaryAccount, []int64{secondaryAccount.ID}, waitErr
		}
		result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
		return result, primaryAccount, []int64{secondaryAccount.ID}, commitErr
	}
	defer secondaryAttempt.abort()

	select {
	case acquisition := <-primaryResult:
		secondaryRelease()
		result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
		return result, primaryAccount, nil, commitErr
	default:
	}

	attemptedSecondaryIDs := []int64{secondaryAccount.ID}
	secondaryResult := secondaryAttempt.start()
	var primaryAcquisition *grokStreamingHedgeAcquisition
	var secondaryAcquisition *grokStreamingHedgeAcquisition

	for {
		select {
		case acquisition := <-primaryResult:
			primaryAcquisition = &acquisition
			primaryResult = nil
			if acquisition.semanticOutput {
				secondaryAttempt.abort()
				secondaryRelease()
				result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, acquisition)
				return result, primaryAccount, attemptedSecondaryIDs, commitErr
			}
		case acquisition := <-secondaryResult:
			secondaryAcquisition = &acquisition
			secondaryResult = nil
			if acquisition.semanticOutput {
				primaryAttempt.abort()
				primaryRelease()
				_ = s.BindStickySession(ctx, options.GroupID, options.SessionHash, secondaryAccount.ID)
				result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, secondaryAttempt, acquisition)
				return result, secondaryAccount, attemptedSecondaryIDs, commitErr
			}
		case <-ctx.Done():
			primaryAttempt.abort()
			secondaryAttempt.abort()
			primaryRelease()
			secondaryRelease()
			return nil, primaryAccount, attemptedSecondaryIDs, ctx.Err()
		}

		if primaryAcquisition != nil && secondaryAcquisition != nil {
			secondaryAttempt.abort()
			secondaryRelease()
			result, commitErr := s.commitGrokStreamingHedgeAttempt(ctx, c, primaryAttempt, *primaryAcquisition)
			return result, primaryAccount, attemptedSecondaryIDs, commitErr
		}
	}
}

func onceOpenAIHedgeRelease(ctx context.Context, release func()) func() {
	if release == nil {
		return func() {}
	}
	releaseOnce := sync.OnceFunc(release)
	stop := func() bool { return true }
	if ctx != nil {
		stop = context.AfterFunc(ctx, releaseOnce)
	}
	return func() {
		_ = stop()
		releaseOnce()
	}
}

func waitForGrokStreamingHedgeAttempt(
	ctx context.Context,
	result <-chan grokStreamingHedgeAcquisition,
) (grokStreamingHedgeAcquisition, error) {
	select {
	case acquisition := <-result:
		return acquisition, nil
	case <-ctx.Done():
		return grokStreamingHedgeAcquisition{}, ctx.Err()
	}
}

func (s *OpenAIGatewayService) prepareGrokStreamingHedgeAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel string,
	startTime time.Time,
) (*grokStreamingHedgeAttempt, error) {
	upstreamModel := account.GetMappedModel(originalModel)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = grokDefaultResponsesModel
	}
	if isGrokImageGenerationModel(upstreamModel) {
		return nil, fmt.Errorf("model %s is an image model and is not available on the Responses endpoint; use /v1/images/generations instead", upstreamModel)
	}

	patchedBody, clientToolMapping, err := patchGrokResponsesBodyWithClientTools(body, upstreamModel)
	if err != nil {
		return nil, &grokStreamingHedgeClientRequestError{err: err}
	}
	if isOpenAIResponsesCompactPath(c) {
		patchedBody, err = buildGrokCompactRequestBody(patchedBody)
		if err != nil {
			return nil, err
		}
	}
	cacheIdentity := resolveGrokCacheIdentity(c, patchedBody, "", upstreamModel)
	mixedCacheIntentBody := append([]byte(nil), patchedBody...)
	patchedBody, err = applyGrokResponsesCacheIdentity(patchedBody, body, cacheIdentity, account.IsGrokOAuth())
	if err != nil {
		return nil, fmt.Errorf("apply grok prompt cache identity: %w", err)
	}
	patchedBody, err = applyGrokFreeRequestToolCacheRoute(c, patchedBody, mixedCacheIntentBody, account, cacheIdentity)
	if err != nil {
		return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", err)
	}

	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	baseContext := context.Background()
	if ctx != nil {
		baseContext = context.WithoutCancel(ctx)
	}
	requestContext, cancel := context.WithCancel(baseContext)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return &grokStreamingHedgeAttempt{
		service:           s,
		account:           account,
		requestContext:    requestContext,
		cancel:            cancel,
		requestBody:       patchedBody,
		originalModel:     originalModel,
		upstreamModel:     upstreamModel,
		cacheIdentity:     cacheIdentity,
		token:             token,
		proxyURL:          proxyURL,
		openAIBeta:        strings.TrimSpace(c.GetHeader("OpenAI-Beta")),
		clientToolMapping: clientToolMapping,
		startTime:         startTime,
	}, nil
}

func (s *OpenAIGatewayService) writeGrokStreamingHedgePreparationError(c *gin.Context, err error) {
	var clientErr *grokStreamingHedgeClientRequestError
	if !errors.As(err, &clientErr) {
		return
	}
	setOpsUpstreamError(c, http.StatusBadRequest, clientErr.Error(), "")
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request_error", "message": clientErr.Error(), "param": "tools",
	}})
}

func (a *grokStreamingHedgeAttempt) start() <-chan grokStreamingHedgeAcquisition {
	result := make(chan grokStreamingHedgeAcquisition, 1)
	go func() {
		result <- a.acquire()
	}()
	return result
}

func (a *grokStreamingHedgeAttempt) acquire() grokStreamingHedgeAcquisition {
	upstreamStartedAt := time.Now()
	requestBody := a.requestBody
	var resp *http.Response
	var err error
	for requestAttempt := 0; ; requestAttempt++ {
		upstreamReq, buildErr := buildGrokResponsesRequest(
			a.requestContext,
			nil,
			a.account,
			requestBody,
			a.token,
			a.cacheIdentity,
			a.service.cfg,
		)
		if buildErr != nil {
			return grokStreamingHedgeAcquisition{requestBody: requestBody, upstreamDuration: time.Since(upstreamStartedAt), err: buildErr}
		}
		if a.openAIBeta != "" {
			upstreamReq.Header.Set("OpenAI-Beta", a.openAIBeta)
			a.account.ApplyHeaderOverrides(upstreamReq.Header)
		}
		resp, err = a.service.httpUpstream.Do(upstreamReq, a.proxyURL, a.account.ID, a.account.Concurrency)
		if err != nil {
			return grokStreamingHedgeAcquisition{requestBody: requestBody, upstreamDuration: time.Since(upstreamStartedAt), err: err}
		}
		a.trackBody(resp.Body)

		if requestAttempt > 0 || resp.StatusCode != http.StatusBadRequest {
			break
		}
		respBody := a.service.readUpstreamErrorBody(resp)
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if !isGrokInvalidEncryptedContentResponse(resp.StatusCode, respBody) {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			a.trackBody(resp.Body)
			break
		}

		retryBody, changed, trimErr := trimGrokInvalidEncryptedContentRetryBody(requestBody)
		if trimErr != nil {
			return grokStreamingHedgeAcquisition{requestBody: requestBody, upstreamDuration: time.Since(upstreamStartedAt), err: fmt.Errorf("prepare Grok invalid encrypted_content retry: %w", trimErr)}
		}
		if !changed {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			a.trackBody(resp.Body)
			break
		}
		requestBody = retryBody
	}

	acquisition := grokStreamingHedgeAcquisition{
		response:         resp,
		requestBody:      requestBody,
		upstreamDuration: time.Since(upstreamStartedAt),
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return acquisition
	}
	semanticOutput, stageErr := stageGrokStreamingHedgeResponse(resp, a.service.cfg)
	acquisition.semanticOutput = semanticOutput
	acquisition.stageErr = stageErr
	if resp.Body != nil {
		a.trackBody(resp.Body)
	}
	return acquisition
}

func (a *grokStreamingHedgeAttempt) trackBody(body io.Closer) {
	a.bodyMu.Lock()
	defer a.bodyMu.Unlock()
	if a.canceled {
		if body != nil {
			_ = body.Close()
		}
		return
	}
	a.currentBody = body
}

func (a *grokStreamingHedgeAttempt) abort() {
	a.bodyMu.Lock()
	if a.canceled {
		a.bodyMu.Unlock()
		return
	}
	a.canceled = true
	a.cancel()
	body := a.currentBody
	a.bodyMu.Unlock()
	if body != nil {
		_ = body.Close()
	}
}

func stageGrokStreamingHedgeResponse(resp *http.Response, cfg *config.Config) (bool, error) {
	if resp == nil || resp.Body == nil {
		return false, errors.New("grok hedge response body is empty")
	}
	stage := newDefaultOpenAIFirstOutputStage()
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	parser := openAICompatSSEFrameParser{}
	maxLineSize := defaultMaxLineSize
	if cfg != nil && cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = cfg.Gateway.MaxLineSize
	}
	if maxLineSize > openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance {
		maxLineSize = openAIFirstOutputStageMaxBytes + openAIFirstOutputScannerFramingAllowance
	}
	var line bytes.Buffer

	replay := func() error {
		var prefix bytes.Buffer
		if err := stage.CommitTo(&prefix); err != nil {
			return err
		}
		if err := stage.Close(); err != nil {
			return err
		}
		resp.Body = &grokStreamingHedgeReplayBody{
			Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), reader),
			source: resp.Body,
		}
		return nil
	}
	finishFrame := func(frame openAICompatSSEFrame, ok bool) (bool, error) {
		if !ok {
			return false, nil
		}
		eventType := strings.TrimSpace(gjson.Get(frame.Data, "type").String())
		if eventType == "" {
			eventType = frame.EventType
		}
		if !openAIStreamDataStartsClientOutput(frame.Data, eventType) {
			return false, nil
		}
		return true, replay()
	}

	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if _, err := stage.Write(fragment); err != nil {
				_ = stage.Close()
				return false, err
			}
			if _, err := line.Write(fragment); err != nil {
				_ = stage.Close()
				return false, err
			}
			if line.Len() > maxLineSize {
				_ = stage.Close()
				return false, errOpenAIFirstOutputScannerLimit
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = stage.Close()
			return false, readErr
		}

		if line.Len() > 0 {
			text := strings.TrimSuffix(line.String(), "\n")
			text = strings.TrimSuffix(text, "\r")
			line.Reset()
			if semantic, err := finishFrame(parser.AddLine(text)); semantic || err != nil {
				return semantic, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			if semantic, err := finishFrame(parser.Finish()); semantic || err != nil {
				return semantic, err
			}
			return false, replay()
		}
	}
}

func (s *OpenAIGatewayService) commitGrokStreamingHedgeAttempt(
	ctx context.Context,
	c *gin.Context,
	attempt *grokStreamingHedgeAttempt,
	acquisition grokStreamingHedgeAcquisition,
) (*OpenAIForwardResult, error) {
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, acquisition.upstreamDuration.Milliseconds())
	if acquisition.err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, attempt.account, acquisition.err, false)
	}
	resp := acquisition.response
	if resp == nil {
		return nil, errors.New("grok hedge upstream returned no response")
	}
	defer func() { _ = resp.Body.Close() }()
	if acquisition.stageErr != nil {
		message := "Grok first-output staging failed"
		if errors.Is(acquisition.stageErr, errOpenAIFirstOutputStageLimit) {
			message = "Grok first-output staging limit exceeded"
		}
		failoverErr := s.newOpenAIStreamFailoverError(
			c,
			attempt.account,
			false,
			firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			nil,
			message,
		)
		failoverErr.SafeToFailoverAfterWrite = true
		return nil, failoverErr
	}

	if resp.StatusCode >= http.StatusBadRequest {
		respBody := s.readUpstreamErrorBody(resp)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		upstreamMsg := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))
		if upstreamMsg == "" {
			upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
		}
		kind := "http_error"
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			kind = "failover"
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           attempt.account.Platform,
			AccountID:          attempt.account.ID,
			AccountName:        attempt.account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			Kind:               kind,
			Message:            upstreamMsg,
		})
		s.handleGrokAccountUpstreamError(ctx, attempt.account, resp.StatusCode, resp.Header, respBody)
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				ResponseHeaders:        resp.Header.Clone(),
				RetryableOnSameAccount: attempt.account.IsPoolMode() && attempt.account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		return s.handleErrorResponse(ctx, resp, c, attempt.account, acquisition.requestBody, attempt.upstreamModel)
	}

	s.updateGrokUsageFromResponse(ctx, attempt.account, resp.Header, resp.StatusCode)
	setGrokResponsesClientToolMapping(c, attempt.clientToolMapping)
	if hasGrokResponsesClientToolMapping(attempt.clientToolMapping) {
		maxLineSize := defaultMaxLineSize
		if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
			maxLineSize = s.cfg.Gateway.MaxLineSize
		}
		resp.Body = newGrokResponsesClientToolStreamBody(resp.Body, attempt.clientToolMapping, maxLineSize)
	}
	streamResult, err := s.handleStreamingResponse(
		ctx,
		resp,
		c,
		attempt.account,
		attempt.startTime,
		attempt.originalModel,
		attempt.upstreamModel,
	)
	if err != nil {
		return nil, err
	}
	usage := streamResult.usage
	if usage == nil {
		usage = &OpenAIUsage{}
	}
	return &OpenAIForwardResult{
		RequestID:                     firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
		ResponseID:                    strings.TrimSpace(streamResult.responseID),
		Usage:                         *usage,
		Model:                         attempt.originalModel,
		UpstreamModel:                 attempt.upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		ReasoningEffort:               extractOpenAIReasoningEffortFromBody(acquisition.requestBody, attempt.originalModel),
		Stream:                        true,
		OpenAIWSMode:                  false,
		ResponseHeaders:               resp.Header.Clone(),
		Duration:                      time.Since(attempt.startTime),
		FirstTokenMs:                  streamResult.firstTokenMs,
	}, nil
}
