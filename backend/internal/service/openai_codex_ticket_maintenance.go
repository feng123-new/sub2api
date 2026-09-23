package service

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const codexTicketCheckInterval = 10 * time.Minute

func codexTicketNextCheck(captured, expires, now time.Time) time.Time {
	next := now.Add(codexTicketCheckInterval)
	renew := expires.Add(-10 * time.Minute)
	if !expires.IsZero() && renew.Before(next) {
		next = renew
	}
	if next.Before(now) {
		return now
	}
	return next
}

func (s *OpenAIGatewayService) scheduleCodexTicketFailure(account *Account, model string, now time.Time) {
	key := openAICodexTicketKey(account.ID, model)
	delay := 10 * time.Second
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(now, openAICodexTicketTargetLength(account, s.openAICodexTicketConfig().TargetLength)) {
		delay = codexTicketJitter(key, now, 20*time.Second, 40*time.Second)
		if remaining := ticket.ExpiresAt.Sub(now); remaining < delay {
			delay = remaining
		}
	}
	s.openaiCodexTicketNextAttempt.Store(key, now.Add(delay))
}

func (s *OpenAIGatewayService) checkOrRenewCodexTicket(ctx context.Context, account *Account, model string) {
	now := time.Now()
	ticket := s.lookupOpenAICodexTicket(account, model)
	if !ticket.valid(now, openAICodexTicketTargetLength(account, s.openAICodexTicketConfig().TargetLength)) || ticket.needsRefresh(now, 10*time.Minute) {
		s.probeOnceOpenAICodexTicket(ctx, account, model)
		return
	}
	if !ticket.ValidationCheckedAt.IsZero() && now.Sub(ticket.ValidationCheckedAt) < codexTicketCheckInterval {
		s.openaiCodexTicketNextAttempt.Store(openAICodexTicketKey(account.ID, model), codexTicketNextCheck(ticket.CapturedAt, ticket.ExpiresAt, ticket.ValidationCheckedAt))
		return
	}
	key := openAICodexTicketKey(account.ID, model)
	if _, busy := s.openaiCodexTicketInFlight.LoadOrStore(key, true); busy {
		return
	}
	defer s.openaiCodexTicketInFlight.Delete(key)
	if s.openaiCodexTicketHistory != nil {
		unlock, acquired, err := s.openaiCodexTicketHistory.TryLock(ctx, account.ID, model)
		if err != nil || !acquired {
			return
		}
		defer unlock()
	}
	defer func() {
		current := s.lookupOpenAICodexTicket(account, model)
		if current != nil && current.Rejected {
			s.openaiCodexTicketNextAttempt.Store(key, time.Now().Add(10*time.Second))
			return
		}
		next := codexTicketNextCheck(ticket.CapturedAt, ticket.ExpiresAt, time.Now())

		s.openaiCodexTicketNextAttempt.Store(key, next)
	}()
	outcome, status := "local_recent_usage", 0
	if !s.codexTicketRecentUsage(ctx, account, model, ticket, now) {
		outcome, status = s.validateCodexTicketBusiness(ctx, account, model, ticket)
	}
	material := &CodexTicketMaterial{AccountID: account.ID, Model: model, State: ticket.State, CapturedAt: ticket.CapturedAt, ExpiresAt: ticket.ExpiresAt}
	report := &CodexTicketValidationStatus{Outcome: outcome, HTTPStatus: status, CheckedAt: time.Now()}
	_ = s.recordCurrentTicketValidation(ctx, account.ID, model, material, report)
}

func (s *OpenAIGatewayService) codexTicketRecentUsage(ctx context.Context, account *Account, model string, ticket *openAICodexTicket, now time.Time) bool {
	if s.usageLogRepo == nil {
		return false
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, _, err := s.usageLogRepo.ListByAccount(readCtx, account.ID, pagination.PaginationParams{Page: 1, PageSize: 20, SortBy: "created_at", SortOrder: "desc"})
	if err != nil {
		return false
	}
	since := ticket.CapturedAt
	if pin := codexTicketPin(account, model); pin != nil && pin.ConfirmedAt.After(since) {
		since = pin.ConfirmedAt
	}
	for _, row := range rows {
		if row.CreatedAt.Before(now.Add(-codexTicketCheckInterval)) || row.CreatedAt.Before(since) || row.OutputTokens <= 0 {
			continue
		}
		if row.UpstreamModel == nil || *row.UpstreamModel != model || row.UpstreamResponseModel == nil || *row.UpstreamResponseModel != model {
			continue
		}
		if row.UpstreamModelMismatch != nil && *row.UpstreamModelMismatch {
			continue
		}
		return true
	}
	return false
}

func (s *OpenAIGatewayService) validateCodexTicketBusiness(ctx context.Context, account *Account, model string, ticket *openAICodexTicket) (string, int) {
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	proxyURL := ""
	if account.ProxyID != nil {
		if account.Proxy == nil {
			return "business_proxy_unavailable", 0
		}
		proxyURL = account.Proxy.URL()
	}
	token, _, err := s.GetAccessToken(checkCtx, account)
	if err != nil || token == "" {
		return "credentials_unavailable", 0
	}
	body := []byte(`{"model":` + jsonString(model) + `,"store":false,"stream":true,"instructions":"Reply with exactly: pong","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	req, err := http.NewRequestWithContext(WithHTTPUpstreamProfile(checkCtx, HTTPUpstreamProfileOpenAI), http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "request_error", 0
	}
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(checkCtx, s.accountRepo, req.Header, account); err != nil {
		return "account_identity_error", 0
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)
	req.Header.Set(openAICodexTurnStateHeader, ticket.State)
	ApplyOpenAIOutboundTimezone(req)
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		if checkCtx.Err() == context.DeadlineExceeded {
			return "validation_timeout", 0
		}
		return "transport_error", 0
	}
	if resp == nil {
		return "empty_response", 0
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode == 429 {
		return "rate_limited", 429
	}
	if resp.StatusCode != 200 {
		return "upstream_http_error", resp.StatusCode
	}
	if resp.Body == nil {
		return "incomplete_response", 200
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		switch gjson.Get(data, "type").String() {
		case "response.completed":
			if gjson.Get(data, "response.model").String() != model {
				return "response_model_mismatch", 200
			}
			return "remote_completed", 200
		case "response.failed", "response.incomplete", "error":
			return "response_failed", 200
		}
	}
	if checkCtx.Err() == context.DeadlineExceeded {
		return "validation_timeout", 200
	}
	return "incomplete_response", 200
}
