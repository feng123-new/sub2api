CREATE TABLE IF NOT EXISTS codex_ticket_material (
 attempt_id BIGINT PRIMARY KEY,
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 model TEXT NOT NULL,
 payload_encrypted TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS codex_ticket_material_account_model ON codex_ticket_material(account_id,model);
