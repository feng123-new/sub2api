package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexIdentityRolloutAccounts(t *testing.T) {
	root := "019956a0-1234-7000-8000-000000000001"
	for _, id := range []int64{17, 19, 22, 24, 156, 157, 160} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			a := newTestOAuthAccount(id, map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true})
			a.Credentials = map[string]any{"chatgpt_account_id": fmt.Sprintf("synthetic-%d", id)}
			require.True(t, codexIdentityRelationsEnabled(a))
			values := map[string]any{"session_id": root, "thread_id": root, "window_id": root + ":4", "parent_thread_id": root, "turn_id": "turn", "root_turn_id": "turn"}
			require.True(t, applyCodexAccountIdentityFields(values, a, 1))
			require.Equal(t, values["session_id"], values["thread_id"])
			mappedThread, ok := values["thread_id"].(string)
			require.True(t, ok)
			require.Equal(t, mappedThread+":4", values["window_id"])
			require.Equal(t, values["thread_id"], values["parent_thread_id"])
			require.Equal(t, values["turn_id"], values["root_turn_id"])
			require.NotEqual(t, values["session_id"], scopeCodexAccountIdentityValue(a, 2, "session", root))
			a.Extra["codex_identity_relations_v1"] = false
			require.False(t, codexIdentityRelationsEnabled(a))
			a.Extra["codex_identity_relations_v1"] = true
			for _, mode := range []string{"off", "session", "full"} {
				a.Extra["codex_fingerprint_mode"] = mode
				require.False(t, codexIdentityRelationsEnabled(a))
			}
		})
	}
	for _, id := range []int64{18, 20, 21, 25, 155, 158, 159, 161} {
		a := newTestOAuthAccount(id, map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true})
		require.False(t, codexIdentityRelationsEnabled(a), "account %d must remain excluded", id)
	}
}
