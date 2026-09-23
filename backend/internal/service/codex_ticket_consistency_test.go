package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTicketConsistencyRejectsCurrentModelMismatch(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	ticket := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	acc.Extra[openAICodexTicketExtraKey(model)] = ticket
	up := &maintenanceUpstream{status: 200, body: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.6-luna\"}}\n\n"}
	cfg := config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, FailClosed: false, Models: []string{model}}
	svc := ticketTestService(t, cfg, up)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	svc.openaiCodexTicketHistory = &validationStatusHistory{materialHistoryRepo: materialHistoryRepo{material: &CodexTicketMaterial{AttemptID: 3041, AccountID: 41, Model: model, State: ticket.State, CapturedAt: ticket.CapturedAt, ExpiresAt: ticket.ExpiresAt}}}
	stale := *ticket
	svc.openaiCodexTickets.Store(openAICodexTicketKey(41, model), &stale)
	result, err := svc.ValidateCodexTicketMaterial(ctx, 41, model, 3041)
	require.NoError(t, err)
	require.Equal(t, "response_model_mismatch", result["outcome"])
	statuses := OpenAICodexTicketStatuses(acc, cfg, time.Now())
	require.Len(t, statuses, 1)
	require.False(t, statuses[0].Ready, "same ticket must not remain ready after a definitive model mismatch")
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "original-client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(ctx, acc, model, headers))
	require.Equal(t, "original-client-state", headers.Get(openAICodexTurnStateHeader), "allow-without-ticket must not inject a rejected ticket")
	cfg.FailClosed = true
	svc.cfg.Gateway.OpenAICodexTicket = cfg
	require.ErrorIs(t, svc.applyOpenAICodexTicket(ctx, acc, model, http.Header{}), ErrOpenAICodexTicketUnavailable)
	fresh := ticketTestService(t, cfg, up)
	require.False(t, fresh.lookupOpenAICodexTicket(acc, model).valid(time.Now(), 292), "persisted rejection must survive process restart")
}

func TestTicketConsistencyOldRecordCannotInvalidateCurrent(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	current := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	acc.Extra[openAICodexTicketExtraKey(model)] = current
	up := &maintenanceUpstream{status: 200, body: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"other\"}}\n\n"}
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{model}}
	svc := ticketTestService(t, cfg, up)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	svc.openaiCodexTicketHistory = &validationStatusHistory{materialHistoryRepo: materialHistoryRepo{material: &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, State: "gAAAAA" + strings.Repeat("x", 286), CapturedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}}}
	_, err := svc.ValidateCodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.True(t, svc.lookupOpenAICodexTicket(acc, model).valid(time.Now(), 292))
	require.True(t, OpenAICodexTicketStatuses(acc, cfg, time.Now())[0].Ready)
}

func TestTicketConsistencyHidesStaleCheckForNewTicket(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Extra[openAICodexTicketExtraKey(model)] = &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	acc.Extra["codex_ticket_check:"+model] = map[string]any{"checked_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "outcome": "response_model_mismatch", "ticket_captured_at": now.Add(-2 * time.Hour).Format(time.RFC3339Nano)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	result, err := svc.CodexTicketBinding(ctx, 41, model)
	require.NoError(t, err)
	require.Empty(t, result["last_check"], "old ticket check must not appear as current ticket check")
}
