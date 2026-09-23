package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type renewalCandidateUpstream struct {
	calls              int
	resultModel, state string
}

func (u *renewalCandidateUpstream) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	header := http.Header{}
	if HTTPUpstreamProfileFromContext(r.Context()) == HTTPUpstreamProfileOpenAIHarvest {
		header.Set(openAICodexTurnStateHeader, u.state)
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"" + u.resultModel + "\"}}\n\n"))}, nil
}
func (u *renewalCandidateUpstream) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}

func TestTicketRenewalAfterManualSelectionRequiresValidatedReplacement(t *testing.T) {
	for _, match := range []bool{false, true} {
		t.Run(map[bool]string{false: "reject_wrong_model_keep_old", true: "accept_replace_old"}[match], func(t *testing.T) {
			now := time.Now()
			model := "gpt-6-astra"
			acc := ticketTestAccount(41)
			acc.Status = StatusActive
			old := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now.Add(-51 * time.Minute), ExpiresAt: now.Add(9 * time.Minute), ValidationOutcome: "remote_completed", ValidationCheckedAt: now.Add(-11 * time.Minute)}
			acc.Extra[openAICodexTicketExtraKey(model)] = old
			acc.Extra[codexTicketPinPrefix+model] = &CodexTicketPin{AttemptID: 72, ConfirmedAt: old.CapturedAt, ExpiresAt: old.ExpiresAt}
			up := &renewalCandidateUpstream{state: "gAAAAA" + strings.Repeat("b", 286), resultModel: "gpt-5.6-luna"}
			if match {
				up.resultModel = model
			}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://harvest.example:8080", Models: []string{model}}, up)
			svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
			svc.checkOrRenewCodexTicket(context.Background(), acc, model)
			require.Equal(t, 2, up.calls, "minute 50 must collect then validate, even after a manual switch")
			current := svc.lookupOpenAICodexTicket(acc, model)
			if match {
				require.Equal(t, up.state, current.State)
				require.Equal(t, "remote_completed", current.ValidationOutcome)
				require.Nil(t, codexTicketPin(acc, model))
			} else {
				require.Equal(t, old.State, current.State)
				require.True(t, current.valid(time.Now(), 292))
				raw, _ := svc.openaiCodexTicketNextAttempt.Load(openAICodexTicketKey(41, model))
				require.LessOrEqual(t, time.Until(raw.(time.Time)), 40*time.Second)
			}
		})
	}
}

func TestTicketManualRecentCheckAvoidsDuplicateProbe(t *testing.T) {
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	acc.Status = StatusActive
	ticket := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now.Add(-30 * time.Minute), ExpiresAt: now.Add(30 * time.Minute), ValidationOutcome: "remote_completed", ValidationCheckedAt: now.Add(-time.Minute)}
	acc.Extra[openAICodexTicketExtraKey(model)] = ticket
	up := &maintenanceUpstream{status: 200}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, up)
	svc.checkOrRenewCodexTicket(context.Background(), acc, model)
	require.Zero(t, up.calls)
}

func TestTicketUncertainResultCannotEraseDefinitiveRejection(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	model := "gpt-6-astra"
	acc := ticketTestAccount(41)
	ticket := &openAICodexTicket{AccountID: 41, Model: model, State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(time.Hour)}
	acc.Extra[openAICodexTicketExtraKey(model)] = ticket
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = &bindingAccounts{materialAccountRepo{account: acc}}
	material := &CodexTicketMaterial{State: ticket.State, CapturedAt: ticket.CapturedAt, ExpiresAt: ticket.ExpiresAt}
	require.NoError(t, svc.recordCurrentTicketValidation(ctx, 41, model, material, &CodexTicketValidationStatus{Outcome: "validation_timeout", CheckedAt: now.Add(time.Second)}))
	require.True(t, svc.lookupOpenAICodexTicket(acc, model).valid(time.Now(), 292), "timeout alone must not reject")
	require.NoError(t, svc.recordCurrentTicketValidation(ctx, 41, model, material, &CodexTicketValidationStatus{Outcome: "response_model_mismatch", CheckedAt: now.Add(2 * time.Second)}))
	require.NoError(t, svc.recordCurrentTicketValidation(ctx, 41, model, material, &CodexTicketValidationStatus{Outcome: "validation_timeout", CheckedAt: now.Add(3 * time.Second)}))
	require.False(t, svc.lookupOpenAICodexTicket(acc, model).valid(time.Now(), 292), "later timeout cannot erase known mismatch")
	require.NoError(t, svc.recordCurrentTicketValidation(ctx, 41, model, material, &CodexTicketValidationStatus{Outcome: "remote_completed", CheckedAt: now.Add(4 * time.Second)}))
	require.True(t, svc.lookupOpenAICodexTicket(acc, model).valid(time.Now(), 292))
}
