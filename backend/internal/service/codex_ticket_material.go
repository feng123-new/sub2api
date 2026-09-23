package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrCodexTicketMaterialMissing = errors.New("ticket material was not saved for this record")
var ErrCodexTicketMaterialExpired = errors.New("selected ticket has expired")

type CodexTicketMaterial struct {
	ValidationOutcome    string    `json:"validation_outcome,omitempty"`
	ValidationCheckedAt  time.Time `json:"validation_checked_at,omitempty"`
	ValidationHTTPStatus int       `json:"validation_http_status,omitempty"`
	AttemptID            int64     `json:"attempt_id"`
	AccountID            int64     `json:"account_id"`
	Model                string    `json:"model"`
	State                string    `json:"ticket"`
	CapturedAt           time.Time `json:"captured_at"`
	ExpiresAt            time.Time `json:"expires_at"`
}

type CodexTicketMaterialRepository interface {
	SaveMaterial(context.Context, *CodexTicketMaterial) error
	GetMaterial(context.Context, int64, string, int64) (*CodexTicketMaterial, error)
}

func (s *OpenAIGatewayService) CodexTicketMaterial(ctx context.Context, accountID int64, model string, attemptID int64) (*CodexTicketMaterial, error) {
	if !s.codexTicketSupportedModel(model) || attemptID < 0 {
		return nil, ErrCodexTicketModel
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, ErrCodexTicketUnavailable
	}
	if attemptID == 0 {
		ticket := s.lookupOpenAICodexTicket(account, model)
		if ticket == nil {
			return nil, ErrCodexTicketMaterialMissing
		}
		return &CodexTicketMaterial{AccountID: accountID, Model: model, State: ticket.State, CapturedAt: ticket.CapturedAt, ExpiresAt: ticket.ExpiresAt}, nil
	}
	repo, ok := s.openaiCodexTicketHistory.(CodexTicketMaterialRepository)
	if !ok {
		return nil, ErrCodexTicketMaterialMissing
	}
	result, err := repo.GetMaterial(ctx, accountID, model, attemptID)
	if err != nil {
		return nil, err
	}
	if result.State != "" {
		return result, nil
	}
	// Older history contains metadata only; never substitute a different ticket.
	current := parseOpenAICodexTicketFromAny(accountID, model, account.Extra[openAICodexTicketExtraKey(model)])
	if current == nil || current.ExpiresAt.Sub(result.ExpiresAt).Abs() > time.Microsecond {
		return nil, ErrCodexTicketMaterialMissing
	}
	result.State = current.State
	result.CapturedAt = current.CapturedAt
	return result, nil
}

func (s *OpenAIGatewayService) ValidateCodexTicketMaterial(ctx context.Context, accountID int64, model string, attemptID int64) (map[string]any, error) {
	return s.validateCodexTicketMaterial(ctx, accountID, model, attemptID, false)
}

func (s *OpenAIGatewayService) validateCodexTicketMaterial(ctx context.Context, accountID int64, model string, attemptID int64, pin bool) (map[string]any, error) {
	key := openAICodexTicketKey(accountID, model)
	if _, busy := s.openaiCodexTicketInFlight.LoadOrStore(key, true); busy {
		return nil, ErrCodexTicketBusy
	}
	defer s.openaiCodexTicketInFlight.Delete(key)
	if s.openaiCodexTicketHistory != nil {
		unlock, acquired, err := s.openaiCodexTicketHistory.TryLock(ctx, accountID, model)
		if err != nil {
			return nil, err
		}
		if !acquired {
			return nil, ErrCodexTicketBusy
		}
		defer unlock()
	}
	material, err := s.CodexTicketMaterial(ctx, accountID, model, attemptID)
	if err != nil {
		if errors.Is(err, ErrCodexTicketMaterialMissing) && attemptID > 0 {
			if repo, ok := s.openaiCodexTicketHistory.(CodexTicketValidationRepository); ok {
				writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				saveErr := repo.SaveValidation(writeCtx, accountID, model, &CodexTicketValidationStatus{AttemptID: attemptID, Outcome: "material_missing", CheckedAt: time.Now()})
				cancel()
				if saveErr != nil {
					return nil, saveErr
				}
			}
		}
		return nil, err
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account.Status != StatusActive {
		return nil, ErrCodexTicketUnavailable
	}
	ticket := &openAICodexTicket{AccountID: accountID, Model: model, State: material.State, Length: len(material.State), CapturedAt: material.CapturedAt, ExpiresAt: material.ExpiresAt}
	if len(ticket.State) != openAICodexTicketTargetLength(account, s.openAICodexTicketConfig().TargetLength) || !strings.HasPrefix(ticket.State, openAICodexTicketStatePrefix) {
		return nil, ErrCodexTicketMaterialMissing
	}
	outcome, status := s.validateCodexTicketBusiness(ctx, account, model, ticket)
	report := &CodexTicketValidationStatus{AttemptID: attemptID, Outcome: outcome, HTTPStatus: status, CheckedAt: time.Now()}
	if report.AttemptID == 0 {
		if p := codexTicketPin(account, model); p != nil && codexTicketPinActive(account, model, time.Now()) {
			report.AttemptID = p.AttemptID
		}
	}
	if repo, ok := s.openaiCodexTicketHistory.(CodexTicketValidationRepository); ok {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := repo.SaveValidation(writeCtx, accountID, model, report)
		cancel()
		if err != nil {
			return nil, err
		}
	}

	if err := s.recordCurrentTicketValidation(ctx, accountID, model, material, report); err != nil {
		return nil, err
	}

	if pin && outcome == "remote_completed" {
		if err := s.persistCodexTicketPin(ctx, account, material); err != nil {
			return nil, err
		}
	}
	return map[string]any{"locked": pin && outcome == "remote_completed", "attempt_id": attemptID, "model": model, "outcome": outcome, "http_status": status, "completed": outcome == "remote_completed", "checked_at": time.Now()}, nil
}
