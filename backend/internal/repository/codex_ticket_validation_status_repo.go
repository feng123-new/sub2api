package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *codexTicketAttemptRepository) SaveValidation(ctx context.Context, id int64, model string, v *service.CodexTicketValidationStatus) error {
	err := r.db.QueryRowContext(ctx, `UPDATE codex_ticket_attempts SET validation_outcome=$4,validation_http_status=$5,validation_checked_at=$6
 WHERE account_id=$1 AND model=$2 AND outcome='success' AND id=CASE WHEN $3::bigint>0 THEN $3::bigint ELSE
 (SELECT id FROM codex_ticket_attempts WHERE account_id=$1 AND model=$2 AND outcome='success' ORDER BY occurred_at DESC,id DESC LIMIT 1) END RETURNING id`, id, model, v.AttemptID, v.Outcome, v.HTTPStatus, v.CheckedAt).Scan(&v.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrCodexTicketMaterialMissing
	}
	return err
}
func (r *codexTicketAttemptRepository) LatestValidation(ctx context.Context, id int64, model string) (*service.CodexTicketValidationStatus, error) {
	v := &service.CodexTicketValidationStatus{}
	err := r.db.QueryRowContext(ctx, `SELECT id,validation_outcome,coalesce(validation_http_status,0),validation_checked_at FROM codex_ticket_attempts
 WHERE account_id=$1 AND model=$2 AND validation_checked_at IS NOT NULL ORDER BY validation_checked_at DESC LIMIT 1`, id, model).Scan(&v.AttemptID, &v.Outcome, &v.HTTPStatus, &v.CheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}
