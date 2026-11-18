package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexIdentityRelationsConfigurationAppliesToAnyAccount(t *testing.T) {
	for _, id := range []int64{7, 23, 25, 166, 167, 171, 173, 174, 175, 177, 178, 900001} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			account := &Account{
				ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"chatgpt_account_id": fmt.Sprintf("synthetic-%d", id)},
				Extra:       map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true},
			}
			require.True(t, codexIdentityRelationsEnabled(account))
			root := "019956a0-1234-7000-8000-000000000001"
			child := "019956a0-1234-7000-8000-000000000002"
			values := map[string]any{
				"session_id": root, "thread_id": child, "window_id": child + ":4",
				"parent_thread_id": root, "forked_from_thread_id": root,
				"turn_id": "turn", "parent_turn_id": "turn", "root_turn_id": "turn",
			}
			require.True(t, applyCodexAccountIdentityFields(values, account, 1))
			require.Equal(t, values["session_id"], values["parent_thread_id"])
			require.Equal(t, values["session_id"], values["forked_from_thread_id"])
			require.NotEqual(t, values["session_id"], values["thread_id"])
			require.Equal(t, values["thread_id"].(string)+":4", values["window_id"])
			require.Equal(t, values["turn_id"], values["root_turn_id"])
			require.Equal(t, values["turn_id"], values["parent_turn_id"])
			require.Equal(t, scopeCodexAccountIdentityValue(account, 1, "session", root),
				scopeCodexAccountIdentityValue(account, 1, "thread", root))
			require.NotEqual(t, values["session_id"], scopeCodexAccountIdentityValue(account, 2, "session", root))
			other := *account
			other.Credentials = map[string]any{"chatgpt_account_id": "different-account"}
			require.NotEqual(t, values["session_id"], scopeCodexAccountIdentityValue(&other, 1, "session", root))
		})
	}
}

func TestCodexIdentityRelationsConfigurationRemainsExplicit(t *testing.T) {
	for _, value := range []any{nil, false, "true", float64(1)} {
		account := &Account{
			ID: 171, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Extra: map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": value},
		}
		require.False(t, codexIdentityRelationsEnabled(account), "value=%v", value)
	}
	for _, mode := range []string{"off", "session", "full", ""} {
		account := &Account{
			ID: 171, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Extra: map[string]any{"codex_fingerprint_mode": mode, "codex_identity_relations_v1": true},
		}
		require.False(t, codexIdentityRelationsEnabled(account), "mode=%s", mode)
	}
	account := &Account{
		ID: 171, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Extra: map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true},
	}
	require.False(t, codexIdentityRelationsEnabled(account))
	require.False(t, codexIdentityRelationsEnabled(nil))
}
