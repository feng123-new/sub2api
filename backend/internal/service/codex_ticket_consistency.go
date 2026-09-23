package service

import (
	"context"
	"encoding/json"
	"time"
)

func (s *OpenAIGatewayService) recordCurrentTicketValidation(ctx context.Context, accountID int64, model string, tested *CodexTicketMaterial, report *CodexTicketValidationStatus) error {
	if s.accountRepo == nil {
		return nil
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	account, err := s.accountRepo.GetByID(writeCtx, accountID)
	if err != nil {
		return err
	}
	current := parseOpenAICodexTicketFromAny(accountID, model, account.Extra[openAICodexTicketExtraKey(model)])
	if current == nil || current.State != tested.State {
		return nil
	}
	sameLease := current.CapturedAt.Sub(tested.CapturedAt).Abs() <= time.Microsecond
	if pin := codexTicketPin(account, model); pin != nil && report.AttemptID > 0 && pin.AttemptID == report.AttemptID {
		sameLease = true
	}
	if !sameLease || current.ValidationCheckedAt.After(report.CheckedAt) {
		return nil
	}
	if report.AttemptID == 0 {
		if archive, ok := s.openaiCodexTicketHistory.(CodexTicketMaterialRepository); ok {
			id := int64(0)
			if pin := codexTicketPin(account, model); pin != nil && pin.ExpiresAt.Equal(current.ExpiresAt) {
				id = pin.AttemptID
			}
			material, readErr := archive.GetMaterial(writeCtx, accountID, model, id)
			if readErr == nil && material != nil && material.State == tested.State {
				if history, ok := s.openaiCodexTicketHistory.(CodexTicketValidationRepository); ok {
					copyReport := *report
					copyReport.AttemptID = material.AttemptID
					if err := history.SaveValidation(writeCtx, accountID, model, &copyReport); err != nil {
						return err
					}
				}
			}
		}
	}
	current.ValidationOutcome = report.Outcome
	current.ValidationCheckedAt = report.CheckedAt
	current.ValidationHTTPStatus = report.HTTPStatus
	if report.Outcome == "response_model_mismatch" {
		current.Rejected = true
	}
	if report.Outcome == "remote_completed" {
		current.Rejected = false
	}
	extra := map[string]any{
		openAICodexTicketExtraKey(model): current,
		"codex_ticket_check:" + model:    map[string]any{"checked_at": report.CheckedAt, "outcome": report.Outcome, "http_status": report.HTTPStatus, "ticket_captured_at": current.CapturedAt},
	}
	if err := s.accountRepo.UpdateExtra(writeCtx, accountID, extra); err != nil {
		return err
	}
	s.openaiCodexTickets.Store(openAICodexTicketKey(accountID, model), current)
	if current.Rejected {
		s.openaiCodexTicketNextAttempt.Store(openAICodexTicketKey(accountID, model), time.Now().Add(10*time.Second))
	} else {
		s.openaiCodexTicketNextAttempt.Store(openAICodexTicketKey(accountID, model), codexTicketNextCheck(current.CapturedAt, current.ExpiresAt, report.CheckedAt))
	}

	return nil
}

func codexTicketCurrentCheck(account *Account, model string) map[string]any {
	check := map[string]any{}
	if account == nil {
		return check
	}
	ticket := parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	if ticket == nil {
		return check
	}
	if !ticket.ValidationCheckedAt.IsZero() {
		return map[string]any{"checked_at": ticket.ValidationCheckedAt, "outcome": ticket.ValidationOutcome, "http_status": ticket.ValidationHTTPStatus}
	}
	raw, err := json.Marshal(account.Extra["codex_ticket_check:"+model])
	if err != nil {
		return check
	}
	var saved struct {
		CheckedAt  time.Time `json:"checked_at"`
		CapturedAt time.Time `json:"ticket_captured_at"`
		Outcome    string    `json:"outcome"`
		HTTPStatus int       `json:"http_status"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.CheckedAt.IsZero() || saved.CapturedAt.Sub(ticket.CapturedAt).Abs() > time.Microsecond {
		return check
	}
	return map[string]any{"checked_at": saved.CheckedAt, "outcome": saved.Outcome, "http_status": saved.HTTPStatus}
}
