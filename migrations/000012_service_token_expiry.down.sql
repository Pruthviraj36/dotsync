DROP INDEX IF EXISTS idx_service_tokens_expires;
ALTER TABLE service_tokens DROP COLUMN IF EXISTS expires_at;
