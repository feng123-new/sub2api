package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const codexTicketPinPrefix = "codex_ticket_pin:"

var ErrCodexTicketPinned = errors.New("ticket is locked; unlock it before harvesting another ticket")

type CodexTicketPin struct {
	AttemptID   int64     `json:"attempt_id"`
	ConfirmedAt time.Time `json:"confirmed_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func codexTicketPin(account *Account, model string) *CodexTicketPin {
	if account == nil {
		return nil
	}
	raw, err := json.Marshal(account.Extra[codexTicketPinPrefix+model])
	if err != nil {
		return nil
	}
	var pin CodexTicketPin
	if json.Unmarshal(raw, &pin) != nil || pin.AttemptID <= 0 {
		return nil
	}
	return &pin
}
func codexTicketPinActive(account *Account, model string, now time.Time) bool {
	pin := codexTicketPin(account, model)
	if pin == nil || !now.Before(pin.ExpiresAt) {
		return false
	}
	ticket := parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	return ticket != nil && !ticket.Rejected
}
func codexPinnedNextCheck(expires, now time.Time) time.Time {
	next := now.Add(codexTicketCheckInterval)
	if expires.Before(next) {
		next = expires
	}
	if next.Before(now) {
		return now
	}
	return next
}

func (s *OpenAIGatewayService) CodexTicketBinding(ctx context.Context, id int64, model string) (map[string]any, error) {
	if !s.codexTicketSupportedModel(model) {
		return nil, ErrCodexTicketModel
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, ErrCodexTicketUnavailable
	}
	check := codexTicketCurrentCheck(account, model)
	modelEnabled := true
	models := map[string]bool{}
	encoded, _ := json.Marshal(account.Extra[codexTicketModelsEnabledKey])
	_ = json.Unmarshal(encoded, &models)
	if enabled, exists := models[model]; exists {
		modelEnabled = enabled
	}
	var lastValidation *CodexTicketValidationStatus
	if repo, ok := s.openaiCodexTicketHistory.(CodexTicketValidationRepository); ok {
		var err error
		lastValidation, err = repo.LatestValidation(ctx, id, model)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"last_validation": lastValidation, "pin": codexTicketPin(account, model), "active": codexTicketPinActive(account, model, time.Now()), "last_check": check, "enabled": account.Extra[codexTicketAccountEnabledKey] == true, "model_enabled": modelEnabled}, nil
}

func (s *OpenAIGatewayService) ConfirmCodexTicketMaterial(ctx context.Context, id int64, model string, attempt int64) (map[string]any, error) {
	if attempt <= 0 {
		return nil, ErrCodexTicketModel
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		return nil, ErrCodexTicketUnavailable
	}
	return s.validateCodexTicketMaterial(ctx, id, model, attempt, true)
}

func (s *OpenAIGatewayService) persistCodexTicketPin(ctx context.Context, account *Account, material *CodexTicketMaterial) error {
	models := map[string]bool{}
	raw, _ := json.Marshal(account.Extra[codexTicketModelsEnabledKey])
	_ = json.Unmarshal(raw, &models)
	if models == nil {
		models = map[string]bool{}
	}
	models[material.Model] = true
	if repo, ok := s.openaiCodexTicketHistory.(CodexTicketMaterialRepository); ok {
		archived, err := repo.GetMaterial(ctx, account.ID, material.Model, material.AttemptID)
		if err != nil {
			return err
		}
		if archived.State == "" {
			if err := repo.SaveMaterial(ctx, material); err != nil {
				return err
			}
		}
	}
	now := time.Now()
	expiresAt := now.Add(time.Duration(s.openAICodexTicketConfig().TTLSeconds) * time.Second)
	ticket := &openAICodexTicket{AccountID: account.ID, Model: material.Model, State: material.State, Length: len(material.State), CapturedAt: now, ExpiresAt: expiresAt, ValidationOutcome: "remote_completed", ValidationCheckedAt: now, ValidationHTTPStatus: 200}
	extra := map[string]any{
		codexTicketPinPrefix + material.Model:     &CodexTicketPin{AttemptID: material.AttemptID, ConfirmedAt: now, ExpiresAt: expiresAt},
		openAICodexTicketExtraKey(material.Model): ticket,
		codexTicketAccountEnabledKey:              true, codexTicketModelsEnabledKey: models,
		"codex_ticket_check:" + material.Model: map[string]any{"checked_at": now, "outcome": "remote_completed", "http_status": 200, "ticket_captured_at": now},
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, extra); err != nil {
		return err
	}
	key := openAICodexTicketKey(account.ID, material.Model)
	s.openaiCodexTickets.Store(key, ticket)
	s.openaiCodexTicketNextAttempt.Store(key, codexPinnedNextCheck(expiresAt, now))
	return nil
}

func (s *OpenAIGatewayService) UnpinCodexTicket(ctx context.Context, id int64, model string) error {
	if !s.codexTicketSupportedModel(model) {
		return ErrCodexTicketModel
	}
	key := openAICodexTicketKey(id, model)
	if _, busy := s.openaiCodexTicketInFlight.LoadOrStore(key, true); busy {
		return ErrCodexTicketBusy
	}
	defer s.openaiCodexTicketInFlight.Delete(key)
	if s.openaiCodexTicketHistory != nil {
		unlock, acquired, err := s.openaiCodexTicketHistory.TryLock(ctx, id, model)
		if err != nil {
			return err
		}
		if !acquired {
			return ErrCodexTicketBusy
		}
		defer unlock()
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if !isOpenAICodexTicketAccount(account) {
		return ErrCodexTicketUnavailable
	}
	extra := map[string]any{codexTicketPinPrefix + model: nil}
	var latest *openAICodexTicket
	if repo, ok := s.openaiCodexTicketHistory.(CodexTicketMaterialRepository); ok {
		m, err := repo.GetMaterial(ctx, id, model, 0)
		if err != nil && !errors.Is(err, ErrCodexTicketMaterialMissing) {
			return err
		}
		if err == nil && m.State != "" && time.Now().Before(m.ExpiresAt) {
			latest = &openAICodexTicket{AccountID: id, Model: model, State: m.State, Length: len(m.State), CapturedAt: m.CapturedAt, ExpiresAt: m.ExpiresAt, ValidationOutcome: m.ValidationOutcome, ValidationCheckedAt: m.ValidationCheckedAt, ValidationHTTPStatus: m.ValidationHTTPStatus, Rejected: m.ValidationOutcome == "response_model_mismatch"}
			extra[openAICodexTicketExtraKey(model)] = latest
		}
	}
	if err := s.accountRepo.UpdateExtra(ctx, id, extra); err != nil {
		return err
	}
	s.openaiCodexTickets.Delete(key)
	s.openaiCodexTicketNextAttempt.Delete(key)
	if latest != nil {
		s.openaiCodexTickets.Store(key, latest)
		s.scheduleCodexTicketAfterSuccess(latest)
	}
	return nil
}
