-- Migration 009: Track when a refresh token was revoked.
--
-- Refresh-token rotation needs a short grace window so two concurrent client
-- requests that both hit a 401 do not look like a stolen-token replay. A timestamp
-- (rather than a boolean alone) lets the server distinguish "just rotated,
-- same client racing itself" from "replayed minutes later, assume compromise".

ALTER TABLE refresh_tokens ADD COLUMN revoked_at TIMESTAMPTZ;

-- Existing revoked rows are conservative: leave revoked_at NULL so they are
-- never treated as being inside the grace window.
CREATE INDEX idx_refresh_tokens_user_revoked ON refresh_tokens(user_id, revoked, revoked_at);
