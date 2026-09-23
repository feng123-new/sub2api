ALTER TABLE codex_ticket_attempts ADD COLUMN IF NOT EXISTS validation_outcome TEXT;
ALTER TABLE codex_ticket_attempts ADD COLUMN IF NOT EXISTS validation_http_status INTEGER;
ALTER TABLE codex_ticket_attempts ADD COLUMN IF NOT EXISTS validation_checked_at TIMESTAMPTZ;
