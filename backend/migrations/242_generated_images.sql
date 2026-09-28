CREATE TABLE IF NOT EXISTS generated_images (
 id CHAR(32) PRIMARY KEY,
 user_id BIGINT NOT NULL,
 api_key_id BIGINT NOT NULL DEFAULT 0,
 group_id BIGINT NOT NULL DEFAULT 0,
 account_id BIGINT NOT NULL DEFAULT 0,
 model TEXT NOT NULL DEFAULT '',
 request_id TEXT NOT NULL DEFAULT '',
 endpoint TEXT NOT NULL DEFAULT '',
 mime_type VARCHAR(20) NOT NULL,
 byte_size BIGINT NOT NULL CHECK (byte_size > 0 AND byte_size <= 20971520),
 width INTEGER NOT NULL CHECK (width > 0),
 height INTEGER NOT NULL CHECK (height > 0),
 sha256 CHAR(64) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ NOT NULL,
 UNIQUE(user_id,request_id,sha256)
);
CREATE INDEX IF NOT EXISTS generated_images_created_idx ON generated_images (created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS generated_images_expires_idx ON generated_images (expires_at);
CREATE INDEX IF NOT EXISTS generated_images_user_created_idx ON generated_images (user_id,created_at DESC);
CREATE INDEX IF NOT EXISTS generated_images_group_created_idx ON generated_images (group_id,created_at DESC);
CREATE INDEX IF NOT EXISTS generated_images_model_created_idx ON generated_images (model,created_at DESC);
CREATE INDEX IF NOT EXISTS generated_images_request_idx ON generated_images (request_id);
