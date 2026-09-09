-- Rollback: Remove key versioning support
--
-- Removes the key_version column added to support master key rotation.
-- Note: This is destructive and will lose information about which key
-- version was used to encrypt each password.

DROP INDEX IF EXISTS idx_project_passwords_key_version;

ALTER TABLE project_passwords 
DROP COLUMN key_version;
