-- Migration 012: expire service tokens by default.

ALTER TABLE service_tokens
ADD COLUMN expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '90 days');

CREATE INDEX idx_service_tokens_expires ON service_tokens(expires_at);
