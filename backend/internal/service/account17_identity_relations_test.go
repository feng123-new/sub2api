package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccount17IdentityRelationsOptInAndShadowIsolation(t *testing.T) {
	root := "019956a0-1234-7000-8000-000000000001"
	for _, id := range []int64{17, 24} {
		a := newTestOAuthAccount(id, map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true})
		a.Credentials = map[string]any{"chatgpt_account_id": "synthetic-upstream"}
		session := scopeCodexAccountIdentityValue(a, 1, "session", root)
		require.Equal(t, session, scopeCodexAccountIdentityValue(a, 1, "thread", root), "account=%d", id)
		require.Equal(t, session+":2", scopeCodexAccountIdentityValue(a, 1, "window", root+":2"))
		require.NotEqual(t, session, scopeCodexAccountIdentityValue(a, 2, "session", root))
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set(codexAccountIdentitySourceContextKey, a)
		for _, selected := range []int64{17, 24, 25} {
			fallback := &Account{ID: selected}
			source := codexAccountIdentitySource(ctx, fallback)
			thread := scopeCodexAccountIdentityValue(source, 1, "thread", root)
			if selected == id {
				require.Equal(t, session, thread)
			} else {
				require.NotEqual(t, session, thread)
			}
		}
		require.Equal(t, session, scopeCodexAccountIdentityValue(a, 1, "thread", root))
		for _, mode := range []string{"off", "session", "full"} {
			a.Extra["codex_fingerprint_mode"] = mode
			require.NotEqual(t, scopeCodexAccountIdentityValue(a, 1, "session", root), scopeCodexAccountIdentityValue(a, 1, "thread", root))
		}
		a.Extra["codex_fingerprint_mode"] = "device"
		a.Extra["codex_identity_relations_v1"] = false
		require.NotEqual(t, scopeCodexAccountIdentityValue(a, 1, "session", root), scopeCodexAccountIdentityValue(a, 1, "thread", root))
	}
}
