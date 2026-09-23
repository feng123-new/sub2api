package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCodexTicketRetryRestoredCadence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining time.Duration
		min, max  time.Duration
	}{
		{"valid", 5 * time.Minute, 20 * time.Second, 40 * time.Second},
		{"near_expiry", 5 * time.Second, 5 * time.Second, 5 * time.Second},
		{"expired", -time.Minute, 10 * time.Second, 10 * time.Second},
		{"missing", 0, 10 * time.Second, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
			account := ticketTestAccount(41)
			now := time.Now()
			model := "gpt-6-astra"
			if tc.name != "missing" {
				svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now.Add(-55 * time.Minute), ExpiresAt: now.Add(tc.remaining)})
			}
			for n := 0; n < 8; n++ {
				svc.scheduleCodexTicketFailure(account, model, now)
				raw, ok := svc.openaiCodexTicketNextAttempt.Load(openAICodexTicketKey(41, model))
				require.True(t, ok)
				delay := raw.(time.Time).Sub(now)
				require.GreaterOrEqual(t, delay, tc.min)
				require.LessOrEqual(t, delay, tc.max)
			}
		})
	}
}
