-- Migration 007: Add key versioning support for master key rotation
--
-- Enables rotating SERVER_MASTER_KEY without losing access to previously
-- encrypted project passwords. Each encrypted password is tagged with the
-- key version that was used to encrypt it. The password service maintains
-- a map of key versions to actual keys, allowing decryption of both old
-- and new passwords.
--
-- During key rotation:
-- 1. Add new master key to the service (new_version)
-- 2. Existing rows remain with their old key_version
-- 3. New passwords are encrypted with new key_version
-- 4. Service can decrypt both old and new passwords
-- 5. Old keys can eventually be rotated out (optional re-encryption)

ALTER TABLE project_passwords 
ADD COLUMN key_version INT DEFAULT 1;

-- Update existing rows to have key_version = 1 (the original key)
UPDATE project_passwords SET key_version = 1 WHERE key_version IS NULL;

-- Make key_version NOT NULL after data migration
ALTER TABLE project_passwords 
ALTER COLUMN key_version SET NOT NULL;

-- Add index for key_version lookups (useful for migration/audit)
CREATE INDEX idx_project_passwords_key_version ON project_passwords(key_version);

-- Add a comment explaining the column
COMMENT ON COLUMN project_passwords.key_version IS 
'Master key version used to encrypt this password. Enables key rotation by keeping old keys available for decryption.';
