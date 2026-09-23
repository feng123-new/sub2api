package service

import (
	"context"
	"time"
)

type CodexTicketValidationStatus struct {
	AttemptID  int64     `json:"attempt_id"`
	Outcome    string    `json:"outcome"`
	HTTPStatus int       `json:"http_status"`
	CheckedAt  time.Time `json:"checked_at"`
}
type CodexTicketValidationRepository interface {
	SaveValidation(context.Context, int64, string, *CodexTicketValidationStatus) error
	LatestValidation(context.Context, int64, string) (*CodexTicketValidationStatus, error)
}
