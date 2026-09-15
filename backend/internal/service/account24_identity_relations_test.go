package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccount24IdentityRelationsProjection(t *testing.T) {
	a := newTestOAuthAccount(24, map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true})
	a.Credentials = map[string]any{"chatgpt_account_id": "synthetic-parent"}
	root := "019956a0-1234-7000-8000-000000000001"
	child := "019956a0-1234-7000-8000-000000000002"
	turn := "019956a0-1234-7000-8000-000000000003"
	for _, thread := range []string{root, child} {
		t.Run(thread, func(t *testing.T) {
			metadata := map[string]any{"session_id": root, "thread_id": thread, "turn_id": turn, "root_turn_id": turn, "parent_turn_id": turn, "parent_thread_id": root, "forked_from_thread_id": root, "window_id": thread + ":2", "window_number": float64(2), "turn_started_at_unix_ms": float64(12345), "installation_id": "installation-a"}
			embedded, err := json.Marshal(metadata)
			require.NoError(t, err)
			cm := map[string]any{"session_id": root, "thread_id": thread, "turn_id": turn, "root_turn_id": turn, "parent_turn_id": turn, "x-codex-parent-thread-id": root, "x-codex-window-id": thread + ":2", "x-codex-installation-id": "installation-a", "x-codex-turn-metadata": string(embedded)}
			body := map[string]any{"prompt_cache_key": root, "client_metadata": cm}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			require.True(t, applyCodexAccountIdentityClientMetadataMap(body, a, 1001))
			projectedRaw, changed, err := applyCodexAccountIdentityClientMetadataRaw(raw, a, 1001)
			require.NoError(t, err)
			require.True(t, changed)
			var rawBody map[string]any
			require.NoError(t, json.Unmarshal(projectedRaw, &rawBody))
			require.Equal(t, body, rawBody)
			if thread == root {
				require.Equal(t, cm["session_id"], cm["thread_id"])
			} else {
				require.NotEqual(t, cm["session_id"], cm["thread_id"])
			}
			mappedThread, ok := cm["thread_id"].(string)
			require.True(t, ok)
			require.Equal(t, mappedThread+":2", cm["x-codex-window-id"])
			require.Equal(t, cm["session_id"], body["prompt_cache_key"])
			require.Equal(t, cm["session_id"], cm["x-codex-parent-thread-id"])
			require.Equal(t, cm["turn_id"], cm["root_turn_id"])
			require.Equal(t, cm["turn_id"], cm["parent_turn_id"])
			var nested map[string]any
			metadataJSON, ok := cm["x-codex-turn-metadata"].(string)
			require.True(t, ok)
			require.NoError(t, json.Unmarshal([]byte(metadataJSON), &nested))
			for _, key := range []string{"session_id", "thread_id", "turn_id", "root_turn_id", "parent_turn_id"} {
				require.Equal(t, cm[key], nested[key])
			}
			require.Equal(t, cm["session_id"], nested["parent_thread_id"])
			require.Equal(t, cm["session_id"], nested["forked_from_thread_id"])
			require.Equal(t, cm["x-codex-window-id"], nested["window_id"])
			require.Equal(t, float64(2), nested["window_number"])
			require.Equal(t, float64(12345), nested["turn_started_at_unix_ms"])
			h := http.Header{}
			h.Set("session-id", root)
			h.Set("thread-id", thread)
			h.Set("x-codex-parent-thread-id", root)
			h.Set("x-codex-window-id", thread+":2")
			h.Set("x-codex-turn-metadata", string(embedded))
			applyCodexAccountIdentityHeaders(h, a, 1001)
			require.Equal(t, cm["session_id"], h.Get("session-id"))
			require.Equal(t, cm["thread_id"], h.Get("thread-id"))
			require.Equal(t, cm["x-codex-window-id"], h.Get("x-codex-window-id"))
			require.Equal(t, cm["session_id"], h.Get("x-codex-parent-thread-id"))
		})
	}
}

func TestAccount24IdentityRelationsIsolationAndOptIn(t *testing.T) {
	raw := "019956a0-1234-7000-8000-000000000001"
	makeAccount := func(id int64, enabled bool) *Account {
		a := newTestOAuthAccount(id, map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": enabled})
		a.Credentials = map[string]any{"chatgpt_account_id": "synthetic-parent"}
		return a
	}
	a := makeAccount(24, true)
	require.Equal(t, scopeCodexAccountIdentityValue(a, 1, "session", raw), scopeCodexAccountIdentityValue(a, 1, "thread", raw))
	require.NotEqual(t, scopeCodexAccountIdentityValue(a, 1, "thread", raw), scopeCodexAccountIdentityValue(a, 2, "thread", raw))
	for _, other := range []*Account{makeAccount(24, false), makeAccount(25, true)} {
		require.NotEqual(t, scopeCodexAccountIdentityValue(other, 1, "session", raw), scopeCodexAccountIdentityValue(other, 1, "thread", raw))
		for _, unknown := range []string{"opaque", raw + ":bad", raw + ":-1"} {
			require.Equal(t, scopeCodexAccountIdentityValue(makeAccount(24, false), 1, "window", unknown), scopeCodexAccountIdentityValue(a, 1, "window", unknown))
		}
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set(codexAccountIdentitySourceContextKey, a)
	source := codexAccountIdentitySource(ctx, makeAccount(25, false))
	require.NotEqual(t, scopeCodexAccountIdentityValue(source, 1, "session", raw), scopeCodexAccountIdentityValue(source, 1, "thread", raw))
	require.Equal(t, scopeCodexAccountIdentityValue(a, 1, "session", raw), scopeCodexAccountIdentityValue(a, 1, "thread", raw))
}

func TestAccount24IdentityRelationsOtherModesUnchanged(t *testing.T) {
	for _, id := range []int64{24, 25} {
		for _, mode := range []string{"off", "device", "session", "full"} {
			if id == 24 && mode == "device" {
				continue
			}
			a := newTestOAuthAccount(id, map[string]any{"codex_fingerprint_mode": mode, "codex_identity_relations_v1": true})
			a.Credentials = map[string]any{"chatgpt_account_id": "synthetic-parent"}
			b := *a
			b.Extra = map[string]any{"codex_fingerprint_mode": mode, "codex_fingerprint_seed": testCodexFingerprintSeed}
			input := func() map[string]any {
				return map[string]any{"session_id": "root", "thread_id": "root", "window_id": "019956a0-1234-7000-8000-000000000001:2", "parent_thread_id": "parent", "forked_from_thread_id": "fork", "x-codex-parent-thread-id": "parent", "parent_turn_id": "turn", "root_turn_id": "turn"}
			}
			actual, expected := input(), input()
			applyCodexAccountIdentityFields(actual, a, 1)
			applyCodexAccountIdentityFields(expected, &b, 1)
			require.Equal(t, expected, actual, "account=%d mode=%s", id, mode)
		}
	}
}

func TestAccount24IdentityRelationsFinalRequestBuilders(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		a := newTestOAuthAccount(24, map[string]any{"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true, "openai_oauth_passthrough": passthrough})
		a.Credentials = map[string]any{"chatgpt_account_id": "synthetic-parent", "user_agent": "codex-tui/0.154.0 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.154.0)"}
		root := "019956a0-1234-7000-8000-000000000001"
		embedded, err := json.Marshal(map[string]any{"session_id": root, "thread_id": root, "installation_id": "client-install", "window_id": root + ":3", "window_number": 3})
		require.NoError(t, err)
		body := map[string]any{"model": "gpt-6-astra", "input": []any{}, "stream": true, "prompt_cache_key": root, "client_metadata": map[string]any{"session_id": root, "thread_id": root, "x-codex-installation-id": "client-install", "x-codex-window-id": root + ":3", "x-codex-turn-metadata": string(embedded)}}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("session-id", root)
		c.Request.Header.Set("thread-id", root)
		c.Request.Header.Set("x-codex-window-id", root+":3")
		c.Request.Header.Set("x-codex-turn-metadata", string(embedded))
		ids := resolveCodexFingerprintIDsFromRequest(a, c.Request.Header)
		applyCodexAccountIdentityClientMetadataMap(body, a, 0)
		applyCodexFingerprintClientMetadata(body, ids)
		stageCodexFingerprintIDs(c, ids)
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		svc := &OpenAIGatewayService{}
		var request *http.Request
		if passthrough {
			request, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, a, raw, "synthetic-token")
		} else {
			cacheKey, ok := body["prompt_cache_key"].(string)
			require.True(t, ok)
			request, err = svc.buildUpstreamRequest(context.Background(), c, a, raw, "synthetic-token", true, cacheKey, true)
		}
		require.NoError(t, err)
		out, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(out, &decoded))
		cm, ok := decoded["client_metadata"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, cm["session_id"], cm["thread_id"])
		mappedThread, ok := cm["thread_id"].(string)
		require.True(t, ok)
		require.Equal(t, mappedThread+":3", cm["x-codex-window-id"])
		require.Equal(t, cm["x-codex-window-id"], request.Header.Get("x-codex-window-id"))
		require.Equal(t, cm["x-codex-installation-id"], request.Header.Get("x-codex-installation-id"))
		var headerMetadata map[string]any
		require.NoError(t, json.Unmarshal([]byte(request.Header.Get("x-codex-turn-metadata")), &headerMetadata))
		require.Equal(t, cm["thread_id"], headerMetadata["thread_id"])
		require.Equal(t, cm["session_id"], headerMetadata["session_id"])
		require.Equal(t, "codex-tui", request.Header.Get("originator"))
		require.Contains(t, request.Header.Get("User-Agent"), "(codex-tui; "+request.Header.Get("version")+")")
	}
}
