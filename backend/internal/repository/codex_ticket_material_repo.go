package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *codexTicketAttemptRepository) SaveMaterial(ctx context.Context, m *service.CodexTicketMaterial) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	encrypted, err := r.encryptor.Encrypt(string(raw))
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO codex_ticket_material (attempt_id,account_id,model,payload_encrypted)
 SELECT id,account_id,model,$4 FROM codex_ticket_attempts WHERE id=$1 AND account_id=$2 AND model=$3 AND outcome='success'
 ON CONFLICT (attempt_id) DO NOTHING`, m.AttemptID, m.AccountID, m.Model, encrypted)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrCodexTicketMaterialMissing
	}
	return nil
}

func (r *codexTicketAttemptRepository) GetMaterial(ctx context.Context, accountID int64, model string, id int64) (*service.CodexTicketMaterial, error) {
	if id == 0 {
		err := r.db.QueryRowContext(ctx, "SELECT attempt_id FROM codex_ticket_material WHERE account_id=$1 AND model=$2 ORDER BY attempt_id DESC LIMIT 1", accountID, model).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrCodexTicketMaterialMissing
		}
		if err != nil {
			return nil, err
		}
	}
	m := &service.CodexTicketMaterial{AttemptID: id, AccountID: accountID, Model: model}
	var ciphertext, verdict sql.NullString
	var checkedAt sql.NullTime
	var httpStatus sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT a.occurred_at,a.expires_at,m.payload_encrypted,a.validation_outcome,a.validation_checked_at,a.validation_http_status FROM codex_ticket_attempts a
 LEFT JOIN codex_ticket_material m ON m.attempt_id=a.id AND m.account_id=a.account_id AND m.model=a.model
 WHERE a.id=$1 AND a.account_id=$2 AND a.model=$3 AND a.outcome='success'`, id, accountID, model).Scan(&m.CapturedAt, &m.ExpiresAt, &ciphertext, &verdict, &checkedAt, &httpStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCodexTicketMaterialMissing
	}
	if err != nil {
		return nil, err
	}
	m.ValidationOutcome = verdict.String
	if checkedAt.Valid {
		m.ValidationCheckedAt = checkedAt.Time
	}
	if httpStatus.Valid {
		m.ValidationHTTPStatus = int(httpStatus.Int64)
	}
	if !ciphertext.Valid {
		return m, nil
	}
	raw, err := r.encryptor.Decrypt(ciphertext.String)
	if err != nil {
		return nil, err
	}
	var decoded service.CodexTicketMaterial
	if err = json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	if decoded.AttemptID != id || decoded.AccountID != accountID || decoded.Model != model {
		return nil, service.ErrCodexTicketMaterialMissing
	}
	decoded.ValidationOutcome = m.ValidationOutcome
	decoded.ValidationCheckedAt = m.ValidationCheckedAt
	decoded.ValidationHTTPStatus = m.ValidationHTTPStatus
	return &decoded, nil
}
