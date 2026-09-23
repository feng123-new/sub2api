package service

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

type materialAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *materialAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account.ID != id {
		return nil, ErrCodexTicketMaterialMissing
	}
	return r.account, nil
}

type materialHistoryRepo struct {
	CodexTicketAttemptRepository
	material *CodexTicketMaterial
}

func (r *materialHistoryRepo) TryLock(context.Context, int64, string) (func(), bool, error) {
	return func() {}, true, nil
}
func (r *materialHistoryRepo) SaveMaterial(context.Context, *CodexTicketMaterial) error { return nil }
func (r *materialHistoryRepo) GetMaterial(_ context.Context, id int64, model string, attempt int64) (*CodexTicketMaterial, error) {
	if r.material == nil || r.material.AccountID != id || r.material.Model != model || r.material.AttemptID != attempt {
		return nil, ErrCodexTicketMaterialMissing
	}
	c := *r.material
	return &c, nil
}

func TestCodexTicketMaterialSelectionPreservesLiveTicket(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	up := &maintenanceUpstream{status: 200, body: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n"}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, up)
	latest := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	svc.storeOpenAICodexTicket(ctx, acc, latest)
	svc.accountRepo = &materialAccountRepo{account: acc}
	older := &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, State: "gAAAAA" + strings.Repeat("b", 286), CapturedAt: now.Add(-time.Minute), ExpiresAt: now.Add(59 * time.Minute)}
	history := &materialHistoryRepo{material: older}
	svc.openaiCodexTicketHistory = history
	result, err := svc.ValidateCodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.Equal(t, true, result["completed"])
	require.Equal(t, older.State, up.req.Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, latest.State, svc.lookupOpenAICodexTicket(acc, model).State)
	_, err = svc.ValidateCodexTicketMaterial(ctx, 41, model, 0)
	require.NoError(t, err)
	require.Equal(t, latest.State, up.req.Header.Get(openAICodexTurnStateHeader))
	_, err = svc.CodexTicketMaterial(ctx, 41, "gpt-5.6-sol", 72)
	require.ErrorIs(t, err, ErrCodexTicketMaterialMissing)
	_, err = svc.CodexTicketMaterial(ctx, 42, model, 72)
	require.ErrorIs(t, err, ErrCodexTicketMaterialMissing)
	calls := up.calls
	older.ExpiresAt = now.Add(-time.Second)
	_, err = svc.ValidateCodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.Equal(t, calls+1, up.calls)
	require.Equal(t, latest.State, svc.lookupOpenAICodexTicket(acc, model).State)
}

func TestCodexTicketMaterialLegacyNeverSubstitutesNewer(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	ticket := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	acc.Extra[openAICodexTicketExtraKey(model)] = ticket
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = &materialAccountRepo{account: acc}
	history := &materialHistoryRepo{material: &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, ExpiresAt: ticket.ExpiresAt.Truncate(time.Microsecond)}}
	svc.openaiCodexTicketHistory = history
	got, err := svc.CodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.Equal(t, ticket.State, got.State)
	history.material.ExpiresAt = now.Add(-time.Hour)
	_, err = svc.CodexTicketMaterial(ctx, 41, model, 72)
	require.ErrorIs(t, err, ErrCodexTicketMaterialMissing)
	encoded, err := json.Marshal(CodexTicketAttempt{ID: 72, AccountID: 41, Model: model, Outcome: "success"})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), ticket.State)
}
