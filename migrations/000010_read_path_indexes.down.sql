CREATE INDEX IF NOT EXISTS idx_secrets_environment ON secrets (environment_id);
DROP INDEX IF EXISTS idx_audit_logs_project_created;
