package repository

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSchedulerExtraPreservesPinnedTicket(t *testing.T) {
	extra := map[string]any{"codex_ticket_pin:gpt-6-astra": map[string]any{"attempt_id": 72}, "codex_turn_ticket:gpt-6-astra": map[string]any{"state": "test-ticket"}, "codex_ticket_harvest_enabled": true}
	filtered := filterSchedulerExtra(extra)
	require.Equal(t, extra, filtered)
}
