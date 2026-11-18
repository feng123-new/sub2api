package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func cutoffTestSession(at time.Time) string {
	var id uuid.UUID
	stamp := uint64(at.UnixMilli())
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], stamp)
	copy(id[:6], encoded[2:])
	id[6], id[8] = 0x70, 0x80
	return id.String()
}

func cutoffTestAccount(cutoff any) *Account {
	extra := map[string]any{
		"codex_fingerprint_mode": "device", "codex_identity_relations_v1": true,
		"codex_fingerprint_seed": "11111111-1111-4111-8111-111111111111",
	}
	if cutoff != nil {
		extra["codex_identity_relations_v1_session_cutoff"] = cutoff
	}
	return &Account{ID: 24, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "synthetic-cutoff-account"}, Extra: extra}
}

func stageCutoffTest(t *testing.T, account *Account, headers http.Header, body []byte) (*gin.Context, *Account) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header = headers.Clone()
	service := &OpenAIGatewayService{}
	source, err := service.prepareCodexAccountIdentitySource(context.Background(), c, account, body)
	require.NoError(t, err)
	return c, source
}

func TestCodexIdentitySessionCutoffKeepsLegacySessions(t *testing.T) {
	cutoff := time.Date(2026, 11, 18, 0, 0, 0, 123000000, time.UTC)
	oldRoot := cutoffTestSession(cutoff.Add(-time.Millisecond))
	newRoot := cutoffTestSession(cutoff.Add(time.Millisecond))
	atCutoff := cutoffTestSession(cutoff)
	for _, tc := range []struct {
		name, session, parent, metadata string
		cutoff                          any
		want                            bool
	}{
		{name: "older_root", session: oldRoot},
		{name: "at_cutoff", session: atCutoff, want: true},
		{name: "newer_root", session: newRoot, want: true},
		{name: "missing_root"},
		{name: "opaque_root", session: "opaque-session"},
		{name: "uuid_v4_root", session: "11111111-1111-4111-8111-111111111111"},
		{name: "old_parent_new_child", session: newRoot, parent: oldRoot},
		{name: "unknown_parent", session: newRoot, parent: "opaque-parent"},
		{name: "new_parent_new_child", session: newRoot, parent: atCutoff, want: true},
		{name: "old_metadata_root", session: newRoot, metadata: `{"session_id":"` + oldRoot + `"}`},
		{name: "new_metadata_root", session: newRoot, metadata: `{"session_id":"` + newRoot + `"}`, want: true},
		{name: "malformed_metadata", session: newRoot, metadata: "not-json"},
		{name: "new_turn_does_not_upgrade_old_session", session: oldRoot, metadata: `{"turn_id":"` + newRoot + `","turn_started_at_unix_ms":9999999999999}`},
		{name: "invalid_cutoff", session: newRoot, cutoff: "tomorrow"},
		{name: "wrong_cutoff_type", session: newRoot, cutoff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configuredCutoff := tc.cutoff
			if configuredCutoff == nil {
				configuredCutoff = cutoff.Format(time.RFC3339Nano)
			}
			account := cutoffTestAccount(configuredCutoff)
			headers := make(http.Header)
			if tc.session != "" {
				headers.Set("session-id", tc.session)
			}
			if tc.parent != "" {
				headers.Set("x-codex-parent-thread-id", tc.parent)
			}
			if tc.metadata != "" {
				headers.Set("x-codex-turn-metadata", tc.metadata)
			}
			c, source := stageCutoffTest(t, account, headers, []byte(`{"input":[]}`))
			require.Equal(t, tc.want, codexIdentityRelationsEnabled(source))
			require.Equal(t, tc.want, codexIdentityRelationsEnabled(codexAccountIdentitySource(c, account)))
			require.True(t, account.Extra["codex_identity_relations_v1"].(bool), "cached account must not be mutated")
			require.Equal(t, account.Extra["codex_fingerprint_seed"], source.Extra["codex_fingerprint_seed"])
			require.Equal(t, codexAccountIdentityNamespace(account), codexAccountIdentityNamespace(source))
			require.Equal(t, account.ID, source.ID)
			if !tc.want {
				legacy := *account
				legacy.Extra = map[string]any{"codex_fingerprint_mode": "device"}
				for _, kind := range []string{"session", "thread", "window", "turn"} {
					require.Equal(t, scopeCodexAccountIdentityValue(&legacy, 1, kind, "legacy-value"), scopeCodexAccountIdentityValue(source, 1, kind, "legacy-value"))
				}
			}
		})
	}
	account := cutoffTestAccount(nil)
	_, source := stageCutoffTest(t, account, make(http.Header), nil)
	require.Same(t, account, source)
	require.True(t, codexIdentityRelationsEnabled(source), "explicit existing opt-in without cutoff remains compatible")
}

func TestCodexIdentitySessionCutoffBodyAndWebSocketLineage(t *testing.T) {
	cutoff := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	oldRoot, newRoot := cutoffTestSession(cutoff.Add(-time.Millisecond)), cutoffTestSession(cutoff.Add(time.Millisecond))
	for _, envelope := range []bool{false, true} {
		for _, parent := range []string{oldRoot, newRoot, "opaque-parent"} {
			metadata := map[string]any{"session_id": newRoot, "thread_id": newRoot, "parent_thread_id": parent}
			embedded, err := json.Marshal(metadata)
			require.NoError(t, err)
			payload := map[string]any{"client_metadata": map[string]any{"session_id": newRoot, "x-codex-turn-metadata": string(embedded)}}
			if envelope {
				payload = map[string]any{"type": "response.create", "response": payload}
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			_, source := stageCutoffTest(t, cutoffTestAccount(cutoff.Format(time.RFC3339Nano)), make(http.Header), body)
			require.Equal(t, parent == newRoot, codexIdentityRelationsEnabled(source), "envelope=%v parent=%s", envelope, parent)
		}
	}
}
