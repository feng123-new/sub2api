package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexIdentityCutoffFinalHTTPAndWSHeaders(t *testing.T) {
	cutoff := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	oldRoot := cutoffTestSession(cutoff.Add(-time.Millisecond))
	newRoot := cutoffTestSession(cutoff.Add(time.Millisecond))
	child := cutoffTestSession(cutoff.Add(2 * time.Millisecond))
	for _, passthrough := range []bool{false, true} {
		for _, tc := range []struct {
			name, root, thread, parent string
			enabled                    bool
		}{
			{name: "old_root", root: oldRoot, thread: oldRoot, parent: oldRoot},
			{name: "new_root", root: newRoot, thread: newRoot, parent: newRoot, enabled: true},
			{name: "new_child_old_root", root: oldRoot, thread: child, parent: oldRoot},
			{name: "new_child_old_parent", root: child, thread: child, parent: oldRoot},
			{name: "new_child_new_root", root: newRoot, thread: child, parent: newRoot, enabled: true},
		} {
			t.Run(tc.name+map[bool]string{false: "_transformed", true: "_passthrough"}[passthrough], func(t *testing.T) {
				account := cutoffTestAccount(cutoff.Format(time.RFC3339Nano))
				account.ID = 171
				account.Status, account.Schedulable, account.Concurrency = StatusActive, true, 1
				account.Credentials["access_token"] = "synthetic-token"
				account.Extra["openai_oauth_passthrough"] = passthrough
				metadata := map[string]any{"session_id": tc.root, "thread_id": tc.thread, "parent_thread_id": tc.parent, "window_id": tc.thread + ":2"}
				embedded, err := json.Marshal(metadata)
				require.NoError(t, err)
				payload := map[string]any{
					"model": "gpt-5.6-sol", "stream": false, "input": []any{map[string]any{"role": "user", "content": "synthetic"}},
					"prompt_cache_key": tc.root,
					"client_metadata":  map[string]any{"session_id": tc.root, "thread_id": tc.thread, "x-codex-parent-thread-id": tc.parent, "x-codex-window-id": tc.thread + ":2", "x-codex-turn-metadata": string(embedded)},
				}
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				c.Set("api_key", &APIKey{ID: 301})
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.169.0")
				c.Request.Header.Set("session-id", tc.root)
				c.Request.Header.Set("thread-id", tc.thread)
				c.Request.Header.Set("x-codex-parent-thread-id", tc.parent)
				c.Request.Header.Set("x-codex-window-id", tc.thread+":2")
				c.Request.Header.Set("x-codex-turn-metadata", string(embedded))
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, toolCorrector: NewCodexToolCorrector()}
				_, err = svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)
				source := codexAccountIdentitySource(c, account)
				require.Equal(t, tc.enabled, codexIdentityRelationsEnabled(source))
				session := scopeCodexAccountIdentityValue(source, 301, "session", tc.root)
				thread := scopeCodexAccountIdentityValue(source, 301, "thread", tc.thread)
				window := scopeCodexAccountIdentityValue(source, 301, "window", tc.thread+":2")
				parent := tc.parent
				if tc.enabled {
					parent = scopeCodexAccountIdentityValue(source, 301, "thread", tc.parent)
				}
				for path, want := range map[string]string{"prompt_cache_key": session, "client_metadata.session_id": session, "client_metadata.thread_id": thread, "client_metadata.x-codex-window-id": window, "client_metadata.x-codex-parent-thread-id": parent} {
					require.Equal(t, want, gjson.GetBytes(upstream.lastBody, path).String(), path)
				}
				if tc.enabled {
					require.Equal(t, session, upstream.lastReq.Header.Get("session-id"))
					require.Equal(t, thread, upstream.lastReq.Header.Get("thread-id"))
					require.Equal(t, parent, upstream.lastReq.Header.Get("x-codex-parent-thread-id"))
				} else {
					require.Empty(t, upstream.lastReq.Header.Get("session-id"))
					require.Empty(t, upstream.lastReq.Header.Get("thread-id"))
				}
				require.Equal(t, window, upstream.lastReq.Header.Get("x-codex-window-id"))
				if tc.enabled && tc.root == tc.thread {
					require.Equal(t, session, thread)
				}
				if tc.enabled {
					require.Equal(t, thread+":2", window)
				}
				seed, _ := codexFingerprintSeed(account.Extra)
				installation := resolveConvergedInstallationID(account, seed)
				require.Equal(t, installation, upstream.lastReq.Header.Get("x-codex-installation-id"))
				require.Equal(t, installation, gjson.GetBytes(upstream.lastBody, "client_metadata.x-codex-installation-id").String())
				wsBody, err := json.Marshal(map[string]any{"type": "response.create", "response": payload})
				require.NoError(t, err)
				_, err = svc.prepareCodexAccountIdentitySource(context.Background(), c, account, wsBody)
				require.NoError(t, err)
				stageCodexFingerprintIDs(c, resolveCodexFingerprintIDsFromRequest(account, c.Request.Header))
				ws, _, err := svc.buildOpenAIWSHeaders(context.Background(), c, account, "synthetic-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, "", string(embedded), tc.root, "", "")
				require.NoError(t, err)
				require.Equal(t, session, ws.Get("session-id"))
				require.Equal(t, thread, ws.Get("thread-id"))
				if tc.enabled {
					require.Equal(t, parent, ws.Get("x-codex-parent-thread-id"))
				}
				for _, header := range []string{"x-codex-window-id", "x-codex-installation-id"} {
					require.Equal(t, upstream.lastReq.Header.Get(header), ws.Get(header), header)
				}
				require.True(t, account.Extra["codex_identity_relations_v1"].(bool))
				require.Equal(t, "11111111-1111-4111-8111-111111111111", account.Extra["codex_fingerprint_seed"])
			})
		}
	}
}
