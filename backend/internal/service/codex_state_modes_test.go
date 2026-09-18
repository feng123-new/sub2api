package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestCodexStateModesDefaultOffAndGateBehavior(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		observe  bool
		inject   bool
		autoMint bool
	}{
		{name: "missing defaults off", mode: "", observe: false, inject: false, autoMint: false},
		{name: "off", mode: CodexStateModeOff, observe: false, inject: false, autoMint: false},
		{name: "observe", mode: CodexStateModeObserve, observe: true, inject: false, autoMint: false},
		{name: "manual", mode: CodexStateModeManual, observe: true, inject: true, autoMint: false},
		{name: "auto", mode: CodexStateModeAuto, observe: true, inject: true, autoMint: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{Extra: map[string]any{}}
			if tt.mode != "" {
				account.Extra[CodexStateModeExtraKey] = tt.mode
			}
			mode := codexStateMode(account)
			if tt.mode == "" {
				require.Equal(t, CodexStateModeOff, mode)
			} else {
				require.Equal(t, tt.mode, mode)
			}
			require.Equal(t, tt.observe, codexStateModeAllowsObserve(mode))
			require.Equal(t, tt.inject, codexStateModeAllowsInjection(mode))
			require.Equal(t, tt.autoMint, codexStateModeAllowsAutoMint(mode))
		})
	}
}

func TestParseCodexTurnStateMetadata(t *testing.T) {
	issuedAt := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	now := issuedAt.Add(7 * time.Minute)

	goodToken := testCodexFernetToken(t, 217, issuedAt)
	require.Len(t, goodToken, CodexStateGoodLength)
	good, err := parseCodexTurnStateMetadata(goodToken, now)
	require.NoError(t, err)
	require.Equal(t, byte(0x80), good.Version)
	require.Equal(t, issuedAt, good.IssuedAt)
	require.Equal(t, 7*time.Minute, good.Age)
	require.Equal(t, 217, good.DecodedLength)
	require.Equal(t, 10, good.CiphertextBlocks)
	require.Len(t, good.Digest, 12)

	degradedToken := testCodexFernetToken(t, 233, issuedAt)
	require.Len(t, degradedToken, CodexStateDegradedLength)
	degraded, err := parseCodexTurnStateMetadata(degradedToken, now)
	require.NoError(t, err)
	require.Equal(t, 233, degraded.DecodedLength)
	require.Equal(t, 11, degraded.CiphertextBlocks)

	invalidRaw := make([]byte, 217)
	invalidRaw[0] = 0x81
	_, err = parseCodexTurnStateMetadata(base64.URLEncoding.EncodeToString(invalidRaw), now)
	require.Error(t, err)
}

func TestCodexStateProbeReusesWireIdentityAndRequiresCompletedTerminalEvent(t *testing.T) {
	issuedAt := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	state := testCodexFernetToken(t, 217, issuedAt)
	upstream := &codexStateTerminalHTTPUpstream{state: state}
	account := &Account{
		ID:       157,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Extra: map[string]any{
			CodexStateModeExtraKey: CodexStateModeManual,
		},
		Credentials: map[string]any{
			"access_token":       "test-token",
			"chatgpt_account_id": "test-account",
		},
		Concurrency: 1,
	}
	manager := NewCodexStateManager(
		&codexStateTestAccountStore{account: account},
		codexStateTestProxyProvider{},
		upstream,
		nil,
	)

	entry, err := manager.mintOnce(context.Background(), account, "gpt-5.6-sol", nil)
	require.Error(t, err)
	require.Nil(t, entry)
	require.Equal(t, 2, upstream.requestCount())
	require.Equal(t, upstream.sessionID(0), upstream.sessionID(1))
	require.Equal(t, upstream.turnMetadata(0), upstream.turnMetadata(1))
}

func TestCodexStateAutomaticAcquisitionIsBounded(t *testing.T) {
	require.Equal(t, 3, codexStateDefaultRetryCount)
	require.Equal(t, 1, CodexStateConcurrencyMax)
}

func TestCodexStateConfiguredModelsUsesExplicitAccountList(t *testing.T) {
	account := &Account{Extra: map[string]any{
		CodexStateManagedModelsExtraKey: []any{
			" GPT-5.6-SOL ",
			"gpt-5.6-sol",
			"gpt-6-astra",
			"",
		},
		CodexStateModelsExtraKey: map[string]any{
			"legacy-model": map[string]any{},
		},
	}}

	require.Equal(t, []string{"gpt-5.6-sol", "gpt-6-astra"}, codexStateConfiguredModels(account))
}
func testCodexFernetToken(t *testing.T, decodedLength int, issuedAt time.Time) string {
	t.Helper()
	raw := make([]byte, decodedLength)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issuedAt.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

type codexStateTerminalHTTPUpstream struct {
	mu       sync.Mutex
	state    string
	headers  []http.Header
	bodies   [][]byte
	requests int
}

func (u *codexStateTerminalHTTPUpstream) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	requestNumber := u.requests
	u.requests++
	u.headers = append(u.headers, request.Header.Clone())
	body, _ := io.ReadAll(request.Body)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()

	terminal := `event: response.completed
data: {"type":"response.completed","response":{"status":"completed"}}

`
	if requestNumber == 1 {
		terminal = `event: response.failed
data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_is_overloaded"}}}

`
	}
	header := make(http.Header)
	header.Set(openAICodexTurnStateHeader, u.state)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(terminal)),
		Request:    request,
	}, nil
}

func (u *codexStateTerminalHTTPUpstream) DoWithTLS(
	request *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(request, proxyURL, accountID, accountConcurrency)
}

func (u *codexStateTerminalHTTPUpstream) requestCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests
}

func (u *codexStateTerminalHTTPUpstream) sessionID(index int) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.headers[index].Get("session-id")
}

func (u *codexStateTerminalHTTPUpstream) turnMetadata(index int) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.headers[index].Get("x-codex-turn-metadata")
}
