package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type maintenanceUpstream struct {
	req    *http.Request
	proxy  string
	status int
	body   string
	calls  int
}

func (m *maintenanceUpstream) Do(r *http.Request, p string, _ int64, _ int) (*http.Response, error) {
	m.req = r
	m.proxy = p
	m.calls++
	return &http.Response{StatusCode: m.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(m.body))}, nil
}
func (m *maintenanceUpstream) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return m.Do(r, p, a, c)
}

type maintenanceUsage struct {
	UsageLogRepository
	rows []UsageLog
}

func (m *maintenanceUsage) ListByAccount(context.Context, int64, pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	return m.rows, nil, nil
}

func TestCodexTicketMaintenanceTiming(t *testing.T) {
	now := time.Now()
	expires := now.Add(time.Hour)
	require.Equal(t, now.Add(10*time.Minute), codexTicketNextCheck(now, expires, now))
	require.Equal(t, now.Add(50*time.Minute), codexTicketNextCheck(now, expires, now.Add(45*time.Minute)))
	for _, minute := range []int{10, 20, 30, 40, 49} {
		require.False(t, (&openAICodexTicket{ExpiresAt: expires}).needsRefresh(now.Add(time.Duration(minute)*time.Minute), 10*time.Minute))
	}
	require.True(t, (&openAICodexTicket{ExpiresAt: expires}).needsRefresh(now.Add(50*time.Minute), 10*time.Minute))

}

func TestCodexTicketMaintenanceLocalEvidenceSkipsNetwork(t *testing.T) {
	now := time.Now()
	model := "gpt-6-astra"
	account := ticketTestAccount(41)
	upstream := &maintenanceUpstream{status: 200}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	svc.usageLogRepo = &maintenanceUsage{rows: []UsageLog{{AccountID: 41, CreatedAt: now.Add(-time.Minute), UpstreamModel: &model, UpstreamResponseModel: &model, OutputTokens: 1}}}
	ticket := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now.Add(-15 * time.Minute), ExpiresAt: now.Add(45 * time.Minute)}
	svc.storeOpenAICodexTicket(context.Background(), account, ticket)
	svc.checkOrRenewCodexTicket(context.Background(), account, model)
	require.Zero(t, upstream.calls)
	require.Equal(t, ticket.State, svc.lookupOpenAICodexTicket(account, model).State)
}

func TestCodexTicketMaintenanceValidationUsesBusinessProxy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body, outcome string
	}{
		{"completed", 200, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n", "remote_completed"},
		{"headers_only", 200, "", "incomplete_response"},
		{"different_model", 200, "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"other\"}}\n\n", "response_model_mismatch"},
		{"rate_limit", 429, "", "rate_limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &maintenanceUpstream{status: tc.status, body: tc.body}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, up)
			account := ticketTestAccount(41)
			id := int64(7)
			account.ProxyID = &id
			account.Proxy = &Proxy{ID: id, Protocol: "http", Host: "business.example", Port: 8080}
			ticket := &openAICodexTicket{State: fakeCodexTicketState(292)}
			outcome, status := svc.validateCodexTicketBusiness(context.Background(), account, "gpt-6-astra", ticket)
			require.Equal(t, tc.outcome, outcome)
			require.Equal(t, tc.status, status)
			require.Equal(t, "http://business.example:8080", up.proxy)
			require.Equal(t, ticket.State, up.req.Header.Get(openAICodexTurnStateHeader))
			require.Equal(t, "America/Los_Angeles", up.req.Header.Get("X-Timezone"))
		})
	}
}
