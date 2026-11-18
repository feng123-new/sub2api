package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type accountUsageProbeUpstream struct {
	do       func(*http.Request, string, int64, int) (*http.Response, error)
	calls    int
	tlsCalls int
}

func (u *accountUsageProbeUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.calls++
	return u.do(req, proxyURL, accountID, concurrency)
}

func (u *accountUsageProbeUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.tlsCalls++
	return nil, errors.New("quota probe must use shared Do routing")
}

type accountUsageProbeBody struct {
	io.Reader
	closed bool
}

func (b *accountUsageProbeBody) Close() error {
	b.closed = true
	return nil
}

// Reject every CONNECT locally: even the pre-fix direct-client path cannot
// contact OpenAI, and a fallback or replay becomes an observable proxy attempt.
func accountUsageProbeRejectingProxy(t *testing.T) (*Proxy, *atomic.Int64) {
	t.Helper()
	attempts := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	addr := server.Listener.Addr().(*net.TCPAddr)
	return &Proxy{ID: 81, Protocol: "http", Host: addr.IP.String(), Port: addr.Port}, attempts
}

func TestAccountUsageService_CodexProbeSharedUpstream(t *testing.T) {
	for _, tc := range []struct {
		name         string
		accountType  string
		status       int
		rustEligible bool
	}{
		{"oauth", AccountTypeOAuth, http.StatusOK, true},
		{"oauth_quota_exhausted", AccountTypeOAuth, http.StatusTooManyRequests, true},
		{"setup_token_not_rust_eligible", AccountTypeSetupToken, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy, directAttempts := accountUsageProbeRejectingProxy(t)
			account := &Account{
				ID: 731, Platform: PlatformOpenAI, Type: tc.accountType, Concurrency: 7,
				ProxyID: &proxy.ID, Proxy: proxy,
				Credentials: map[string]any{"access_token": "synthetic-access-token", "chatgpt_account_id": "synthetic-chatgpt-account"},
			}
			body := &accountUsageProbeBody{Reader: strings.NewReader("data: [DONE]\n\n")}
			var requestCtx context.Context
			upstream := &accountUsageProbeUpstream{do: func(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
				requestCtx = req.Context()
				if got := HTTPUpstreamProfileFromContext(req.Context()); got != HTTPUpstreamProfileOpenAI {
					t.Errorf("profile = %q, want OpenAI", got)
				}
				if got := IsOpenAICodexOAuthRequest(req.Context()); got != tc.rustEligible {
					t.Errorf("Rust OAuth eligibility = %v, want %v", got, tc.rustEligible)
				}
				if proxyURL != proxy.URL() || accountID != account.ID || concurrency != account.Concurrency {
					t.Errorf("routing arguments = (%q, %d, %d), want (%q, %d, %d)", proxyURL, accountID, concurrency, proxy.URL(), account.ID, account.Concurrency)
				}
				deadline, ok := req.Context().Deadline()
				if remaining := time.Until(deadline); !ok || remaining <= 14*time.Second || remaining > 15*time.Second {
					t.Errorf("probe deadline = %v, want 15s context timeout", deadline)
				}
				if req.Method != http.MethodPost || req.URL.String() != chatgptCodexURL || req.Host != "chatgpt.com" {
					t.Errorf("unexpected probe destination: %s %s host=%s", req.Method, req.URL, req.Host)
				}
				for name, want := range map[string]string{
					"Authorization": "Bearer synthetic-access-token",
					"Content-Type":  "application/json",
					"Accept":        "text/event-stream",
					"OpenAI-Beta":   "responses=experimental",
					"X-Timezone":    OpenAIOutboundTimezone,
				} {
					if got := req.Header.Get(name); got != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
				if tc.accountType == AccountTypeOAuth && req.Header.Get("Chatgpt-Account-Id") != "synthetic-chatgpt-account" {
					t.Errorf("ChatGPT account header = %q", req.Header.Get("Chatgpt-Account-Id"))
				}
				for _, name := range []string{"User-Agent", "Originator", "Version"} {
					if req.Header.Get(name) == "" {
						t.Errorf("missing identity header %s", name)
					}
				}
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["model"] != openaipkg.CodexUsageProbeModel || payload["stream"] != true {
					t.Errorf("unexpected quota probe payload: %#v", payload)
				}
				headers := make(http.Header)
				headers.Set("X-Codex-Primary-Used-Percent", "25")
				headers.Set("X-Codex-Primary-Window-Minutes", "300")
				headers.Set("X-Codex-Secondary-Used-Percent", "80")
				headers.Set("X-Codex-Secondary-Window-Minutes", "10080")
				return &http.Response{StatusCode: tc.status, Header: headers, Body: body}, nil
			}}
			svc := NewAccountUsageService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, upstream)
			updates, err := svc.probeOpenAICodexSnapshot(context.Background(), account)
			if upstream.calls != 1 || upstream.tlsCalls != 0 {
				t.Fatalf("shared upstream calls = %d, TLS calls = %d, want 1 and 0; probe error: %v", upstream.calls, upstream.tlsCalls, err)
			}
			if err != nil {
				t.Fatal(err)
			}
			if updates["codex_5h_used_percent"] != 25.0 || updates["codex_7d_used_percent"] != 80.0 {
				t.Errorf("quota headers were not parsed: %#v", updates)
			}
			if !body.closed || requestCtx.Err() != context.Canceled {
				t.Errorf("probe did not close body and cancel context: closed=%v, err=%v", body.closed, requestCtx.Err())
			}
			if got := directAttempts.Load(); got != 0 {
				t.Errorf("direct-client attempts = %d, want 0", got)
			}
		})
	}
}

func TestAccountUsageService_CodexProbeUpstreamFailureNoReplay(t *testing.T) {
	for _, upstreamErr := range []error{
		errors.New("synthetic Go transport failure"),
		&PluginTransportError{Code: "CODEX_RUST_UPSTREAM", Message: "synthetic Rust transport failure", RequestSent: true},
	} {
		t.Run(upstreamErr.Error(), func(t *testing.T) {
			proxy, directAttempts := accountUsageProbeRejectingProxy(t)
			account := &Account{ID: 732, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &proxy.ID, Proxy: proxy,
				Credentials: map[string]any{"access_token": "synthetic-access-token"}}
			upstream := &accountUsageProbeUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
				return nil, upstreamErr
			}}
			svc := NewAccountUsageService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, upstream)
			updates, err := svc.probeOpenAICodexSnapshot(context.Background(), account)
			if !errors.Is(err, upstreamErr) || updates != nil {
				t.Errorf("probe result = (%v, %v), want wrapped upstream error %v", updates, err, upstreamErr)
			}
			if upstream.calls != 1 || upstream.tlsCalls != 0 || directAttempts.Load() != 0 {
				t.Errorf("replay/bypass: shared=%d TLS=%d direct=%d, want 1/0/0", upstream.calls, upstream.tlsCalls, directAttempts.Load())
			}
		})
	}
}

func TestAccountUsageService_CodexProbePreservesCancellationWithoutProxy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // The pre-fix client also cannot send a real request.
	account := &Account{ID: 733, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 3,
		Credentials: map[string]any{"access_token": "synthetic-access-token"}}
	upstream := &accountUsageProbeUpstream{do: func(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
		if proxyURL != "" || accountID != account.ID || concurrency != 3 {
			t.Errorf("direct routing arguments = (%q, %d, %d)", proxyURL, accountID, concurrency)
		}
		if req.Context().Err() != context.Canceled {
			t.Error("parent cancellation was lost")
		}
		return nil, req.Context().Err()
	}}
	svc := NewAccountUsageService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, upstream)
	_, err := svc.probeOpenAICodexSnapshot(ctx, account)
	if !errors.Is(err, context.Canceled) || upstream.calls != 1 || upstream.tlsCalls != 0 {
		t.Fatalf("canceled probe: err=%v shared=%d TLS=%d, want context.Canceled and 1/0", err, upstream.calls, upstream.tlsCalls)
	}
}

func TestAccountUsageService_CodexProbeSkipsNonOAuth(t *testing.T) {
	for _, account := range []*Account{nil, {ID: 734, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}} {
		upstream := &accountUsageProbeUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
			t.Fatal("non-OAuth account must not send a quota probe")
			return nil, nil
		}}
		svc := NewAccountUsageService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, upstream)
		updates, err := svc.probeOpenAICodexSnapshot(context.Background(), account)
		if updates != nil || err != nil || upstream.calls != 0 || upstream.tlsCalls != 0 {
			t.Fatalf("non-OAuth probe result = (%v, %v), calls=%d/%d", updates, err, upstream.calls, upstream.tlsCalls)
		}
	}
}
