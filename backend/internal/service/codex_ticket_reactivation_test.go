package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"strconv"
	"testing"
	"time"
)

func TestCodexTicketExpiredReactivationUpdatesCurrentStatus(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			now := time.Now()
			model := "gpt-6-astra"
			acc := ticketTestAccount(41)
			acc.Status = StatusActive
			up := &maintenanceUpstream{status: status, body: "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n"}
			cfg := config.OpenAICodexTicketConfig{Enabled: true, TTLSeconds: 3600, TargetLength: 292, Models: []string{model}}
			svc := ticketTestService(t, cfg, up)
			svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
			old := &CodexTicketMaterial{AttemptID: 72, AccountID: 41, Model: model, State: fakeCodexTicketState(292), CapturedAt: now.Add(-8 * time.Hour), ExpiresAt: now.Add(-7 * time.Hour)}
			svc.openaiCodexTicketHistory = &materialHistoryRepo{material: old}
			result, err := svc.ConfirmCodexTicketMaterial(context.Background(), 41, model, 72)
			require.NoError(t, err)
			require.Equal(t, 1, up.calls)
			if status == 429 {
				require.Equal(t, false, result["locked"])
				require.Nil(t, codexTicketPin(acc, model))
				return
			}
			require.Equal(t, true, result["locked"])
			pin := codexTicketPin(acc, model)
			require.NotNil(t, pin)
			require.WithinDuration(t, now.Add(time.Hour), pin.ExpiresAt, time.Second)
			ticket := svc.lookupOpenAICodexTicket(acc, model)
			require.True(t, ticket.valid(time.Now(), 292))
			require.WithinDuration(t, now, ticket.CapturedAt, time.Second)
			states := OpenAICodexTicketStatuses(acc, cfg, time.Now())
			require.Len(t, states, 1)
			require.True(t, states[0].Ready)
			require.Greater(t, states[0].RemainingSeconds, int64(3590))
			require.Equal(t, now.Add(-7*time.Hour), old.ExpiresAt)
		})
	}
}
