package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type validationStatusHistory struct {
	materialHistoryRepo
	record *CodexTicketValidationStatus
}

func (r *validationStatusHistory) SaveValidation(_ context.Context, _ int64, _ string, v *CodexTicketValidationStatus) error {
	c := *v
	r.record = &c
	return nil
}
func (r *validationStatusHistory) LatestValidation(context.Context, int64, string) (*CodexTicketValidationStatus, error) {
	return r.record, nil
}

func TestCodexTicketValidationFailureIsPersisted(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	up := &maintenanceUpstream{status: 200, body: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.6-luna\"}}\n\n"}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, up)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	repo := &validationStatusHistory{materialHistoryRepo: materialHistoryRepo{material: &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, State: fakeCodexTicketState(292), CapturedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}}}
	svc.openaiCodexTicketHistory = repo
	result, err := svc.ConfirmCodexTicketMaterial(ctx, 41, model, 72)
	require.NoError(t, err)
	require.Equal(t, false, result["locked"])
	require.NotNil(t, repo.record)
	require.Equal(t, "response_model_mismatch", repo.record.Outcome)
	require.Equal(t, int64(72), repo.record.AttemptID)
	require.Equal(t, 200, repo.record.HTTPStatus)
	binding, err := svc.CodexTicketBinding(ctx, 41, model)
	require.NoError(t, err)
	require.Equal(t, repo.record, binding["last_validation"])
	require.False(t, binding["active"].(bool))
}

func TestCodexTicketMissingMaterialStatusIsPersisted(t *testing.T) {
	ctx := context.Background()
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	repo := &validationStatusHistory{}
	svc.openaiCodexTicketHistory = repo
	_, err := svc.ConfirmCodexTicketMaterial(ctx, 41, "gpt-6-astra", 72)
	require.ErrorIs(t, err, ErrCodexTicketMaterialMissing)
	require.NotNil(t, repo.record)
	require.Equal(t, "material_missing", repo.record.Outcome)
	require.Equal(t, int64(72), repo.record.AttemptID)
}
