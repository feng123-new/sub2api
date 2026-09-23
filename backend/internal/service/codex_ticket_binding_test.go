package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
	"time"
)

type bindingAccounts struct{ materialAccountRepo }

func (r *bindingAccounts) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	if r.account.Extra == nil {
		r.account.Extra = map[string]any{}
	}
	for k, v := range extra {
		r.account.Extra[k] = v
	}
	return nil
}

type bindingHistory struct {
	materialHistoryRepo
	latest *CodexTicketMaterial
}

func (r *bindingHistory) GetMaterial(ctx context.Context, id int64, model string, attempt int64) (*CodexTicketMaterial, error) {
	if attempt == 0 {
		return r.latest, nil
	}
	return r.materialHistoryRepo.GetMaterial(ctx, id, model, attempt)
}
func TestCodexTicketPinPersistsAndOverridesNewerMemory(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	acc.Extra = map[string]any{}
	up := &maintenanceUpstream{status: 200, body: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n"}
	cfg := config.OpenAICodexTicketConfig{Enabled: true}
	svc := ticketTestService(t, cfg, up)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	old := &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, State: "gAAAAA" + strings.Repeat("b", 286), CapturedAt: now.Add(-55 * time.Minute), ExpiresAt: now.Add(5 * time.Minute)}
	latest := &CodexTicketMaterial{AttemptID: 73, AccountID: 41, Model: model, State: fakeCodexTicketState(292), CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	svc.openaiCodexTicketHistory = &bindingHistory{materialHistoryRepo: materialHistoryRepo{material: old}, latest: latest}
	result, err := svc.ConfirmCodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.Equal(t, true, result["locked"])
	require.True(t, CodexTicketHarvestEnabled(acc, model))
	require.True(t, codexTicketPinActive(acc, model, now))
	svc.openaiCodexTickets.Store(openAICodexTicketKey(41, model), &openAICodexTicket{State: latest.State, Length: 292, CapturedAt: latest.CapturedAt, ExpiresAt: latest.ExpiresAt})
	h := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(ctx, acc, model, h))
	require.Equal(t, old.State, h.Get(openAICodexTurnStateHeader))
	fresh := ticketTestService(t, cfg, up)
	fresh.accountRepo = svc.accountRepo
	fresh.openaiCodexTicketHistory = svc.openaiCodexTicketHistory
	require.Equal(t, old.State, fresh.lookupOpenAICodexTicket(acc, model).State)
	calls := up.calls
	fresh.checkOrRenewCodexTicket(ctx, acc, model)
	require.Equal(t, calls, up.calls, "recent manual confirmation suppresses duplicate automatic validation")
	_, err = fresh.runCodexTicketAttempt(ctx, acc, model, "manual")
	require.NotErrorIs(t, err, ErrCodexTicketPinned, "manual selection must not permanently disable renewal")
	require.NoError(t, svc.UnpinCodexTicket(ctx, 41, model))
	require.False(t, codexTicketPinActive(acc, model, now))
	require.Equal(t, latest.State, svc.lookupOpenAICodexTicket(acc, model).State)
}
func TestCodexTicketPinFailureLeavesBindingUnchanged(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	up := &maintenanceUpstream{status: 429}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, up)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	svc.openaiCodexTicketHistory = &materialHistoryRepo{material: &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, State: fakeCodexTicketState(292), ExpiresAt: now.Add(time.Hour)}}
	result, err := svc.ConfirmCodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.Equal(t, false, result["locked"])
	require.Nil(t, codexTicketPin(acc, model))
	require.Equal(t, now.Add(5*time.Minute), codexPinnedNextCheck(now.Add(5*time.Minute), now))
}
